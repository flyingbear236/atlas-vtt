package main

import (
	"fmt"
	"maps"
	"slices"
)

type ActionChangePlan struct {
	SessionID         string         `json:"sessionId"`
	SceneID           string         `json:"sceneId"`
	SceneRevision     uint64         `json:"sceneRevision"`
	CampaignRevision  uint64         `json:"campaignRevision"`
	RegistryRevision  uint64         `json:"registryRevision"`
	CharacterRevision uint64         `json:"characterRevision"`
	Changes           []ActionChange `json:"changes"`

	// Keep the validated copies for the atomic server commit. The
	// public change list describes the declared writes in execution order.
	replacements map[string]CharacterInstance
}

type ActionChange struct {
	Instance ActionInstanceRef `json:"instance"`
	Field    string            `json:"field"`
	Before   StatValue         `json:"before"`
	After    StatValue         `json:"after"`
}

func (r *actionExecution) apply(apply *actionApply) error {
	path := apply.path[1:]
	address := rulesetPath(apply.path)
	ref, ok := r.refs[apply.path[0]]
	if !ok {
		return fmt.Errorf("no instance at %s", address)
	}
	draft, ok := r.plan.replacements[ref.CharacterID]
	if !ok {
		draft = cloneCharacterInstance(r.session.CharacterInstances[ref.CharacterID])
	}
	before, write, err := r.savedField(&draft, ref, path)
	if err != nil {
		return fmt.Errorf("%s: %w", address, err)
	}
	r.frame.Bindings["value"] = before.Interface()
	value, err := r.frame.Evaluate(apply.formula)
	delete(r.frame.Bindings, "value")
	if err != nil {
		return err
	}
	after, err := tomlStatValue(value.scalar(), before.Type())
	if err != nil {
		return fmt.Errorf("%s: %w", address, err)
	}
	if err := write(after); err != nil {
		return fmt.Errorf("%s: %w", address, err)
	}
	if r.plan.replacements == nil {
		r.plan.replacements = map[string]CharacterInstance{}
	}
	r.plan.replacements[ref.CharacterID] = draft
	r.plan.Changes = append(r.plan.Changes, ActionChange{Instance: ref, Field: rulesetPath(path), Before: before, After: after})
	return nil
}

type savedFieldWriter func(StatValue) error

func (r *actionExecution) savedField(draft *CharacterInstance, ref ActionInstanceRef, path []string) (StatValue, savedFieldWriter, error) {
	if ref.ItemID != "" {
		i := slices.IndexFunc(draft.Inventory, func(item InventoryItem) bool { return item.ID == ref.ItemID })
		if i < 0 {
			return StatValue{}, nil, fmt.Errorf("unknown item instance")
		}
		item := &draft.Inventory[i]
		switch path[0] {
		case "equipped":
			if len(path) == 1 {
				return BooleanStatValue(item.Equipped), func(v StatValue) error { item.Equipped = v.Boolean(); return nil }, nil
			}
		case "id", "object", "actions", "effects":
			return StatValue{}, nil, fmt.Errorf("item field is read-only")
		default:
			return customSavedField(&item.Fields, path)
		}
		return StatValue{}, nil, fmt.Errorf("unknown item field")
	}
	switch path[0] {
	case "stat":
		return r.savedStat(draft, path)
	case "name":
		if len(path) == 1 {
			return StringStatValue(draft.Name), func(v StatValue) error {
				if err := validateText("character name", v.String(), MaxDefinitionNameRunes, true); err != nil {
					return err
				}
				draft.Name = v.String()
				return nil
			}, nil
		}
	case "id", "inventory", "actions", "effects":
		return StatValue{}, nil, fmt.Errorf("character field is read-only")
	default:
		return customSavedField(&draft.V04, path)
	}
	return StatValue{}, nil, fmt.Errorf("unknown character field")
}

func (r *actionExecution) savedStat(draft *CharacterInstance, path []string) (StatValue, savedFieldWriter, error) {
	if len(path) < 3 {
		return StatValue{}, nil, fmt.Errorf("expected an editable stat base or resource current")
	}
	id, field := rulesetPath(path[1:len(path)-1]), path[len(path)-1]
	def := r.registry.Stats[id]
	if def.V04 == nil {
		return StatValue{}, nil, fmt.Errorf("unknown v0.4 stat %q", id)
	}
	preset := r.registry.Presets[draft.PresetID]
	switch field {
	case "current":
		if def.IsResource() {
			before, exists := draft.ResourceCurrent[id]
			if !exists {
				// The initially projected current is independent of a staged
				// base or equipment change and can be initialized only once.
				instance := r.session.CharacterInstances[draft.ID]
				state, err := resourceState(instance, &preset, r.registry)
				if err != nil {
					return StatValue{}, nil, err
				}
				before = state.Resources[id].Current
			}
			return before, func(v StatValue) error {
				if draft.ResourceCurrent == nil {
					draft.ResourceCurrent = map[string]StatValue{}
				}
				draft.ResourceCurrent[id] = v
				return nil
			}, nil
		}
	case "base":
		if def.Default != nil {
			if draft.Materialized == nil {
				*draft = MaterializeCharacter(*draft, &preset, r.registry)
			}
			values := draft.Materialized.Stats
			if def.IsResource() {
				values = draft.Materialized.ResourceInitial
			}
			before, ok := values[id]
			if !ok {
				before = *def.Default
			}
			return before, func(v StatValue) error { values[id] = v; return nil }, nil
		}
	}
	return StatValue{}, nil, fmt.Errorf("expected an editable stat base or resource current")
}

func customSavedField(fields **RulesetFields, path []string) (StatValue, savedFieldWriter, error) {
	if *fields == nil {
		return StatValue{}, nil, fmt.Errorf("no saved instance field")
	}
	raw := (*fields).values
	var ancestors []map[string]any
	for _, key := range path[:len(path)-1] {
		ancestors = append(ancestors, raw)
		child, _ := raw[key].(map[string]any)
		if child == nil {
			return StatValue{}, nil, fmt.Errorf("no saved instance field")
		}
		if _, formula := child["formula"].(string); formula && len(child) == 1 {
			return StatValue{}, nil, fmt.Errorf("computed field is read-only")
		}
		raw = child
	}
	key := path[len(path)-1]
	old := raw[key]
	if rulesetStatType(old) == "" {
		return StatValue{}, nil, fmt.Errorf("expected an existing saved scalar field")
	}
	before, err := tomlStatValue(old, rulesetStatType(old))
	if err != nil {
		return StatValue{}, nil, err
	}
	return before, func(v StatValue) error {
		child := maps.Clone(raw)
		child[key] = v.Interface()
		for i := len(ancestors) - 1; i >= 0; i-- {
			parent := maps.Clone(ancestors[i])
			parent[path[i]] = child
			child = parent
		}
		next, err := newRulesetFields(child)
		if err == nil {
			*fields = next
		}
		return err
	}, nil
}

func (r *actionExecution) validatePlan() error {
	for _, id := range sortedDefinitionIDs(r.plan.replacements) {
		draft := r.plan.replacements[id]
		if err := ValidateCharacterInstance(draft, r.registry); err != nil {
			return fmt.Errorf("character %q: %w", id, err)
		}
		draft, _, err := preserveResourceCurrents(r.session.CharacterInstances[id], r.registry, draft, r.registry, "")
		if err != nil {
			return fmt.Errorf("character %q: %w", id, err)
		}
		r.plan.replacements[id] = draft
	}
	return nil
}
