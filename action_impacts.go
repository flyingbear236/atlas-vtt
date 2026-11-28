package main

import (
	"math"
	"strconv"
	"strings"
)

// Only the explicitly selected character's committed numeric writes are
// announced. No formulas, item IDs, or before/after values travel in this event.
type actionImpact struct {
	Name     string `json:"name"`
	Delta    string `json:"delta"`
	Negative bool   `json:"negative"`
}

func actionImpacts(plan *ActionChangePlan, target *ActionInstanceRef, stats map[string]StatDefinition) []actionImpact {
	if plan == nil || target == nil || target.CharacterID == "" || target.ItemID != "" || target.ElementID != "" {
		return nil
	}
	var impacts []actionImpact
	for _, change := range plan.Changes {
		if change.Instance != *target || change.Before.Type() != change.After.Type() {
			continue
		}
		var delta string
		var negative bool
		switch change.Before.Type() {
		case StatTypeInteger:
			d := change.After.Integer() - change.Before.Integer() // both values are safe integers
			if d == 0 {
				continue
			}
			delta, negative = strconv.FormatInt(d, 10), d < 0
		case StatTypeNumber:
			d := change.After.Number() - change.Before.Number()
			if d == 0 || math.IsInf(d, 0) || math.IsNaN(d) {
				continue
			}
			delta, negative = strconv.FormatFloat(d, 'g', 12, 64), d < 0
		default:
			continue
		}
		name := change.Field
		if path, err := parseRulesetPath(change.Field); err == nil && len(path) >= 3 && path[0] == "stat" {
			id := rulesetPath(path[1 : len(path)-1])
			name = strings.TrimSpace(stats[id].Name)
			if name == "" {
				name = "stat." + id
			}
		}
		impacts = append(impacts, actionImpact{Name: name, Delta: delta, Negative: negative})
	}
	return impacts
}

// Include only names of referenced mechanics, not the entire ruleset registry.
func sheetReferenceNames(session *Session, registry EffectiveRegistry, actions map[string]ActionDefinition, items map[string]inventorySheetDetail, direct []inventorySheetEffect) map[string]string {
	names := map[string]string{}
	add := func(raw string) {
		parts, err := parseRulesetPath(raw)
		if err != nil || len(parts) < 2 {
			return
		}
		if parts[0] == "actor" || parts[0] == "target" {
			parts = parts[1:]
		}
		if len(parts) < 2 {
			return
		}
		for end := len(parts); end >= 2; end-- {
			id := rulesetPath(parts[1:end])
			var name string
			var fields *RulesetFields
			switch parts[0] {
			case "stat":
				def, ok := registry.Stats[id]
				if !ok {
					continue
				}
				name, fields = def.Name, def.V04
			case "action":
				def, ok := registry.Actions[id]
				if !ok {
					continue
				}
				name, fields = def.Name, def.V04
			default:
				return
			}
			name = strings.TrimSpace(name)
			if name == "" {
				return
			}
			if end < len(parts) {
				suffix := rulesetPath(parts[end:])
				label := suffix
				switch suffix {
				case "base", "current", "default":
					label = ""
				case "bonus":
					label = "Бонус"
				case "roll":
					label = "Бросок"
				}
				if fields != nil && len(parts)-end == 1 {
					if field, ok := fields.values[parts[end]].(map[string]any); ok {
						if v, ok := field["name"].(string); ok && strings.TrimSpace(v) != "" {
							label = v
						}
					}
				}
				if label != "" {
					name += " · " + label
				}
			}
			names[raw] = name
			names[rulesetPath(parts)] = name
			return
		}
	}
	for _, effect := range direct {
		for _, target := range effect.Targets {
			add(target)
		}
	}
	for _, item := range items {
		for _, effect := range item.Effects {
			for _, target := range effect.Targets {
				add(target)
			}
		}
	}
	for id := range actions {
		if plan, ok := session.Ruleset.actionPlans[id]; ok {
			for _, block := range []actionConsequences{plan.direct, plan.onSuccess, plan.onFail} {
				if block.apply != nil {
					add(rulesetPath(block.apply.path))
				}
			}
		}
	}
	return names
}
