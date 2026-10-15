package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// CharacterEditPayload replaces the complete mechanical state in one persisted command.
// It is submitted only by the GM. ResourceCurrent remains separate from materialization.
type CharacterEditPayload struct {
	ExpectedCharacterRevision uint64               `json:"expectedCharacterRevision"`
	ExpectedInstance          *CharacterInstance   `json:"expectedInstance"`
	ExpectedRegistryRevision  uint64               `json:"expectedRegistryRevision"`
	Name                      string               `json:"name"`
	Stats                     map[string]StatValue `json:"stats"`
	ResourceInitial           map[string]StatValue `json:"resourceInitial"`
	ResourceCurrent           map[string]StatValue `json:"resourceCurrent"`
	ActionIDs                 []string             `json:"actionIds"`
	Definitions               []StatDefinition     `json:"definitions,omitempty"`
	NewActions                []ActionDefinition   `json:"newActions,omitempty"`
	ResetAvatar               bool                 `json:"resetAvatar,omitempty"`
	AvatarAssetID             string               `json:"avatarAssetId,omitempty"`
	ArtworkPath               string               `json:"artworkPath,omitempty"`
	PlaceholderAvatar         bool                 `json:"placeholderAvatar,omitempty"`
}

func applyCharacterEdit(session *Session, command Command, registry EffectiveRegistry, mutation *characterMutation) error {
	draft := command.CharacterEdit
	if draft == nil {
		return errors.New("character edit payload is required")
	}
	if draft.ExpectedRegistryRevision != session.RegistryRevision {
		return errors.New("character or registry changed; reload before saving")
	}
	existing, ok := session.CharacterInstances[command.CharacterID]
	if !ok {
		return errors.New("character not found")
	}
	if draft.ExpectedInstance == nil || draft.ExpectedInstance.ID != command.CharacterID {
		return errors.New("expected character snapshot is required")
	}
	originalJSON, err := json.Marshal(existing)
	if err != nil {
		return err
	}
	expectedJSON, err := json.Marshal(draft.ExpectedInstance)
	if err != nil {
		return err
	}
	if !bytes.Equal(originalJSON, expectedJSON) {
		return errors.New("character changed; reload before saving")
	}

	if len(draft.Definitions) > MaxStatsPerRegistry || len(draft.NewActions) > MaxActionsPerRegistry || len(draft.Stats)+len(draft.ResourceInitial) > MaxStatsPerCharacter || len(draft.ResourceCurrent) > MaxStatsPerCharacter || len(draft.ActionIDs) > MaxActionsPerCharacter {
		return errors.New("character edit exceeds limits")
	}
	campaign := cloneCampaignRegistry(session.CampaignDefinitions)
	if campaign.Stats == nil {
		campaign.Stats = map[string]StatDefinition{}
	}
	for _, definition := range draft.Definitions {
		if definition.Type == StatTypeNumber {
			return errors.New("new fractional stats are unsupported")
		}
		if err := ValidateStatDefinition(definition); err != nil {
			return err
		}
		if _, exists := registry.Stats[definition.ID]; exists {
			return fmt.Errorf("stat %q already exists", definition.ID)
		}
		if _, exists := campaign.Stats[definition.ID]; exists {
			return fmt.Errorf("stat %q duplicated", definition.ID)
		}
		campaign.Stats[definition.ID] = definition
	}
	if campaign.Actions == nil {
		campaign.Actions = map[string]ActionDefinition{}
	}
	for _, definition := range draft.NewActions {
		if _, exists := registry.Actions[definition.ID]; exists {
			return fmt.Errorf("action %q already exists", definition.ID)
		}
		if _, exists := campaign.Actions[definition.ID]; exists {
			return fmt.Errorf("action %q duplicated", definition.ID)
		}
		campaign.Actions[definition.ID] = definition
	}
	if len(draft.Definitions) > 0 || len(draft.NewActions) > 0 {
		merged, err := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: session.Ruleset.Registry, Campaign: campaign})
		if err != nil {
			return err
		}
		registry = merged
	}
	for id, value := range draft.Stats {
		definition, ok := registry.Stats[id]
		if !ok || definition.IsResource() {
			return fmt.Errorf("unknown characteristic %q", id)
		}
		if err := ValidateStatValueForDefinition(definition, value); err != nil {
			return err
		}
	}
	for id, value := range draft.ResourceInitial {
		definition, ok := registry.Stats[id]
		if !ok || !definition.IsResource() {
			return fmt.Errorf("unknown tracked characteristic %q", id)
		}
		if err := ValidateStatValueForDefinition(definition, value); err != nil {
			return err
		}
	}
	for id, value := range draft.ResourceCurrent {
		definition, ok := registry.Stats[id]
		if !ok || !definition.IsResource() {
			return fmt.Errorf("unknown tracked characteristic %q", id)
		}
		if _, hasInitial := draft.ResourceInitial[id]; !hasInitial {
			return fmt.Errorf("missing initial value for %q", id)
		}
		if err := ValidateStatValueForDefinition(definition, value); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(draft.ActionIDs))
	for _, id := range draft.ActionIDs {
		if _, exists := registry.Actions[id]; !exists || seen[id] {
			return fmt.Errorf("unknown or duplicated action %q", id)
		}
		seen[id] = true
	}
	name := strings.TrimSpace(draft.Name)
	if err := validateText("character name", name, MaxDefinitionNameRunes, true); err != nil {
		return err
	}
	instance := cloneCharacterInstance(existing)
	instance.Name = name
	var preset *CharacterPresetDefinition
	if instance.Materialized == nil && instance.PresetID != "" {
		p, exists := registry.Presets[instance.PresetID]
		if !exists {
			return fmt.Errorf("unknown preset %q", instance.PresetID)
		}
		preset = &p
	}
	originalState := EffectiveCharacterState(existing, preset, registry)
	originalInitial := make(map[string]StatValue, len(originalState.Resources))
	for id, resource := range originalState.Resources {
		originalInitial[id] = resource.Initial
	}
	changedMechanics := len(draft.Definitions) > 0 || len(draft.NewActions) > 0 ||
		!maps.Equal(originalState.Stats, draft.Stats) ||
		!maps.Equal(originalInitial, draft.ResourceInitial) ||
		!slices.Equal(originalState.ActionIDs, draft.ActionIDs)
	if changedMechanics {
		instance.Materialized = &CharacterMechanicalState{
			Stats:           cloneStatValues(draft.Stats),
			ResourceInitial: cloneStatValues(draft.ResourceInitial),
			ActionIDs:       append([]string(nil), draft.ActionIDs...),
		}
		instance.StatOverrides = nil
		instance.AddedActionIDs = nil
		instance.RemovedActionIDs = nil
	}
	instance.ResourceCurrent = make(map[string]StatValue, len(draft.ResourceCurrent))
	for id, current := range draft.ResourceCurrent {
		if current != draft.ResourceInitial[id] {
			instance.ResourceCurrent[id] = current
		}
	}
	artworkChanges := 0
	if draft.ResetAvatar {
		artworkChanges++
	}
	if draft.PlaceholderAvatar {
		artworkChanges++
	}
	if draft.ArtworkPath != "" {
		artworkChanges++
	}
	if draft.AvatarAssetID != "" {
		artworkChanges++
	}
	if artworkChanges > 1 {
		return errors.New("only one avatar operation is allowed")
	}
	if draft.ArtworkPath != "" {
		if err := validateRulesetMediaPath(draft.ArtworkPath); err != nil {
			return err
		}
		assetID, exists := session.Ruleset.Media[draft.ArtworkPath]
		if !exists || !isCharacterArtworkAsset(session.Assets[assetID]) {
			return errors.New("artwork path is not registered in media")
		}
		instance.ArtworkPath = draft.ArtworkPath
		instance.AvatarAssetID = nil
	} else if draft.PlaceholderAvatar {
		instance = WithAvatarOverride(instance, "")
	} else if draft.AvatarAssetID != "" {
		asset, exists := session.Assets[draft.AvatarAssetID]
		if !exists || asset.Kind != assetKindAvatar {
			return errors.New("prepared avatar is not available")
		}
		instance = WithAvatarOverride(instance, draft.AvatarAssetID)
	} else if draft.ResetAvatar {
		instance = ResetAvatarOverride(instance)
	}

	if len(draft.Definitions) > 0 || len(draft.NewActions) > 0 {
		mutation.setCampaign(campaign)
	}
	mutation.setInstance(instance)
	if artworkChanges > 0 {
		mutation.refreshAssets()
	}
	mutation.resultCharacterID = command.CharacterID
	return nil
}
