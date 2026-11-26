package main

import (
	"strings"
	"testing"
)

func TestInventoryDetailsForSheetNestedFields(t *testing.T) {
	registry := EffectiveRegistry{Objects: map[string]*RulesetFields{
		"weapon.test": {values: map[string]any{
			"name": "Пистолет", "description": "Описание",
			"actions": []any{"action.weapon.hit"},
			"магазин": map[string]any{"прочность": int64(40), "патроны": int64(12)},
			"рукоять": map[string]any{"прочность": int64(80)},
		}},
	}}
	items := []InventoryItem{{ID: "gun1", ObjectID: "weapon.test", Equipped: true, Fields: &RulesetFields{values: map[string]any{"заряды": int64(2)}}}}
	result := inventoryDetailsForSheet(registry, items)["gun1"]
	if result.Description != "Описание" {
		t.Fatalf("description: %q", result.Description)
	}
	values := make(map[string]string)
	for _, field := range result.Fields {
		values[strings.Join(field.Path, ".")] = field.Value
	}
	for path, want := range map[string]string{"магазин.прочность": "40", "магазин.патроны": "12", "рукоять.прочность": "80", "заряды": "2"} {
		if values[path] != want {
			t.Errorf("%s: got %q, want %q", path, values[path], want)
		}
	}
	if _, exists := values["actions"]; exists {
		t.Fatal("internal action grants leaked into item properties")
	}
}

func TestInventoryDetailsForSheetItemEffectOverrides(t *testing.T) {
	registry := EffectiveRegistry{
		Objects:   map[string]*RulesetFields{"ring": {values: map[string]any{"name": "Кольцо силы"}}},
		Effects:   map[string]*RulesetFields{"strength": {values: map[string]any{"name": "Усиление", "formula": "target + 2", "target": "stat.strength"}}},
		mechanics: &rulesetMechanics{objects: map[string]objectMechanics{"ring": {effects: []string{"strength"}}}, effects: map[string]*compiledEffect{"strength": {id: "strength", formulaSource: "target + 2", targets: []string{"stat.strength"}, additiveEditable: true, additiveValue: 2}}},
	}
	fields, _ := newRulesetFields(map[string]any{"effect_overrides": map[string]any{"strength": int64(5)}})
	items := []InventoryItem{{ID: "ring-a", ObjectID: "ring", Equipped: true, Fields: fields}, {ID: "ring-b", ObjectID: "ring", Equipped: true}}
	result := inventoryDetailsForSheet(registry, items)
	if len(result["ring-a"].Effects) != 1 || result["ring-a"].Effects[0].Value == nil || *result["ring-a"].Effects[0].Value != 5 ||
		len(result["ring-b"].Effects) != 1 || result["ring-b"].Effects[0].Value == nil || *result["ring-b"].Effects[0].Value != 2 {
		t.Fatalf("instance override leaked into template: %+v", result)
	}
}

func TestCharacterEffectsForSheetIncludesDirectAndItemSources(t *testing.T) {
	registry := EffectiveRegistry{
		Objects: map[string]*RulesetFields{"ring": {values: map[string]any{"name": "Кольцо испытателя"}}},
		Effects: map[string]*RulesetFields{
			"strength": {values: map[string]any{"name": "Сила", "formula": "target + 2"}},
			"focus":    {values: map[string]any{"name": "Фокус", "formula": "target + 3"}},
			"training": {values: map[string]any{"name": "Подготовка", "formula": "target + 1", "when": "actor.ready"}},
		},
		mechanics: &rulesetMechanics{
			objects: map[string]objectMechanics{"ring": {effects: []string{"strength", "focus"}}},
			effects: map[string]*compiledEffect{
				"strength": {id: "strength", targets: []string{"stat.strength"}, formulaSource: "target + 2", additiveEditable: true, additiveValue: 2},
				"focus":    {id: "focus", targets: []string{"stat.focus"}, formulaSource: "target + 3", unique: true, additiveEditable: true, additiveValue: 3},
				"training": {id: "training", targets: []string{"action.hit.roll"}, formulaSource: "target + 1"},
			},
		},
	}
	fields, err := newRulesetFields(map[string]any{"effect_overrides": map[string]any{"strength": int64(5)}})
	if err != nil {
		t.Fatal(err)
	}
	hero := CharacterInstance{ID: "hero", EffectIDs: []string{"training"}, Inventory: []InventoryItem{
		{ID: "ring-a", ObjectID: "ring", Equipped: true, Fields: fields},
		{ID: "ring-b", ObjectID: "ring", Equipped: true},
		{ID: "ring-c", ObjectID: "ring", Equipped: false},
	}}
	direct := []EffectGrant{{EffectID: "training", SourceID: "hero"}}
	details := inventoryDetailsForSheet(registry, hero.Inventory, direct...)
	// Item effects are sent once in itemDetails; only direct grants have a
	// separate wire field. Reconstruct the combined view as the client does.
	combined := func(details map[string]inventorySheetDetail) []inventorySheetEffect {
		var entries []inventorySheetEffect
		for _, item := range hero.Inventory {
			entries = append(entries, details[item.ID].Effects...)
		}
		return append(entries, directCharacterEffectsForSheet(registry, hero, details)...)
	}
	entries := combined(details)
	if len(entries) != 7 {
		t.Fatalf("effect count %d: %+v", len(entries), entries)
	}
	byKey := map[string]inventorySheetEffect{}
	for _, entry := range entries {
		byKey[entry.SourceID+":"+entry.ID] = entry
	}
	a := byKey["ring-a:effect.strength"]
	b := byKey["ring-b:effect.strength"]
	if a.Value == nil || *a.Value != 5 || b.Value == nil || *b.Value != 2 || a.SourceKind != "item" || a.SourceName != "Кольцо испытателя" {
		t.Fatalf("instance-specific sources: %+v %+v", a, b)
	}
	if byKey["ring-b:effect.focus"].State != "inactive" || byKey["ring-c:effect.strength"].State != "inactive" || byKey["ring-c:effect.focus"].State != "inactive" {
		t.Fatalf("expected suppressed and unequipped effects: %+v", entries)
	}
	if byKey["ring-a:effect.focus"].State != "active" {
		t.Fatalf("first unique grant should be active: %+v", entries)
	}
	// Equipment changes update both status and machine-readable state; the
	// remaining equipped ring becomes the first unique effect provider.
	hero.Inventory[0].Equipped = false
	details = inventoryDetailsForSheet(registry, hero.Inventory, direct...)
	entries = combined(details)
	byKey = map[string]inventorySheetEffect{}
	for _, entry := range entries {
		byKey[entry.SourceID+":"+entry.ID] = entry
	}
	if byKey["ring-a:effect.strength"].State != "inactive" || byKey["ring-a:effect.focus"].State != "inactive" || byKey["ring-b:effect.focus"].State != "active" {
		t.Fatalf("unequip did not change effect states: %+v", entries)
	}
	training := byKey["hero:effect.training"]
	if training.SourceKind != "character" || training.SourceName != "У персонажа" || training.State != "conditional" {
		t.Fatalf("direct effect: %+v", training)
	}
	// A character without inventory still needs its direct effect list.
	if alone := directCharacterEffectsForSheet(registry, CharacterInstance{ID: "hero", EffectIDs: []string{"training"}}, nil); len(alone) != 1 || alone[0].SourceID != "hero" {
		t.Fatalf("direct-only: %+v", alone)
	}
}
