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

// A calculation records the values actually produced by the evaluator. The
// source identity is an inventory instance ID, never an object definition ID.
type statEffectTrace struct {
	EffectID string `json:"effectId"`
	SourceID string `json:"sourceId"`
	Item     bool   `json:"item"`
	Before   any    `json:"before"`
	After    any    `json:"after"`
	Formula  string `json:"formula,omitempty"`
	Editable bool   `json:"editable,omitempty"`
	Value    int64  `json:"value"`
}
type statEffectSkip struct {
	EffectID string `json:"effectId"`
	Reason   string `json:"reason"`
}
type statCalculationTrace struct {
	Skipped []statEffectSkip  `json:"skipped,omitempty"`
	Initial any               `json:"initial"`
	Result  any               `json:"result,omitempty"`
	Effects []statEffectTrace `json:"effects,omitempty"`
}

type characterTraceChange struct {
	Kind     string `json:"kind"`
	StatID   string `json:"statId,omitempty"`
	ItemID   string `json:"itemId,omitempty"`
	EffectID string `json:"effectId,omitempty"`
	Target   string `json:"target,omitempty"`
	Value    int64  `json:"value"`
}
type characterTraceEdit struct {
	ExpectedInstance         *CharacterInstance     `json:"expectedInstance"`
	ExpectedRegistryRevision uint64                 `json:"expectedRegistryRevision"`
	Changes                  []characterTraceChange `json:"changes"`
}

func applyCharacterTraceEdit(session *Session, command Command, registry EffectiveRegistry, mutation *characterMutation) error {
	draft := command.TraceEdit
	if draft == nil || draft.ExpectedInstance == nil || len(draft.Changes) == 0 || len(draft.Changes) > 64 {
		return errors.New("invalid trace edit payload")
	}
	if draft.ExpectedRegistryRevision != session.RegistryRevision {
		return errors.New("registry changed; reload before saving")
	}
	existing, ok := session.CharacterInstances[command.CharacterID]
	if !ok || draft.ExpectedInstance.ID != existing.ID {
		return errors.New("character not found or invalid expected snapshot")
	}
	old, err := json.Marshal(existing)
	if err != nil {
		return err
	}
	expected, err := json.Marshal(draft.ExpectedInstance)
	if err != nil {
		return err
	}
	if !bytes.Equal(old, expected) {
		return errors.New("character changed; reload before saving")
	}
	instance := cloneCharacterInstance(existing)
	seen := map[string]bool{}
	for _, change := range draft.Changes {
		key := change.Kind + ":" + change.StatID + ":" + change.ItemID + ":" + change.EffectID + ":" + change.Target
		if seen[key] {
			return fmt.Errorf("duplicate trace change %q", key)
		}
		seen[key] = true
		switch change.Kind {
		case "statField":
			definition, ok := registry.Stats[change.StatID]
			if !ok || definition.V04 == nil || registry.statPlan == nil ||
				!strings.HasPrefix(change.Target, "stat."+change.StatID+".") ||
				change.Target == "stat."+change.StatID+".base" ||
				change.Target == "stat."+change.StatID+".current" {
				return fmt.Errorf("invalid stat field override %q", change.Target)
			}
			literal, ok := registry.statPlan.constants[change.Target].(int64)
			if !ok || change.Value < -MaxSafeInteger || change.Value > MaxSafeInteger {
				return fmt.Errorf("stat field %q is not an editable integer constant", change.Target)
			}
			instance.StatFieldOverrides = maps.Clone(instance.StatFieldOverrides)
			if change.Value == literal {
				delete(instance.StatFieldOverrides, change.Target)
			} else {
				if instance.StatFieldOverrides == nil {
					instance.StatFieldOverrides = map[string]int64{}
				}
				instance.StatFieldOverrides[change.Target] = change.Value
			}
		case "statBase":
			def, ok := registry.Stats[change.StatID]
			if !ok || def.IsResource() || def.Type != StatTypeInteger || def.V04 == nil {
				return fmt.Errorf("stat %q cannot be edited here", change.StatID)
			}
			if raw, ok := def.V04.values["default"].(map[string]any); ok && raw["formula"] != nil {
				return fmt.Errorf("stat %q has a computed base", change.StatID)
			}
			var preset *CharacterPresetDefinition
			if defPreset, ok := registry.Presets[instance.PresetID]; ok {
				preset = &defPreset
			}
			instance = MaterializeCharacter(instance, preset, registry)
			instance = WithStatOverride(instance, change.StatID, IntegerStatValue(change.Value))
		case "resourceCurrent":
			def, ok := registry.Stats[change.StatID]
			if !ok || !def.IsResource() || def.Type != StatTypeInteger {
				return fmt.Errorf("resource %q cannot be edited here", change.StatID)
			}
			instance = WithResourceCurrent(instance, change.StatID, IntegerStatValue(change.Value))
		case "itemEffect":
			index := slices.IndexFunc(instance.Inventory, func(item InventoryItem) bool { return item.ID == change.ItemID })
			if index < 0 || !instance.Inventory[index].Equipped {
				return fmt.Errorf("item %q is missing or unequipped", change.ItemID)
			}
			item := instance.Inventory[index]
			object := registry.mechanics.objects[item.ObjectID]
			if !slices.Contains(object.effects, change.EffectID) {
				return fmt.Errorf("item %q does not grant %q", change.ItemID, change.EffectID)
			}
			effect := registry.mechanics.effects[change.EffectID]
			target := "stat." + change.StatID
			if change.Target != "" {
				target = change.Target
			}
			if !strings.HasPrefix(target, "stat."+change.StatID+".") && target != "stat."+change.StatID {
				return fmt.Errorf("invalid stat effect target %q", target)
			}
			if effect == nil || !effect.additiveEditable || !slices.Contains(effect.targets, target) {
				return fmt.Errorf("effect %q cannot be edited for stat %q", change.EffectID, change.StatID)
			}
			if change.Value > 1000000 || change.Value < -1000000 {
				return errors.New("effect override exceeds limit")
			}
			updated, err := withItemEffectOverride(item, change.EffectID, change.Value)
			if err != nil {
				return err
			}
			instance.Inventory[index] = updated
		default:
			return fmt.Errorf("unknown trace change kind %q", change.Kind)
		}
	}
	if err := ValidateCharacterInstance(instance, registry); err != nil {
		return err
	}
	if _, err := characterStatsForRegistry(instance, registry); err != nil {
		return fmt.Errorf("trace edit would make the character invalid: %w", err)
	}
	mutation.setInstance(instance)
	mutation.resultCharacterID = instance.ID
	return nil
}
