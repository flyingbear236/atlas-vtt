package main

import (
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"
)

// Immutable and shared by projections of the same compiled ruleset. Literal
// bases come first, followed by formulas in dependency order.
type statPlan struct {
	statProgram
	resources statProgram
	types     map[string]StatType
}

type statProgram struct {
	steps     []statStep
	constants map[string]any
}

type statStep struct {
	id, owner, field string
	formula          *Expression
}

func EffectiveCharacterState(instance CharacterInstance, preset *CharacterPresetDefinition, registry EffectiveRegistry) (EffectiveCharacter, error) {
	state := characterSourceState(instance, preset, registry)
	if registry.statPlan == nil {
		return state, nil
	}
	return registry.statPlan.statProgram.evaluate(instance, registry, state)
}

// Select the transitive inputs of resource maxima once. Token display updates
// need neither unrelated formulas nor a copy of all the sheet's metadata.
func (plan *statPlan) compileResources(stats map[string]StatDefinition, aliases map[string]string) {
	wanted := map[string]bool{}
	for id, def := range stats {
		if def.IsResource() {
			wanted["stat."+id+".default"] = true
		}
	}
	for i := len(plan.steps) - 1; i >= 0; i-- {
		step := plan.steps[i]
		if !wanted[step.owner+"."+step.field] || step.formula == nil {
			continue
		}
		for _, dep := range step.formula.Dependencies {
			dep = strings.TrimPrefix(dep, "actor.")
			if key, ok := aliases[dep]; ok {
				dep = key
			}
			wanted[dep] = true
		}
	}
	for _, step := range plan.steps {
		if wanted[step.owner+"."+step.field] {
			plan.resources.steps = append(plan.resources.steps, step)
		}
	}
	plan.resources.constants = map[string]any{}
	for key := range wanted {
		if raw, ok := plan.constants[key]; ok {
			plan.resources.constants[key] = raw
		}
	}
}

func (p statProgram) evaluate(instance CharacterInstance, registry EffectiveRegistry, state EffectiveCharacter) (EffectiveCharacter, error) {
	values := maps.Clone(p.constants)
	context := map[string]any{"actor.name": instance.Name, "actor.id": instance.ID}
	frame := ExpressionFrame{Bindings: context, characterStats: values}
	for _, step := range p.steps {
		address := step.owner + "." + step.field
		def := registry.Stats[step.id]
		var raw any
		if step.formula != nil {
			context["value"] = values[step.owner]
			frame.steps = 0 // each compiled expression retains its own operation budget
			v, err := frame.Evaluate(step.formula)
			if err != nil {
				return EffectiveCharacter{}, fmt.Errorf("%s: %w", address, err)
			}
			raw = v.scalar()
		}
		if step.field != "default" {
			values[address] = raw
			continue
		}
		var base StatValue
		if step.formula == nil {
			base = *def.Default
			if def.IsResource() {
				if source, ok := state.Resources[step.id]; ok {
					base = source.Initial
				}
			} else if source, ok := state.Stats[step.id]; ok {
				base = source
			}
		} else {
			var err error
			base, err = tomlStatValue(raw, rulesetStatType(raw))
			if err != nil {
				return EffectiveCharacter{}, fmt.Errorf("%s: %w", address, err)
			}
		}
		values[step.owner+".base"] = base.Interface()
		values[step.owner] = base.Interface()
		if def.IsResource() {
			current, ok := instance.ResourceCurrent[step.id]
			if !ok {
				current = base
			}
			if current.Type() != base.Type() {
				return EffectiveCharacter{}, fmt.Errorf("%s.current requires %s", step.owner, base.Type())
			}
			state.Resources[step.id] = EffectiveResource{Initial: base, Current: current}
			values[step.owner+".current"] = current.Interface()
		} else {
			state.Stats[step.id] = base
		}
	}
	state.StatValues = values
	return state, nil
}

func (v exprValue) scalar() any {
	switch v.typ {
	case exprBool:
		return v.flag
	case exprText:
		return v.text
	default:
		if v.num.IsInt() {
			return v.num.Num().Int64()
		}
		return v.num // keep the exact fraction for dependent formulas
	}
}

// TOML permits non-finite custom metadata. Keep it outside numeric expression
// evaluation and present its spelling to JSON clients.
type statMetadataFloat float64

func (v statMetadataFloat) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(strconv.FormatFloat(float64(v), 'g', -1, 64))), nil
}

func statFieldLiteral(v any) any {
	if n, ok := v.(float64); ok && (math.IsNaN(n) || math.IsInf(n, 0)) {
		return statMetadataFloat(n)
	}
	return v
}

func characterStatsForRegistry(instance CharacterInstance, registry EffectiveRegistry) (EffectiveCharacter, error) {
	preset := registry.Presets[instance.PresetID]
	return EffectiveCharacterState(instance, &preset, registry)
}

func resourceState(instance CharacterInstance, preset *CharacterPresetDefinition, registry EffectiveRegistry) (EffectiveCharacter, error) {
	state := characterSourceState(instance, preset, registry)
	if registry.statPlan == nil {
		return state, nil
	}
	return registry.statPlan.resources.evaluate(instance, registry, state)
}

// Older saves may omit current when a resource is full. Carry that value
// across a write which changes its maximum, including an indirect dependency.
// Reads never materialize either current or computed fields.
func preserveResourceCurrents(before CharacterInstance, old EffectiveRegistry, after CharacterInstance, next EffectiveRegistry, resetID string) (CharacterInstance, bool, error) {
	if next.statPlan == nil {
		return after, false, nil
	}
	var inherited []string
	for id, def := range next.Stats {
		if before.ID != "" && def.V04 != nil && def.IsResource() && old.Stats[id].IsResource() && id != resetID {
			if _, explicit := after.ResourceCurrent[id]; !explicit {
				inherited = append(inherited, id)
			}
		}
	}
	original := after
	if len(inherited) != 0 {
		preset := old.Presets[before.PresetID]
		previous, err := resourceState(before, &preset, old)
		if err != nil {
			return original, false, err
		}
		after.ResourceCurrent = cloneStatValues(after.ResourceCurrent)
		for _, id := range inherited {
			if resource, ok := previous.Resources[id]; ok {
				after.ResourceCurrent[id] = resource.Current
			}
		}
	}
	state, err := characterStatsForRegistry(after, next)
	if err != nil {
		return original, false, err
	}
	for _, id := range inherited {
		if after.ResourceCurrent[id] == state.Resources[id].Initial {
			delete(after.ResourceCurrent, id)
		}
	}
	if len(after.ResourceCurrent) == len(original.ResourceCurrent) {
		return original, false, nil
	}
	return after, true, nil
}
