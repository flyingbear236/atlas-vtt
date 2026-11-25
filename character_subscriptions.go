package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type statFieldsView struct {
	Description string          `json:"description,omitempty"`
	Base        any             `json:"base"`
	Derived     []statFieldView `json:"derived,omitempty"`
}

type statFieldView struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

func statFieldsForSheet(registry EffectiveRegistry, character EffectiveCharacter) map[string]statFieldsView {
	if registry.statPlan == nil {
		return nil
	}
	fields := make(map[string]statFieldsView)
	for _, address := range sortedDefinitionIDs(character.StatValues) {
		parts, err := parseRulesetPath(address)
		if err != nil {
			continue
		}
		if len(parts) < 3 || registry.Stats[rulesetPath(parts[1:])].V04 != nil {
			continue
		}
		for end := len(parts) - 1; end > 1; end-- {
			id := rulesetPath(parts[1:end])
			definition := registry.Stats[id]
			if definition.V04 == nil {
				continue
			}
			field := rulesetPath(parts[end:])
			view := fields[id]
			switch {
			case field == "base":
				view.Base = character.StatValues[address]
				view.Description, _ = definition.V04.values["description"].(string)
			case field != "current" || !definition.IsResource():
				view.Derived = append(view.Derived, statFieldView{Name: field, Value: character.StatValues[address]})
			}
			fields[id] = view
			break
		}
	}
	return fields
}

// Inventory properties are flattened for the sheet. Only the owning character
// receives this snapshot; rule mechanics and source IDs remain unchanged.
type inventorySheetField struct {
	Path  []string `json:"path"`
	Value string   `json:"value"`
}
type inventorySheetEffect struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Targets    []string `json:"targets,omitempty"`
	Formula    string   `json:"formula,omitempty"`
	Condition  string   `json:"condition,omitempty"`
	Stack      string   `json:"stack,omitempty"`
	Scope      string   `json:"scope,omitempty"`
	Value      *int64   `json:"value,omitempty"`
	Status     string   `json:"status"`
	State      string   `json:"state"` // active, conditional, inactive
	SourceID   string   `json:"sourceId,omitempty"`
	SourceName string   `json:"sourceName,omitempty"`
	SourceKind string   `json:"sourceKind,omitempty"` // item or character
}
type inventorySheetDetail struct {
	Effects     []inventorySheetEffect `json:"effects,omitempty"`
	Description string                 `json:"description,omitempty"`
	Fields      []inventorySheetField  `json:"fields,omitempty"`
}

func inventoryDetailsForSheet(registry EffectiveRegistry, items []InventoryItem, directGrants ...EffectGrant) map[string]inventorySheetDetail {
	if len(items) == 0 {
		return nil
	}
	result := make(map[string]inventorySheetDetail, len(items))
	// Same deterministic order as ActiveEffects for unique stacking.
	firstSource := map[string]string{}
	for _, item := range items {
		if !item.Equipped {
			continue
		}
		if registry.mechanics != nil {
			if object, ok := registry.mechanics.objects[item.ObjectID]; ok {
				for _, id := range object.effects {
					if firstSource[id] == "" || item.ID < firstSource[id] {
						firstSource[id] = item.ID
					}
				}
			}
		}
	}
	for _, grant := range directGrants {
		if grant.EffectID == "" || grant.SourceID == "" {
			continue
		}
		if firstSource[grant.EffectID] == "" || grant.SourceID < firstSource[grant.EffectID] {
			firstSource[grant.EffectID] = grant.SourceID
		}
	}
	for _, item := range items {
		object := registry.Objects[item.ObjectID]
		if object == nil {
			continue
		}
		detail := inventorySheetDetail{}
		detail.Description, _ = object.values["description"].(string)
		if mechanics := registry.mechanics; mechanics != nil {
			if compiled, ok := mechanics.objects[item.ObjectID]; ok {
				overrides := map[string]int64{}
				if item.Fields != nil {
					raw, ok := item.Fields.values["effect_overrides"].(map[string]any)
					if ok {
						for id, v := range raw {
							if n, ok := v.(int64); ok {
								overrides[id] = n
							}
						}
					}
				}
				for _, id := range compiled.effects {
					effect := mechanics.effects[id]
					if effect == nil {
						continue
					}
					def := registry.Effects[id]
					name := id
					if def != nil {
						if raw, ok := def.values["name"].(string); ok && raw != "" {
							name = raw
						}
					}
					entry := inventorySheetEffect{ID: "effect." + id, Name: name, Targets: append([]string(nil), effect.targets...), Formula: effect.formulaSource, Status: "Активен", State: "active", SourceID: item.ID, SourceName: inventoryItemName(registry.Objects, item), SourceKind: "item"}
					if def != nil {
						entry.Condition, _ = def.values["when"].(string)
					}
					if effect.unique {
						entry.Stack = "unique"
					}
					if effect.sourceScope {
						entry.Scope = "source"
					}
					if !item.Equipped {
						entry.Status, entry.State = "Снят", "inactive"
					} else if effect.unique && !effect.sourceScope && firstSource[id] != item.ID {
						entry.Status, entry.State = "Не складывается (unique)", "inactive"
					} else if entry.Condition != "" || effect.needsSource || effect.needsTarget || effect.sourceScope {
						entry.Status, entry.State = "Условие проверяется при расчёте", "conditional"
					}
					if effect.additiveEditable {
						value := effect.additiveValue
						if override, ok := overrides[id]; ok {
							value = override
						}
						entry.Value = &value
					}
					detail.Effects = append(detail.Effects, entry)
				}
			}
		}
		if len(detail.Description) > 2048 {
			detail.Description = strings.ToValidUTF8(detail.Description[:2048], "�")
		}
		fields := registry.itemBindings(item)
		for _, key := range []string{"id", "object", "equipped", "name", "description", "actions", "effects", "effect_overrides"} {
			delete(fields, key)
		}
		var walk func(map[string]any, []string)
		walk = func(m map[string]any, path []string) {
			if len(detail.Fields) >= 96 || len(path) > 5 {
				return
			}
			keys := make([]string, 0, len(m))
			for key := range m {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if len(detail.Fields) >= 96 {
					break
				}
				next := append(append([]string(nil), path...), key)
				if child, ok := m[key].(map[string]any); ok {
					if len(next) <= 5 {
						walk(child, next)
					}
					continue
				}
				value := fmt.Sprint(m[key])
				value = strings.ToValidUTF8(value, "�")
				if len(value) > 240 {
					value = value[:240]
					value = strings.ToValidUTF8(value, "�")
				}
				detail.Fields = append(detail.Fields, inventorySheetField{Path: next, Value: value})
			}
		}
		walk(fields, nil)
		result[item.ID] = detail
	}
	return result
}

// The same inventory effect entries power both the item drawer and the
// character-wide view. A direct character effect has no inventory source.
func characterEffectsForSheet(registry EffectiveRegistry, instance CharacterInstance, details map[string]inventorySheetDetail) []inventorySheetEffect {
	var result []inventorySheetEffect
	for _, item := range instance.Inventory {
		result = append(result, details[item.ID].Effects...)
	}
	if registry.mechanics == nil {
		return result
	}
	// The same sorted first-source rule is used by ActiveEffects for unique.
	firstItemSource := make(map[string]string)
	for _, item := range instance.Inventory {
		if !item.Equipped {
			continue
		}
		for _, entry := range details[item.ID].Effects {
			if firstItemSource[entry.ID] == "" || item.ID < firstItemSource[entry.ID] {
				firstItemSource[entry.ID] = item.ID
			}
		}
	}
	for _, id := range instance.EffectIDs {
		effect := registry.mechanics.effects[id]
		if effect == nil {
			continue
		}
		name := id
		if definition := registry.Effects[id]; definition != nil {
			if title, ok := definition.values["name"].(string); ok && title != "" {
				name = title
			}
		}
		entry := inventorySheetEffect{
			ID: "effect." + id, Name: name,
			Targets: append([]string(nil), effect.targets...),
			Formula: effect.formulaSource, Status: "Активен", State: "active",
			SourceID: instance.ID, SourceName: "У персонажа", SourceKind: "character",
		}
		if definition := registry.Effects[id]; definition != nil {
			entry.Condition, _ = definition.values["when"].(string)
		}
		if effect.unique {
			entry.Stack = "unique"
		}
		if effect.sourceScope {
			entry.Scope = "source"
		}
		if effect.additiveEditable {
			value := effect.additiveValue
			entry.Value = &value
		}
		// ActiveEffects selects the lowest source ID for stack=unique,
		// regardless of whether the grant came from an item or directly.
		if effect.unique && !effect.sourceScope && firstItemSource[entry.ID] != "" && firstItemSource[entry.ID] < instance.ID {
			entry.Status, entry.State = "Не складывается (unique)", "inactive"
		}
		if entry.State == "active" && (entry.Condition != "" || effect.needsSource || effect.needsTarget || effect.sourceScope) {
			entry.Status, entry.State = "Условие проверяется при расчёте", "conditional"
		}
		result = append(result, entry)
	}
	return result
}

type characterPermissions struct {
	Edit   bool `json:"edit"`
	Manage bool `json:"manage"`
}

type characterSnapshot struct {
	ActionSelectors       map[string]string                `json:"actionSelectors,omitempty"`
	ActionGroups          map[string]string                `json:"actionGroups,omitempty"`
	ActionPreviews        []ActionPreview                  `json:"actionPreviews,omitempty"`
	InactiveActionSources []ActionSource                   `json:"inactiveActionSources,omitempty"`
	ItemNames             map[string]string                `json:"itemNames,omitempty"`
	ItemDetails           map[string]inventorySheetDetail  `json:"itemDetails,omitempty"`
	CharacterEffects      []inventorySheetEffect           `json:"characterEffects,omitempty"`
	StatFields            map[string]statFieldsView        `json:"statFields,omitempty"`
	StatTraces            map[string]*statCalculationTrace `json:"statTraces,omitempty"`
	ExecutionID           string                           `json:"executionId,omitempty"`
	Type                  string                           `json:"type"`
	CharacterWatch        uint64                           `json:"characterWatch"`
	CharacterID           string                           `json:"characterId"`
	Revision              uint64                           `json:"revision"`
	RegistryRevision      uint64                           `json:"registryRevision"`
	Instance              CharacterInstance                `json:"instance"`
	Preset                *CharacterPresetDefinition       `json:"preset,omitempty"`
	PresetArtwork         ArtworkResolution                `json:"presetArtwork,omitempty"`
	Effective             EffectiveCharacter               `json:"effective"`
	StatDefinitions       map[string]StatDefinition        `json:"statDefinitions"`
	Actions               map[string]ActionDefinition      `json:"actions"`
	Permissions           characterPermissions             `json:"permissions"`
}

func characterSnapshotForMember(session *Session, member *Member, characterID string, watch uint64) (characterSnapshot, error) {
	if !memberCanAccessCharacter(session, member, characterID) {
		return characterSnapshot{}, errors.New("character access denied")
	}
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		return characterSnapshot{}, err
	}
	instance := cloneCharacterInstance(session.CharacterInstances[characterID])
	var preset *CharacterPresetDefinition
	if definition, exists := registry.Presets[instance.PresetID]; instance.PresetID != "" && exists {
		copy := clonePresetDefinition(definition)
		preset = &copy
	}
	var traces map[string]*statCalculationTrace
	if memberIsGM(member) && registry.statPlan != nil {
		traces = map[string]*statCalculationTrace{}
	}
	effective, err := projectCharacterStatsWithTrace(instance, preset, registry, nil, nil, false, traces)
	if err != nil {
		return characterSnapshot{}, err
	}
	presetArtwork := resolvePresetArtwork(preset, session.Ruleset, session.Assets)
	effective.Artwork = resolveCharacterArtwork(session, instance, preset, session.Ruleset)
	effective.AvatarAssetID = effective.Artwork.AssetID
	effective.HasAvatar = effective.Artwork.AssetID != ""
	stats := make(map[string]StatDefinition, len(effective.Stats))
	for statID := range effective.Stats {
		if definition, ok := registry.Stats[statID]; ok {
			stats[statID] = cloneStatDefinition(definition)
		}
	}
	for statID := range effective.Resources {
		if definition, ok := registry.Stats[statID]; ok {
			stats[statID] = cloneStatDefinition(definition)
		}
	}
	actions := make(map[string]ActionDefinition, len(effective.ActionIDs))
	for _, option := range effective.ActionSources {
		actions[option.ActionID] = cloneActionDefinition(registry.Actions[option.ActionID])
	}
	for _, actionID := range effective.ActionIDs {
		if definition, ok := registry.Actions[actionID]; ok {
			actions[actionID] = cloneActionDefinition(definition)
			for _, roll := range definition.Rolls {
				if stat, exists := registry.Stats[roll.ModifierStat]; roll.ModifierStat != "" && exists {
					stats[roll.ModifierStat] = cloneStatDefinition(stat)
				}
			}
		}
	}
	// Expose inactive item actions for the sheet only. They are never inserted
	// into EffectiveCharacter.ActionSources, so the executor still rejects them.
	var inactiveSources []ActionSource
	if mechanics := registry.mechanics; mechanics != nil {
		seen := make(map[ActionSource]bool)
		for _, item := range instance.Inventory {
			if item.Equipped {
				continue
			}
			for _, id := range mechanics.objects[item.ObjectID].actions {
				for _, leaf := range mechanics.actionLeaves[id] {
					if _, executable := session.Ruleset.actionPlans[leaf]; !executable {
						continue
					}
					choice := ActionSource{ActionID: leaf, SourceID: item.ID}
					if seen[choice] {
						continue
					}
					seen[choice] = true
					inactiveSources = append(inactiveSources, choice)
					if def, exists := registry.Actions[leaf]; exists {
						actions[leaf] = cloneActionDefinition(def)
					}
				}
			}
		}
	}
	selectors := map[string]string{}
	groups := map[string]string{}
	for id := range actions {
		if action, ok := session.Ruleset.actionPlans[id]; ok {
			selectors[id] = action.selector
			parts, _ := parseRulesetPath(id)
			for end := len(parts) - 1; end > 0; end-- {
				groupID := rulesetPath(parts[:end])
				if group, ok := registry.Actions[groupID]; ok {
					groups[id] = group.Name
					break
				}
			}
		}
	}
	itemNames := map[string]string{}
	for _, item := range instance.Inventory {
		itemNames[item.ID] = inventoryItemName(registry.Objects, item)
	}
	directEffects := make([]EffectGrant, 0, len(instance.EffectIDs))
	for _, id := range instance.EffectIDs {
		directEffects = append(directEffects, EffectGrant{EffectID: id, SourceID: instance.ID})
	}
	itemDetails := inventoryDetailsForSheet(registry, instance.Inventory, directEffects...)
	return characterSnapshot{
		ActionSelectors: selectors, ActionGroups: groups, ItemNames: itemNames,
		ItemDetails:           itemDetails,
		CharacterEffects:      characterEffectsForSheet(registry, instance, itemDetails),
		ActionPreviews:        actionPreviewsForSheet(session.Ruleset, instance, effective, registry),
		InactiveActionSources: inactiveSources,
		StatFields:            statFieldsForSheet(registry, effective),
		StatTraces:            traces,
		Type:                  "characterSnapshot", CharacterWatch: watch, CharacterID: characterID,
		Revision: session.CharacterRevision, RegistryRevision: session.RegistryRevision, Instance: instance, Preset: preset, PresetArtwork: presetArtwork, Effective: effective,
		StatDefinitions: stats, Actions: actions,
		Permissions: characterPermissions{Edit: true, Manage: memberIsGM(member)},
	}, nil
}

func (s *Server) closeCharacterWatch(peer *peer, revision uint64, reason string) {
	if peer.characterID == "" {
		return
	}
	characterID, watch := peer.characterID, peer.characterWatch
	peer.characterID = ""
	s.send(peer, map[string]any{
		"type": "characterWatchClosed", "characterWatch": watch, "characterId": characterID,
		"revision": revision, "reason": reason,
	})
}

func (s *Server) watchCharacter(session *Session, peer *peer, characterID string, watch uint64) {
	if watch == 0 || watch < peer.characterWatch {
		return
	}
	peer.characterWatch = watch
	peer.characterID = characterID
	snapshot, err := characterSnapshotForMember(session, peer.member, characterID, watch)
	if err != nil {
		s.closeCharacterWatch(peer, session.CharacterRevision, "accessRevoked")
		return
	}
	s.send(peer, snapshot)
}

func (s *Server) unwatchCharacter(peer *peer, characterID string, watch uint64) {
	if watch == 0 || watch < peer.characterWatch {
		return
	}
	peer.characterWatch = watch
	if characterID == "" || peer.characterID == characterID {
		peer.characterID = ""
	}
}

// refreshCharacterWatches runs only after a successful durable mutation. Each
// peer owns one watch slot, so access revocation and fan-out are bounded by the
// number of connected peers and never depend on viewport delivery.
func (s *Server) refreshCharacterWatches(session *Session, changed map[string]bool, all bool, executionID ...string) {
	for peer := range s.peers {
		if peer.session != session.ID || peer.characterID == "" {
			continue
		}
		characterID := peer.characterID
		if !memberCanAccessCharacter(session, peer.member, characterID) {
			s.closeCharacterWatch(peer, session.CharacterRevision, "accessRevoked")
			continue
		}
		if !all {
			if _, ok := changed[characterID]; !ok {
				continue
			}
		}
		snapshot, err := characterSnapshotForMember(session, peer.member, characterID, peer.characterWatch)
		if err != nil {
			s.closeCharacterWatch(peer, session.CharacterRevision, "unavailable")
			continue
		}
		if len(executionID) > 0 {
			snapshot.ExecutionID = executionID[0]
		}
		s.send(peer, snapshot)
	}
}

func (s *Server) publishCharacterTokens(session *Session, characterIDs map[string]bool) {
	for characterID, deleted := range characterIDs {
		if deleted {
			continue
		}
		for reference := range session.characterReferences[characterID] {
			scene := session.Scenes[reference.SceneID]
			if scene == nil {
				continue
			}
			token, exists := scene.Tokens[reference.TokenID]
			if exists {
				s.publish(session, scene.ID, "upsert", token, true, token)
			}
		}
	}
}
