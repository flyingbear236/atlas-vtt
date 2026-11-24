package main

import (
	"slices"
	"testing"
)

func TestActionTraceCapturesRollAndEffectsWithoutReroll(t *testing.T) {
	session, member := executorSession(t)
	rng := &deterministicDiceRNG{values: []int{10}, failAt: 1}
	result, err := ExecuteRulesetAction(session, "arena", member, ActionExecutionRequest{CharacterID: "hero", ActionID: "weapon.hit", SourceID: "blade", Target: &ActionInstanceRef{CharacterID: "goblin"}}, rng)
	if err != nil {
		t.Fatal(err)
	}
	trace := result.Trace
	if trace == nil || trace.RollBase != "14" || trace.RollTotal != "15" || trace.RollFormula != "1d20 + stat.strength.bonus" || rng.next != 1 {
		t.Fatalf("incorrect actual trace: %#v, rng=%d", trace, rng.next)
	}
	if !slices.ContainsFunc(trace.Reads, func(row ActionTraceRead) bool { return row.Path == "stat.strength.bonus" && row.Value == "3" }) {
		t.Fatalf("stat dependency missing: %+v", trace.Reads)
	}
	if !slices.ContainsFunc(trace.Effects, func(effect ActionTraceEffect) bool {
		return effect.EffectID == "accurate" && effect.SourceID == "blade" && effect.Before == "14" && effect.After == "15"
	}) {
		t.Fatalf("source-specific effect missing: %+v", trace.Effects)
	}
}

func TestActionTracePrivateToGM(t *testing.T) {
	session, player := executorSession(t)
	data := &actionJournalEntry{ActionEvent: ActionEvent{SceneID: "arena", CharacterID: "hero", ActionID: "simple", Trace: &ActionTrace{RollFormula: "1d20 + hidden"}}}
	audience := audienceForAction(session, &peer{member: player, sceneID: "arena"})
	projected, ok := audience.project(data)
	if ok && projected.Trace != nil {
		t.Fatalf("trace leaked to player: %v %+v", ok, projected)
	}
	gm := &Member{ID: "gm", GM: true}
	audience = audienceForAction(session, &peer{member: gm, sceneID: "arena"})
	projected, ok = audience.project(data)
	if !ok || projected.Trace == nil {
		t.Fatalf("GM trace hidden: %v %+v", ok, projected)
	}
	audience.peer.playerPreview = true
	projected, ok = audience.project(data)
	if ok && projected.Trace != nil {
		t.Fatal("GM player preview received debug data")
	}
}
