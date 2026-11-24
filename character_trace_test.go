package main

import (
	"bytes"
	"testing"
)

func TestTraceEditOverridesOnlyInventoryInstanceAndSurvivesTOML(t *testing.T) {
	snapshot, err := ParseRulesetTOML([]byte(inventoryRules + inventoryHero))
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{Ruleset: snapshot, RegistryRevision: 1, CharacterInstances: map[string]CharacterInstance{}, CharacterSources: map[string]CharacterTOMLSource{}}
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		t.Fatal(err)
	}
	hero, err := createCharacterInstance(session, Command{CharacterDefinitionID: "hero"}, registry)
	if err != nil {
		t.Fatal(err)
	}
	hero.Inventory[1].Equipped = true
	session.CharacterInstances["hero"] = hero
	before, err := characterStatsForRegistry(hero, registry)
	if err != nil {
		t.Fatal(err)
	}
	if before.Stats["basic.strength"].Integer() != 20 {
		t.Fatalf("initial strength %v", before.Stats["basic.strength"])
	}
	mutations := newCharacterMutation(session)
	cmd := Command{CharacterID: "hero", TraceEdit: &characterTraceEdit{ExpectedInstance: &hero, ExpectedRegistryRevision: 1, Changes: []characterTraceChange{{Kind: "itemEffect", StatID: "basic.strength", ItemID: "ring-1", EffectID: "ring_strength", Value: 5}}}}
	if err := applyCharacterTraceEdit(session, cmd, registry, mutations); err != nil {
		t.Fatal(err)
	}
	next := session.CharacterInstances["hero"]
	state, err := characterStatsForRegistry(next, registry)
	if err != nil {
		t.Fatal(err)
	}
	if state.Stats["basic.strength"].Integer() != 23 {
		t.Fatalf("updated strength %+v", state.Stats["basic.strength"])
	}
	if _, ok := itemEffectOverrides(next)["ring-2"]; ok {
		t.Fatal("unrelated instance changed")
	}
	if _, ok := registry.Objects["ring"].values["effect_overrides"]; ok {
		t.Fatal("shared object definition changed")
	}
	encoded, err := EncodeCharacterTOML(next, session.Ruleset.Media, registry)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte("effect_overrides")) {
		t.Fatalf("override missing from TOML: %s", encoded)
	}
	restored, err := DecodeCharacterTOML(encoded, session.Ruleset.Media, registry)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemEffectOverrides(restored)["ring-1"]["ring_strength"]; got != 5 {
		t.Fatalf("roundtrip override %d", got)
	}
	again, err := characterStatsForRegistry(restored, registry)
	if err != nil || again.Stats["basic.strength"].Integer() != 23 {
		t.Fatalf("roundtrip projection %+v %v", again.Stats, err)
	}
	if err := applyCharacterTraceEdit(session, cmd, registry, newCharacterMutation(session)); err == nil {
		t.Fatal("stale expected character accepted")
	}
}

func TestTraceEditRejectsUnavailableOrNonAdditiveEffects(t *testing.T) {
	snapshot, err := ParseRulesetTOML([]byte(inventoryRules + inventoryHero))
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{Ruleset: snapshot, RegistryRevision: 1, CharacterInstances: map[string]CharacterInstance{}, CharacterSources: map[string]CharacterTOMLSource{}}
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		t.Fatal(err)
	}
	hero, err := createCharacterInstance(session, Command{CharacterDefinitionID: "hero"}, registry)
	if err != nil {
		t.Fatal(err)
	}
	session.CharacterInstances["hero"] = hero
	for _, invalid := range []characterTraceChange{
		{Kind: "itemEffect", StatID: "basic.strength", ItemID: "ring-2", EffectID: "ring_strength", Value: 9},
		{Kind: "itemEffect", StatID: "basic.strength", ItemID: "ring-1", EffectID: "broken", Value: 9},
	} {
		cmd := Command{CharacterID: "hero", TraceEdit: &characterTraceEdit{ExpectedInstance: &hero, ExpectedRegistryRevision: 1, Changes: []characterTraceChange{invalid}}}
		if err := applyCharacterTraceEdit(session, cmd, registry, newCharacterMutation(session)); err == nil {
			t.Fatalf("accepted invalid change %+v", invalid)
		}
	}
}

const traceConstantRules = `
[stat.signal]
name = "Signal"
default = 0
gain = 10
[stat.result]
default = { formula = "stat.signal.gain" }
[stat.formula]
default = 4
bonus = { formula = "value + stat.signal.gain" }
[effect.signal_boost]
target = "stat.signal.gain"
formula = "target + 5"
priority = 1
[effect.signal_reduce]
target = "stat.signal.gain"
formula = "floor(target / 2)"
priority = 2
`

func TestTraceEditStatLiteralAffectsOnlySelectedCharacterAndSurvivesTOML(t *testing.T) {
	session, registry := computedStatSession(t, traceConstantRules)
	hero := session.CharacterInstances["hero"]
	hero.EffectIDs = []string{"signal_boost", "signal_reduce"}
	session.CharacterInstances[hero.ID] = hero
	other := cloneCharacterInstance(hero)
	other.ID, other.Name = "other", "Other"
	session.CharacterInstances[other.ID] = other

	before, err := characterStatsForRegistry(hero, registry)
	if err != nil || before.StatValues["stat.signal.gain"] != int64(7) {
		t.Fatalf("initial gain = %v, error = %v", before.StatValues["stat.signal.gain"], err)
	}
	change := characterTraceChange{Kind: "statField", StatID: "signal", Target: "stat.signal.gain", Value: 12}
	command := Command{CharacterID: hero.ID, TraceEdit: &characterTraceEdit{ExpectedInstance: &hero, ExpectedRegistryRevision: 1, Changes: []characterTraceChange{change}}}
	if err := applyCharacterTraceEdit(session, command, registry, newCharacterMutation(session)); err != nil {
		t.Fatal(err)
	}
	updated := session.CharacterInstances[hero.ID]
	if updated.StatFieldOverrides["stat.signal.gain"] != 12 {
		t.Fatalf("missing per-character override: %+v", updated.StatFieldOverrides)
	}
	after, err := characterStatsForRegistry(updated, registry)
	if err != nil || after.StatValues["stat.signal.gain"] != int64(8) || after.Stats["result"].Integer() != 8 || after.StatValues["stat.formula.bonus"] != int64(12) {
		t.Fatalf("incorrect recalculation: %+v, error = %v", after, err)
	}
	untouched, err := characterStatsForRegistry(other, registry)
	if err != nil || untouched.StatValues["stat.signal.gain"] != int64(7) || len(other.StatFieldOverrides) != 0 {
		t.Fatalf("another character changed: %+v, error = %v", untouched, err)
	}
	if registry.statPlan.constants["stat.signal.gain"] != int64(10) {
		t.Fatal("shared ruleset literal changed")
	}
	data, err := EncodeCharacterTOML(updated, session.Ruleset.Media, registry)
	if err != nil || !bytes.Contains(data, []byte("stat_field_overrides")) {
		t.Fatalf("override missing from export: %s, error = %v", data, err)
	}
	restored, err := DecodeCharacterTOML(data, session.Ruleset.Media, registry)
	if err != nil || restored.StatFieldOverrides["stat.signal.gain"] != 12 {
		t.Fatalf("override was lost on import: %+v, error = %v", restored.StatFieldOverrides, err)
	}
	// Restoring the ruleset literal removes the local override and resumes inheritance.
	reset := characterTraceChange{Kind: "statField", StatID: "signal", Target: "stat.signal.gain", Value: 10}
	command.TraceEdit.ExpectedInstance = &updated
	command.TraceEdit.Changes = []characterTraceChange{reset}
	if err := applyCharacterTraceEdit(session, command, registry, newCharacterMutation(session)); err != nil {
		t.Fatal(err)
	}
	if len(session.CharacterInstances[hero.ID].StatFieldOverrides) != 0 {
		t.Fatal("default value should remove the per-character override")
	}
}

func TestTraceEditStatLiteralRejectsNonLiteralOrWrongCharacterField(t *testing.T) {
	session, registry := computedStatSession(t, traceConstantRules)
	hero := session.CharacterInstances["hero"]
	for _, change := range []characterTraceChange{
		{Kind: "statField", StatID: "formula", Target: "stat.formula.bonus", Value: 8},
		{Kind: "statField", StatID: "signal", Target: "stat.signal", Value: 10},
		{Kind: "statField", StatID: "signal", Target: "stat.formula.bonus", Value: 10},
		{Kind: "statField", StatID: "signal", Target: "stat.signal.gain", Value: MaxSafeInteger + 1},
	} {
		command := Command{CharacterID: hero.ID, TraceEdit: &characterTraceEdit{ExpectedInstance: &hero, ExpectedRegistryRevision: 1, Changes: []characterTraceChange{change}}}
		if err := applyCharacterTraceEdit(session, command, registry, newCharacterMutation(session)); err == nil {
			t.Fatalf("accepted invalid override: %+v", change)
		}
	}
}

func TestTraceEditLiteralNestedCharacterTOMLRoundTrip(t *testing.T) {
	source := traceConstantRules + `
[character.hero]
name = "Hero"
effects = ["effect.signal_boost", "effect.signal_reduce"]
`
	snapshot, err := ParseRulesetTOML([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{Ruleset: snapshot, RegistryRevision: 1, CharacterInstances: map[string]CharacterInstance{}}
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		t.Fatal(err)
	}
	hero, err := createCharacterInstance(session, Command{CharacterDefinitionID: "hero"}, registry)
	if err != nil {
		t.Fatal(err)
	}
	hero.Persistent = true
	session.CharacterInstances[hero.ID] = hero
	command := Command{CharacterID: hero.ID, TraceEdit: &characterTraceEdit{ExpectedInstance: &hero, ExpectedRegistryRevision: 1, Changes: []characterTraceChange{
		{Kind: "statField", StatID: "signal", Target: "stat.signal.gain", Value: 12},
	}}}
	if err := applyCharacterTraceEdit(session, command, registry, newCharacterMutation(session)); err != nil {
		t.Fatal(err)
	}
	after := session.CharacterInstances[hero.ID]
	data, err := EncodeCharacterTOML(after, session.Ruleset.Media, registry)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("stat_field_overrides")) {
		t.Fatalf("override not exported: %s", data)
	}
	restored, err := DecodeCharacterTOML(data, session.Ruleset.Media, registry)
	if err != nil {
		t.Fatalf("nested import failed: %v\n%s", err, data)
	}
	if restored.StatFieldOverrides["stat.signal.gain"] != 12 {
		t.Fatalf("override missing: %+v", restored.StatFieldOverrides)
	}
}
