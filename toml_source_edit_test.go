package main

import (
	"strings"
	"testing"
)

func TestSourcePreservingPresetStatAndRoll(t *testing.T) {
	original := "schema_version = 1\n\n# Author notes\n[stats.\"ability.strength\"]\nname = 'Сила'\ntype = \"integer\"\n\n[actions.hit]\nname = 'Удар'\n[[actions.hit.rolls]]\nid = \"attack\"\nname = 'Атака'\ncount = 1\nsides = 20\nmodifier_stat = \"ability.strength\"\nmodifier_fixed = 0  # preserve explicit zero\n\n[presets.goblin]\nname = 'Гоблин'\nactions = [\"hit\"]\n[presets.goblin.stats]\n\"ability.strength\" = 5  # GM comment\n"
	snapshot, err := CompileRulesetFiles(map[string]string{"goblins.toml": original})
	if err != nil {
		t.Fatal(err)
	}
	pristine, err := RenderRulesetFileTOML(snapshot, "goblins.toml")
	if err != nil || pristine != original {
		t.Fatalf("untouched source changed: %v\n%s", err, pristine)
	}
	preset := snapshot.Registry.Presets["goblin"]
	preset.Stats["ability.strength"] = IntegerStatValue(6)
	snapshot.Registry.Presets["goblin"] = preset
	result, err := RenderRulesetFileTOML(snapshot, "goblins.toml")
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.Replace(original, `"ability.strength" = 5  # GM comment`, `"ability.strength" = 6  # GM comment`, 1)
	if result != expected {
		t.Fatalf("unexpected rewrite\n%s", result)
	}
	if snapshot.Files["goblins.toml"].Source != original {
		t.Fatal("original source was mutated")
	}
	compiled, err := CompileRulesetFiles(map[string]string{"goblins.toml": result})
	if err != nil || !equalRulesetRegistries(compiled.Registry, snapshot.Registry) {
		t.Fatalf("round trip changed mechanics: %v", err)
	}
}

func TestSourcePreservingChangedRollAndRemoval(t *testing.T) {
	src := `schema_version = 1
# header
[stats.mod]
name = "Mod"
type = "integer"

[actions.hit]
name = "Hit"
tags = []  # retained zero-valued declaration
[[actions.hit.rolls]]
id = "attack"
name = "Attack"
count = 1
sides = 20
modifier_stat = "mod"

[[actions.hit.rolls]]
id = "damage"
name = "Damage"
count = 1
sides = 8

[presets.goblin]
name = "Goblin"
actions = ["hit"]
`
	snapshot, err := CompileRulesetFiles(map[string]string{"rolls.toml": src})
	if err != nil {
		t.Fatal(err)
	}
	action := snapshot.Registry.Actions["hit"]
	action.Rolls[0].Sides = 12
	snapshot.Registry.Actions["hit"] = action
	current, err := RenderRulesetFileTOML(snapshot, "rolls.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(current, "sides = 12\n") || !strings.Contains(current, "tags = []  # retained zero-valued declaration") || !strings.Contains(current, "sides = 8") {
		t.Fatalf("roll edit changed unrelated text:\n%s", current)
	}
	compiled, err := CompileRulesetFiles(map[string]string{"rolls.toml": current})
	if err != nil || !equalRulesetRegistries(compiled.Registry, snapshot.Registry) {
		t.Fatalf("round trip: %v", err)
	}
	// Once a definition is removed, its file remains and unrelated source stays.
	delete(snapshot.Registry.Presets, "goblin")
	mf := snapshot.Files["rolls.toml"]
	mf.Presets = nil
	snapshot.Files["rolls.toml"] = mf
	current, err = RenderRulesetFileTOML(snapshot, "rolls.toml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(current, "[presets.goblin]") || !strings.Contains(current, "# header") {
		t.Fatalf("removed unexpected text:\n%s", current)
	}
}

func TestSourcePreservingRejectsAmbiguousEdit(t *testing.T) {
	src := "schema_version = 1\n[stats.note]\nname = \"\"\"Multiline\nvalue\"\"\"\ntype = \"string\"\n"
	snapshot, err := CompileRulesetFiles(map[string]string{"notes.toml": src})
	if err != nil {
		t.Fatal(err)
	}
	stat := snapshot.Registry.Stats["note"]
	stat.Name = "Changed"
	snapshot.Registry.Stats["note"] = stat
	if _, err := RenderRulesetFileTOML(snapshot, "notes.toml"); err == nil {
		t.Fatal("must reject unsupported multiline editing")
	}
	if snapshot.Files["notes.toml"].Source != src {
		t.Fatal("source was damaged")
	}
}

func TestSourcePreservingStatArrayAndCRLF(t *testing.T) {
	source := "schema_version = 1\r\n# manual formatting\r\n[[ stats ]]\r\nid = 'strength'\r\nname = 'Сила'\r\ntype = 'integer'\r\ndefault = 5  # tracked\r\n"
	snapshot, err := CompileRulesetFiles(map[string]string{"abilities.toml": source})
	if err != nil {
		t.Fatal(err)
	}
	stat := snapshot.Registry.Stats["strength"]
	v := IntegerStatValue(6)
	stat.Default = &v
	snapshot.Registry.Stats["strength"] = stat
	current, err := RenderRulesetFileTOML(snapshot, "abilities.toml")
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.Replace(source, "default = 5", "default = 6", 1)
	if current != expected {
		t.Fatalf("source changed unexpectedly:\n%s", current)
	}
}
