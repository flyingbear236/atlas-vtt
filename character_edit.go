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

	editLimit := MaxStatsPerCharacter
	if registry.statPlan != nil {
		editLimit = MaxStatsPerRegistry // the form may echo every ruleset default
	}
	if len(draft.Definitions) > MaxStatsPerRegistry || len(draft.Stats)+len(draft.ResourceInitial) > editLimit || len(draft.ResourceCurrent) > editLimit || len(draft.ActionIDs) > MaxActionsPerCharacter {
		return errors.New("character edit exceeds limits")
	}
	campaign := emptyCampaignRegistry()
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
	if len(draft.Definitions) > 0 {
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
		if _, hasInitial := draft.ResourceInitial[id]; !hasInitial && definition.V04 == nil {
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
	originalState := characterSourceState(existing, preset, registry)
	stats, initial := cloneStatValues(draft.Stats), cloneStatValues(draft.ResourceInitial)
	var displayedResources map[string]EffectiveResource
	if registry.statPlan != nil {
		displayed, err := EffectiveCharacterState(existing, preset, registry)
		if err != nil {
			return err
		}
		displayedResources = displayed.Resources
		for id, def := range registry.Stats {
			if def.V04 == nil {
				continue
			}
			submitted := stats
			shown := displayed.Stats[id]
			source, hasSource := originalState.Stats[id]
			if def.IsResource() {
				submitted = initial
				shown = displayed.Resources[id].Initial
				resource, exists := originalState.Resources[id]
				source, hasSource = resource.Initial, exists
			}
			if v, ok := submitted[id]; ok {
				if v == shown {
					if hasSource {
						submitted[id] = source
					} else {
						delete(submitted, id)
					}
				} else if err := validateStatBase(def, v); err != nil {
					return err
				}
			}
		}
	}
	originalInitial := make(map[string]StatValue, len(originalState.Resources))
	for id, resource := range originalState.Resources {
		originalInitial[id] = resource.Initial
	}
	changedMechanics := len(draft.Definitions) > 0 ||
		!maps.Equal(originalState.Stats, stats) ||
		!maps.Equal(originalInitial, initial) ||
		!slices.Equal(originalState.ActionIDs, draft.ActionIDs)
	if changedMechanics {
		instance.Materialized = &CharacterMechanicalState{
			Stats:           stats,
			ResourceInitial: initial,
			ActionIDs:       append([]string(nil), draft.ActionIDs...),
		}
	}
	instance.ResourceCurrent = make(map[string]StatValue, len(draft.ResourceCurrent))
	for id, current := range existing.ResourceCurrent {
		if registry.Stats[id].V04 != nil {
			instance.ResourceCurrent[id] = current
		}
	}
	for id, current := range draft.ResourceCurrent {
		if previous, ok := displayedResources[id]; ok && registry.Stats[id].V04 != nil && current == previous.Current {
			continue
		}
		if registry.Stats[id].V04 != nil || current != draft.ResourceInitial[id] {
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

	if len(draft.Definitions) > 0 {
		if err := mutation.setCampaign(campaign); err != nil {
			return err
		}
	}
	mutation.setInstance(instance)
	if artworkChanges > 0 {
		mutation.refreshAssets()
	}
	mutation.resultCharacterID = command.CharacterID
	return nil
}
