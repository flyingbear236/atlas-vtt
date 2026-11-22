package main

import (
	"cmp"
	"fmt"
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
	formula, when                                 *Expression
}

func (s *RulesetSnapshot) compileEffects() {
	s.effects = make(map[string]*compiledEffect, len(s.Registry.Effects))
	for id, fields := range s.Registry.Effects {
		f := fields.values
		prefix := "effect." + id + "."
		e := &compiledEffect{id: id, formula: s.expressions[prefix+"formula"], when: s.expressions[prefix+"when"],
			sourceScope: f["scope"] == "source", unique: f["stack"] == "unique"}
		e.priority, _ = f["priority"].(int64)
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
		s.effects[id] = e
	}
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
	active := &ActiveEffects{byTarget: map[string][]*activeDefinition{}}
	groups := map[string]*activeDefinition{}
	for _, grant := range grants {
		def := s.effects[grant.EffectID]
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

func applyEffects(entries []*activeDefinition, value exprValue, parent *ExpressionFrame, ctx effectContext, integerDestination bool) (exprValue, error) {
	for _, e := range entries {
		if e.needsSource && !ctx.hasSource || e.needsTarget && !ctx.hasTarget {
			continue
		}
		count := len(e.sources)
		if e.sourceScope {
			if ctx.sourceID == "" || !e.grantedByItem(ctx.sourceID) {
				continue
			}
			count = 1
		}
		// when sees the action source and actor, which are the same for every
		// grant of this definition. Its result is valid for the whole group.
		frame := ExpressionFrame{Bindings: parent.Bindings, readStat: parent.readStat, intermediate: &value}
		if e.when != nil {
			ok, err := frame.evaluateOnce(e.when)
			if err != nil {
				return exprValue{}, fmt.Errorf("effect.%s.when: %w", e.id, err)
			}
			if !ok.flag {
				continue
			}
		}
		if e.unique {
			count = 1
		}
		for range count {
			// Each source transforms the previous result, even if it uses the same program.
			frame.steps = 0
			next, err := frame.evaluateOnce(e.formula)
			if err != nil {
				return exprValue{}, fmt.Errorf("effect.%s.formula: %w", e.id, err)
			}
			if next.typ != value.typ || integerDestination && next.typ == exprNumber && !next.num.IsInt() {
				return exprValue{}, fmt.Errorf("effect.%s.formula: incompatible target result", e.id)
			}
			value = next
		}
	}
	return value, nil
}
