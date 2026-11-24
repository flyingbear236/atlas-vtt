package main

import (
	"fmt"
	"strings"
)

// ActionTrace is a bounded, GM-only explanation captured during execution.
// Its scalar values are snapshots, not formulas re-evaluated after the action.
type ActionTrace struct {
	RollFormula       string                 `json:"rollFormula,omitempty"`
	SuccessFormula    string                 `json:"successFormula,omitempty"`
	SkippedOperations []ActionExecutionIssue `json:"skippedOperations,omitempty"`
	RollBase          string                 `json:"rollBase,omitempty"`
	RollTotal         string                 `json:"rollTotal,omitempty"`
	Reads             []ActionTraceRead      `json:"reads,omitempty"`
	Effects           []ActionTraceEffect    `json:"effects,omitempty"`
	Skipped           []ActionTraceSkip      `json:"skipped,omitempty"`
	Changes           []ActionChange         `json:"changes,omitempty"`
	Variables         map[string]any         `json:"variables,omitempty"`
	Truncated         bool                   `json:"truncated,omitempty"`
}

type ActionTraceOrigin struct {
	Path    string            `json:"path"`
	Name    string            `json:"name"`
	Initial string            `json:"initial,omitempty"`
	Current string            `json:"current"`
	Effects []statEffectTrace `json:"effects,omitempty"`
	Skipped []statEffectSkip  `json:"skipped,omitempty"`
}
type ActionTraceRead struct {
	Formula string             `json:"formula,omitempty"`
	Origin  *ActionTraceOrigin `json:"origin,omitempty"`
	Path    string             `json:"path"`
	Name    string             `json:"name,omitempty"`
	Value   string             `json:"value"`
	Initial string             `json:"initial,omitempty"`
	Effects []statEffectTrace  `json:"effects,omitempty"`
	Skipped []statEffectSkip   `json:"skipped,omitempty"`
}
type ActionTraceEffect struct {
	EffectID   string `json:"effectId"`
	SourceID   string `json:"sourceId"`
	SourceName string `json:"sourceName,omitempty"`
	Formula    string `json:"formula"`
	Before     string `json:"before"`
	After      string `json:"after"`
}
type ActionTraceSkip struct {
	EffectID string `json:"effectId"`
	Reason   string `json:"reason"`
}

const maxTraceReads = 24
const maxTraceEffects = 24
const maxTraceSkips = 24

func traceScalar(v any) string {
	s := fmt.Sprint(v)
	if len(s) > 128 {
		s = strings.ToValidUTF8(s[:128], "�")
	}
	return s
}
func traceStatName(path string, registry EffectiveRegistry) string {
	path = strings.TrimPrefix(path, "actor.")
	if !strings.HasPrefix(path, "stat.") {
		return ""
	}
	id := strings.TrimPrefix(path, "stat.")
	if def, ok := registry.Stats[id]; ok {
		return def.Name
	}
	for len(id) > 0 {
		cut := strings.LastIndexByte(id, '.')
		if cut < 0 {
			break
		}
		id = id[:cut]
		if def, ok := registry.Stats[id]; ok {
			return def.Name + " · " + strings.TrimPrefix(path, "stat."+id+".")
		}
	}
	return ""
}
func traceItemName(instance CharacterInstance, registry EffectiveRegistry, grant EffectGrant) string {
	if !grant.Item {
		return "У персонажа"
	}
	for _, item := range instance.Inventory {
		if item.ID == grant.SourceID {
			return inventoryItemName(registry.Objects, item)
		}
	}
	return grant.SourceID
}
func (r *actionExecution) appendTraceRead(path string, v exprValue) {
	t := r.result.Trace
	if t == nil {
		return
	}
	if len(t.Reads) >= maxTraceReads {
		t.Truncated = true
		return
	}
	for _, existing := range t.Reads {
		if existing.Path == path {
			return
		}
	}
	row := ActionTraceRead{Path: path, Name: traceStatName(path, r.registry), Value: traceScalar(v.scalar())}
	address := strings.TrimPrefix(path, "actor.")
	if strings.HasPrefix(address, "stat.") {
		// Resolve nested derived values back to the owning stat. This
		// exposes, for example, the two ring contributions behind strength.bonus.
		id := strings.TrimPrefix(address, "stat.")
		for end := len(id); end > 0; {
			if def, exists := r.registry.Stats[id[:end]]; exists {
				parentPath := "stat." + id[:end]
				if address != parentPath {
					field := strings.TrimPrefix(address, parentPath+".")
					if def.V04 != nil {
						if raw, ok := def.V04.values[field].(map[string]any); ok {
							row.Formula, _ = raw["formula"].(string)
						}
					}
					if origin := r.statTraces[parentPath]; origin != nil {
						entry := &ActionTraceOrigin{Path: parentPath, Name: def.Name, Current: traceScalar(origin.Result)}
						if origin.Initial != nil {
							entry.Initial = traceScalar(origin.Initial)
						}
						for i, e := range origin.Effects {
							if i >= 8 {
								t.Truncated = true
								break
							}
							entry.Effects = append(entry.Effects, e)
						}
						for i, e := range origin.Skipped {
							if i >= 8 {
								t.Truncated = true
								break
							}
							entry.Skipped = append(entry.Skipped, e)
						}
						row.Origin = entry
					}
				}
				break
			}
			cut := strings.LastIndexByte(id[:end], '.')
			if cut < 0 {
				break
			}
			end = cut
		}
		if record := r.statTraces[address]; record != nil {
			if record.Initial != nil {
				row.Initial = traceScalar(record.Initial)
			}
			for i, e := range record.Skipped {
				if i >= 8 {
					t.Truncated = true
					break
				}
				row.Skipped = append(row.Skipped, e)
			}
			for i, e := range record.Effects {
				if i >= 8 {
					t.Truncated = true
					break
				}
				row.Effects = append(row.Effects, e)
			}
		}
	}
	t.Reads = append(t.Reads, row)
}
func (r *actionExecution) appendTraceEffect(effect *activeDefinition, grant EffectGrant, before, after exprValue) {
	t := r.result.Trace
	if t == nil {
		return
	}
	if len(t.Effects) >= maxTraceEffects {
		t.Truncated = true
		return
	}
	actor := r.session.CharacterInstances[r.result.Actor.CharacterID]
	t.Effects = append(t.Effects, ActionTraceEffect{EffectID: effect.id, SourceID: grant.SourceID, SourceName: traceItemName(actor, r.registry, grant), Formula: effect.formulaSource, Before: traceScalar(before.scalar()), After: traceScalar(after.scalar())})
}
func (r *actionExecution) appendTraceSkip(effect *activeDefinition, reason string) {
	t := r.result.Trace
	if t == nil {
		return
	}
	if len(t.Skipped) >= maxTraceSkips {
		t.Truncated = true
		return
	}
	t.Skipped = append(t.Skipped, ActionTraceSkip{EffectID: effect.id, Reason: reason})
}
