package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Character-domain limits are kept together so import, commands and UI can use
// the same contract in later stages.
const (
	defaultCharacterName                = "Новый персонаж"
	MaxDefinitionIDRunes                = 128
	MaxDefinitionNameRunes              = 200
	MaxDefinitionDescriptionRunes       = 2000
	MaxStatStringRunes                  = 4000
	MaxStatsPerRegistry                 = 256
	MaxActionsPerRegistry               = 256
	MaxPresetsPerRegistry               = 256
	MaxStatsPerCharacter                = 128
	MaxActionsPerCharacter              = 128
	MaxCharacterInstances               = 100000
	MaxRollSpecsPerAction               = 16
	MaxTagsPerAction                    = 32
	MaxDefinitionDocumentBytes          = 1 << 20
	MaxDicePerRoll                      = 100
	MaxSafeInteger                int64 = 1<<53 - 1
)

type StatType string

const (
	StatTypeNumber  StatType = "number"
	StatTypeInteger StatType = "integer"
	StatTypeBoolean StatType = "boolean"
	StatTypeString  StatType = "string"
)

// StatValue is a tagged scalar. A missing map key remains distinguishable from
// zero, false and an empty string.
type StatValue struct {
	typ     StatType
	number  float64
	integer int64
	boolean bool
	text    string
}

func NumberStatValue(value float64) StatValue { return StatValue{typ: StatTypeNumber, number: value} }
func IntegerStatValue(value int64) StatValue  { return StatValue{typ: StatTypeInteger, integer: value} }
func BooleanStatValue(value bool) StatValue   { return StatValue{typ: StatTypeBoolean, boolean: value} }
func StringStatValue(value string) StatValue  { return StatValue{typ: StatTypeString, text: value} }

func (v StatValue) Type() StatType  { return v.typ }
func (v StatValue) Number() float64 { return v.number }
func (v StatValue) Integer() int64  { return v.integer }
func (v StatValue) Boolean() bool   { return v.boolean }
func (v StatValue) String() string  { return v.text }
func (v StatValue) Interface() any {
	switch v.typ {
	case StatTypeNumber:
		return v.number
	case StatTypeInteger:
		return v.integer
	case StatTypeBoolean:
		return v.boolean
	case StatTypeString:
		return v.text
	default:
		return nil
	}
}

func (v StatValue) MarshalJSON() ([]byte, error) {
	if err := ValidateStatValue(v); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Type  StatType `json:"type"`
		Value any      `json:"value"`
	}{Type: v.Type(), Value: v.Interface()})
}

func (v *StatValue) UnmarshalJSON(data []byte) error {
	if v == nil {
		return errors.New("stat value: nil receiver")
	}
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '{' {
		var tagged struct {
			Type  StatType        `json:"type"`
			Value json.RawMessage `json:"value"`
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&tagged); err != nil || tagged.Type == "" || tagged.Value == nil || bytes.Equal(bytes.TrimSpace(tagged.Value), []byte("null")) {
			return errors.New("stat value: invalid tagged value")
		}
		var parsed StatValue
		switch tagged.Type {
		case StatTypeNumber:
			if err := json.Unmarshal(tagged.Value, &parsed.number); err != nil {
				return fmt.Errorf("stat value: number: %w", err)
			}
			parsed.typ = StatTypeNumber
		case StatTypeInteger:
			if err := json.Unmarshal(tagged.Value, &parsed.integer); err != nil {
				return fmt.Errorf("stat value: integer: %w", err)
			}
			parsed.typ = StatTypeInteger
		case StatTypeBoolean:
			if err := json.Unmarshal(tagged.Value, &parsed.boolean); err != nil {
				return fmt.Errorf("stat value: boolean: %w", err)
			}
			parsed.typ = StatTypeBoolean
		case StatTypeString:
			if err := json.Unmarshal(tagged.Value, &parsed.text); err != nil {
				return fmt.Errorf("stat value: string: %w", err)
			}
			parsed.typ = StatTypeString
		default:
			return fmt.Errorf("stat value: invalid type %q", tagged.Type)
		}
		if err := ValidateStatValue(parsed); err != nil {
			return err
		}
		*v = parsed
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var raw any
	if err := decoder.Decode(&raw); err != nil {
		return fmt.Errorf("stat value: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("stat value: trailing JSON")
	}
	var parsed StatValue
	switch value := raw.(type) {
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			parsed = IntegerStatValue(integer)
		} else {
			number, err := value.Float64()
			if err != nil {
				return fmt.Errorf("stat value: invalid number")
			}
			if math.Trunc(number) == number {
				if math.Abs(number) > float64(MaxSafeInteger) {
					return fmt.Errorf("stat value: integer must be between %d and %d", -MaxSafeInteger, MaxSafeInteger)
				}
				parsed = IntegerStatValue(int64(number))
			} else {
				parsed = NumberStatValue(number)
			}
		}
	case bool:
		parsed = BooleanStatValue(value)
	case string:
		parsed = StringStatValue(value)
	default:
		return errors.New("stat value must be a number, integer, boolean, or string")
	}
	if err := ValidateStatValue(parsed); err != nil {
		return err
	}
	*v = parsed
	return nil
}

type StatKind string

const StatKindResource StatKind = "resource"

type StatDefinition struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Type    StatType   `json:"type"`
	Kind    StatKind   `json:"kind,omitempty"`
	Group   string     `json:"group,omitempty"`
	Default *StatValue `json:"default,omitempty"`
}

func (definition StatDefinition) IsResource() bool { return definition.Kind == StatKindResource }

type RollSpec struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Count         int     `json:"count"`
	Sides         int     `json:"sides"`
	ModifierStat  string  `json:"modifierStat,omitempty"`
	ModifierFixed float64 `json:"modifierFixed,omitempty"`
}

type ActionDefinition struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Group       string     `json:"group,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	Rolls       []RollSpec `json:"rolls,omitempty"`
}

type CharacterPresetDefinition struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Kind          string               `json:"kind,omitempty"`
	AvatarAssetID string               `json:"avatarAssetId,omitempty"`
	Artwork       string               `json:"artwork,omitempty"`
	Stats         map[string]StatValue `json:"stats,omitempty"`
	ActionIDs     []string             `json:"actionIds,omitempty"`
}

// CharacterMechanicalState is a complete, independent mechanical snapshot.
// Resource current values stay on CharacterInstance and are intentionally not
// part of materialization.
type CharacterMechanicalState struct {
	Stats           map[string]StatValue `json:"stats,omitempty"`
	ResourceInitial map[string]StatValue `json:"resourceInitial,omitempty"`
	ActionIDs       []string             `json:"actionIds,omitempty"`
}

type CharacterInstance struct {
	ID              string                    `json:"id"`
	PresetID        string                    `json:"presetId,omitempty"`
	Name            string                    `json:"name"`
	AvatarAssetID   *string                   `json:"avatarAssetId,omitempty"`
	ArtworkPath     string                    `json:"artworkPath,omitempty"`
	Materialized    *CharacterMechanicalState `json:"materialized,omitempty"`
	ResourceCurrent map[string]StatValue      `json:"resourceCurrent,omitempty"`
	// Legacy fields are read only during saved-session migration.
	StatOverrides    map[string]StatValue `json:"statOverrides,omitempty"`
	AddedActionIDs   []string             `json:"addedActionIds,omitempty"`
	RemovedActionIDs []string             `json:"removedActionIds,omitempty"`
	Persistent       bool                 `json:"persistent,omitempty"`
}

type RulesetRegistry struct {
	Stats   map[string]StatDefinition            `json:"stats,omitempty"`
	Actions map[string]ActionDefinition          `json:"actions,omitempty"`
	Presets map[string]CharacterPresetDefinition `json:"presets,omitempty"`
}

type CampaignRegistry struct {
	Stats   map[string]StatDefinition            `json:"stats,omitempty"`
	Actions map[string]ActionDefinition          `json:"actions,omitempty"`
	Presets map[string]CharacterPresetDefinition `json:"presets,omitempty"`
}

type DefinitionRegistries struct {
	Ruleset  RulesetRegistry
	Campaign CampaignRegistry
}

type EffectiveRegistry struct {
	Stats   map[string]StatDefinition            `json:"stats"`
	Actions map[string]ActionDefinition          `json:"actions"`
	Presets map[string]CharacterPresetDefinition `json:"presets"`
}

type EffectiveResource struct {
	Current StatValue `json:"current"`
	Initial StatValue `json:"initial"`
}

type EffectiveCharacter struct {
	ID            string                       `json:"id"`
	PresetID      string                       `json:"presetId,omitempty"`
	Name          string                       `json:"name"`
	AvatarAssetID string                       `json:"avatarAssetId,omitempty"`
	HasAvatar     bool                         `json:"hasAvatar"`
	Artwork       ArtworkResolution            `json:"artwork"`
	Stats         map[string]StatValue         `json:"stats"`
	Resources     map[string]EffectiveResource `json:"resources"`
	ActionIDs     []string                     `json:"actionIds"`
	Persistent    bool                         `json:"persistent"`
}

func NewCharacterInstance(instanceID string, preset *CharacterPresetDefinition) CharacterInstance {
	instance := CharacterInstance{ID: instanceID, Name: defaultCharacterName}
	if preset != nil {
		instance.PresetID = preset.ID
		instance.Name = preset.Name
	} else {
		instance.Materialized = &CharacterMechanicalState{Stats: map[string]StatValue{}, ResourceInitial: map[string]StatValue{}}
	}
	return instance
}

func MergeDefinitionRegistries(registries DefinitionRegistries) (EffectiveRegistry, error) {
	merged := EffectiveRegistry{
		Stats:   make(map[string]StatDefinition, len(registries.Ruleset.Stats)+len(registries.Campaign.Stats)),
		Actions: make(map[string]ActionDefinition, len(registries.Ruleset.Actions)+len(registries.Campaign.Actions)),
		Presets: make(map[string]CharacterPresetDefinition, len(registries.Ruleset.Presets)+len(registries.Campaign.Presets)),
	}
	for id, definition := range registries.Ruleset.Stats {
		merged.Stats[id] = cloneStatDefinition(definition)
	}
	for id, definition := range registries.Ruleset.Actions {
		merged.Actions[id] = cloneActionDefinition(definition)
	}
	for id, definition := range registries.Ruleset.Presets {
		merged.Presets[id] = clonePresetDefinition(definition)
	}
	for id, definition := range registries.Campaign.Stats {
		if _, exists := merged.Stats[id]; exists {
			return EffectiveRegistry{}, fmt.Errorf("duplicate stat %q across definition registries", id)
		}
		merged.Stats[id] = cloneStatDefinition(definition)
	}
	for id, definition := range registries.Campaign.Actions {
		if _, exists := merged.Actions[id]; exists {
			return EffectiveRegistry{}, fmt.Errorf("duplicate action %q across definition registries", id)
		}
		merged.Actions[id] = cloneActionDefinition(definition)
	}
	for id, definition := range registries.Campaign.Presets {
		if _, exists := merged.Presets[id]; exists {
			return EffectiveRegistry{}, fmt.Errorf("duplicate preset %q across definition registries", id)
		}
		merged.Presets[id] = clonePresetDefinition(definition)
	}
	if err := ValidateEffectiveRegistry(merged); err != nil {
		return EffectiveRegistry{}, err
	}
	return merged, nil
}

// WithInferredStatDefinitions returns an independent campaign registry with
// definitions for unknown values. Existing definitions are never changed, and
// any conflict returns the original registry untouched.
func WithInferredStatDefinitions(registries DefinitionRegistries, values map[string]StatValue) (CampaignRegistry, error) {
	result := cloneCampaignRegistry(registries.Campaign)
	for id, value := range values {
		if err := ValidateStatValue(value); err != nil {
			return CampaignRegistry{}, fmt.Errorf("stat %q: %w", id, err)
		}
		existing, ok := result.Stats[id]
		if !ok {
			existing, ok = registries.Ruleset.Stats[id]
		}
		if ok {
			if existing.Type != value.Type() {
				return CampaignRegistry{}, fmt.Errorf("stat %q requires %q, got %q", id, existing.Type, value.Type())
			}
			continue
		}
		if err := validateID(id); err != nil {
			return CampaignRegistry{}, fmt.Errorf("stat id: %w", err)
		}
		if len(result.Stats) >= MaxStatsPerRegistry {
			return CampaignRegistry{}, errors.New("campaign stat registry exceeds domain limit")
		}
		result.Stats[id] = StatDefinition{ID: id, Name: id, Type: value.Type()}
	}
	candidate := registries
	candidate.Campaign = result
	if _, err := MergeDefinitionRegistries(candidate); err != nil {
		return CampaignRegistry{}, err
	}
	return result, nil
}

func (r EffectiveRegistry) LookupStat(id string) (StatDefinition, bool) {
	definition, ok := r.Stats[id]
	return cloneStatDefinition(definition), ok
}

func (r EffectiveRegistry) LookupAction(id string) (ActionDefinition, bool) {
	definition, ok := r.Actions[id]
	return cloneActionDefinition(definition), ok
}

func (r EffectiveRegistry) LookupPreset(id string) (CharacterPresetDefinition, bool) {
	definition, ok := r.Presets[id]
	return clonePresetDefinition(definition), ok
}

// MaterializeCharacter freezes every effective mechanical field at once.
// The preset ID is preserved as provenance; name, artwork and resource current
// stay independent.
func MaterializeCharacter(instance CharacterInstance, preset *CharacterPresetDefinition, registry EffectiveRegistry) CharacterInstance {
	result := cloneCharacterInstance(instance)
	if result.Materialized != nil {
		return result
	}
	snapshot := &CharacterMechanicalState{Stats: map[string]StatValue{}, ResourceInitial: map[string]StatValue{}}
	if preset != nil {
		for statID, value := range preset.Stats {
			if definition, ok := registry.Stats[statID]; ok && definition.IsResource() {
				snapshot.ResourceInitial[statID] = value
			} else {
				snapshot.Stats[statID] = value
			}
		}
		snapshot.ActionIDs = appendUnique(nil, preset.ActionIDs...)
	}
	result.Materialized = snapshot
	result.StatOverrides = nil
	result.AddedActionIDs = nil
	result.RemovedActionIDs = nil
	return result
}

// migrateLegacyCharacterMechanics is called at the saved-session loading boundary.
func migrateLegacyCharacterMechanics(session *Session) (bool, error) {
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		return false, err
	}
	changed := false
	for id, original := range session.CharacterInstances {
		hasLegacy := len(original.StatOverrides) != 0 || len(original.AddedActionIDs) != 0 || len(original.RemovedActionIDs) != 0
		if original.Materialized != nil {
			if hasLegacy {
				return false, fmt.Errorf("character %q mixes legacy overrides with materialized state", id)
			}
			continue
		}
		if original.PresetID != "" && !hasLegacy {
			continue
		}
		var preset *CharacterPresetDefinition
		if original.PresetID != "" {
			definition, exists := registry.Presets[original.PresetID]
			if !exists {
				return false, fmt.Errorf("legacy character %q references unknown preset %q", id, original.PresetID)
			}
			preset = &definition
		}
		instance := MaterializeCharacter(original, preset, registry)
		for statID, value := range original.StatOverrides {
			if definition, ok := registry.Stats[statID]; ok && definition.IsResource() {
				instance.Materialized.ResourceInitial[statID] = value
			} else {
				instance.Materialized.Stats[statID] = value
			}
		}
		instance.Materialized.ActionIDs = appendUnique(instance.Materialized.ActionIDs, original.AddedActionIDs...)
		for _, actionID := range original.RemovedActionIDs {
			instance.Materialized.ActionIDs = removeString(instance.Materialized.ActionIDs, actionID)
		}
		session.CharacterInstances[id] = instance
		changed = true
	}
	return changed, nil
}

func EffectiveCharacterState(instance CharacterInstance, preset *CharacterPresetDefinition, registry EffectiveRegistry) EffectiveCharacter {
	effective := EffectiveCharacter{
		ID: instance.ID, PresetID: instance.PresetID, Name: instance.Name,
		Stats: map[string]StatValue{}, Resources: map[string]EffectiveResource{}, Persistent: instance.Persistent,
	}
	resourceInitial := map[string]StatValue{}
	if instance.Materialized != nil {
		effective.Stats = cloneStatValues(instance.Materialized.Stats)
		resourceInitial = cloneStatValues(instance.Materialized.ResourceInitial)
		effective.ActionIDs = append([]string(nil), instance.Materialized.ActionIDs...)
	} else if preset != nil {
		for statID, value := range preset.Stats {
			if definition, ok := registry.Stats[statID]; ok && definition.IsResource() {
				resourceInitial[statID] = value
			} else {
				effective.Stats[statID] = value
			}
		}
		effective.ActionIDs = appendUnique(nil, preset.ActionIDs...)
	}
	for statID, initial := range resourceInitial {
		current := initial
		if explicit, exists := instance.ResourceCurrent[statID]; exists {
			current = explicit
		}
		effective.Resources[statID] = EffectiveResource{Current: current, Initial: initial}
	}
	// Legacy asset IDs can be presented without a session; ruleset logical
	// artwork is resolved against committed media by the session caller.
	if preset != nil && preset.AvatarAssetID != "" {
		effective.AvatarAssetID = preset.AvatarAssetID
		effective.HasAvatar = true
	}
	if instance.AvatarAssetID != nil {
		effective.AvatarAssetID = *instance.AvatarAssetID
		effective.HasAvatar = *instance.AvatarAssetID != ""
	}
	return effective
}

func WithStatOverride(instance CharacterInstance, id string, value StatValue) CharacterInstance {
	result := cloneCharacterInstance(instance)
	if result.Materialized == nil {
		panic("WithStatOverride requires a materialized character")
	}
	result.Materialized.Stats[id] = value
	return result
}

// Reset removes a value from an already independent snapshot. It never resumes
// inheritance from the preset for just this stat.
func ResetStatOverride(instance CharacterInstance, id string) CharacterInstance {
	result := cloneCharacterInstance(instance)
	if result.Materialized != nil {
		delete(result.Materialized.Stats, id)
	}
	return result
}

func WithResourceInitial(instance CharacterInstance, id string, value StatValue) CharacterInstance {
	result := cloneCharacterInstance(instance)
	if result.Materialized == nil {
		panic("WithResourceInitial requires a materialized character")
	}
	if result.Materialized.ResourceInitial == nil {
		result.Materialized.ResourceInitial = map[string]StatValue{}
	}
	result.Materialized.ResourceInitial[id] = value
	return result
}

func WithResourceCurrent(instance CharacterInstance, id string, value StatValue) CharacterInstance {
	result := cloneCharacterInstance(instance)
	if result.ResourceCurrent == nil {
		result.ResourceCurrent = map[string]StatValue{}
	}
	result.ResourceCurrent[id] = value
	return result
}

func ResetResourceCurrent(instance CharacterInstance, id string) CharacterInstance {
	result := cloneCharacterInstance(instance)
	delete(result.ResourceCurrent, id)
	return result
}

func WithActionAdded(instance CharacterInstance, id string) CharacterInstance {
	result := cloneCharacterInstance(instance)
	if result.Materialized == nil {
		panic("WithActionAdded requires a materialized character")
	}
	result.Materialized.ActionIDs = appendUnique(result.Materialized.ActionIDs, id)
	return result
}

func WithActionRemoved(instance CharacterInstance, id string) CharacterInstance {
	result := cloneCharacterInstance(instance)
	if result.Materialized == nil {
		panic("WithActionRemoved requires a materialized character")
	}
	result.Materialized.ActionIDs = removeString(result.Materialized.ActionIDs, id)
	return result
}

func ResetAvatarOverride(instance CharacterInstance) CharacterInstance {
	result := cloneCharacterInstance(instance)
	result.AvatarAssetID = nil
	result.ArtworkPath = ""
	return result
}

func WithAvatarOverride(instance CharacterInstance, assetID string) CharacterInstance {
	result := cloneCharacterInstance(instance)
	result.AvatarAssetID = new(string)
	*result.AvatarAssetID = assetID
	result.ArtworkPath = ""
	return result
}

// ResolveAddedStatValue applies a default only for the explicit "add stat"
// operation. EffectiveCharacterState deliberately never calls it.
func ResolveAddedStatValue(definition StatDefinition, value *StatValue) (StatValue, error) {
	if value == nil {
		if definition.Default == nil {
			return StatValue{}, fmt.Errorf("stat %q has no value or default", definition.ID)
		}
		value = definition.Default
	}
	if err := ValidateStatValueForDefinition(definition, *value); err != nil {
		return StatValue{}, err
	}
	return *value, nil
}

func ValidateEffectiveRegistry(registry EffectiveRegistry) error {
	if len(registry.Stats) > MaxStatsPerRegistry || len(registry.Actions) > MaxActionsPerRegistry || len(registry.Presets) > MaxPresetsPerRegistry {
		return errors.New("definition registry exceeds domain limits")
	}
	for id, definition := range registry.Stats {
		if id != definition.ID {
			return fmt.Errorf("stat map key %q does not match id %q", id, definition.ID)
		}
		if err := ValidateStatDefinition(definition); err != nil {
			return err
		}
	}
	for id, definition := range registry.Actions {
		if id != definition.ID {
			return fmt.Errorf("action map key %q does not match id %q", id, definition.ID)
		}
		if err := validateActionDefinition(definition, registry.Stats); err != nil {
			return err
		}
	}
	for id, definition := range registry.Presets {
		if id != definition.ID {
			return fmt.Errorf("preset map key %q does not match id %q", id, definition.ID)
		}
		if err := validatePresetDefinition(definition, registry); err != nil {
			return err
		}
	}
	return nil
}

func ValidateCharacterInstance(instance CharacterInstance, registry EffectiveRegistry) error {
	if err := validateID(instance.ID); err != nil {
		return fmt.Errorf("character id: %w", err)
	}
	if err := validateText("character name", instance.Name, MaxDefinitionNameRunes, true); err != nil {
		return err
	}
	var linkedPreset *CharacterPresetDefinition
	if instance.PresetID != "" {
		if err := validateID(instance.PresetID); err != nil {
			return fmt.Errorf("preset id: %w", err)
		}
		if instance.Materialized == nil {
			preset, ok := registry.Presets[instance.PresetID]
			if !ok {
				return fmt.Errorf("linked character references unknown preset %q", instance.PresetID)
			}
			linkedPreset = &preset
		}
	} else if instance.Materialized == nil {
		return errors.New("standalone character requires materialized state")
	}
	if instance.ArtworkPath != "" {
		if err := validateRulesetMediaPath(instance.ArtworkPath); err != nil {
			return fmt.Errorf("character artwork: %w", err)
		}
		if instance.AvatarAssetID != nil {
			return errors.New("character artwork cannot have both path and asset override")
		}
	}
	if len(instance.StatOverrides) != 0 || len(instance.AddedActionIDs) != 0 || len(instance.RemovedActionIDs) != 0 {
		return errors.New("legacy mechanical overrides require migration")
	}
	if len(instance.ResourceCurrent) > MaxStatsPerCharacter {
		return errors.New("character resource current values exceed domain limits")
	}
	for id, value := range instance.ResourceCurrent {
		definition, ok := registry.Stats[id]
		if !ok {
			return fmt.Errorf("unknown resource current stat %q", id)
		}
		if !definition.IsResource() {
			return fmt.Errorf("stat %q is not a resource", id)
		}
		if err := ValidateStatValueForDefinition(definition, value); err != nil {
			return err
		}
		if instance.Materialized != nil {
			if _, exists := instance.Materialized.ResourceInitial[id]; !exists {
				return fmt.Errorf("resource %q current has no materialized initial value", id)
			}
		} else if linkedPreset != nil {
			if _, exists := linkedPreset.Stats[id]; !exists {
				return fmt.Errorf("resource %q current has no preset initial value", id)
			}
		}
	}
	if instance.Materialized == nil {
		return nil
	}
	snapshot := instance.Materialized
	if len(snapshot.Stats) > MaxStatsPerRegistry || len(snapshot.ResourceInitial) > MaxStatsPerCharacter || len(snapshot.ActionIDs) > MaxActionsPerRegistry {
		return errors.New("character mechanical snapshot exceeds domain limits")
	}
	for id, value := range snapshot.Stats {
		definition, ok := registry.Stats[id]
		if !ok {
			return fmt.Errorf("unknown stat %q", id)
		}
		if definition.IsResource() {
			return fmt.Errorf("resource stat %q must be stored as resource initial", id)
		}
		if err := ValidateStatValueForDefinition(definition, value); err != nil {
			return err
		}
	}
	for id, value := range snapshot.ResourceInitial {
		definition, ok := registry.Stats[id]
		if !ok {
			return fmt.Errorf("unknown resource initial stat %q", id)
		}
		if !definition.IsResource() {
			return fmt.Errorf("stat %q is not a resource", id)
		}
		if err := ValidateStatValueForDefinition(definition, value); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(snapshot.ActionIDs))
	for _, id := range snapshot.ActionIDs {
		if _, ok := registry.Actions[id]; !ok {
			return fmt.Errorf("unknown action %q", id)
		}
		if seen[id] {
			return fmt.Errorf("duplicate character action %q", id)
		}
		seen[id] = true
	}
	return nil
}

func ValidateStatDefinition(definition StatDefinition) error {
	if err := validateID(definition.ID); err != nil {
		return fmt.Errorf("stat id: %w", err)
	}
	if err := validateText("stat name", definition.Name, MaxDefinitionNameRunes, true); err != nil {
		return err
	}
	if err := validateText("stat group", definition.Group, MaxDefinitionNameRunes, true); err != nil {
		return err
	}
	switch definition.Type {
	case StatTypeNumber, StatTypeInteger, StatTypeBoolean, StatTypeString:
	default:
		return fmt.Errorf("stat %q has invalid type %q", definition.ID, definition.Type)
	}
	switch definition.Kind {
	case "", StatKindResource:
	default:
		return fmt.Errorf("stat %q has invalid kind %q", definition.ID, definition.Kind)
	}
	if definition.Default != nil {
		if err := ValidateStatValueForDefinition(definition, *definition.Default); err != nil {
			return fmt.Errorf("stat %q default: %w", definition.ID, err)
		}
	}
	return nil
}

func ValidateStatValue(value StatValue) error {
	switch value.typ {
	case StatTypeNumber:
		if math.IsNaN(value.number) || math.IsInf(value.number, 0) {
			return errors.New("number stat must be finite")
		}
	case StatTypeInteger:
		if value.integer < -MaxSafeInteger || value.integer > MaxSafeInteger {
			return fmt.Errorf("integer stat must be between %d and %d", -MaxSafeInteger, MaxSafeInteger)
		}
	case StatTypeBoolean:
	case StatTypeString:
		if !utf8.ValidString(value.text) || utf8.RuneCountInString(value.text) > MaxStatStringRunes {
			return errors.New("string stat exceeds domain limits")
		}
	default:
		return fmt.Errorf("invalid stat value type %q", value.typ)
	}
	return nil
}

func ValidateStatValueForDefinition(definition StatDefinition, value StatValue) error {
	if err := ValidateStatValue(value); err != nil {
		return fmt.Errorf("stat %q: %w", definition.ID, err)
	}
	if value.Type() != definition.Type {
		return fmt.Errorf("stat %q requires %q, got %q", definition.ID, definition.Type, value.Type())
	}
	return nil
}

func validateActionDefinition(definition ActionDefinition, stats map[string]StatDefinition) error {
	if err := validateID(definition.ID); err != nil {
		return fmt.Errorf("action id: %w", err)
	}
	if err := validateText("action name", definition.Name, MaxDefinitionNameRunes, false); err != nil {
		return err
	}
	if err := validateText("action description", definition.Description, MaxDefinitionDescriptionRunes, true); err != nil {
		return err
	}
	if err := validateText("action group", definition.Group, MaxDefinitionNameRunes, true); err != nil {
		return err
	}
	if len(definition.Tags) > MaxTagsPerAction || len(definition.Rolls) > MaxRollSpecsPerAction {
		return fmt.Errorf("action %q exceeds domain limits", definition.ID)
	}
	rollIDs := make(map[string]struct{}, len(definition.Rolls))
	for _, roll := range definition.Rolls {
		if err := validateID(roll.ID); err != nil {
			return fmt.Errorf("action %q roll id: %w", definition.ID, err)
		}
		if _, duplicate := rollIDs[roll.ID]; duplicate {
			return fmt.Errorf("action %q has duplicate roll %q", definition.ID, roll.ID)
		}
		rollIDs[roll.ID] = struct{}{}
		if err := validateText("roll name", roll.Name, MaxDefinitionNameRunes, false); err != nil {
			return err
		}
		if roll.Count < 1 || roll.Count > MaxDicePerRoll || !validDiceSides(roll.Sides) || math.IsNaN(roll.ModifierFixed) || math.IsInf(roll.ModifierFixed, 0) {
			return fmt.Errorf("action %q roll %q has invalid numeric fields", definition.ID, roll.ID)
		}
		if roll.ModifierStat != "" {
			stat, ok := stats[roll.ModifierStat]
			if !ok {
				return fmt.Errorf("action %q roll %q references unknown stat %q", definition.ID, roll.ID, roll.ModifierStat)
			}
			if stat.Type != StatTypeNumber && stat.Type != StatTypeInteger {
				return fmt.Errorf("action %q roll %q modifier stat %q is not numeric", definition.ID, roll.ID, roll.ModifierStat)
			}
		}
	}
	return nil
}

func validDiceSides(sides int) bool {
	switch sides {
	case 4, 6, 8, 10, 12, 20:
		return true
	default:
		return false
	}
}

func validatePresetDefinition(definition CharacterPresetDefinition, registry EffectiveRegistry) error {
	if err := validateID(definition.ID); err != nil {
		return fmt.Errorf("preset id: %w", err)
	}
	if err := validateText("preset name", definition.Name, MaxDefinitionNameRunes, false); err != nil {
		return err
	}
	switch definition.Kind {
	case "", "character", "npc", "monster":
	default:
		return fmt.Errorf("preset %q has invalid kind %q", definition.ID, definition.Kind)
	}
	if definition.Artwork != "" {
		if err := validateRulesetMediaPath(definition.Artwork); err != nil {
			return fmt.Errorf("preset %q artwork: %w", definition.ID, err)
		}
	}
	if len(definition.Stats) > MaxStatsPerCharacter || len(definition.ActionIDs) > MaxActionsPerCharacter {
		return fmt.Errorf("preset %q exceeds domain limits", definition.ID)
	}
	for id, value := range definition.Stats {
		stat, ok := registry.Stats[id]
		if !ok {
			return fmt.Errorf("preset %q references unknown stat %q", definition.ID, id)
		}
		if err := ValidateStatValueForDefinition(stat, value); err != nil {
			return fmt.Errorf("preset %q: %w", definition.ID, err)
		}
	}
	for _, id := range definition.ActionIDs {
		if _, ok := registry.Actions[id]; !ok {
			return fmt.Errorf("preset %q references unknown action %q", definition.ID, id)
		}
	}
	return nil
}

func validateID(id string) error {
	if id == "" || !utf8.ValidString(id) || strings.TrimSpace(id) != id || utf8.RuneCountInString(id) > MaxDefinitionIDRunes {
		return errors.New("must be non-empty valid Unicode without surrounding whitespace and within the length limit")
	}
	for _, r := range id {
		if unicode.IsControl(r) || r == '\u0000' {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}

func validateText(field, value string, maxRunes int, emptyAllowed bool) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes || (!emptyAllowed && strings.TrimSpace(value) == "") {
		return fmt.Errorf("%s is invalid or exceeds its length limit", field)
	}
	return nil
}

func cloneStatDefinition(definition StatDefinition) StatDefinition {
	if definition.Default != nil {
		value := *definition.Default
		definition.Default = &value
	}
	return definition
}

func cloneActionDefinition(definition ActionDefinition) ActionDefinition {
	definition.Tags = append([]string(nil), definition.Tags...)
	definition.Rolls = append([]RollSpec(nil), definition.Rolls...)
	return definition
}

func clonePresetDefinition(definition CharacterPresetDefinition) CharacterPresetDefinition {
	stats := definition.Stats
	definition.Stats = make(map[string]StatValue, len(stats))
	for id, value := range stats {
		definition.Stats[id] = value
	}
	definition.ActionIDs = append([]string(nil), definition.ActionIDs...)
	return definition
}

func cloneCharacterInstance(instance CharacterInstance) CharacterInstance {
	if instance.Materialized != nil {
		snapshot := *instance.Materialized
		snapshot.Stats = cloneStatValues(snapshot.Stats)
		snapshot.ResourceInitial = cloneStatValues(snapshot.ResourceInitial)
		snapshot.ActionIDs = append([]string(nil), snapshot.ActionIDs...)
		instance.Materialized = &snapshot
	}
	instance.ResourceCurrent = cloneStatValues(instance.ResourceCurrent)
	instance.StatOverrides = cloneStatValues(instance.StatOverrides)
	instance.AddedActionIDs = append([]string(nil), instance.AddedActionIDs...)
	instance.RemovedActionIDs = append([]string(nil), instance.RemovedActionIDs...)
	if instance.AvatarAssetID != nil {
		avatar := *instance.AvatarAssetID
		instance.AvatarAssetID = &avatar
	}
	return instance
}

func cloneCampaignRegistry(registry CampaignRegistry) CampaignRegistry {
	result := CampaignRegistry{
		Stats:   make(map[string]StatDefinition, len(registry.Stats)),
		Actions: make(map[string]ActionDefinition, len(registry.Actions)),
		Presets: make(map[string]CharacterPresetDefinition, len(registry.Presets)),
	}
	for id, definition := range registry.Stats {
		result.Stats[id] = cloneStatDefinition(definition)
	}
	for id, definition := range registry.Actions {
		result.Actions[id] = cloneActionDefinition(definition)
	}
	for id, definition := range registry.Presets {
		result.Presets[id] = clonePresetDefinition(definition)
	}
	return result
}

func cloneStatValues(values map[string]StatValue) map[string]StatValue {
	result := make(map[string]StatValue, len(values))
	for id, value := range values {
		result[id] = value
	}
	return result
}

func appendUnique(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	result := make([]string, 0, len(values)+len(additions))
	for _, value := range append(append([]string(nil), values...), additions...) {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func removeString(values []string, remove string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != remove {
			result = append(result, value)
		}
	}
	return result
}
