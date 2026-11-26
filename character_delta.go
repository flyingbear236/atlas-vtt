package main

import (
	"reflect"
	"strconv"
	"strings"
)

func sameSheetKeys[V any](a, b map[string]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

type characterPatchOp struct {
	Path   []string `json:"path"`
	Value  any      `json:"value"` // null is a legitimate value; Remove distinguishes deletion.
	Remove bool     `json:"remove,omitempty"`
}

type characterDeltaMessage struct {
	Type              string             `json:"type"`
	CharacterID       string             `json:"characterId"`
	CharacterWatch    uint64             `json:"characterWatch"`
	BaseSequence      uint64             `json:"baseSequence"`
	CharacterSequence uint64             `json:"characterSequence"`
	Revision          uint64             `json:"revision"`
	ExecutionID       string             `json:"executionId,omitempty"`
	Changes           []characterPatchOp `json:"changes"`
}

func sheetJSONEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64, reflect.Interface, reflect.Pointer:
		return v.IsZero()
	}
	return false
}

// Diff only ordinary wire structs and string-keyed maps. Values with custom JSON
// encoding (StatValue, RulesetFields) are atomic; arrays retain their exact order.
// The old projection is immutable until its watch slot is replaced.
func characterSnapshotChanges(old, next characterSnapshot) []characterPatchOp {
	changes := make([]characterPatchOp, 0, 8)
	var diff func(reflect.Value, reflect.Value, []string)
	add := func(path []string, k string) []string { return append(append([]string(nil), path...), k) }
	diff = func(a, b reflect.Value, path []string) {
		if reflect.DeepEqual(a.Interface(), b.Interface()) {
			return
		}
		if a.Kind() == reflect.Pointer && !a.IsNil() && !b.IsNil() && a.Type().Elem() == reflect.TypeOf(CharacterMechanicalState{}) {
			diff(a.Elem(), b.Elem(), path)
			return
		}
		if a.Kind() == reflect.Struct && (len(path) == 0 || a.Type() == reflect.TypeOf(CharacterInstance{}) || a.Type() == reflect.TypeOf(EffectiveCharacter{}) || a.Type() == reflect.TypeOf(CharacterMechanicalState{})) {
			for i := 0; i < a.NumField(); i++ {
				field := a.Type().Field(i)
				if field.PkgPath != "" {
					continue
				}
				tag := strings.Split(field.Tag.Get("json"), ",")
				key := tag[0]
				if key == "" || key == "-" {
					continue
				}
				if len(path) == 0 && (key == "type" || key == "characterWatch" || key == "characterId" || key == "characterSequence" || key == "revision" || key == "executionId") {
					continue
				}
				av, bv := a.Field(i), b.Field(i)
				p := add(path, key)
				if len(tag) > 1 && tag[1] == "omitempty" {
					ae, be := sheetJSONEmpty(av), sheetJSONEmpty(bv)
					if be {
						if !ae {
							changes = append(changes, characterPatchOp{Path: p, Remove: true})
						}
						continue
					}
					if ae {
						changes = append(changes, characterPatchOp{Path: p, Value: bv.Interface()})
						continue
					}
				}
				diff(av, bv, p)
			}
			return
		}
		if a.Kind() == reflect.Map && a.Type().Key().Kind() == reflect.String && !a.IsNil() && !b.IsNil() {
			for _, k := range a.MapKeys() {
				if !b.MapIndex(k).IsValid() {
					changes = append(changes, characterPatchOp{Path: add(path, k.String()), Remove: true})
				}
			}
			for _, k := range b.MapKeys() {
				av, bv := a.MapIndex(k), b.MapIndex(k)
				if !av.IsValid() || !reflect.DeepEqual(av.Interface(), bv.Interface()) {
					changes = append(changes, characterPatchOp{Path: add(path, k.String()), Value: bv.Interface()})
				}
			}
			return
		}
		// Same-size arrays can be patched by index. Insertions/removals use
		// a full replacement, keeping ordering and JSON array semantics.
		if a.Kind() == reflect.Slice && !a.IsNil() && !b.IsNil() && a.Len() == b.Len() && len(path) > 0 {
			for i := 0; i < a.Len(); i++ {
				if !reflect.DeepEqual(a.Index(i).Interface(), b.Index(i).Interface()) {
					changes = append(changes, characterPatchOp{Path: add(path, strconv.Itoa(i)), Value: b.Index(i).Interface()})
				}
			}
			return
		}
		changes = append(changes, characterPatchOp{Path: path, Value: b.Interface()})
	}
	diff(reflect.ValueOf(old), reflect.ValueOf(next), nil)
	return changes
}

func (s *Server) sendCharacterProjection(p *peer, snapshot characterSnapshot) {
	if !p.characterDelta {
		s.send(p, snapshot)
		return
	}
	previous := p.characterBaseline
	snapshot.CharacterSequence = 1
	// Internal active effects are used only while projecting, never retained by
	// the wire baseline or compared by the diff.
	snapshot.Effective.activeEffects = nil
	if previous != nil {
		snapshot.CharacterSequence = previous.CharacterSequence + 1
		changes := characterSnapshotChanges(*previous, snapshot)
		if len(changes) <= 128 {
			s.send(p, characterDeltaMessage{Type: "characterDelta", CharacterID: snapshot.CharacterID, CharacterWatch: snapshot.CharacterWatch, BaseSequence: previous.CharacterSequence, CharacterSequence: snapshot.CharacterSequence, Revision: snapshot.Revision, ExecutionID: snapshot.ExecutionID, Changes: changes})
		} else {
			s.send(p, snapshot)
		}
	} else {
		s.send(p, snapshot)
	}
	p.characterBaseline = &snapshot
}

// Trace requests are scoped to the currently watched character and projection.
// No result from an older projection is ever labeled as current.
func (s *Server) sendCharacterStatTrace(session *Session, p *peer, msg Command) {
	if !p.characterDelta || p.characterBaseline == nil || !memberIsGM(p.member) || p.playerPreview || msg.CharacterWatch != p.characterWatch || msg.CharacterID != p.characterID || !memberCanAccessCharacter(session, p.member, msg.CharacterID) {
		return
	}
	baseline := p.characterBaseline
	response := map[string]any{"type": "characterStatTrace", "characterWatch": msg.CharacterWatch, "characterId": msg.CharacterID, "characterSequence": msg.CharacterSequence, "statId": msg.StatID}
	if msg.CharacterSequence != baseline.CharacterSequence || session.RegistryRevision != baseline.RegistryRevision {
		response["error"] = "stale"
		s.send(p, response)
		return
	}
	if _, ok := baseline.StatDefinitions[msg.StatID]; !ok || !baseline.TraceAvailable {
		response["error"] = "unavailable"
		s.send(p, response)
		return
	}
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		response["error"] = "unavailable"
		s.send(p, response)
		return
	}
	instance, preset := baseline.Instance, baseline.Preset
	traces := map[string]*statCalculationTrace{}
	addresses := map[string]bool{"stat." + msg.StatID: true}
	for _, field := range baseline.StatFields[msg.StatID].Derived {
		addresses["stat."+msg.StatID+"."+field.Name] = true
	}
	if _, err = projectCharacterStatsWithSelectedTrace(instance, preset, registry, nil, nil, false, traces, addresses); err != nil {
		response["error"] = "unavailable"
	} else {
		response["statTraces"] = traces
	}
	s.send(p, response)
}
