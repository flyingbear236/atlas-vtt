package main

import (
	"reflect"
	"testing"
)

func TestActionImpactNumericTarget(t *testing.T) {
	target := ActionInstanceRef{CharacterID: "victim"}
	plan := &ActionChangePlan{Changes: []ActionChange{
		{Instance: target, Field: "stat.hp.current", Before: IntegerStatValue(10), After: IntegerStatValue(8)},
		{Instance: target, Field: "stat.strength.base", Before: IntegerStatValue(4), After: IntegerStatValue(6)},
		{Instance: target, Field: "stat.unnamed.base", Before: NumberStatValue(1), After: NumberStatValue(1.5)},
		{Instance: target, Field: "stat.hp.current", Before: IntegerStatValue(8), After: IntegerStatValue(8)},
		{Instance: target, Field: "stat.flag.base", Before: BooleanStatValue(false), After: BooleanStatValue(true)},
		{Instance: ActionInstanceRef{CharacterID: "attacker"}, Field: "stat.hp.current", Before: IntegerStatValue(10), After: IntegerStatValue(8)},
	}}
	stats := map[string]StatDefinition{"hp": {Name: "Здоровье"}, "strength": {Name: "Сила"}}
	want := []actionImpact{{Name: "Здоровье", Delta: "-2", Negative: true}, {Name: "Сила", Delta: "2"}, {Name: "stat.unnamed", Delta: "0.5"}}
	if got := actionImpacts(plan, &target, stats); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if got := actionImpacts(plan, &ActionInstanceRef{ElementID: "object"}, stats); len(got) != 0 {
		t.Fatal("scene object received character impact")
	}
}
