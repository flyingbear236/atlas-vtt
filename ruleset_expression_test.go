package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func actionExpr(t *testing.T, source string) *Expression {
	t.Helper()
	snapshot, err := ParseRulesetTOML([]byte(fmt.Sprintf("[action.test]\nroll = '1'\nvar.result = %q\n", source)))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.expressions["action.test.var.result"]
}

func TestRulesetExpressionValues(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"floor((16 - 10) / 2)", "3"}, {"2 + floor((6 - 1) / 4)", "3"}, {"floor(-5/4)", "-2"},
		{"9007199254740993 - 9007199254740992", "1"}, {"(1 / 3) * 3", "1"}, {"1 / 3", "1/3"},
		{"-9223372036854775808", "-9223372036854775808"}, {"2 + 3 * 4", "14"},
		{"true && !(3 >= 4) && 'abc' != 'abcd'", "true"}, {"false && target.stateless == 0", "false"},
		{"actor.profile.\"custom.key\"", "hello"}, {"source.weapon_group == 'martial'", "true"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			p := actionExpr(t, tc.source)
			f := ExpressionFrame{Bindings: map[string]any{"actor": map[string]any{"profile": map[string]any{"custom.key": "hello"}}, "source.weapon_group": "martial"}}
			value, err := f.Evaluate(p)
			if err != nil {
				t.Fatal(err)
			}
			var got string
			switch value.typ {
			case exprNumber:
				got = value.num.RatString()
			case exprText:
				got = value.text
			case exprBool:
				got = fmt.Sprint(value.flag)
			}
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
func TestRulesetExpressionRejectsInvalidPrograms(t *testing.T) {
	for _, source := range []string{"x", "foo(1)", "1 + true", "'bad' * 3", "2 == '2'", "floor(false)", "1d0", "101d6", "dice('0d6')", "1d10001", "stat.missing", "roll.missing", "value", "var.unknown", "[1]", "1;2", "1.1", "9223372036854775808", "true garbage", "(1", strings.Repeat("(", 60) + "1" + strings.Repeat(")", 60), strings.Repeat("1+", 60) + "1", strings.Repeat(" ", maxExpressionBytes) + "1"} {
		_, err := ParseRulesetTOML([]byte(fmt.Sprintf("[action.test]\nroll = %q\n", source)))
		if err == nil || !strings.Contains(err.Error(), "action.test.roll") {
			t.Errorf("missing diagnostic for %.80q: %v", source, err)
		}
	}
	for _, source := range []string{
		"[stat.x]\ndefault={formula='1d20'}",
		"[stat.x]\ntype='boolean'\ndefault={formula='2'}",
		"[stat.x]\ndefault={formula='stat.y'}\n[stat.y]\ndefault={formula='stat.x'}",
		"[action.x]\nroll='var.x'\nvar.x='roll + 1'",
		"[stat.s]\ndefault='text'\n[effect.x]\ntarget='stat.s'\nformula='target + 1'",
		"[object.x]\ndamage='1d6'\n[action.x]\nroll='source.damage + 1'",
		"[action.x]\nroll='1'\nsuccess='42'",
	} {
		if _, err := ParseRulesetTOML([]byte(source)); err == nil {
			t.Errorf("accepted invalid TOML %s", source)
		}
	}
}
func TestRulesetExpressionRuntimeLimits(t *testing.T) {
	for _, source := range []string{"9223372036854775807 + 1", "-9223372036854775808 / -1", "1 / 0", "1 % 0", "1 / 9223372036854775807 / 9223372036854775807 / 9223372036854775807", "100d6 + 1d6", "source.damage + 1", "dice(source.damage)", "target.stateless"} {
		p := actionExpr(t, source)
		rng := &deterministicDiceRNG{failAt: -1}
		frame := ExpressionFrame{Bindings: map[string]any{"source.damage": "2d90000"}, RNG: rng}
		if _, err := frame.Evaluate(p); err == nil {
			t.Errorf("unexpected success for %s", source)
		}
		if rng.next > MaxDicePerRoll {
			t.Fatal("dice limit exceeded")
		}
	}
	frame := ExpressionFrame{steps: maxExpressionSteps - 1}
	if _, err := frame.Evaluate(actionExpr(t, "1+2")); err == nil {
		t.Fatal("execution limit ignored")
	}
}
func TestRulesetExpressionExecutionFrame(t *testing.T) {
	source := "[stat.ac]\ndefault=12\n[action.hit]\nroll='1d20 + dice(source.damage) + dice(\"1d8\")'\nvar.twice='roll + roll'\nsuccess='var.twice >= target.stat.ac'\n[effect.bonus]\ntarget='stat.ac'\nformula='target + action.target.stat.ac'"
	s, err := ParseRulesetTOML([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var restored RulesetSnapshot
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	s = cloneRulesetSnapshot(restored)
	rng := &deterministicDiceRNG{values: []int{9, 2, 3}, failAt: -1}
	frame := s.actionExpressionFrame("hit", map[string]any{"source.damage": "1d8", "target.stat.ac": int64(12)}, rng)
	success := s.expressions["action.hit.success"]
	if !success.UsesOptionalTarget || !success.UsesRandom || !slices.Contains(success.Dependencies, "target.stat.ac") {
		t.Fatalf("dependencies: %+v", success)
	}
	pass, err := frame.Evaluate(success)
	if err != nil || !pass.flag {
		t.Fatalf("success: %v %v", pass, err)
	}
	for _, key := range []string{"roll", "var.twice", "roll"} {
		result, err := frame.Evaluate(frame.Locals[key])
		if err != nil {
			t.Fatal(err)
		}
		want := int64(17)
		if key == "var.twice" {
			want *= 2
		}
		if result.num.Num().Int64() != want || rng.next != 3 {
			t.Fatalf("unstable %s: %v draws %d", key, result, rng.next)
		}
	}
	effect := s.expressions["effect.bonus.formula"]
	e := ExpressionFrame{Bindings: map[string]any{"target": int64(2), "action.target.stat.ac": int64(12)}}
	v, err := e.Evaluate(effect)
	if err != nil || v.num.RatString() != "14" || !effect.UsesOptionalTarget {
		t.Fatalf("effect: %v %v", v, err)
	}
}

func TestRulesetExpressionBindingAndLimits(t *testing.T) {
	notEqual := actionExpr(t, "not 1 == 2 and true")
	logic, err := new(ExpressionFrame).Evaluate(notEqual)
	if err != nil || !logic.flag {
		t.Fatalf("boolean precedence: %v %v", logic, err)
	}
	numericAndString := "[stat.n]\ndefault=2\n[stat.s]\ndefault='some text'\n[effect.e]\ntarget=['stat.n','stat.s']\n"
	if _, err := ParseRulesetTOML([]byte(numericAndString + "formula='target + 1'")); err == nil {
		t.Fatal("mixed effect targets accepted numeric-only formula")
	}
	s, err := ParseRulesetTOML([]byte(numericAndString + "formula='target'"))
	if err != nil {
		t.Fatal(err)
	}
	e := s.expressions["effect.e.formula"]
	if e.UsesOptionalTarget || !slices.Contains(e.Dependencies, "stat.s") {
		t.Fatalf("effect dependencies: %+v", e)
	}
	f := ExpressionFrame{Bindings: map[string]any{"target": "a"}}
	got, err := f.Evaluate(e)
	if err != nil || got.text != "a" {
		t.Fatalf("polymorphic target: %v %v", got, err)
	}
	balanced := "1"
	for i := 0; i < 8; i++ {
		balanced = "(" + balanced + "+" + balanced + ")"
	}
	if _, err := parseExpression(balanced); err == nil {
		t.Fatal("node budget exceeded without error")
	}
	p := actionExpr(t, "1d8 + 1d8")
	rng := &deterministicDiceRNG{failAt: 1}
	f = ExpressionFrame{RNG: rng}
	if _, err := f.Evaluate(p); err == nil {
		t.Fatal("RNG failure ignored")
	}
	rng.failAt = -1
	if _, err := f.Evaluate(p); err == nil || rng.next != 1 {
		t.Fatal("failed roll retried within the frame")
	}
}

func TestRulesetExpressionSourceReplacement(t *testing.T) {
	s, err := CompileRulesetFiles(map[string]string{"base.toml": "[stat.hp]\ndefault=1", "calc.toml": "[stat.max]\ndefault={formula='stat.hp * 2'}"})
	if err != nil {
		t.Fatal(err)
	}
	live := &Session{Ruleset: s, RegistryRevision: 1, CharacterRevision: 1}
	candidate := cloneLibrarySession(live)
	err = applyLibraryOperation(candidate, libraryRequest{Operation: "deleteFile", Path: "base.toml"})
	if err == nil || !strings.Contains(err.Error(), "stat.hp") {
		t.Fatalf("dangling expression reference: %v", err)
	}
	if live.Ruleset.expressions["stat.max.default"] == nil || live.Ruleset.Registry.Stats["hp"].Default == nil {
		t.Fatal("failed deletion changed live snapshot")
	}
}
