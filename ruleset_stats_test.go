package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const computedStatsTOML = `
[stat.basic.strength]
default = 16
bonus = { formula = "floor((value - 10) / 2)" }
[stat.level]
default = 6
[stat.proficiency]
default = { formula = "2 + floor((stat.level - 1) / 4)" }
[stat.athletics]
default = { formula = "actor.stat.basic.strength.bonus" }
trained.score = { formula = "value + stat.proficiency.base" }
ratio = { formula = "value / 3" }
[stat.result]
default = { formula = "stat.athletics.trained.score + stat.athletics.ratio * 3" }
[stat.hp]
default = { formula = "stat.basic.strength.base + 84" }
resource = true
[stat.full]
default = { formula = "stat.hp.current == stat.hp" }
[stat.label]
default = { formula = "actor.name" }
`

func mustEffectiveCharacter(t *testing.T, instance CharacterInstance, preset *CharacterPresetDefinition, registry EffectiveRegistry) EffectiveCharacter {
	t.Helper()
	state, err := EffectiveCharacterState(instance, preset, registry)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func computedStatSession(t *testing.T, source string) (*Session, EffectiveRegistry) {
	t.Helper()
	snapshot, err := ParseRulesetTOML([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: snapshot.Registry})
	if err != nil {
		t.Fatal(err)
	}
	hero := NewCharacterInstance("hero", nil)
	hero.Persistent = true
	return &Session{Ruleset: snapshot, RegistryRevision: 1, CharacterRevision: 1,
		CharacterInstances: map[string]CharacterInstance{"hero": hero}}, registry
}

func runStatCommand(t *testing.T, session *Session, cmd Command) error {
	t.Helper()
	cmd.CharacterID = "hero"
	mutation := newCharacterMutation(session)
	err := applyCharacterCommand(session, &Member{GM: true}, "", cmd, mutation)
	if err != nil {
		mutation.rollback()
	}
	return err
}

func TestComputedStatProjectionAndCommands(t *testing.T) {
	session, registry := computedStatSession(t, computedStatsTOML)
	check := func(bonus, score, result, max, current int64) {
		t.Helper()
		instance := session.CharacterInstances["hero"]
		state := mustEffectiveCharacter(t, instance, nil, registry)
		if state.StatValues["stat.basic.strength.bonus"] != bonus || state.Stats["athletics"] != IntegerStatValue(bonus) ||
			state.StatValues["stat.athletics.trained.score"] != score || state.Stats["result"] != IntegerStatValue(result) ||
			state.Stats["proficiency"] != IntegerStatValue(3) || state.Stats["label"] != StringStatValue(instance.Name) ||
			state.StatValues["stat.hp.base"] != max || state.StatValues["stat.hp"] != max ||
			state.StatValues["stat.hp.current"] != current || state.Stats["full"] != BooleanStatValue(max == current) {
			t.Fatalf("unexpected values %#v", state.StatValues)
		}
	}
	check(3, 6, 9, 100, 100)
	set := func(command, id string, n int64) error {
		return runStatCommand(t, session, Command{Type: command, StatID: id, StatValue: statValuePointer(IntegerStatValue(n))})
	}
	if err := set("characterStatSet", "basic.strength", 18); err != nil {
		t.Fatal(err)
	}
	check(4, 7, 11, 102, 100)
	if err := set("characterResourceCurrentSet", "hp", 85); err != nil {
		t.Fatal(err)
	}
	if err := set("characterStatSet", "basic.strength", 12); err != nil {
		t.Fatal(err)
	}
	check(1, 4, 5, 96, 85)
	if err := set("characterStatSet", "athletics", 77); err == nil || !strings.Contains(err.Error(), "computed") {
		t.Fatalf("editing computed base: %v", err)
	}
	if err := set("characterResourceInitialSet", "hp", 77); err == nil || !strings.Contains(err.Error(), "computed") {
		t.Fatalf("editing computed resource base: %v", err)
	}
	instance := session.CharacterInstances["hero"]
	before, err := EncodeCharacterTOML(instance, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		check(1, 4, 5, 96, 85)
	}
	after, err := EncodeCharacterTOML(session.CharacterInstances["hero"], nil, registry)
	if err != nil || !bytes.Equal(before, after) || bytes.Contains(after, []byte("athletics")) {
		t.Fatalf("read changed saved sources: %s, %v", after, err)
	}
	restored, err := DecodeCharacterTOML(after, nil, registry)
	if err != nil || len(restored.Materialized.Stats) != 1 || len(restored.Materialized.ResourceInitial) != 0 || restored.ResourceCurrent["hp"] != IntegerStatValue(85) {
		t.Fatalf("source round trip: %+v, %v", restored, err)
	}
	session.ResourceOverlays = []ResourceOverlayConfig{{StatID: "hp", Enabled: true}}
	display, ok := characterDisplayForConfig(session, registry, "hero")
	if !ok || display.Resources["hp"] != (EffectiveResource{Current: IntegerStatValue(85), Initial: IntegerStatValue(96)}) {
		t.Fatalf("resource overlay: %+v", display)
	}
}

func TestComputedStatBulkEdit(t *testing.T) {
	session, registry := computedStatSession(t, computedStatsTOML)
	instance := session.CharacterInstances["hero"]
	state := mustEffectiveCharacter(t, instance, nil, registry)
	edit := CharacterEditPayload{ExpectedRegistryRevision: 1, ExpectedInstance: &instance, Name: instance.Name,
		Stats: state.Stats, ResourceInitial: map[string]StatValue{"hp": IntegerStatValue(100)},
		ResourceCurrent: map[string]StatValue{"hp": IntegerStatValue(100)}}
	edit.Stats["basic.strength"] = IntegerStatValue(20)
	if err := runStatCommand(t, session, Command{Type: "characterEdit", CharacterEdit: &edit}); err != nil {
		t.Fatal(err)
	}
	instance = session.CharacterInstances["hero"]
	if len(instance.Materialized.Stats) != 1 || len(instance.Materialized.ResourceInitial) != 0 || instance.ResourceCurrent["hp"] != IntegerStatValue(100) {
		t.Fatalf("effective values leaked into sources: %+v", instance)
	}
	edit.Stats = mustEffectiveCharacter(t, instance, nil, registry).Stats
	edit.Stats["athletics"] = IntegerStatValue(99)
	edit.ResourceInitial["hp"] = IntegerStatValue(104)
	if err := runStatCommand(t, session, Command{Type: "characterEdit", CharacterEdit: &edit}); err == nil || !strings.Contains(err.Error(), "computed") {
		t.Fatalf("bulk computed override: %v", err)
	}
}

func TestComputedStatGraphValidationAndReload(t *testing.T) {
	for _, src := range []string{
		"[stat.a]\ndefault={formula='stat.b.score'}\n[stat.b]\ndefault={formula='stat.a.base'}\nscore={formula='value'}",
		"[stat.a]\ndefault=0\nx={formula='actor.stat.a.y'}\ny={formula='stat.a.x'}",
	} {
		if _, err := ParseRulesetTOML([]byte(src)); err == nil || !strings.Contains(err.Error(), "cyclic") {
			t.Fatalf("cycle accepted: %v", err)
		}
	}
	session, registry := computedStatSession(t, computedStatsTOML)
	updated, err := ParseRulesetTOML([]byte(strings.Replace(computedStatsTOML, "+ 84", "+ 94", 1)))
	if err != nil {
		t.Fatal(err)
	}
	changes, err := prepareDefinitionCharacters(session, updated, emptyCampaignRegistry())
	if err != nil || changes["hero"].ResourceCurrent["hp"] != IntegerStatValue(100) || len(session.CharacterInstances["hero"].ResourceCurrent) != 0 {
		t.Fatalf("ruleset replacement changed or refilled live state: %+v %v", changes, err)
	}
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var restored Session
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	loaded, err := MergeDefinitionRegistries(sessionRegistries(&restored))
	if err != nil {
		t.Fatal(err)
	}
	got := mustEffectiveCharacter(t, restored.CharacterInstances["hero"], nil, loaded)
	want := mustEffectiveCharacter(t, session.CharacterInstances["hero"], nil, registry)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("reloaded graph produced different values")
	}
}

func TestComputedStatResourceImportAndReset(t *testing.T) {
	const rules = "[stat.hp]\nresource=true\ndefault=100"
	session, registry := computedStatSession(t, rules)
	archive := portableImportArchive{Ruleset: map[string]string{"stats.toml": strings.ReplaceAll(rules, "100", "120")}}
	candidate, err := buildPortableImportCandidate(context.Background(), session, archive, portableImportOptions{Mode: "replace"}, t.TempDir(), nil, nil, true)
	if err != nil || candidate.Characters["hero"].ResourceCurrent["hp"] != IntegerStatValue(100) {
		t.Fatalf("ZIP replacement changed implicit current: %+v, %v", candidate.Characters, err)
	}
	if err := runStatCommand(t, session, Command{Type: "characterResourceInitialSet", StatID: "hp", StatValue: statValuePointer(IntegerStatValue(80))}); err != nil {
		t.Fatal(err)
	}
	state := mustEffectiveCharacter(t, session.CharacterInstances["hero"], nil, registry)
	if state.Resources["hp"] != (EffectiveResource{Initial: IntegerStatValue(80), Current: IntegerStatValue(100)}) {
		t.Fatalf("maximum edit changed or clamped current: %+v", state.Resources)
	}
	if err := runStatCommand(t, session, Command{Type: "characterResourceCurrentReset", StatID: "hp"}); err != nil {
		t.Fatal(err)
	}
	if got := mustEffectiveCharacter(t, session.CharacterInstances["hero"], nil, registry).StatValues["stat.hp.current"]; got != int64(80) {
		t.Fatalf("current after explicit reset: %v", got)
	}
}

func TestComputedStatErrorsAndBounds(t *testing.T) {
	const rules = "[stat.denominator]\ndefault=2\n[stat.hp]\ndefault={formula='10 / stat.denominator'}\nresource=true"
	session, registry := computedStatSession(t, rules)
	for _, n := range []int64{0, 3} {
		if err := runStatCommand(t, session, Command{Type: "characterStatSet", StatID: "denominator", StatValue: statValuePointer(IntegerStatValue(n))}); err == nil || !strings.Contains(err.Error(), "stat.hp.default") {
			t.Fatalf("expected formula diagnostic for %d: %v", n, err)
		}
		if state := mustEffectiveCharacter(t, session.CharacterInstances["hero"], nil, registry); state.StatValues["stat.hp"] != int64(5) {
			t.Fatal("failed edit changed source data")
		}
	}
	if _, err := ParseRulesetTOML([]byte(rules + "\n[character.hero.stat.hp]\ncurrent='85'")); err == nil {
		t.Fatal("accepted wrong current type for inferred computed maximum")
	}
	if _, err := DecodeCharacterTOML([]byte("schema_version=1\n[character]\nid='hero'\nname='Hero'\npersistent=true\nmaterialized=true\n[character.resource_initial]\nhp=20"), nil, registry); err == nil || !strings.Contains(err.Error(), "computed") {
		t.Fatalf("imported editable copy of computed base: %v", err)
	}
}

func TestComputedStatResourceDependencies(t *testing.T) {
	const rules = `[stat.settings]
default = "visible"
profile."sides.count" = 8
unused = { formula = "1 / 0" }
[stat.bonus]
default = 2
part = { formula = 'value * stat.settings.profile."sides.count"' }
[stat.hp]
resource = true
default = { formula = "actor.stat.bonus.part" }
`
	session, registry := computedStatSession(t, rules)
	hero := session.CharacterInstances["hero"]
	if _, err := EffectiveCharacterState(hero, nil, registry); err == nil || !strings.Contains(err.Error(), "stat.settings.unused") {
		t.Fatalf("missing diagnostic for sheet: %v", err)
	}
	session.ResourceOverlays = []ResourceOverlayConfig{{StatID: "hp", Enabled: true}}
	display, ok := characterDisplayForConfig(session, registry, hero.ID)
	if !ok || display.Resources["hp"].Initial != IntegerStatValue(16) {
		t.Fatalf("unrelated field blocked resource overlay: %+v", display)
	}
}
