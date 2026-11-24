package main

import (
	"cmp"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"
)

// EffectGrant identifies one provider on the actor. For an inventory grant,
// SourceID is the item instance ID, including when two items share a definition.
type EffectGrant struct {
	EffectID, SourceID string
	Item               bool
}

type compiledEffect struct {
	id                                            string
	targets                                       []string
	priority                                      int64
	sourceScope, unique, needsSource, needsTarget bool
	additiveValue                                 int64
	additiveEditable                              bool
	formulaSource                                 string
	formula, when                                 *Expression
}

func (s *RulesetSnapshot) compileEffects() map[string]*compiledEffect {
	effects := make(map[string]*compiledEffect, len(s.Registry.Effects))
	for id, fields := range s.Registry.Effects {
		f := fields.values
		prefix := "effect." + id + "."
		e := &compiledEffect{id: id, formula: s.expressions[prefix+"formula"], when: s.expressions[prefix+"when"],
			sourceScope: f["scope"] == "source", unique: f["stack"] == "unique"}
		e.priority, _ = f["priority"].(int64)
		e.formulaSource, _ = f["formula"].(string)
		targets, _ := rulesetStrings(f["target"], "target", true)
		for _, target := range targets {
			parts, _ := parseRulesetPath(target)
			e.targets = append(e.targets, rulesetPath(parts))
		}
		slices.Sort(e.targets)
		e.targets = slices.Compact(e.targets)
		for _, p := range []*Expression{e.formula, e.when} {
			if p != nil {
				e.needsTarget = e.needsTarget || p.UsesOptionalTarget
				for _, dep := range p.Dependencies {
					if strings.HasPrefix(dep, "source.") {
						e.needsSource = true
					}
				}
			}
		}
		// An individual item may override an additive constant, never a
		// conditional, multiplicative or arbitrarily computed effect.
		if e.formula != nil && e.formula.root != nil {
			n := e.formula.root
			if (n.op == "+" || n.op == "-") && n.left != nil && n.left.op == "read" && n.left.address == "target" && n.right != nil && n.right.op == "literal" {
				if literal, ok := n.right.literal.(int64); ok {
					e.additiveEditable = true
					e.additiveValue = literal
					if n.op == "-" {
						e.additiveValue = -literal
					}
				}
			}
		}
		effects[id] = e
	}
	return effects
}

type activeDefinition struct {
	*compiledEffect
	sources []EffectGrant
}

func (e *activeDefinition) grantedByItem(id string) bool {
	i, found := slices.BinarySearchFunc(e.sources, id, func(g EffectGrant, id string) int {
		return strings.Compare(g.SourceID, id)
	})
	if found {
		for ; i < len(e.sources) && e.sources[i].SourceID == id; i++ {
			if e.sources[i].Item {
				return true
			}
		}
	}
	return false
}

// ActiveEffects is an immutable index for a single actor and ruleset revision.
// Replace it when the actor's grants change. It holds no evaluated stat or roll.
type ActiveEffects struct {
	byTarget map[string][]*activeDefinition
}

func (s RulesetSnapshot) ActiveEffects(grants []EffectGrant) (*ActiveEffects, error) {
	return indexActiveEffects(s.Registry.mechanics, grants)
}

func indexActiveEffects(mechanics *rulesetMechanics, grants []EffectGrant) (*ActiveEffects, error) {
	if len(grants) == 0 {
		return nil, nil
	}
	active := &ActiveEffects{byTarget: map[string][]*activeDefinition{}}
	groups := map[string]*activeDefinition{}
	for _, grant := range grants {
		var def *compiledEffect
		if mechanics != nil {
			def = mechanics.effects[grant.EffectID]
		}
		if def == nil || grant.SourceID == "" {
			return nil, fmt.Errorf("invalid effect grant %q from %q", grant.EffectID, grant.SourceID)
		}
		g := groups[def.id]
		if g == nil {
			g = &activeDefinition{compiledEffect: def}
			groups[def.id] = g
			for _, target := range def.targets {
				active.byTarget[target] = append(active.byTarget[target], g)
			}
		}
		g.sources = append(g.sources, grant)
	}
	for _, g := range groups {
		slices.SortFunc(g.sources, func(a, b EffectGrant) int {
			if c := strings.Compare(a.SourceID, b.SourceID); c != 0 {
				return c
			}
			if a.Item == b.Item {
				return 0
			}
			if a.Item {
				return 1
			}
			return -1
		})
		g.sources = slices.Compact(g.sources)
	}
	for _, entries := range active.byTarget {
		slices.SortFunc(entries, func(a, b *activeDefinition) int {
			if c := cmp.Compare(a.priority, b.priority); c != 0 {
				return c
			}
			return strings.Compare(a.id, b.id)
		})
	}
	return active, nil
}

func (e *ActiveEffects) forTarget(target string) []*activeDefinition {
	if e == nil {
		return nil
	}
	return e.byTarget[target]
}

type effectContext struct {
	sourceID             string
	hasSource, hasTarget bool
	overrides            map[string]map[string]int64
	trace                func(*activeDefinition, EffectGrant, exprValue, exprValue)
	skip                 func(*activeDefinition, string)
}

func actionEffectContext(bindings map[string]any) effectContext {
	raw, _ := expressionBinding(bindings, "source.id", []string{"source", "id"})
	id, _ := raw.(string)
	ctx := effectContext{sourceID: id, hasSource: bindings["source"] != nil, hasTarget: bindings["target"] != nil}
	for key := range bindings {
		ctx.hasSource = ctx.hasSource || strings.HasPrefix(key, "source.")
		ctx.hasTarget = ctx.hasTarget || strings.HasPrefix(key, "target.") || strings.HasPrefix(key, "action.target.")
		if ctx.hasSource && ctx.hasTarget {
			break
		}
	}
	return ctx
}

// Individual effect overrides are stored with the inventory instance, not the
// shared object or effect definition. The engine uses the same override for
// each projection, including after a restart or an import.
func itemEffectOverrides(instance CharacterInstance) map[string]map[string]int64 {
	result := make(map[string]map[string]int64)
	for _, item := range instance.Inventory {
		if item.Fields == nil {
			continue
		}
		raw, ok := item.Fields.values["effect_overrides"].(map[string]any)
		if !ok {
			continue
		}
		values := make(map[string]int64)
		for id, value := range raw {
			if n, ok := value.(int64); ok {
				values[id] = n
			}
		}
		if len(values) > 0 {
			result[item.ID] = values
		}
	}
	return result
}

func withItemEffectOverride(item InventoryItem, effectID string, value int64) (InventoryItem, error) {
	fields := map[string]any{}
	if item.Fields != nil {
		maps.Copy(fields, item.Fields.values)
	}
	entries := map[string]any{}
	if existing, ok := fields["effect_overrides"].(map[string]any); ok {
		maps.Copy(entries, existing)
	}
	entries[effectID] = value
	fields["effect_overrides"] = entries
	updated, err := newRulesetFields(fields)
	if err != nil {
		return item, err
	}
	item.Fields = updated
	return item, nil
}

func applyEffects(entries []*activeDefinition, value exprValue, parent *ExpressionFrame, ctx effectContext, integerDestination bool) (exprValue, error) {
	for _, e := range entries {
		if e.needsSource && !ctx.hasSource || e.needsTarget && !ctx.hasTarget {
			if ctx.skip != nil {
				ctx.skip(e, "Отсутствует необходимый source или target")
			}
			continue
		}
		grants := e.sources
		if e.sourceScope {
			if ctx.sourceID == "" || !e.grantedByItem(ctx.sourceID) {
				if ctx.skip != nil {
					ctx.skip(e, "Эффект принадлежит другому источнику")
				}
				continue
			}
			grants = nil
			for _, source := range e.sources {
				if source.Item && source.SourceID == ctx.sourceID {
					grants = append(grants, source)
					break
				}
			}
		}
		frame := ExpressionFrame{Bindings: parent.Bindings, readStat: parent.readStat, intermediate: &value}
		if e.when != nil {
			ok, err := frame.evaluateOnce(e.when)
			if err != nil {
				return exprValue{}, fmt.Errorf("effect.%s.when: %w", e.id, err)
			}
			if !ok.flag {
				if ctx.skip != nil {
					ctx.skip(e, "Условие when не выполнено")
				}
				continue
			}
		}
		if e.unique && len(grants) > 1 {
			if ctx.skip != nil {
				ctx.skip(e, "stack = unique: повторные источники пропущены")
			}
			grants = grants[:1]
		}
		for _, grant := range grants {
			frame.steps = 0
			before := value
			next, err := frame.evaluateOnce(e.formula)
			if err != nil {
				return exprValue{}, fmt.Errorf("effect.%s.formula: %w", e.id, err)
			}
			if grant.Item && e.additiveEditable && next.typ == exprNumber {
				if override, ok := ctx.overrides[grant.SourceID][e.id]; ok {
					delta := new(big.Int).Sub(big.NewInt(override), big.NewInt(e.additiveValue))
					next.num = new(big.Rat).Add(next.num, new(big.Rat).SetInt(delta))
				}
			}
			if next.typ != value.typ || integerDestination && next.typ == exprNumber && !next.num.IsInt() {
				return exprValue{}, fmt.Errorf("effect.%s.formula: incompatible target result", e.id)
			}
			value = next
			if ctx.trace != nil {
				ctx.trace(e, grant, before, next)
			}
		}
	}
	return value, nil
}
