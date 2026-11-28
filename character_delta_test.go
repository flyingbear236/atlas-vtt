package main

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
)

func deltaJSONValue(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var result any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertDeltaProjection(t *testing.T, before characterSnapshot, delta characterDeltaMessage, after characterSnapshot) {
	t.Helper()
	wire := deltaJSONValue(t, before).(map[string]any)
	for _, op := range delta.Changes {
		var parent any = wire
		for _, key := range op.Path[:len(op.Path)-1] {
			switch n := parent.(type) {
			case map[string]any:
				parent = n[key]
			case []any:
				i, err := strconv.Atoi(key)
				if err != nil {
					t.Fatal(err)
				}
				parent = n[i]
			default:
				t.Fatalf("invalid path %v", op.Path)
			}
		}
		key := op.Path[len(op.Path)-1]
		switch n := parent.(type) {
		case map[string]any:
			if op.Remove {
				delete(n, key)
			} else {
				n[key] = deltaJSONValue(t, op.Value)
			}
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || op.Remove {
				t.Fatalf("invalid array patch %v", op)
			}
			n[i] = deltaJSONValue(t, op.Value)
		default:
			t.Fatalf("invalid path %v", op.Path)
		}
	}
	wire["revision"] = float64(delta.Revision)
	wire["characterSequence"] = float64(delta.CharacterSequence)
	if delta.ExecutionID == "" {
		delete(wire, "executionId")
	} else {
		wire["executionId"] = delta.ExecutionID
	}
	if !reflect.DeepEqual(wire, deltaJSONValue(t, after)) {
		t.Fatal("delta differs from full projection")
	}
}

func TestCharacterDeltaProjection(t *testing.T) {
	s, ss, gm := diagnosticFixture(t, diagnosticScale{"delta", 1, 2, 30, 20, 10, 0})
	cid := "character-0000"
	p := &peer{member: gm, session: ss.ID, out: make(chan any, 1)}
	s.peers = map[*peer]bool{p: true}
	s.watchCharacter(ss, p, cid, 1, true)
	before := (<-p.out).(characterSnapshot)
	if before.CharacterSequence != 1 || !before.TraceAvailable || len(before.StatTraces) != 0 {
		t.Fatal("invalid initial projection")
	}
	ch := ss.CharacterInstances[cid]
	ch.ResourceCurrent["hp"] = IntegerStatValue(99)
	ss.CharacterInstances[cid] = ch
	ss.CharacterRevision++
	s.refreshCharacterWatches(ss, map[string]bool{cid: true}, false, "attack")
	delta, ok := (<-p.out).(characterDeltaMessage)
	if !ok {
		t.Fatal("expected delta")
	}
	if delta.BaseSequence != 1 || delta.CharacterSequence != 2 || delta.ExecutionID != "attack" || len(delta.Changes) == 0 {
		t.Fatalf("bad delta: %+v", delta)
	}
	for _, op := range delta.Changes {
		switch op.Path[0] {
		case "actions", "statDefinitions", "itemDetails":
			t.Fatalf("static data in HP delta: %v", op.Path)
		}
	}
	expected, err := buildCharacterSnapshot(ss, gm, cid, 1, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected.CharacterSequence = 2
	expected.ExecutionID = "attack"
	assertDeltaProjection(t, before, delta, expected)
	if diagnosticJSON(t, delta) >= diagnosticJSON(t, expected) {
		t.Fatal("HP delta is not smaller")
	}
	// Equipment changes must invalidate cached inventory and action definitions.
	before = *p.characterBaseline
	ch = ss.CharacterInstances[cid]
	ch = cloneCharacterInstance(ch)
	ch.Inventory[0].Equipped = false
	ss.CharacterInstances[cid] = ch
	ss.CharacterRevision++
	s.refreshCharacterWatches(ss, map[string]bool{cid: true}, false)
	result := <-p.out
	expected, err = buildCharacterSnapshot(ss, gm, cid, 1, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected.CharacterSequence = 3
	switch v := result.(type) {
	case characterDeltaMessage:
		assertDeltaProjection(t, before, v, expected)
	case characterSnapshot:
		if !reflect.DeepEqual(deltaJSONValue(t, v), deltaJSONValue(t, expected)) {
			t.Fatal("fallback projection differs")
		}
	default:
		t.Fatalf("unexpected %T", result)
	}
	// Legacy clients still receive complete GM traces.
	s.watchCharacter(ss, p, cid, 2, false)
	legacy := (<-p.out).(characterSnapshot)
	if len(legacy.StatTraces) == 0 {
		t.Fatal("legacy traces missing")
	}
}

func TestCharacterDeltaTraceScope(t *testing.T) {
	s, ss, gm := diagnosticFixture(t, diagnosticScale{"trace", 1, 2, 30, 20, 10, 0})
	cid := "character-0000"
	p := &peer{member: gm, session: ss.ID, out: make(chan any, 1)}
	s.watchCharacter(ss, p, cid, 1, true)
	<-p.out
	cmd := Command{CharacterID: cid, CharacterWatch: 1, CharacterSequence: 1, StatID: "s000"}
	s.sendCharacterStatTrace(ss, p, cmd)
	reply := (<-p.out).(map[string]any)
	traces, ok := reply["statTraces"].(map[string]*statCalculationTrace)
	if !ok || len(traces) == 0 {
		t.Fatalf("trace missing: %v", reply)
	}
	full, err := characterSnapshotForMember(ss, gm, cid, 1)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range traces {
		if k != "stat.s000" && k != "stat.s000.bonus" {
			t.Fatalf("unrequested trace %s", k)
		}
		if !reflect.DeepEqual(deltaJSONValue(t, v), deltaJSONValue(t, full.StatTraces[k])) {
			t.Fatalf("trace differs: %s", k)
		}
	}
	cmd.CharacterSequence = 0
	s.sendCharacterStatTrace(ss, p, cmd)
	if (<-p.out).(map[string]any)["error"] != "stale" {
		t.Fatal("stale request accepted")
	}
	player := &peer{member: ss.Members["player"], session: ss.ID, out: make(chan any, 1)}
	s.watchCharacter(ss, player, cid, 1, true)
	<-player.out
	cmd.CharacterSequence = 1
	s.sendCharacterStatTrace(ss, player, cmd)
	if len(player.out) != 0 {
		t.Fatal("player received GM trace")
	}
	player.playerDetails = true
	s.watchCharacter(ss, player, cid, 2, true)
	detailed := (<-player.out).(characterSnapshot)
	if !detailed.TraceAvailable || detailed.Permissions.Manage {
		t.Fatal("detailed player should receive read-only trace access")
	}
	cmd.CharacterWatch = 2
	s.sendCharacterStatTrace(ss, player, cmd)
	if len(player.out) != 1 {
		t.Fatal("detailed player did not receive trace")
	}
	if reply := (<-player.out).(map[string]any); reply["error"] != nil || len(reply["statTraces"].(map[string]*statCalculationTrace)) == 0 {
		t.Fatalf("trace missing: %v", reply)
	}
	player.playerDetails = false
	s.sendCharacterStatTrace(ss, player, cmd)
	if len(player.out) != 0 {
		t.Fatal("disabled player mode received trace")
	}
}
