package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	DefinitionsSchemaVersion = 1
	MaxRulesetFiles           = 32
	MaxRulesetPackageBytes    = MaxDefinitionDocumentBytes
)

type DefinitionImportChanges struct {
	Added   []string `json:"added"`
	Updated []string `json:"updated"`
}

type CampaignImportDiff struct {
	Stats   DefinitionImportChanges `json:"stats"`
	Actions DefinitionImportChanges `json:"actions"`
	Presets DefinitionImportChanges `json:"presets"`
}

type CampaignImportPreview struct {
	Registry CampaignRegistry
	Diff     CampaignImportDiff
}

type definitionsTOMLDocument struct {
	SchemaVersion int                             `toml:"schema_version"`
	Ruleset       *rulesetTOMLMetadata            `toml:"ruleset,omitempty"`
	Stats         map[string]statTOMLDefinition   `toml:"stats,omitempty"`
	Actions       map[string]actionTOMLDefinition `toml:"actions,omitempty"`
	Presets       map[string]presetTOMLDefinition `toml:"presets,omitempty"`
}

type rulesetTOMLMetadata struct {
	ID      string `toml:"id"`
	Name    string `toml:"name"`
	Version string `toml:"version"`
}

type statTOMLDefinition struct {
	Name    string `toml:"name,omitempty"`
	Type    string `toml:"type"`
	Default any    `toml:"default,omitempty"`
}

type actionTOMLDefinition struct {
	Name        string         `toml:"name"`
	Description string         `toml:"description,omitempty"`
	Tags        []string       `toml:"tags,omitempty"`
	Rolls       []rollTOMLSpec `toml:"rolls,omitempty"`
}

type rollTOMLSpec struct {
	ID            string  `toml:"id"`
	Name          string  `toml:"name"`
	Count         int     `toml:"count"`
	Sides         int     `toml:"sides"`
	ModifierStat  string  `toml:"modifier_stat,omitempty"`
	ModifierFixed float64 `toml:"modifier_fixed,omitempty"`
}

type presetTOMLDefinition struct {
	Name   string         `toml:"name"`
	Kind   string         `toml:"kind,omitempty"`
	Avatar string         `toml:"avatar,omitempty"`
	Stats  map[string]any `toml:"stats,omitempty"`
	// The exchange format uses "actions" while runtime explicitly uses
	// CharacterPresetDefinition.ActionIDs.
	Actions []string `toml:"actions,omitempty"`
}


type DefinitionReplacementChanges struct {
	Added   []string `json:"added"`
	Changed []string `json:"changed"`
	Removed []string `json:"removed"`
}

type RulesetReplacementDiff struct {
	Stats   DefinitionReplacementChanges `json:"stats"`
	Actions DefinitionReplacementChanges `json:"actions"`
	Presets DefinitionReplacementChanges `json:"presets"`
}

func validateRulesetFileName(name string) error {
	if name == "" || len(name) > 255 || strings.TrimSpace(name) != name || strings.ContainsRune(name, '/') || strings.ContainsRune(name, '\\') || !strings.HasSuffix(strings.ToLower(name), ".toml") {
		return fmt.Errorf("invalid ruleset filename %q", name)
	}
	return nil
}

func rulesetFileDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func sortedDefinitionIDs[T any](values map[string]T) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func rulesetFileManifest(content string, document definitionsTOMLDocument) RulesetFileManifest {
	return RulesetFileManifest{
		Digest:      rulesetFileDigest(content),
		HasMetadata: document.Ruleset != nil,
		Stats:       sortedDefinitionIDs(document.Stats),
		Actions:     sortedDefinitionIDs(document.Actions),
		Presets:     sortedDefinitionIDs(document.Presets),
	}
}

func decodeRulesetSourceFile(name, content string) (definitionsTOMLDocument, error) {
	if err := validateRulesetFileName(name); err != nil {
		return definitionsTOMLDocument{}, err
	}
	if len(content) > MaxDefinitionDocumentBytes {
		return definitionsTOMLDocument{}, fmt.Errorf("ruleset file %q exceeds %d bytes", name, MaxDefinitionDocumentBytes)
	}
	var document definitionsTOMLDocument
	decoder := toml.NewDecoder(strings.NewReader(content)).DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return definitionsTOMLDocument{}, fmt.Errorf("ruleset file %q: %w", name, err)
	}
	if document.SchemaVersion != DefinitionsSchemaVersion {
		return definitionsTOMLDocument{}, fmt.Errorf("ruleset file %q: unsupported schema_version %d", name, document.SchemaVersion)
	}
	return document, nil
}

func CompileRulesetFiles(files map[string]string) (RulesetSnapshot, error) {
	if len(files) == 0 || len(files) > MaxRulesetFiles {
		return RulesetSnapshot{}, fmt.Errorf("ruleset package must contain between 1 and %d TOML files", MaxRulesetFiles)
	}
	totalBytes := 0
	names := make([]string, 0, len(files))
	for name, content := range files {
		totalBytes += len(content)
		if totalBytes > MaxRulesetPackageBytes {
			return RulesetSnapshot{}, fmt.Errorf("ruleset package exceeds %d bytes", MaxRulesetPackageBytes)
		}
		names = append(names, name)
	}
	sort.Strings(names)

	merged := definitionsTOMLDocument{
		SchemaVersion: DefinitionsSchemaVersion,
		Stats:         map[string]statTOMLDefinition{},
		Actions:       map[string]actionTOMLDefinition{},
		Presets:       map[string]presetTOMLDefinition{},
	}
	manifests := make(map[string]RulesetFileManifest, len(files))
	statSource, actionSource, presetSource := map[string]string{}, map[string]string{}, map[string]string{}
	metadataSource := ""

	for _, name := range names {
		content := files[name]
		document, err := decodeRulesetSourceFile(name, content)
		if err != nil {
			return RulesetSnapshot{}, err
		}
		manifests[name] = rulesetFileManifest(content, document)
		if document.Ruleset != nil {
			if metadataSource != "" {
				return RulesetSnapshot{}, fmt.Errorf("ruleset metadata is defined in both %q and %q", metadataSource, name)
			}
			metadata := *document.Ruleset
			merged.Ruleset = &metadata
			metadataSource = name
		}
		for id, definition := range document.Stats {
			if source := statSource[id]; source != "" {
				return RulesetSnapshot{}, fmt.Errorf("duplicate stat %q in %q and %q", id, source, name)
			}
			statSource[id], merged.Stats[id] = name, definition
		}
		for id, definition := range document.Actions {
			if source := actionSource[id]; source != "" {
				return RulesetSnapshot{}, fmt.Errorf("duplicate action %q in %q and %q", id, source, name)
			}
			actionSource[id], merged.Actions[id] = name, definition
		}
		for id, definition := range document.Presets {
			if source := presetSource[id]; source != "" {
				return RulesetSnapshot{}, fmt.Errorf("duplicate preset %q in %q and %q", id, source, name)
			}
			presetSource[id], merged.Presets[id] = name, definition
		}
	}
	if merged.Ruleset == nil {
		return RulesetSnapshot{}, fmt.Errorf("ruleset package must contain [ruleset] metadata in exactly one file")
	}

	combined, err := toml.Marshal(merged)
	if err != nil {
		return RulesetSnapshot{}, fmt.Errorf("encode merged ruleset: %w", err)
	}
	registry, metadata, err := decodeDefinitionsTOML(combined, EffectiveRegistry{})
	if err != nil {
		return RulesetSnapshot{}, err
	}
	snapshot := RulesetSnapshot{
		Metadata: *metadata,
		Registry: RulesetRegistry{Stats: registry.Stats, Actions: registry.Actions, Presets: registry.Presets},
		Files:    manifests,
	}
	if err := ValidateRulesetSnapshot(snapshot); err != nil {
		return RulesetSnapshot{}, fmt.Errorf("ruleset package: %w", err)
	}
	return snapshot, nil
}

func definitionOwner(files map[string]RulesetFileManifest, kind, id string) string {
	for name, manifest := range files {
		var ids []string
		switch kind {
		case definitionKindStat:
			ids = manifest.Stats
		case definitionKindAction:
			ids = manifest.Actions
		case definitionKindPreset:
			ids = manifest.Presets
		}
		if slices.Contains(ids, id) {
			return name
		}
	}
	return ""
}

func ReplaceRulesetFile(current RulesetSnapshot, name, content string) (RulesetSnapshot, error) {
	oldManifest, exists := current.Files[name]
	if !exists {
		if len(current.Files) == 0 {
			return RulesetSnapshot{}, fmt.Errorf("current ruleset has no file manifest; replace the whole ruleset first")
		}
		return RulesetSnapshot{}, fmt.Errorf("ruleset file %q is not installed", name)
	}
	document, err := decodeRulesetSourceFile(name, content)
	if err != nil {
		return RulesetSnapshot{}, err
	}
	if oldManifest.HasMetadata != (document.Ruleset != nil) {
		return RulesetSnapshot{}, fmt.Errorf("single-file replacement cannot move [ruleset] metadata between files")
	}

	candidate := cloneRulesetSnapshot(current)
	for _, id := range oldManifest.Stats {
		delete(candidate.Registry.Stats, id)
	}
	for _, id := range oldManifest.Actions {
		delete(candidate.Registry.Actions, id)
	}
	for _, id := range oldManifest.Presets {
		delete(candidate.Registry.Presets, id)
	}

	for id := range document.Stats {
		if _, duplicate := candidate.Registry.Stats[id]; duplicate {
			return RulesetSnapshot{}, fmt.Errorf("duplicate stat %q in %q and %q", id, definitionOwner(candidate.Files, definitionKindStat, id), name)
		}
	}
	for id := range document.Actions {
		if _, duplicate := candidate.Registry.Actions[id]; duplicate {
			return RulesetSnapshot{}, fmt.Errorf("duplicate action %q in %q and %q", id, definitionOwner(candidate.Files, definitionKindAction, id), name)
		}
	}
	for id := range document.Presets {
		if _, duplicate := candidate.Registry.Presets[id]; duplicate {
			return RulesetSnapshot{}, fmt.Errorf("duplicate preset %q in %q and %q", id, definitionOwner(candidate.Files, definitionKindPreset, id), name)
		}
	}

	base := EffectiveRegistry{Stats: candidate.Registry.Stats, Actions: candidate.Registry.Actions, Presets: candidate.Registry.Presets}
	fragment, metadata, err := decodeDefinitionsTOML([]byte(content), base)
	if err != nil {
		return RulesetSnapshot{}, err
	}
	for id, definition := range fragment.Stats {
		candidate.Registry.Stats[id] = cloneStatDefinition(definition)
	}
	for id, definition := range fragment.Actions {
		candidate.Registry.Actions[id] = cloneActionDefinition(definition)
	}
	for id, definition := range fragment.Presets {
		candidate.Registry.Presets[id] = clonePresetDefinition(definition)
	}
	if metadata != nil {
		candidate.Metadata = *metadata
	}
	candidate.Files[name] = rulesetFileManifest(content, document)
	if err := ValidateRulesetSnapshot(candidate); err != nil {
		return RulesetSnapshot{}, fmt.Errorf("ruleset file %q: %w", name, err)
	}
	return candidate, nil
}

func DiffRulesetRegistries(oldRegistry, newRegistry RulesetRegistry) RulesetReplacementDiff {
	diff := RulesetReplacementDiff{}
	for id, definition := range newRegistry.Stats {
		old, exists := oldRegistry.Stats[id]
		if !exists {
			diff.Stats.Added = append(diff.Stats.Added, id)
		} else if !equalStatDefinition(old, definition) {
			diff.Stats.Changed = append(diff.Stats.Changed, id)
		}
	}
	for id := range oldRegistry.Stats {
		if _, exists := newRegistry.Stats[id]; !exists {
			diff.Stats.Removed = append(diff.Stats.Removed, id)
		}
	}
	for id, definition := range newRegistry.Actions {
		old, exists := oldRegistry.Actions[id]
		if !exists {
			diff.Actions.Added = append(diff.Actions.Added, id)
		} else if !equalActionDefinition(old, definition) {
			diff.Actions.Changed = append(diff.Actions.Changed, id)
		}
	}
	for id := range oldRegistry.Actions {
		if _, exists := newRegistry.Actions[id]; !exists {
			diff.Actions.Removed = append(diff.Actions.Removed, id)
		}
	}
	for id, definition := range newRegistry.Presets {
		old, exists := oldRegistry.Presets[id]
		if !exists {
			diff.Presets.Added = append(diff.Presets.Added, id)
		} else if !equalPresetDefinition(old, definition) {
			diff.Presets.Changed = append(diff.Presets.Changed, id)
		}
	}
	for id := range oldRegistry.Presets {
		if _, exists := newRegistry.Presets[id]; !exists {
			diff.Presets.Removed = append(diff.Presets.Removed, id)
		}
	}
	for _, changes := range []*DefinitionReplacementChanges{&diff.Stats, &diff.Actions, &diff.Presets} {
		sort.Strings(changes.Added)
		sort.Strings(changes.Changed)
		sort.Strings(changes.Removed)
	}
	return diff
}

// ParseDefinitionsTOML parses and validates a self-contained definitions
// document. Campaign imports that may reference an installed ruleset should be
// parsed through PreviewCampaignDefinitionsImport instead.
func ParseDefinitionsTOML(data []byte) (CampaignRegistry, error) {
	registry, metadata, err := decodeDefinitionsTOML(data, EffectiveRegistry{})
	if err != nil {
		return CampaignRegistry{}, err
	}
	if metadata != nil {
		return CampaignRegistry{}, fmt.Errorf("definitions TOML: ruleset metadata is not allowed in campaign definitions")
	}
	if _, err := MergeDefinitionRegistries(DefinitionRegistries{Campaign: registry}); err != nil {
		return CampaignRegistry{}, fmt.Errorf("definitions TOML: %w", err)
	}
	return registry, nil
}

func ParseRulesetTOML(data []byte) (RulesetSnapshot, error) {
	registry, metadata, err := decodeDefinitionsTOML(data, EffectiveRegistry{})
	if err != nil {
		return RulesetSnapshot{}, err
	}
	if metadata == nil {
		return RulesetSnapshot{}, fmt.Errorf("definitions TOML: ruleset metadata is required")
	}
	snapshot := RulesetSnapshot{
		Metadata: *metadata,
		Registry: RulesetRegistry{Stats: registry.Stats, Actions: registry.Actions, Presets: registry.Presets},
	}
	if err := ValidateRulesetSnapshot(snapshot); err != nil {
		return RulesetSnapshot{}, fmt.Errorf("definitions TOML: %w", err)
	}
	return snapshot, nil
}

// ExportDefinitionsTOML validates and canonically exports a self-contained
// registry. The TOML encoder sorts map keys and quotes keys as required.
func ExportDefinitionsTOML(registry CampaignRegistry) ([]byte, error) {
	if _, err := MergeDefinitionRegistries(DefinitionRegistries{Campaign: registry}); err != nil {
		return nil, fmt.Errorf("definitions TOML: %w", err)
	}
	return encodeDefinitionsTOML(registry)
}

// ExportCampaignDefinitionsTOML exports only campaign extensions after
// validating their references against the complete ruleset overlay.
func ExportCampaignDefinitionsTOML(registries DefinitionRegistries) ([]byte, error) {
	if _, err := MergeDefinitionRegistries(registries); err != nil {
		return nil, fmt.Errorf("definitions TOML: %w", err)
	}
	return encodeDefinitionsTOML(registries.Campaign)
}

// ExportRulesetSnapshotTOML exports the immutable installed snapshot together
// with its required metadata. Runtime characters and campaign overlays are not
// part of this document.
func ExportRulesetSnapshotTOML(snapshot RulesetSnapshot) ([]byte, error) {
	if err := ValidateRulesetSnapshot(snapshot); err != nil {
		return nil, fmt.Errorf("definitions TOML: %w", err)
	}
	registry := CampaignRegistry{
		Stats:   maps.Clone(snapshot.Registry.Stats),
		Actions: maps.Clone(snapshot.Registry.Actions),
		Presets: maps.Clone(snapshot.Registry.Presets),
	}
	metadata := snapshot.Metadata
	return encodeDefinitionsDocumentTOML(registry, &metadata)
}

// PreviewCampaignDefinitionsImport performs a pure all-or-nothing upsert.
// Definitions omitted from the document remain in the returned registry.
func PreviewCampaignDefinitionsImport(registries DefinitionRegistries, data []byte) (CampaignImportPreview, error) {
	base, err := MergeDefinitionRegistries(registries)
	if err != nil {
		return CampaignImportPreview{}, fmt.Errorf("current definitions: %w", err)
	}
	imported, metadata, err := decodeDefinitionsTOML(data, base)
	if err != nil {
		return CampaignImportPreview{}, err
	}
	if metadata != nil {
		return CampaignImportPreview{}, fmt.Errorf("definitions TOML: ruleset metadata is not allowed in campaign import")
	}

	result := cloneCampaignRegistry(registries.Campaign)
	diff := CampaignImportDiff{}
	for id, definition := range imported.Stats {
		old, exists := result.Stats[id]
		if !exists {
			diff.Stats.Added = append(diff.Stats.Added, id)
		} else if !equalStatDefinition(old, definition) {
			diff.Stats.Updated = append(diff.Stats.Updated, id)
		}
		result.Stats[id] = cloneStatDefinition(definition)
	}
	for id, definition := range imported.Actions {
		old, exists := result.Actions[id]
		if !exists {
			diff.Actions.Added = append(diff.Actions.Added, id)
		} else if !equalActionDefinition(old, definition) {
			diff.Actions.Updated = append(diff.Actions.Updated, id)
		}
		result.Actions[id] = cloneActionDefinition(definition)
	}
	for id, definition := range imported.Presets {
		old, exists := result.Presets[id]
		if !exists {
			diff.Presets.Added = append(diff.Presets.Added, id)
		} else if !equalPresetDefinition(old, definition) {
			diff.Presets.Updated = append(diff.Presets.Updated, id)
		}
		result.Presets[id] = clonePresetDefinition(definition)
	}

	candidate := registries
	candidate.Campaign = result
	merged, err := MergeDefinitionRegistries(candidate)
	if err != nil {
		return CampaignImportPreview{}, fmt.Errorf("definitions TOML: %w", err)
	}
	for id, importedDefinition := range imported.Stats {
		if oldDefinition, exists := base.Stats[id]; exists && oldDefinition.Type != importedDefinition.Type && statDefinitionReferenced(merged, id) {
			return CampaignImportPreview{}, fmt.Errorf("definitions TOML: cannot change type of referenced stat %q", id)
		}
	}
	sortImportDiff(&diff)
	return CampaignImportPreview{Registry: result, Diff: diff}, nil
}

func decodeDefinitionsTOML(data []byte, base EffectiveRegistry) (CampaignRegistry, *RulesetMetadata, error) {
	if len(data) > MaxDefinitionDocumentBytes {
		return CampaignRegistry{}, nil, fmt.Errorf("definitions TOML exceeds %d bytes", MaxDefinitionDocumentBytes)
	}
	var document definitionsTOMLDocument
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return CampaignRegistry{}, nil, fmt.Errorf("definitions TOML: %w", err)
	}
	if document.SchemaVersion != DefinitionsSchemaVersion {
		return CampaignRegistry{}, nil, fmt.Errorf("definitions TOML: unsupported schema_version %d", document.SchemaVersion)
	}
	if len(document.Stats) > MaxStatsPerRegistry || len(document.Actions) > MaxActionsPerRegistry || len(document.Presets) > MaxPresetsPerRegistry {
		return CampaignRegistry{}, nil, fmt.Errorf("definitions TOML exceeds registry limits")
	}

	registry := CampaignRegistry{
		Stats:   make(map[string]StatDefinition, len(document.Stats)),
		Actions: make(map[string]ActionDefinition, len(document.Actions)),
		Presets: make(map[string]CharacterPresetDefinition, len(document.Presets)),
	}
	knownStats := make(map[string]StatDefinition, len(base.Stats)+len(document.Stats))
	for id, definition := range base.Stats {
		knownStats[id] = cloneStatDefinition(definition)
	}
	for id, raw := range document.Stats {
		definition := StatDefinition{ID: id, Name: raw.Name, Type: StatType(raw.Type)}
		if raw.Default != nil {
			value, err := tomlStatValue(raw.Default, definition.Type)
			if err != nil {
				return CampaignRegistry{}, nil, fmt.Errorf("definitions TOML: stat %q default: %w", id, err)
			}
			definition.Default = &value
		}
		if err := ValidateStatDefinition(definition); err != nil {
			return CampaignRegistry{}, nil, fmt.Errorf("definitions TOML: %w", err)
		}
		registry.Stats[id] = definition
		knownStats[id] = definition
	}
	for id, raw := range document.Actions {
		definition := ActionDefinition{
			ID:          id,
			Name:        raw.Name,
			Description: raw.Description,
			Tags:        append([]string(nil), raw.Tags...),
			Rolls:       make([]RollSpec, len(raw.Rolls)),
		}
		for index, roll := range raw.Rolls {
			definition.Rolls[index] = RollSpec{
				ID: roll.ID, Name: roll.Name, Count: roll.Count, Sides: roll.Sides,
				ModifierStat: roll.ModifierStat, ModifierFixed: roll.ModifierFixed,
			}
		}
		registry.Actions[id] = definition
	}
	for id, raw := range document.Presets {
		definition := CharacterPresetDefinition{
			ID: id, Name: raw.Name, Kind: raw.Kind, AvatarAssetID: raw.Avatar,
			Stats: make(map[string]StatValue, len(raw.Stats)), ActionIDs: append([]string(nil), raw.Actions...),
		}
		for statID, rawValue := range raw.Stats {
			stat, exists := knownStats[statID]
			if !exists {
				return CampaignRegistry{}, nil, fmt.Errorf("definitions TOML: preset %q references unknown stat %q", id, statID)
			}
			value, err := tomlStatValue(rawValue, stat.Type)
			if err != nil {
				return CampaignRegistry{}, nil, fmt.Errorf("definitions TOML: preset %q stat %q: %w", id, statID, err)
			}
			definition.Stats[statID] = value
		}
		registry.Presets[id] = definition
	}
	var metadata *RulesetMetadata
	if document.Ruleset != nil {
		metadata = &RulesetMetadata{ID: document.Ruleset.ID, Name: document.Ruleset.Name, Version: document.Ruleset.Version}
	}
	return registry, metadata, nil
}

func encodeDefinitionsTOML(registry CampaignRegistry) ([]byte, error) {
	return encodeDefinitionsDocumentTOML(registry, nil)
}

func encodeDefinitionsDocumentTOML(registry CampaignRegistry, metadata *RulesetMetadata) ([]byte, error) {
	document := definitionsTOMLDocument{
		SchemaVersion: DefinitionsSchemaVersion,
		Stats:         make(map[string]statTOMLDefinition, len(registry.Stats)),
		Actions:       make(map[string]actionTOMLDefinition, len(registry.Actions)),
		Presets:       make(map[string]presetTOMLDefinition, len(registry.Presets)),
	}
	if metadata != nil {
		document.Ruleset = &rulesetTOMLMetadata{ID: metadata.ID, Name: metadata.Name, Version: metadata.Version}
	}
	for id, definition := range registry.Stats {
		raw := statTOMLDefinition{Name: definition.Name, Type: string(definition.Type)}
		if definition.Default != nil {
			raw.Default = definition.Default.Interface()
		}
		document.Stats[id] = raw
	}
	for id, definition := range registry.Actions {
		raw := actionTOMLDefinition{
			Name: definition.Name, Description: definition.Description,
			Tags: append([]string(nil), definition.Tags...), Rolls: make([]rollTOMLSpec, len(definition.Rolls)),
		}
		for index, roll := range definition.Rolls {
			raw.Rolls[index] = rollTOMLSpec{
				ID: roll.ID, Name: roll.Name, Count: roll.Count, Sides: roll.Sides,
				ModifierStat: roll.ModifierStat, ModifierFixed: roll.ModifierFixed,
			}
		}
		document.Actions[id] = raw
	}
	for id, definition := range registry.Presets {
		raw := presetTOMLDefinition{
			Name: definition.Name, Kind: definition.Kind, Avatar: definition.AvatarAssetID,
			Stats: make(map[string]any, len(definition.Stats)), Actions: append([]string(nil), definition.ActionIDs...),
		}
		for statID, value := range definition.Stats {
			raw.Stats[statID] = value.Interface()
		}
		document.Presets[id] = raw
	}
	data, err := toml.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode definitions TOML: %w", err)
	}
	if len(data) > MaxDefinitionDocumentBytes {
		return nil, fmt.Errorf("encoded definitions TOML exceeds %d bytes", MaxDefinitionDocumentBytes)
	}
	return data, nil
}

func tomlStatValue(raw any, expected StatType) (StatValue, error) {
	switch expected {
	case StatTypeNumber:
		switch value := raw.(type) {
		case int64:
			result := NumberStatValue(float64(value))
			return result, ValidateStatValue(result)
		case float64:
			result := NumberStatValue(value)
			return result, ValidateStatValue(result)
		}
	case StatTypeInteger:
		switch value := raw.(type) {
		case int64:
			result := IntegerStatValue(value)
			return result, ValidateStatValue(result)
		case float64:
			if value >= -float64(MaxSafeInteger) && value <= float64(MaxSafeInteger) && value == float64(int64(value)) {
				result := IntegerStatValue(int64(value))
				return result, ValidateStatValue(result)
			}
		}
	case StatTypeBoolean:
		if value, ok := raw.(bool); ok {
			return BooleanStatValue(value), nil
		}
	case StatTypeString:
		if value, ok := raw.(string); ok {
			result := StringStatValue(value)
			return result, ValidateStatValue(result)
		}
	default:
		return StatValue{}, fmt.Errorf("unknown stat type %q", expected)
	}
	return StatValue{}, fmt.Errorf("value has incompatible TOML type for %q", expected)
}

func sortImportDiff(diff *CampaignImportDiff) {
	for _, changes := range []*DefinitionImportChanges{&diff.Stats, &diff.Actions, &diff.Presets} {
		sort.Strings(changes.Added)
		sort.Strings(changes.Updated)
	}
}

func equalStatDefinition(left, right StatDefinition) bool {
	if left.ID != right.ID || left.Name != right.Name || left.Type != right.Type {
		return false
	}
	if left.Default == nil || right.Default == nil {
		return left.Default == nil && right.Default == nil
	}
	return *left.Default == *right.Default
}

func equalActionDefinition(left, right ActionDefinition) bool {
	return left.ID == right.ID && left.Name == right.Name && left.Description == right.Description &&
		slices.Equal(left.Tags, right.Tags) && slices.Equal(left.Rolls, right.Rolls)
}

func equalPresetDefinition(left, right CharacterPresetDefinition) bool {
	return left.ID == right.ID && left.Name == right.Name && left.Kind == right.Kind && left.AvatarAssetID == right.AvatarAssetID &&
		maps.Equal(left.Stats, right.Stats) && slices.Equal(left.ActionIDs, right.ActionIDs)
}

func statDefinitionReferenced(registry EffectiveRegistry, statID string) bool {
	for _, action := range registry.Actions {
		for _, roll := range action.Rolls {
			if roll.ModifierStat == statID {
				return true
			}
		}
	}
	for _, preset := range registry.Presets {
		if _, exists := preset.Stats[statID]; exists {
			return true
		}
	}
	return false
}
