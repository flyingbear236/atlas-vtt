package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func loadRulesetSnapshotFile(path string) (RulesetSnapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return RulesetSnapshot{}, fmt.Errorf("open default ruleset: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxDefinitionDocumentBytes+1))
	if err != nil {
		return RulesetSnapshot{}, fmt.Errorf("read default ruleset: %w", err)
	}
	snapshot, err := CompileRulesetFiles(map[string]string{filepath.Base(path): string(data)})
	if err != nil {
		return RulesetSnapshot{}, fmt.Errorf("parse default ruleset: %w", err)
	}
	return snapshot, nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateUniqueJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, structured := token.(json.Delim)
	if !structured {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if err := validateUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := validateUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	expected := json.Delim('}')
	if delimiter == '[' {
		expected = ']'
	}
	if closing != expected {
		return errors.New("mismatched JSON delimiter")
	}
	return nil
}

type RulesetMetadata struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type RulesetFileManifest struct {
	Digest      string   `json:"digest"`
	HasMetadata bool     `json:"hasMetadata,omitempty"`
	Stats       []string `json:"stats,omitempty"`
	Actions     []string `json:"actions,omitempty"`
	Presets     []string `json:"presets,omitempty"`
}

type RulesetSnapshot struct {
	Metadata RulesetMetadata                  `json:"metadata"`
	Registry RulesetRegistry                  `json:"registry"`
	Files    map[string]RulesetFileManifest   `json:"files,omitempty"`
}

type CharacterTokenReference struct {
	SceneID string
	TokenID string
}

func emptyRulesetRegistry() RulesetRegistry {
	return RulesetRegistry{
		Stats:   map[string]StatDefinition{},
		Actions: map[string]ActionDefinition{},
		Presets: map[string]CharacterPresetDefinition{},
	}
}

func emptyCampaignRegistry() CampaignRegistry {
	return CampaignRegistry{
		Stats:   map[string]StatDefinition{},
		Actions: map[string]ActionDefinition{},
		Presets: map[string]CharacterPresetDefinition{},
	}
}

func cloneRulesetSnapshot(snapshot RulesetSnapshot) RulesetSnapshot {
	result := RulesetSnapshot{Metadata: snapshot.Metadata, Registry: emptyRulesetRegistry()}
	for id, definition := range snapshot.Registry.Stats {
		result.Registry.Stats[id] = cloneStatDefinition(definition)
	}
	for id, definition := range snapshot.Registry.Actions {
		result.Registry.Actions[id] = cloneActionDefinition(definition)
	}
	for id, definition := range snapshot.Registry.Presets {
		result.Registry.Presets[id] = clonePresetDefinition(definition)
	}
	if snapshot.Files != nil {
		result.Files = make(map[string]RulesetFileManifest, len(snapshot.Files))
		for name, manifest := range snapshot.Files {
			manifest.Stats = append([]string(nil), manifest.Stats...)
			manifest.Actions = append([]string(nil), manifest.Actions...)
			manifest.Presets = append([]string(nil), manifest.Presets...)
			result.Files[name] = manifest
		}
	}
	return result
}

func validateRulesetFileManifests(snapshot RulesetSnapshot) error {
	if snapshot.Files == nil {
		return nil
	}
	if len(snapshot.Files) == 0 || len(snapshot.Files) > MaxRulesetFiles {
		return fmt.Errorf("ruleset file manifest must contain between 1 and %d files", MaxRulesetFiles)
	}
	statOwners, actionOwners, presetOwners := map[string]string{}, map[string]string{}, map[string]string{}
	metadataFiles := 0
	checkIDs := func(name, kind string, ids []string, registryHas func(string) bool, owners map[string]string) error {
		local := map[string]struct{}{}
		for _, id := range ids {
			if _, duplicate := local[id]; duplicate {
				return fmt.Errorf("ruleset file %q lists duplicate %s %q", name, kind, id)
			}
			local[id] = struct{}{}
			if !registryHas(id) {
				return fmt.Errorf("ruleset file %q owns missing %s %q", name, kind, id)
			}
			if owner := owners[id]; owner != "" {
				return fmt.Errorf("ruleset %s %q is owned by both %q and %q", kind, id, owner, name)
			}
			owners[id] = name
		}
		return nil
	}
	for name, manifest := range snapshot.Files {
		if err := validateRulesetFileName(name); err != nil {
			return err
		}
		if len(manifest.Digest) != 64 {
			return fmt.Errorf("ruleset file %q has invalid digest", name)
		}
		if _, err := hex.DecodeString(manifest.Digest); err != nil {
			return fmt.Errorf("ruleset file %q has invalid digest", name)
		}
		if manifest.HasMetadata {
			metadataFiles++
		}
		if err := checkIDs(name, "stat", manifest.Stats, func(id string) bool { _, ok := snapshot.Registry.Stats[id]; return ok }, statOwners); err != nil {
			return err
		}
		if err := checkIDs(name, "action", manifest.Actions, func(id string) bool { _, ok := snapshot.Registry.Actions[id]; return ok }, actionOwners); err != nil {
			return err
		}
		if err := checkIDs(name, "preset", manifest.Presets, func(id string) bool { _, ok := snapshot.Registry.Presets[id]; return ok }, presetOwners); err != nil {
			return err
		}
	}
	if metadataFiles != 1 {
		return fmt.Errorf("ruleset file manifest must identify exactly one metadata file")
	}
	if len(statOwners) != len(snapshot.Registry.Stats) || len(actionOwners) != len(snapshot.Registry.Actions) || len(presetOwners) != len(snapshot.Registry.Presets) {
		return errors.New("ruleset file manifest does not own every definition")
	}
	return nil
}

func ValidateRulesetSnapshot(snapshot RulesetSnapshot) error {
	if err := validateRulesetFileManifests(snapshot); err != nil {
		return err
	}
	registryEmpty := len(snapshot.Registry.Stats) == 0 && len(snapshot.Registry.Actions) == 0 && len(snapshot.Registry.Presets) == 0
	metadataEmpty := snapshot.Metadata == (RulesetMetadata{})
	if metadataEmpty && registryEmpty {
		return nil
	}
	if snapshot.Metadata.ID == "" || snapshot.Metadata.Name == "" || snapshot.Metadata.Version == "" {
		return errors.New("ruleset metadata id, name, and version are required")
	}
	if err := validateID(snapshot.Metadata.ID); err != nil {
		return fmt.Errorf("ruleset metadata id: %w", err)
	}
	if err := validateText("ruleset name", snapshot.Metadata.Name, MaxDefinitionNameRunes, false); err != nil {
		return err
	}
	if err := validateText("ruleset version", snapshot.Metadata.Version, MaxDefinitionNameRunes, false); err != nil {
		return err
	}
	for id, preset := range snapshot.Registry.Presets {
		if preset.AvatarAssetID != "" {
			return fmt.Errorf("ruleset preset %q cannot reference a campaign avatar", id)
		}
	}
	_, err := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: snapshot.Registry})
	if err != nil {
		return fmt.Errorf("invalid ruleset registry: %w", err)
	}
	return nil
}

// initializeSessionCharacterCollections is the legacy JSON migration boundary.
// It initializes absent fields but never creates character instances.
func initializeSessionCharacterCollections(session *Session) bool {
	changed := false
	if session.RegistryRevision == 0 {
		session.RegistryRevision = 1
		changed = true
	}
	if session.CharacterRevision == 0 {
		session.CharacterRevision = 1
		changed = true
	}
	if session.DefinitionOperations == nil {
		session.DefinitionOperations = map[string]DefinitionOperationReceipt{}
		changed = true
	}
	if session.Ruleset.Registry.Stats == nil {
		session.Ruleset.Registry.Stats = map[string]StatDefinition{}
		changed = true
	}
	if session.Ruleset.Registry.Actions == nil {
		session.Ruleset.Registry.Actions = map[string]ActionDefinition{}
		changed = true
	}
	if session.Ruleset.Registry.Presets == nil {
		session.Ruleset.Registry.Presets = map[string]CharacterPresetDefinition{}
		changed = true
	}
	if session.CampaignDefinitions.Stats == nil {
		session.CampaignDefinitions.Stats = map[string]StatDefinition{}
		changed = true
	}
	if session.CampaignDefinitions.Actions == nil {
		session.CampaignDefinitions.Actions = map[string]ActionDefinition{}
		changed = true
	}
	if session.CampaignDefinitions.Presets == nil {
		session.CampaignDefinitions.Presets = map[string]CharacterPresetDefinition{}
		changed = true
	}
	if session.CharacterInstances == nil {
		session.CharacterInstances = map[string]CharacterInstance{}
		changed = true
	}
	return changed
}

func validateSessionCharacterState(session *Session) error {
	if session == nil {
		return errors.New("session is nil")
	}
	if err := ValidateRulesetSnapshot(session.Ruleset); err != nil {
		return err
	}
	if session.RegistryRevision == 0 {
		return errors.New("registry revision must be positive")
	}
	if session.CharacterRevision == 0 {
		return errors.New("character revision must be positive")
	}
	if err := validateDefinitionOperationReceipts(session.DefinitionOperations); err != nil {
		return err
	}
	registry, err := MergeDefinitionRegistries(DefinitionRegistries{
		Ruleset: session.Ruleset.Registry, Campaign: session.CampaignDefinitions,
	})
	if err != nil {
		return fmt.Errorf("invalid definitions: %w", err)
	}
	if len(session.CharacterInstances) > MaxCharacterInstances {
		return fmt.Errorf("character instances exceed limit %d", MaxCharacterInstances)
	}
	for id, instance := range session.CharacterInstances {
		if instance.ID != id {
			return fmt.Errorf("character instance map key %q does not match id %q", id, instance.ID)
		}
		if err := ValidateCharacterInstance(instance, registry); err != nil {
			return fmt.Errorf("character instance %q: %w", id, err)
		}
		if instance.AvatarAssetID != nil && *instance.AvatarAssetID != "" {
			if asset, exists := session.Assets[*instance.AvatarAssetID]; !exists || asset.Kind != assetKindAvatar {
				return fmt.Errorf("character instance %q references unknown avatar asset %q", id, *instance.AvatarAssetID)
			}
		}
	}
	for id, preset := range session.CampaignDefinitions.Presets {
		if preset.AvatarAssetID != "" {
			if asset, exists := session.Assets[preset.AvatarAssetID]; !exists || asset.Kind != assetKindAvatar {
				return fmt.Errorf("campaign preset %q references unknown avatar asset %q", id, preset.AvatarAssetID)
			}
		}
	}
	for sceneID, scene := range session.Scenes {
		for tokenID, token := range scene.Tokens {
			if !validateTokenOwners(token, session.Members) {
				return fmt.Errorf("scene %q token %q has invalid owners", sceneID, tokenID)
			}
			if token.CharacterInstanceID != "" {
				if _, exists := session.CharacterInstances[token.CharacterInstanceID]; !exists {
					return fmt.Errorf("scene %q token %q references unknown character instance %q", sceneID, tokenID, token.CharacterInstanceID)
				}
			}
		}
	}
	return nil
}

func rebuildCharacterReferences(session *Session) {
	references := make(map[string]map[CharacterTokenReference]struct{}, len(session.CharacterInstances))
	for sceneID, scene := range session.Scenes {
		for tokenID, token := range scene.Tokens {
			if token.CharacterInstanceID == "" {
				continue
			}
			if references[token.CharacterInstanceID] == nil {
				references[token.CharacterInstanceID] = map[CharacterTokenReference]struct{}{}
			}
			references[token.CharacterInstanceID][CharacterTokenReference{SceneID: sceneID, TokenID: tokenID}] = struct{}{}
		}
	}
	session.characterReferences = references
}

func updateCharacterReference(session *Session, sceneID string, old Token, oldExists bool, next Token, nextExists bool) {
	if oldExists && nextExists && old.CharacterInstanceID == next.CharacterInstanceID {
		return
	}
	if oldExists && old.CharacterInstanceID != "" {
		references := session.characterReferences[old.CharacterInstanceID]
		delete(references, CharacterTokenReference{SceneID: sceneID, TokenID: old.ID})
		if len(references) == 0 {
			delete(session.characterReferences, old.CharacterInstanceID)
		}
	}
	if nextExists && next.CharacterInstanceID != "" {
		if session.characterReferences == nil {
			session.characterReferences = map[string]map[CharacterTokenReference]struct{}{}
		}
		if session.characterReferences[next.CharacterInstanceID] == nil {
			session.characterReferences[next.CharacterInstanceID] = map[CharacterTokenReference]struct{}{}
		}
		session.characterReferences[next.CharacterInstanceID][CharacterTokenReference{SceneID: sceneID, TokenID: next.ID}] = struct{}{}
	}
}
