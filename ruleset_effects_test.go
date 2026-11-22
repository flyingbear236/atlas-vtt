package main

import (
	"encoding/json"
	"math/big"
	"slices"
	"strings"
	"testing"
)

const effectRules = `
[stat.strength]
default=16
bonus={formula="floor((value - 10) / 2)"}
ratio={formula="value / 3"}
flat=2
[stat.athletics]
default={formula="actor.stat.strength.bonus + stat.strength.flat"}
[stat.hp]
default={formula="stat.athletics + 80"}
resource=true
[stat.z]
default=1

[effect.buff]
target="stat.strength"
formula="target + 2"
[effect.skill]
target=["stat.strength.bonus","stat.strength.flat","stat.strength.flat"]
formula="target + stat.z"
[effect.half]
target="stat.strength.ratio"
formula="target / 2"
[effect.training]
target="stat.z"
when="actor.profile.trained"
formula="target + 2"
[effect.frombase]
target="stat.strength"
formula="target + stat.strength.base"
[effect.loop]
target="stat.strength"
formula="target + stat.hp"
[effect.disabled]
target="stat.strength"
when="false"
formula="target + stat.hp"
[effect.bad]
target="stat.strength"
formula="target / 3"
`

func effectSet(t *testing.T, s RulesetSnapshot, grants ...EffectGrant) *ActiveEffects {
	t.Helper()
	e, err := s.ActiveEffects(grants)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestEffectsStatProjection(t *testing.T) {
	session, registry := computedStatSession(t, effectRules)
	s, hero := session.Ruleset, session.CharacterInstances["hero"]
	hero = WithResourceCurrent(hero, "hp", IntegerStatValue(85))
	before, err := json.Marshal(hero)
	if err != nil {
		t.Fatal(err)
	}
	check := func(e *ActiveEffects, trained bool, strength, bonus, flat, hp int64) {
		t.Helper()
		actor := map[string]any{"profile": map[string]any{"trained": trained}}
		st, err := e.CharacterState(hero, nil, registry, actor)
		if err != nil {
			t.Fatal(err)
		}
		if st.StatValues["stat.strength.base"] != int64(16) || st.Stats["strength"] != IntegerStatValue(strength) ||
			st.StatValues["stat.strength.bonus"] != bonus || st.StatValues["stat.strength.flat"] != flat ||
			st.Stats["athletics"] != IntegerStatValue(bonus+flat) || st.Resources["hp"] != (EffectiveResource{Current: IntegerStatValue(85), Initial: IntegerStatValue(hp)}) {
			t.Fatalf("incorrect projection: %+v", st)
		}
		overlay, err := projectCharacterStats(hero, nil, registry, e, actor, true)
		if err != nil || overlay.Resources["hp"] != st.Resources["hp"] {
			t.Fatalf("overlay %+v %v", overlay, err)
		}
	}
	buff := EffectGrant{"buff", "hero", false}
	on := effectSet(t, s, buff)
	check(on, false, 18, 4, 2, 86)
	check(on, false, 18, 4, 2, 86)
	check(effectSet(t, s), false, 16, 3, 2, 85)
	both := effectSet(t, s, buff, EffectGrant{"skill", "ring", true}, EffectGrant{"training", "hero", false})
	check(both, true, 18, 7, 5, 92)
	check(both, false, 18, 5, 3, 88)
	check(effectSet(t, s, EffectGrant{"frombase", "hero", false}), false, 32, 11, 2, 93)
	check(effectSet(t, s, EffectGrant{"disabled", "hero", false}), false, 16, 3, 2, 85)
	mods := effectSet(t, s, EffectGrant{"half", "hero", false})
	st, err := mods.CharacterState(hero, nil, registry, nil)
	ratio, ok := st.StatValues["stat.strength.ratio"].(*big.Rat)
	if err != nil || !ok || ratio.RatString() != "8/3" {
		t.Fatalf("exact custom field: %+v %v", st, err)
	}
	for _, tc := range []struct{ id, diagnostic string }{{"loop", "cyclic"}, {"bad", "effect.bad.formula"}} {
		_, err := effectSet(t, s, EffectGrant{tc.id, "hero", false}).CharacterState(hero, nil, registry, nil)
		if err == nil || !strings.Contains(err.Error(), tc.diagnostic) {
			t.Fatalf("%s: %v", tc.id, err)
		}
	}
	after, err := json.Marshal(hero)
	if err != nil || string(after) != string(before) {
		t.Fatalf("changed persisted source: %s", after)
	}
}

func TestEffectsTypedFieldsAndExactAddresses(t *testing.T) {
	const rules = `
[stat.sheet]
default=0
profile."display.name"="Scout"
visible=false
[action.target.hit]
roll="2"
[effect.rename]
target='stat.sheet.profile."display.name"'
formula="actor.title"
[effect.show]
target="stat.sheet.visible"
formula="not target"
[effect.add]
target=["action.target.hit.roll","stat.sheet"]
formula="target + 1"
`
	session, registry := computedStatSession(t, rules)
	s := session.Ruleset
	e := effectSet(t, s, EffectGrant{"rename", "hero", false}, EffectGrant{"show", "hero", false}, EffectGrant{"add", "hero", false})
	hero := session.CharacterInstances["hero"]
	state, err := e.CharacterState(hero, nil, registry, map[string]any{"title": "Ranger"})
	if err != nil || state.StatValues[`stat.sheet.profile."display.name"`] != "Ranger" || state.StatValues["stat.sheet.visible"] != true ||
		state.StatValues["stat.sheet"] != int64(1) || state.StatValues["stat.sheet.base"] != int64(0) {
		t.Fatalf("typed fields: %+v %v", state, err)
	}
	f := s.actionFrameWithEffects("target.hit", nil, nil, e)
	if v, err := f.Evaluate(f.Locals["roll"]); err != nil || v.scalar() != int64(3) {
		t.Fatalf("target-prefixed action ID: %v %v", v, err)
	}
	if _, err := e.CharacterState(hero, nil, registry, map[string]any{"title": int64(7)}); err == nil || !strings.Contains(err.Error(), "effect.rename.formula") {
		t.Fatalf("unexpected result for invalid dynamic field type: %v", err)
	}
}

func TestEffectsOrderAndStack(t *testing.T) {
	const stat = "[stat.n]\ndefault=16\n"
	const add = "[effect.add]\ntarget='stat.n'\nstack='unique'\nformula='target + 5'\n"
	const half = "[effect.half]\ntarget='stat.n'\nformula='floor(target / 2)'\n"
	grants := []EffectGrant{{"half", "b", true}, {"add", "a", true}, {"half", "a", true}, {"add", "b", true}, {"half", "a", true}}
	for _, tc := range []struct {
		priority string
		want     int64
		warning  bool
	}{{"", 5, true}, {"priority=-1\n", 9, false}} {
		for _, swap := range []bool{false, true} {
			first, second := add, half+tc.priority
			if swap {
				first, second = second, first
			}
			s, err := CompileRulesetFiles(map[string]string{"stats.toml": stat, "a.toml": first, "z.toml": second})
			if err != nil {
				t.Fatal(err)
			}
			reg, err := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: s.Registry})
			if err != nil {
				t.Fatal(err)
			}
			e := effectSet(t, s, grants...)
			hero := NewCharacterInstance("hero", nil)
			st, err := e.CharacterState(hero, nil, reg, nil)
			if err != nil || st.Stats["n"] != IntegerStatValue(tc.want) {
				t.Fatalf("order: %+v %v", st, err)
			}
			ws := s.effectPriorityWarnings()
			if (len(ws) > 0) != tc.warning || tc.warning && !strings.Contains(ws[0], "stat.n") {
				t.Fatalf("warnings %v", ws)
			}
			slices.Reverse(grants)
		}
	}
}

func TestEffectsActionInvocation(t *testing.T) {
	const rules = `
[action.weapon.hit]
roll="1d20"
success="roll == 12"
[action.weapon.miss]
roll="1"
[effect.edge]
scope="source"
stack="unique"
target="action.weapon.hit.roll"
when="source.group == 'martial'"
formula="target + actor.skill + action.target.guard"
[effect.source]
target="action.weapon.hit.roll"
formula="target + source.bonus"
[effect.invalid]
target="action.weapon.hit.roll"
formula="target / 0"
`
	s, err := ParseRulesetTOML([]byte(rules))
	if err != nil {
		t.Fatal(err)
	}
	// Snapshot reload and cloning must retain the compiled plans.
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	s = cloneRulesetSnapshot(s)
	active := effectSet(t, s, EffectGrant{"edge", "a", true}, EffectGrant{"edge", "b", true}, EffectGrant{"edge", "c", false}, EffectGrant{"source", "hero", false})
	for _, tc := range []struct {
		id, group string
		target    bool
		want      int64
	}{
		{"b", "martial", true, 12}, {"b", "simple", true, 7}, {"c", "martial", true, 7}, {"other", "martial", true, 7}, {"b", "martial", false, 7}, {"", "", true, 5},
	} {
		bindings := map[string]any{"actor.skill": int64(3)}
		if tc.id != "" {
			bindings["source"] = map[string]any{"id": tc.id, "definition": "sword", "group": tc.group, "bonus": int64(2)}
		}
		if tc.target {
			bindings["target.guard"] = int64(2)
		}
		rng := &deterministicDiceRNG{values: []int{4}, failAt: -1}
		f := s.actionFrameWithEffects("weapon.hit", bindings, rng, active)
		success, err := f.Evaluate(s.expressions["action.weapon.hit.success"])
		if err != nil || success.flag != (tc.want == 12) {
			t.Fatalf("%+v: success %v %v", tc, success, err)
		}
		v, err := f.Evaluate(f.Locals["roll"])
		if err != nil || v.scalar() != tc.want || rng.next != 1 {
			t.Fatalf("%+v: %v %v, draws %d", tc, v, err, rng.next)
		}
	}
	broken := effectSet(t, s, EffectGrant{"invalid", "hero", false})
	rng := &deterministicDiceRNG{values: []int{2}, failAt: -1}
	f := s.actionFrameWithEffects("weapon.hit", nil, rng, broken)
	for range 2 {
		if _, err := f.Evaluate(f.Locals["roll"]); err == nil || !strings.Contains(err.Error(), "effect.invalid.formula") || rng.next != 1 {
			t.Fatalf("failed roll was lost: %v draws %d", err, rng.next)
		}
	}
	f = s.actionFrameWithEffects("weapon.miss", nil, rng, broken)
	if v, err := f.Evaluate(f.Locals["roll"]); err != nil || v.scalar() != int64(1) {
		t.Fatalf("unrelated action: %v %v", v, err)
	}
	if _, err := s.ActiveEffects([]EffectGrant{{"missing", "hero", false}}); err == nil {
		t.Fatal("accepted unknown grant")
	}
}
