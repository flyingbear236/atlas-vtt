package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// CharacterTOMLSource retains the user's original text for future source-preserving exports.
// It is independent of the game state; only persistent characters are transferable.
type CharacterTOMLSource struct {
	FileName string `json:"fileName"`
	Source   string `json:"source"`
	AutoName bool   `json:"autoName,omitempty"`
}

type characterTOMLDocument struct {
	SchemaVersion int               `toml:"schema_version"`
	Character     characterTOMLBody `toml:"character"`
}
type characterTOMLBody struct {
	ID              string         `toml:"id"`
	Name            string         `toml:"name"`
	PresetID        string         `toml:"preset_id,omitempty"`
	Persistent      bool           `toml:"persistent"`
	Materialized    bool           `toml:"materialized"`
	Artwork         string         `toml:"artwork,omitempty"`
	ArtworkMode     string         `toml:"artwork_mode,omitempty"` // "placeholder" is distinct from inheritance.
	Actions         []string       `toml:"actions,omitempty"`
	Stats           map[string]any `toml:"stats,omitempty"`
	ResourceInitial map[string]any `toml:"resource_initial,omitempty"`
	ResourceCurrent map[string]any `toml:"resource_current,omitempty"`
}

// CharacterTOMLFileName is stable and safe even for Unicode or path-like IDs.
// A hash avoids case-insensitive filename collisions on Windows.
func CharacterTOMLFileName(characterID string) string {
	hash := sha256.Sum256([]byte(characterID))
	return "characters/" + hex.EncodeToString(hash[:12]) + ".toml"
}

func PortableCharacterIDs(instances map[string]CharacterInstance) []string {
	ids := make([]string, 0)
	for id, instance := range instances {
		if instance.Persistent {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func characterTOMLValues(values map[string]StatValue) map[string]any {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]any, len(values))
	for id, v := range values {
		result[id] = v.Interface()
	}
	return result
}

func characterTOMLPath(instance CharacterInstance, media map[string]string) (string, string, error) {
	if instance.ArtworkPath != "" {
		if err := validateRulesetMediaPath(instance.ArtworkPath); err != nil {
			return "", "", err
		}
		return instance.ArtworkPath, "", nil
	}
	if instance.AvatarAssetID == nil {
		return "", "", nil
	}
	if *instance.AvatarAssetID == "" {
		return "", "placeholder", nil
	}
	paths := make([]string, 0)
	for name, asset := range media {
		if asset == *instance.AvatarAssetID {
			paths = append(paths, name)
		}
	}
	if len(paths) > 0 {
		sort.Strings(paths)
		return paths[0], "", nil
	}
	// Generated mapping is resolved when the export snapshot is assembled in stage 3.
	return "character-artwork-" + *instance.AvatarAssetID + ".png", "", nil
}

func buildCharacterTOMLDocument(instance CharacterInstance, media map[string]string, registry EffectiveRegistry) (characterTOMLDocument, error) {
	if !instance.Persistent {
		return characterTOMLDocument{}, fmt.Errorf("character %q is not persistent", instance.ID)
	}
	if err := ValidateCharacterInstance(instance, registry); err != nil {
		return characterTOMLDocument{}, err
	}
	artwork, mode, err := characterTOMLPath(instance, media)
	if err != nil {
		return characterTOMLDocument{}, err
	}
	body := characterTOMLBody{ID: instance.ID, Name: instance.Name, PresetID: instance.PresetID, Persistent: true,
		Materialized: instance.Materialized != nil, Artwork: artwork, ArtworkMode: mode,
		ResourceCurrent: characterTOMLValues(instance.ResourceCurrent)}
	if instance.Materialized != nil {
		body.Stats = characterTOMLValues(instance.Materialized.Stats)
		body.ResourceInitial = characterTOMLValues(instance.Materialized.ResourceInitial)
		body.Actions = append([]string(nil), instance.Materialized.ActionIDs...)
	}
	return characterTOMLDocument{SchemaVersion: 1, Character: body}, nil
}

// EncodeCharacterTOML is intentionally pure: writing a character's current HP does
// not rewrite the originating TOML or mutate the session at runtime.
func EncodeCharacterTOML(instance CharacterInstance, media map[string]string, registry EffectiveRegistry) ([]byte, error) {
	doc, err := buildCharacterTOMLDocument(instance, media, registry)
	if err != nil {
		return nil, err
	}
	return toml.Marshal(doc)
}

// DecodeCharacterTOML strictly parses an instance without importing it into a session.
// Missing artwork paths are allowed; resolution falls back to a placeholder.
func DecodeCharacterTOML(data []byte, media map[string]string, registry EffectiveRegistry) (CharacterInstance, error) {
	if len(data) > MaxDefinitionDocumentBytes {
		return CharacterInstance{}, fmt.Errorf("character TOML exceeds size limit")
	}
	var doc characterTOMLDocument
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&doc); err != nil {
		return CharacterInstance{}, err
	}
	if doc.SchemaVersion != 1 {
		return CharacterInstance{}, fmt.Errorf("unsupported character schema_version %d", doc.SchemaVersion)
	}
	b := doc.Character
	if !b.Persistent {
		return CharacterInstance{}, fmt.Errorf("character TOML requires persistent = true")
	}
	if b.ArtworkMode != "" && b.ArtworkMode != "placeholder" {
		return CharacterInstance{}, fmt.Errorf("unknown artwork_mode %q", b.ArtworkMode)
	}
	if b.ArtworkMode == "placeholder" && b.Artwork != "" {
		return CharacterInstance{}, fmt.Errorf("placeholder cannot specify artwork")
	}
	if b.Artwork != "" {
		if err := validateRulesetMediaPath(b.Artwork); err != nil {
			return CharacterInstance{}, err
		}
	}
	if !b.Materialized && (len(b.Stats) > 0 || len(b.ResourceInitial) > 0 || len(b.Actions) > 0) {
		return CharacterInstance{}, fmt.Errorf("inherited character cannot define materialized mechanics")
	}
	instance := CharacterInstance{ID: b.ID, Name: b.Name, PresetID: b.PresetID, Persistent: true, ArtworkPath: b.Artwork}
	if b.ArtworkMode == "placeholder" {
		empty := ""
		instance.AvatarAssetID = &empty
	}
	if b.Materialized {
		stats, err := decodeCharacterTOMLStats(b.Stats, registry, false)
		if err != nil {
			return CharacterInstance{}, err
		}
		initial, err := decodeCharacterTOMLStats(b.ResourceInitial, registry, true)
		if err != nil {
			return CharacterInstance{}, err
		}
		instance.Materialized = &CharacterMechanicalState{Stats: stats, ResourceInitial: initial, ActionIDs: append([]string(nil), b.Actions...)}
	}
	current, err := decodeCharacterTOMLStats(b.ResourceCurrent, registry, true)
	if err != nil {
		return CharacterInstance{}, err
	}
	instance.ResourceCurrent = current
	if err := ValidateCharacterInstance(instance, registry); err != nil {
		return CharacterInstance{}, err
	}
	_ = media // logical path is retained even when a matching asset is unavailable
	return instance, nil
}

func decodeCharacterTOMLStats(raw map[string]any, registry EffectiveRegistry, resource bool) (map[string]StatValue, error) {
	result := make(map[string]StatValue, len(raw))
	for id, v := range raw {
		def, ok := registry.Stats[id]
		if !ok {
			return nil, fmt.Errorf("unknown stat %q", id)
		}
		if def.IsResource() != resource {
			return nil, fmt.Errorf("stat %q has wrong resource kind", id)
		}
		value, err := tomlStatValue(v, def.Type)
		if err != nil {
			return nil, fmt.Errorf("stat %q: %w", id, err)
		}
		if err := ValidateStatValueForDefinition(def, value); err != nil {
			return nil, err
		}
		result[id] = value
	}
	return result, nil
}

// CurrentCharacterTOML preserves imported character source, if available. It is
// called only when the user requests content or export; no background rewriting.
func CurrentCharacterTOML(session *Session, id string) ([]byte, error) {
	instance, ok := session.CharacterInstances[id]
	if !ok {
		return nil, fmt.Errorf("unknown character %q", id)
	}
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		return nil, err
	}
	canonical, err := EncodeCharacterTOML(instance, session.Ruleset.Media, registry)
	if err != nil {
		return nil, err
	}
	origin, ok := session.CharacterSources[id]
	if !ok || origin.Source == "" {
		return canonical, nil
	}
	var original characterTOMLDocument
	if err := toml.NewDecoder(strings.NewReader(origin.Source)).DisallowUnknownFields().Decode(&original); err != nil {
		return nil, fmt.Errorf("character %q original TOML: %w", id, err)
	}
	wanted, err := buildCharacterTOMLDocument(instance, session.Ruleset.Media, registry)
	if err != nil {
		return nil, err
	}
	if sameCharacterDocument(original, wanted) {
		return []byte(origin.Source), nil
	}
	// Reuse the stage-1 token-level source-preserving editor, not a TOML reformatter.
	originalTables, err := sourceTOMLTables(origin.Source)
	if err != nil {
		return nil, err
	}
	wantedTables, err := sourceTOMLTables(string(canonical))
	if err != nil {
		return nil, err
	}
	desired := make(map[string]sourceTOMLTable, len(wantedTables))
	order := make([]string, 0, len(wantedTables))
	for _, table := range wantedTables {
		desired[table.key] = table
		order = append(order, table.key)
	}
	var result strings.Builder
	seen := map[string]bool{}
	for _, table := range originalTables {
		next, exists := desired[table.key]
		if !exists { // Preserve comments outside removed tables.
			for _, line := range table.lines {
				if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
					result.WriteString(line)
				}
			}
			continue
		}
		seen[table.key] = true
		updated, err := rewriteTOMLTable(table, next)
		if err != nil {
			return nil, fmt.Errorf("character %q table %q: %w", id, table.key, err)
		}
		result.WriteString(updated)
	}
	for _, key := range order {
		if !seen[key] {
			result.WriteString("\n")
			result.WriteString(strings.Join(desired[key].lines, ""))
		}
	}
	updated := []byte(result.String())
	var parsed characterTOMLDocument
	if err := toml.NewDecoder(bytes.NewReader(updated)).DisallowUnknownFields().Decode(&parsed); err != nil {
		return nil, fmt.Errorf("character %q edited TOML: %w", id, err)
	}
	if !sameCharacterDocument(parsed, wanted) {
		return nil, fmt.Errorf("character %q source-preserving edit changed semantics", id)
	}
	if _, err := DecodeCharacterTOML(updated, session.Ruleset.Media, registry); err != nil {
		return nil, err
	}
	return updated, nil
}

func sameCharacterDocument(left, right characterTOMLDocument) bool {
	a, b := left.Character, right.Character
	return left.SchemaVersion == right.SchemaVersion && a.ID == b.ID && a.Name == b.Name && a.PresetID == b.PresetID &&
		a.Persistent == b.Persistent && a.Materialized == b.Materialized && a.Artwork == b.Artwork && a.ArtworkMode == b.ArtworkMode &&
		slices.Equal(a.Actions, b.Actions) && equalCharacterTOMLMap(a.Stats, b.Stats) && equalCharacterTOMLMap(a.ResourceInitial, b.ResourceInitial) && equalCharacterTOMLMap(a.ResourceCurrent, b.ResourceCurrent)
}
func equalCharacterTOMLMap(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !reflect.DeepEqual(v, b[k]) {
			return false
		}
	}
	return true
}

// CharacterSourceFileName reuses imported names and otherwise generates a safe path.
func CharacterSourceFileName(session *Session, id string) string {
	if source, ok := session.CharacterSources[id]; ok && source.FileName != "" {
		return source.FileName
	}
	return CharacterTOMLFileName(id)
}

func validateCharacterSourceFileName(name string) error {
	if err := validateLibraryFilePath(name); err != nil {
		return err
	}
	if !strings.EqualFold(path.Ext(name), ".toml") {
		return fmt.Errorf("invalid character TOML filename %q", name)
	}
	return nil
}
