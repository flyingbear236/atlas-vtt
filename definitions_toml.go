package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	DefinitionsSchemaVersion = 1
	MaxRulesetFiles          = 32
	MaxRulesetPackageBytes   = MaxDefinitionDocumentBytes
)

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
	Kind    string `toml:"kind,omitempty"`
	Group   string `toml:"group,omitempty"`
	Default any    `toml:"default,omitempty"`
}

type statTOMLArrayDefinition struct {
	ID      string `toml:"id"`
	Name    string `toml:"name,omitempty"`
	Type    string `toml:"type"`
	Kind    string `toml:"kind,omitempty"`
	Group   string `toml:"group,omitempty"`
	Default any    `toml:"default,omitempty"`
}

type definitionsTOMLArrayDocument struct {
	SchemaVersion int                             `toml:"schema_version"`
	Ruleset       *rulesetTOMLMetadata            `toml:"ruleset,omitempty"`
	Stats         []statTOMLArrayDefinition       `toml:"stats,omitempty"`
	Actions       map[string]actionTOMLDefinition `toml:"actions,omitempty"`
	Presets       map[string]presetTOMLDefinition `toml:"presets,omitempty"`
}

type actionTOMLDefinition struct {
	Name        string         `toml:"name"`
	Description string         `toml:"description,omitempty"`
	Group       string         `toml:"group,omitempty"`
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
	Name    string         `toml:"name"`
	Kind    string         `toml:"kind,omitempty"`
	Avatar  string         `toml:"avatar,omitempty"`
	Artwork string         `toml:"artwork,omitempty"`
	Stats   map[string]any `toml:"stats,omitempty"`
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
	Stats      DefinitionReplacementChanges `json:"stats"`
	Actions    DefinitionReplacementChanges `json:"actions"`
	Presets    DefinitionReplacementChanges `json:"presets"`
	Effects    DefinitionReplacementChanges `json:"effects"`
	Objects    DefinitionReplacementChanges `json:"objects"`
	Characters DefinitionReplacementChanges `json:"characters"`
}

type RulesetDefinitionConflictError struct {
	Kind       string
	ID         string
	FirstFile  string
	SecondFile string
}

func (err *RulesetDefinitionConflictError) Error() string {
	return fmt.Sprintf("duplicate %s %q in %q and %q", err.Kind, err.ID, err.FirstFile, err.SecondFile)
}

func validateRulesetFileName(name string) error {
	if err := validateLibraryFilePath(name); err != nil {
		return err
	}
	if !strings.EqualFold(path.Ext(name), ".toml") {
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
		Source:      content,
		HasMetadata: document.Ruleset != nil,
		Stats:       sortedDefinitionIDs(document.Stats),
		Actions:     sortedDefinitionIDs(document.Actions),
		Presets:     sortedDefinitionIDs(document.Presets),
	}
}

func usesStatArrayTables(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		header := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		header = strings.ReplaceAll(strings.ReplaceAll(header, " ", ""), "\t", "")
		if header == "[[stats]]" {
			return true
		}
	}
	return false
}

func decodeDefinitionsTOMLDocument(data []byte) (definitionsTOMLDocument, error) {
	if !usesStatArrayTables(data) {
		var document definitionsTOMLDocument
		decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
		if err := decoder.Decode(&document); err != nil {
			return definitionsTOMLDocument{}, err
		}
		return document, nil
	}

	var arrayDocument definitionsTOMLArrayDocument
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := decoder.Decode(&arrayDocument); err != nil {
		return definitionsTOMLDocument{}, err
	}
	document := definitionsTOMLDocument{
		SchemaVersion: arrayDocument.SchemaVersion, Ruleset: arrayDocument.Ruleset,
		Stats: map[string]statTOMLDefinition{}, Actions: arrayDocument.Actions, Presets: arrayDocument.Presets,
	}
	for _, raw := range arrayDocument.Stats {
		if raw.ID == "" {
			return definitionsTOMLDocument{}, fmt.Errorf("stat array entry requires id")
		}
		if _, duplicate := document.Stats[raw.ID]; duplicate {
			return definitionsTOMLDocument{}, fmt.Errorf("duplicate stat %q", raw.ID)
		}
		document.Stats[raw.ID] = statTOMLDefinition{Name: raw.Name, Type: raw.Type, Kind: raw.Kind, Group: raw.Group, Default: raw.Default}
	}
	return document, nil
}

func decodeRulesetSourceFile(name, content string) (definitionsTOMLDocument, error) {
	if err := validateRulesetFileName(name); err != nil {
		return definitionsTOMLDocument{}, err
	}
	if len(content) > MaxDefinitionDocumentBytes {
		return definitionsTOMLDocument{}, fmt.Errorf("ruleset file %q exceeds %d bytes", name, MaxDefinitionDocumentBytes)
	}
	document, err := decodeDefinitionsTOMLDocument([]byte(content))
	if err != nil {
		return definitionsTOMLDocument{}, fmt.Errorf("ruleset file %q: %w", name, err)
	}
	if document.SchemaVersion != DefinitionsSchemaVersion {
		return definitionsTOMLDocument{}, fmt.Errorf("ruleset file %q: unsupported schema_version %d", name, document.SchemaVersion)
	}
	return document, nil
}

func CompileRulesetFiles(files map[string]string) (RulesetSnapshot, error) {
	if len(files) > MaxRulesetFiles {
		return RulesetSnapshot{}, fmt.Errorf("ruleset package may contain at most %d TOML files", MaxRulesetFiles)
	}
	if len(files) == 0 {
		return RulesetSnapshot{Registry: emptyRulesetRegistry(), Files: map[string]RulesetFileManifest{}}, nil
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
	documents := make(map[string]rulesetV04Document, len(files))
	for _, name := range names {
		if err := validateRulesetFileName(name); err != nil {
			return RulesetSnapshot{}, err
		}
		doc, v04, err := rulesetV04Source([]byte(files[name]))
		if err != nil {
			return RulesetSnapshot{}, fmt.Errorf("ruleset file %q: %w", name, err)
		}
		if v04 {
			documents[name] = doc
		}
	}
	if len(documents) == 0 {
		return compileLegacyRulesetFiles(files, names)
	}
	if len(documents) != len(files) {
		return RulesetSnapshot{}, fmt.Errorf("v0.4 ruleset cannot include legacy definition sources")
	}
	return compileRulesetV04(files, names, documents)
}

func compileLegacyRulesetFiles(files map[string]string, names []string) (RulesetSnapshot, error) {
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
			metadataSource = name
			metadata := *document.Ruleset
			merged.Ruleset = &metadata
		}
		for id, definition := range document.Stats {
			if source := statSource[id]; source != "" {
				return RulesetSnapshot{}, &RulesetDefinitionConflictError{Kind: "stat", ID: id, FirstFile: source, SecondFile: name}
			}
			statSource[id], merged.Stats[id] = name, definition
		}
		for id, definition := range document.Actions {
			if source := actionSource[id]; source != "" {
				return RulesetSnapshot{}, &RulesetDefinitionConflictError{Kind: "action", ID: id, FirstFile: source, SecondFile: name}
			}
			actionSource[id], merged.Actions[id] = name, definition
		}
		for id, definition := range document.Presets {
			if source := presetSource[id]; source != "" {
				return RulesetSnapshot{}, &RulesetDefinitionConflictError{Kind: "preset", ID: id, FirstFile: source, SecondFile: name}
			}
			presetSource[id], merged.Presets[id] = name, definition
		}
	}

	combined, err := toml.Marshal(merged)
	if err != nil {
		return RulesetSnapshot{}, fmt.Errorf("encode merged ruleset: %w", err)
	}
	registry, metadata, err := decodeDefinitionsTOML(combined)
	if err != nil {
		return RulesetSnapshot{}, err
	}
	snapshot := RulesetSnapshot{
		Registry: RulesetRegistry{Stats: registry.Stats, Actions: registry.Actions, Presets: registry.Presets},
		Files:    manifests,
	}
	if metadata != nil {
		snapshot.Metadata = *metadata
	}
	if err := ValidateRulesetSnapshot(snapshot); err != nil {
		return RulesetSnapshot{}, fmt.Errorf("ruleset package: %w", err)
	}
	return snapshot, nil
}

func rulesetSnapshotHasContent(snapshot RulesetSnapshot) bool {
	return snapshot.Metadata != (RulesetMetadata{}) ||
		len(snapshot.Registry.Stats) != 0 ||
		len(snapshot.Registry.Actions) != 0 ||
		len(snapshot.Registry.Presets) != 0 || len(snapshot.Registry.Effects) != 0 ||
		len(snapshot.Registry.Objects) != 0 || len(snapshot.Characters) != 0
}

func rulesetFileRegistry(snapshot RulesetSnapshot, manifest RulesetFileManifest) CampaignRegistry {
	registry := emptyCampaignRegistry()
	for _, id := range manifest.Stats {
		if definition, exists := snapshot.Registry.Stats[id]; exists {
			registry.Stats[id] = cloneStatDefinition(definition)
		}
	}
	for _, id := range manifest.Actions {
		if definition, exists := snapshot.Registry.Actions[id]; exists {
			registry.Actions[id] = cloneActionDefinition(definition)
		}
	}
	for _, id := range manifest.Presets {
		if definition, exists := snapshot.Registry.Presets[id]; exists {
			registry.Presets[id] = clonePresetDefinition(definition)
		}
	}
	return registry
}

// RulesetSnapshotWithSources upgrades legacy snapshots for editing without
// mutating persisted state. New snapshots always persist the original TOML
// source for every file.
func RulesetSnapshotWithSources(snapshot RulesetSnapshot) (RulesetSnapshot, error) {
	result := cloneRulesetSnapshot(snapshot)
	if len(result.Files) == 0 {
		if !rulesetSnapshotHasContent(result) {
			if result.Files == nil {
				result.Files = map[string]RulesetFileManifest{}
			}
			return result, nil
		}
		registry := CampaignRegistry{
			Stats:   maps.Clone(result.Registry.Stats),
			Actions: maps.Clone(result.Registry.Actions),
			Presets: maps.Clone(result.Registry.Presets),
		}
		var metadata *RulesetMetadata
		if result.Metadata != (RulesetMetadata{}) {
			copy := result.Metadata
			metadata = &copy
		}
		data, err := encodeDefinitionsDocumentTOML(registry, metadata)
		if err != nil {
			return RulesetSnapshot{}, err
		}
		var document definitionsTOMLDocument
		if err := toml.Unmarshal(data, &document); err != nil {
			return RulesetSnapshot{}, err
		}
		result.Files = map[string]RulesetFileManifest{"ruleset.toml": rulesetFileManifest(string(data), document)}
		return result, nil
	}

	for name, manifest := range result.Files {
		if manifest.Source != "" {
			continue
		}
		registry := rulesetFileRegistry(result, manifest)
		var metadata *RulesetMetadata
		if manifest.HasMetadata && result.Metadata != (RulesetMetadata{}) {
			copy := result.Metadata
			metadata = &copy
		}
		data, err := encodeDefinitionsDocumentTOML(registry, metadata)
		if err != nil {
			return RulesetSnapshot{}, fmt.Errorf("reconstruct ruleset file %q: %w", name, err)
		}
		manifest.Source = string(data)
		manifest.Digest = rulesetFileDigest(manifest.Source)
		result.Files[name] = manifest
	}
	return result, nil
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
	diff.Effects = diffRulesetFields(oldRegistry.Effects, newRegistry.Effects)
	diff.Objects = diffRulesetFields(oldRegistry.Objects, newRegistry.Objects)
	return diff
}

func diffRulesetFields(old, next map[string]*RulesetFields) DefinitionReplacementChanges {
	diff := DefinitionReplacementChanges{}
	for _, id := range sortedDefinitionIDs(next) {
		if value, exists := old[id]; !exists {
			diff.Added = append(diff.Added, id)
		} else if !equalRulesetFields(value, next[id]) {
			diff.Changed = append(diff.Changed, id)
		}
	}
	for _, id := range sortedDefinitionIDs(old) {
		if _, exists := next[id]; !exists {
			diff.Removed = append(diff.Removed, id)
		}
	}
	return diff
}

func diffRulesetSnapshots(old, next RulesetSnapshot) RulesetReplacementDiff {
	diff := DiffRulesetRegistries(old.Registry, next.Registry)
	diff.Characters = diffRulesetFields(old.Characters, next.Characters)
	return diff
}

// ParseDefinitionsTOML validates a self-contained versioned definitions document.
func ParseDefinitionsTOML(data []byte) (CampaignRegistry, error) {
	registry, metadata, err := decodeDefinitionsTOML(data)
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
	snapshot, err := CompileRulesetFiles(map[string]string{"ruleset.toml": string(data)})
	if err != nil {
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

// ExportRulesetSnapshotTOML exports one TOML document. A single v0.4 source
// can be returned verbatim; ZIP export retains every source in a package.
func ExportRulesetSnapshotTOML(snapshot RulesetSnapshot) ([]byte, error) {
	if err := ValidateRulesetSnapshot(snapshot); err != nil {
		return nil, fmt.Errorf("definitions TOML: %w", err)
	}
	if snapshot.usesV04() {
		if len(snapshot.Files) == 1 {
			for name := range snapshot.Files {
				source, err := RenderRulesetFileTOML(snapshot, name)
				return []byte(source), err
			}
		}
		data := encodeRulesetV04(snapshot.v04Document(nil))
		if len(data) > MaxDefinitionDocumentBytes {
			return nil, fmt.Errorf("encoded definitions TOML exceeds %d bytes", MaxDefinitionDocumentBytes)
		}
		return data, nil
	}
	registry := CampaignRegistry{
		Stats:   maps.Clone(snapshot.Registry.Stats),
		Actions: maps.Clone(snapshot.Registry.Actions),
		Presets: maps.Clone(snapshot.Registry.Presets),
	}
	var metadata *RulesetMetadata
	if snapshot.Metadata != (RulesetMetadata{}) {
		copy := snapshot.Metadata
		metadata = &copy
	}
	return encodeDefinitionsDocumentTOML(registry, metadata)
}

func decodeDefinitionsTOML(data []byte) (CampaignRegistry, *RulesetMetadata, error) {
	if len(data) > MaxDefinitionDocumentBytes {
		return CampaignRegistry{}, nil, fmt.Errorf("definitions TOML exceeds %d bytes", MaxDefinitionDocumentBytes)
	}
	document, err := decodeDefinitionsTOMLDocument(data)
	if err != nil {
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
	for id, raw := range document.Stats {
		definition := StatDefinition{ID: id, Name: raw.Name, Type: StatType(raw.Type), Kind: StatKind(raw.Kind), Group: raw.Group}
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
	}
	for id, raw := range document.Actions {
		definition := ActionDefinition{
			ID:          id,
			Name:        raw.Name,
			Description: raw.Description, Group: raw.Group,
			Tags:  append([]string(nil), raw.Tags...),
			Rolls: make([]RollSpec, len(raw.Rolls)),
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
			ID: id, Name: raw.Name, Kind: raw.Kind, AvatarAssetID: raw.Avatar, Artwork: raw.Artwork,
			Stats: make(map[string]StatValue, len(raw.Stats)), ActionIDs: append([]string(nil), raw.Actions...),
		}
		for statID, rawValue := range raw.Stats {
			stat, exists := registry.Stats[statID]
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
		raw := statTOMLDefinition{Name: definition.Name, Type: string(definition.Type), Kind: string(definition.Kind), Group: definition.Group}
		if definition.Default != nil {
			raw.Default = definition.Default.Interface()
		}
		document.Stats[id] = raw
	}
	for id, definition := range registry.Actions {
		raw := actionTOMLDefinition{
			Name: definition.Name, Description: definition.Description, Group: definition.Group,
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
			Name: definition.Name, Kind: definition.Kind, Avatar: definition.AvatarAssetID, Artwork: definition.Artwork,
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

func equalStatDefinition(left, right StatDefinition) bool {
	if left.ID != right.ID || left.Name != right.Name || left.Type != right.Type || left.Kind != right.Kind || left.Group != right.Group || !equalRulesetFields(left.V04, right.V04) {
		return false
	}
	if left.Default == nil || right.Default == nil {
		return left.Default == nil && right.Default == nil
	}
	return *left.Default == *right.Default
}

func equalActionDefinition(left, right ActionDefinition) bool {
	return left.ID == right.ID && left.Name == right.Name && left.Description == right.Description && left.Group == right.Group &&
		slices.Equal(left.Tags, right.Tags) && slices.Equal(left.Rolls, right.Rolls) && equalRulesetFields(left.V04, right.V04)
}

func equalPresetDefinition(left, right CharacterPresetDefinition) bool {
	return left.ID == right.ID && left.Name == right.Name && left.Kind == right.Kind && left.AvatarAssetID == right.AvatarAssetID && left.Artwork == right.Artwork &&
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
