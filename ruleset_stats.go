package main

import (
	"fmt"
	"math"
	"strconv"
)

// Compiled addresses are shared by projections of the same ruleset.
type statPlan struct {
	steps                    []statStep
	constants                map[string]any
	types                    map[string]StatType
	byAddress                map[string]statStep
	outputs, resourceOutputs []string
}

type statStep struct {
	id, owner, field string
	formula          *Expression
}

func (p *statPlan) index(stats map[string]StatDefinition) {
	p.byAddress = make(map[string]statStep)
	for _, step := range p.steps {
		address := step.owner + "." + step.field
		if step.field == "default" {
			address = step.owner
			p.byAddress[address+".base"] = step
			if stats[step.id].IsResource() {
				current := address + ".current"
				p.byAddress[current] = step
				p.outputs = append(p.outputs, current)
				p.resourceOutputs = append(p.resourceOutputs, address, current)
			}
		}
		p.byAddress[address] = step
		p.outputs = append(p.outputs, address)
	}
	p.outputs = append(p.outputs, sortedDefinitionIDs(p.constants)...)
	p.steps = nil
}

func EffectiveCharacterState(instance CharacterInstance, preset *CharacterPresetDefinition, registry EffectiveRegistry) (EffectiveCharacter, error) {
	return projectCharacterStats(instance, preset, registry, nil, nil, false)
}

// actorFields carries custom instance data supplied by the caller.
func (effects *ActiveEffects) CharacterState(instance CharacterInstance, preset *CharacterPresetDefinition, registry EffectiveRegistry, actorFields map[string]any) (EffectiveCharacter, error) {
	return projectCharacterStats(instance, preset, registry, effects, actorFields, false)
}

type statPass struct {
	instance       CharacterInstance
	registry       EffectiveRegistry
	state          EffectiveCharacter
	effects        *ActiveEffects
	values         map[string]any
	visiting       map[string]bool
	frame          ExpressionFrame
	trace          map[string]*statCalculationTrace
	traceAddresses map[string]bool
	overrides      map[string]map[string]int64
}

func projectCharacterStats(instance CharacterInstance, preset *CharacterPresetDefinition, registry EffectiveRegistry, effects *ActiveEffects, actorFields map[string]any, resourcesOnly bool) (EffectiveCharacter, error) {
	return projectCharacterStatsWithTrace(instance, preset, registry, effects, actorFields, resourcesOnly, nil)
}
func projectCharacterStatsWithTrace(instance CharacterInstance, preset *CharacterPresetDefinition, registry EffectiveRegistry, effects *ActiveEffects, actorFields map[string]any, resourcesOnly bool, trace map[string]*statCalculationTrace) (EffectiveCharacter, error) {
	return projectCharacterStatsWithSelectedTrace(instance, preset, registry, effects, actorFields, resourcesOnly, trace, nil)
}
func projectCharacterStatsWithSelectedTrace(instance CharacterInstance, preset *CharacterPresetDefinition, registry EffectiveRegistry, effects *ActiveEffects, actorFields map[string]any, resourcesOnly bool, trace map[string]*statCalculationTrace, addresses map[string]bool) (EffectiveCharacter, error) {
	state := characterSourceState(instance, preset, registry)
	if registry.mechanics != nil {
		actions, active, err := registry.characterGrants(instance, state.ActionIDs, resourcesOnly)
		if err != nil {
			return EffectiveCharacter{}, err
		}
		state.ActionSources = actions
		if effects == nil {
			effects = active
		}
		state.activeEffects = effects
	}
	if actorFields == nil && instance.V04 != nil {
		actorFields = instance.V04.values
	}
	plan := registry.statPlan
	if plan == nil {
		return state, nil
	}
	pass := statPass{instance: instance, registry: registry, state: state, effects: effects, values: map[string]any{}, visiting: map[string]bool{}, trace: trace, traceAddresses: addresses, overrides: itemEffectOverrides(instance)}
	pass.frame = ExpressionFrame{Bindings: map[string]any{"actor": actorFields, "actor.id": instance.ID, "actor.name": instance.Name}, readStat: pass.read}
	outputs := plan.outputs
	if resourcesOnly {
		outputs = plan.resourceOutputs
	}
	for _, address := range outputs {
		if _, err := pass.read(address); err != nil {
			return EffectiveCharacter{}, err
		}
	}
	pass.state.StatValues = pass.values
	return pass.state, nil
}

// Effect conditions can introduce dependencies in either direction relative to
// declaration order. Resolve actual reads, once per projection.
func (p *statPass) read(address string) (any, error) {
	if raw, ok := p.values[address]; ok {
		return raw, nil
	}
	if p.visiting[address] {
		return nil, fmt.Errorf("cyclic stat/effect dependency at %s", address)
	}
	// Effects can connect independently compiled graphs into a very deep chain.
	if len(p.visiting) >= maxExpressionSteps {
		return nil, fmt.Errorf("stat/effect dependency depth exceeded at %s", address)
	}
	p.visiting[address] = true
	defer delete(p.visiting, address)
	raw, err := p.compute(address)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", address, err)
	}
	p.values[address] = raw
	return raw, nil
}

func (p *statPass) compute(address string) (any, error) {
	plan := p.registry.statPlan
	step, ok := plan.byAddress[address]
	if !ok {
		raw, found := plan.constants[address]
		if !found {
			return nil, fmt.Errorf("unknown stat address")
		}
		if override, present := p.instance.StatFieldOverrides[address]; present {
			if _, integer := raw.(int64); integer {
				raw = override
			}
		}
		return p.modify(address, raw, false)
	}
	def := p.registry.Stats[step.id]
	if step.field != "default" {
		raw, err := p.formula(step)
		if err != nil {
			return nil, err
		}
		return p.modify(address, raw, false)
	}
	if address == step.owner+".base" {
		if step.formula != nil {
			raw, err := p.formula(step)
			if err != nil {
				return nil, err
			}
			value, err := tomlStatValue(raw, rulesetStatType(raw))
			if err != nil {
				return nil, fmt.Errorf("%s.default: %w", step.owner, err)
			}
			return value.Interface(), nil
		}
		if def.IsResource() {
			if source, ok := p.state.Resources[step.id]; ok {
				return source.Initial.Interface(), nil
			}
		} else if source, ok := p.state.Stats[step.id]; ok {
			return source.Interface(), nil
		}
		return def.Default.Interface(), nil
	}
	base, err := p.read(step.owner + ".base")
	if err != nil {
		return nil, err
	}
	if address == step.owner+".current" {
		current, ok := p.instance.ResourceCurrent[step.id]
		if !ok {
			current, err = tomlStatValue(base, rulesetStatType(base))
		}
		if err != nil || current.Type() != rulesetStatType(base) {
			return nil, fmt.Errorf("current requires %s", rulesetStatType(base))
		}
		resource := p.state.Resources[step.id]
		resource.Current = current
		p.state.Resources[step.id] = resource
		return current.Interface(), nil
	}
	raw, err := p.modify(address, base, true)
	if err != nil {
		return nil, err
	}
	value, err := tomlStatValue(raw, rulesetStatType(base))
	if err != nil {
		return nil, err
	}
	if def.IsResource() {
		resource := p.state.Resources[step.id]
		resource.Initial = value
		p.state.Resources[step.id] = resource
	} else {
		p.state.Stats[step.id] = value
	}
	return raw, nil
}

func (p *statPass) formula(step statStep) (any, error) {
	frame := ExpressionFrame{Bindings: p.frame.Bindings, readStat: p.read, valueAddress: step.owner}
	v, err := frame.evaluateOnce(step.formula)
	if err != nil {
		return nil, fmt.Errorf("%s.%s: %w", step.owner, step.field, err)
	}
	return v.scalar(), nil
}

func (p *statPass) modify(address string, raw any, integerDestination bool) (any, error) {
	entries := p.effects.forTarget(address)
	var record *statCalculationTrace
	if p.trace != nil && (p.traceAddresses == nil || p.traceAddresses[address]) {
		record = &statCalculationTrace{Initial: raw}
		p.trace[address] = record
	}
	if len(entries) == 0 {
		if record != nil {
			record.Result = raw
		}
		return raw, nil
	}
	v, err := exprScalar(raw)
	if err != nil {
		return nil, err
	}
	ctx := effectContext{overrides: p.overrides}
	if record != nil {
		ctx.skip = func(effect *activeDefinition, reason string) {
			if len(record.Skipped) < 16 {
				record.Skipped = append(record.Skipped, statEffectSkip{EffectID: effect.id, Reason: reason})
			}
		}
		ctx.trace = func(effect *activeDefinition, grant EffectGrant, before, after exprValue) {
			entry := statEffectTrace{EffectID: effect.id, SourceID: grant.SourceID, Item: grant.Item, Before: before.scalar(), After: after.scalar(), Formula: effect.formulaSource}
			if grant.Item && effect.additiveEditable && before.typ == exprNumber && after.typ == exprNumber {
				entry.Editable = true
				entry.Value = effect.additiveValue
				if override, ok := p.overrides[grant.SourceID][effect.id]; ok {
					entry.Value = override
				}
				if entry.Value >= 0 {
					entry.Formula = fmt.Sprintf("target + %d", entry.Value)
				} else {
					entry.Formula = fmt.Sprintf("target - %d", -entry.Value)
				}
			}
			record.Effects = append(record.Effects, entry)
		}
	}
	v, err = applyEffects(entries, v, &p.frame, ctx, integerDestination)
	if err != nil {
		return nil, err
	}
	if record != nil {
		record.Result = v.scalar()
	}
	return v.scalar(), nil
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
	return projectCharacterStats(instance, preset, registry, nil, nil, true)
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
