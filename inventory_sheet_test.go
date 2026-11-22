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
