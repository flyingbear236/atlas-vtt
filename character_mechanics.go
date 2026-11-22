package main

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

type InventoryItem struct {
	ID       string         `json:"id"`
	ObjectID string         `json:"objectId"` // canonical TOML path below object
	Equipped bool           `json:"equipped"`
	Fields   *RulesetFields `json:"fields,omitempty"`
}

// An empty SourceID denotes an action supplied by the character directly.
type ActionSource struct {
	ActionID string `json:"actionId"`
	SourceID string `json:"sourceId,omitempty"`
}

type objectMechanics struct {
	actions, effects []string
}

type rulesetMechanics struct {
	effects      map[string]*compiledEffect
	objects      map[string]objectMechanics
	actionLeaves map[string][]string
}

func createCharacterInstance(session *Session, cmd Command, registry EffectiveRegistry) (CharacterInstance, error) {
	if id := cmd.CharacterDefinitionID; id != "" {
		if cmd.PresetID != "" {
			return CharacterInstance{}, fmt.Errorf("choose one character definition or preset")
		}
		if _, exists := session.CharacterInstances[id]; exists {
			return CharacterInstance{}, fmt.Errorf("character %q already exists; link the existing instance", id)
		}
		definition := session.Ruleset.Characters[id]
		if definition == nil {
			return CharacterInstance{}, fmt.Errorf("unknown character definition %q", id)
		}
		return decodeNestedCharacter(id, definition, registry)
	}
	if cmd.PresetID != "" {
		preset, exists := registry.Presets[cmd.PresetID]
		if !exists {
			return CharacterInstance{}, fmt.Errorf("unknown preset %q", cmd.PresetID)
		}
		return NewCharacterInstance(nextCharacterID(session), &preset), nil
	}
	instance := NewCharacterInstance(nextCharacterID(session), nil)
	if name := strings.TrimSpace(cmd.CharacterName); name != "" {
		instance.Name = name
	}
	return instance, nil
}

func (s *RulesetSnapshot) compileMechanics() {
	m := &rulesetMechanics{
		effects:      s.compileEffects(),
		objects:      make(map[string]objectMechanics, len(s.Registry.Objects)),
		actionLeaves: map[string][]string{},
	}
	var executable []string
	for _, id := range sortedDefinitionIDs(s.Registry.Actions) {
		if a := s.Registry.Actions[id]; a.V04 != nil && rulesetExecutable(a.V04.values) {
			executable = append(executable, id)
		}
	}
	for id, a := range s.Registry.Actions {
		if a.V04 == nil {
			continue
		}
		if rulesetExecutable(a.V04.values) {
			m.actionLeaves[id] = []string{id}
			continue
		}
		prefix := id + "."
		start := sort.SearchStrings(executable, prefix)
		end := start
		for end < len(executable) && strings.HasPrefix(executable[end], prefix) {
			end++
		}
		m.actionLeaves[id] = executable[start:end]
	}
	for id, definition := range s.Registry.Objects {
		f := definition.values
		m.objects[id] = objectMechanics{actions: rulesetGrantIDs(f["actions"]), effects: rulesetGrantIDs(f["effects"])}
	}
	s.Registry.mechanics = m
}

// References have already been validated by the TOML model.
func rulesetGrantIDs(raw any) []string {
	refs, _ := rulesetStrings(raw, "grants", false)
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		parts, _ := parseRulesetPath(ref)
		ids = append(ids, rulesetPath(parts[1:]))
	}
	return appendUnique(nil, ids...)
}

func validateCharacterGrants(instance CharacterInstance, registry EffectiveRegistry) error {
	for _, id := range instance.EffectIDs {
		if registry.Effects[id] == nil {
			return fmt.Errorf("unknown character effect %q", id)
		}
	}
	seen := make(map[string]bool, len(instance.Inventory))
	for _, item := range instance.Inventory {
		if err := validateID(item.ID); err != nil {
			return fmt.Errorf("inventory id %q: %w", item.ID, err)
		}
		if seen[item.ID] {
			return fmt.Errorf("duplicate inventory id %q", item.ID)
		}
		seen[item.ID] = true
		if registry.Objects[item.ObjectID] == nil {
			return fmt.Errorf("inventory %q references unknown object %q", item.ID, item.ObjectID)
		}
	}
	return nil
}

func (r EffectiveRegistry) characterGrants(instance CharacterInstance, directActions []string, effectsOnly bool) ([]ActionSource, *ActiveEffects, error) {
	if r.mechanics == nil {
		return nil, nil, validateCharacterGrants(instance, r)
	}
	var actions []ActionSource
	seen := map[ActionSource]bool{}
	addActions := func(ids []string, source string) {
		for _, id := range ids {
			for _, leaf := range r.mechanics.actionLeaves[id] {
				option := ActionSource{ActionID: leaf, SourceID: source}
				if !seen[option] {
					seen[option] = true
					actions = append(actions, option)
				}
			}
		}
	}
	if !effectsOnly {
		addActions(directActions, "")
	}
	grants := make([]EffectGrant, 0, len(instance.EffectIDs))
	for _, id := range instance.EffectIDs {
		grants = append(grants, EffectGrant{id, instance.ID, false})
	}
	for _, item := range instance.Inventory {
		if !item.Equipped {
			continue
		}
		object, ok := r.mechanics.objects[item.ObjectID]
		if !ok {
			return nil, nil, fmt.Errorf("inventory %q references unknown object %q", item.ID, item.ObjectID)
		}
		if !effectsOnly {
			addActions(object.actions, item.ID)
		}
		for _, id := range object.effects {
			grants = append(grants, EffectGrant{id, item.ID, true})
		}
	}
	effects, err := indexActiveEffects(r.mechanics, grants)
	return actions, effects, err
}

func (r EffectiveRegistry) itemBindings(item InventoryItem) map[string]any {
	fields := maps.Clone(r.Objects[item.ObjectID].values)
	if item.Fields != nil {
		maps.Copy(fields, item.Fields.values)
	}
	fields["id"] = item.ID
	fields["object"] = "object." + item.ObjectID
	fields["equipped"] = item.Equipped
	return fields
}

// The action executor can use this after checking access to the actor. Source
// selection always uses current server-owned inventory and grants.
func (s RulesetSnapshot) characterActionFrame(instance CharacterInstance, registry EffectiveRegistry, choice ActionSource, target map[string]any, rng DiceRNG) (*ExpressionFrame, error) {
	state, err := characterStatsForRegistry(instance, registry)
	if err != nil {
		return nil, err
	}
	bindings, _, err := characterActionBindings(instance, state, registry, choice)
	if err != nil {
		return nil, err
	}
	if target != nil {
		bindings["target"] = target
	}
	return s.actionFrameWithEffects(choice.ActionID, bindings, rng, state.activeEffects), nil
}

func characterInstanceBindings(instance CharacterInstance) map[string]any {
	fields := map[string]any{}
	if instance.V04 != nil {
		maps.Copy(fields, instance.V04.values)
	}
	fields["id"], fields["name"] = instance.ID, instance.Name
	return fields
}

func characterActionBindings(instance CharacterInstance, state EffectiveCharacter, registry EffectiveRegistry, choice ActionSource) (map[string]any, *InventoryItem, error) {
	if !slices.Contains(state.ActionSources, choice) {
		return nil, nil, fmt.Errorf("character %q cannot use action %q from source %q", instance.ID, choice.ActionID, choice.SourceID)
	}
	bindings := map[string]any{"actor": characterInstanceBindings(instance)}
	var source *InventoryItem
	if choice.SourceID != "" {
		for _, item := range instance.Inventory {
			if item.ID == choice.SourceID {
				bindings["source"] = registry.itemBindings(item)
				source = &item
				break
			}
		}
	}
	for address, value := range state.StatValues {
		bindings[address] = value
	}
	return bindings, source, nil
}
