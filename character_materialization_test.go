package main

import (
	"strings"
	"testing"
)

func testWolfPreset() CharacterPresetDefinition {
	return CharacterPresetDefinition{
		ID: "wolf", Name: "Волк", Kind: "monster",
		Stats: map[string]StatValue{
			"hp": IntegerStatValue(55),
			"name": StringStatValue(""),
			"awake": BooleanStatValue(false),
		},
		ActionIDs: []string{"bite", "run"},
	}
}

func TestLinkedMaterialization(t *testing.T) {
	preset := testWolfPreset()
	linked := NewCharacterInstance("wolf_1", &preset)
	if linked.Materialized != nil || linked.Name != "Волк" { t.Fatal("preset character should follow without snapshot") }
	linked.ResourceCurrent = map[string]StatValue{"hp": IntegerStatValue(44)}
	if linked.Materialized != nil { t.Fatal("changing current resource materialized mechanics") }
	custom := MaterializeCharacter(linked, &preset)
	custom = WithStatOverride(custom, "name", StringStatValue("Lupus"))
	if custom.Materialized == nil || len(custom.Materialized.Stats) != 3 || len(custom.Materialized.ActionIDs) != 2 {
		t.Fatal("first mechanical edit did not preserve complete effective state")
	}
	preset.Stats["hp"] = IntegerStatValue(100)
	preset.ActionIDs = []string{"run"}
	if got := EffectiveCharacterState(custom, &preset); got.Stats["hp"].Integer() != 55 || len(got.ActionIDs) != 2 {
		t.Fatal("materialized mechanics changed with preset")
	}
	if got := EffectiveCharacterState(linked, &preset); got.Stats["hp"].Integer() != 100 {
		t.Fatal("linked character failed to follow preset")
	}
	if custom.ResourceCurrent["hp"].Integer() != 44 { t.Fatal("current resource was lost") }
}

func TestResourceValueZeroAndMissing(t *testing.T) {
	preset := testWolfPreset()
	instance := NewCharacterInstance("wolf_2", &preset)
	effective := EffectiveCharacterState(instance, &preset)
	if _, exists := effective.ResourceCurrent["name"]; exists { t.Fatal("unset current must remain absent") }
	instance.ResourceCurrent = map[string]StatValue{"name": StringStatValue(""), "awake": BooleanStatValue(false)}
	effective = EffectiveCharacterState(instance, &preset)
	if value, exists := effective.ResourceCurrent["name"]; !exists || value.String() != "" { t.Fatal("empty string current is present and valid") }
	if value, exists := effective.ResourceCurrent["awake"]; !exists || value.Boolean() { t.Fatal("boolean false current is present and valid") }
	if instance.Materialized != nil { t.Fatal("current alone must not materialize") }
}

func TestMaterializationOfActions(t *testing.T) {
	preset := testWolfPreset()
	instance := MaterializeCharacter(NewCharacterInstance("wolf_3", &preset), &preset)
	instance = WithActionRemoved(instance, "bite")
	instance = WithActionAdded(instance, "howl")
	if len(instance.Materialized.ActionIDs) != 2 || instance.Materialized.ActionIDs[0] != "run" || instance.Materialized.ActionIDs[1] != "howl" {
		t.Fatalf("unexpected action snapshot: %v", instance.Materialized.ActionIDs)
	}
}

func TestIndependentCharacterInitialState(t *testing.T) {
	instance := NewCharacterInstance("new_character", nil)
	if instance.PresetID != "" || instance.Materialized == nil || len(instance.Materialized.Stats) != 0 {
		t.Fatal("new independent character must own empty mechanics")
	}
}

func TestPresetSourceSplicePreservesOutsideBytes(t *testing.T) {
	source := "schema_version = 1\n# important metadata\n\n[presets.wolf]\nname = \"Wolf\"\nkind = \"monster\"\n\n[presets.wolf.stats]\nhp = 55\n\n# kept\n[stats.hp]\nname = \"Hit Points\"\ntype = \"integer\"\n"
	newBlock := "[presets.wolf]\nname = \"Волк\"\nkind = \"monster\"\n"
	output, err := replacePresetSourceBlock(source, "wolf", newBlock)
	if err != nil { t.Fatal(err) }
	if !strings.HasPrefix(output, "schema_version = 1\n# important metadata\n\n") ||
		!strings.Contains(output, "# kept\n[stats.hp]\nname = \"Hit Points\"\ntype = \"integer\"\n") {
		t.Fatal("unrelated source bytes were rewritten")
	}
	if !strings.Contains(output, "name = \"Волк\"") || strings.Contains(output, "hp = 55") { t.Fatal("preset block was not replaced") }
}

func TestPresetSourceAmbiguousRangeRejected(t *testing.T) {
	source := "schema_version = 1\n[presets.wolf]\nname = \"Wolf\"\n[stats.hp]\ntype = \"integer\"\n[presets.wolf.stats]\nhp = 55\n"
	if _, err := replacePresetSourceBlock(source, "wolf", "[presets.wolf]\nname = \"New\"\n"); err == nil {
		t.Fatal("interleaved table must require manual editing")
	}
}

func TestRulesetArtworkPathValidation(t *testing.T) {
	for _, path := range []string{"media/wolf.webp", "media/скелет.png", ""} {
		if err := validateRulesetArtworkPath(path); err != nil { t.Fatalf("%q: %v", path, err) }
	}
	for _, path := range []string{"C:\\wolf.png", "https://example.com/wolf.png", "../wolf.jpg", "media/../wolf.png", "media/evil.svg"} {
		if err := validateRulesetArtworkPath(path); err == nil { t.Fatalf("unsafe artwork path accepted: %q", path) }
	}
}
