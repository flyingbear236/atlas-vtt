package main

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"sort"

	"github.com/pelletier/go-toml/v2"
)

const DefinitionsSchemaVersion = 1

type DefinitionImportChanges struct {
	Added   []string
	Updated []string
}

type CampaignImportDiff struct {
	Stats   DefinitionImportChanges
	Actions DefinitionImportChanges
	Presets DefinitionImportChanges
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
	document := definitionsTOMLDocument{
		SchemaVersion: DefinitionsSchemaVersion,
		Stats:         make(map[string]statTOMLDefinition, len(registry.Stats)),
		Actions:       make(map[string]actionTOMLDefinition, len(registry.Actions)),
		Presets:       make(map[string]presetTOMLDefinition, len(registry.Presets)),
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
