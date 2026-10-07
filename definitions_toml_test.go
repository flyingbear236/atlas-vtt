package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func selfContainedTOMLRegistry() CampaignRegistry {
	integerDefault := IntegerStatValue(0)
	numberDefault := NumberStatValue(1.5)
	booleanDefault := BooleanStatValue(false)
	stringDefault := StringStatValue("")
	return CampaignRegistry{
		Stats: map[string]StatDefinition{
			"strength_mod": {ID: "strength_mod", Name: "Strength modifier", Type: StatTypeInteger, Default: &integerDefault},
			"speed":        {ID: "speed", Name: "Speed", Type: StatTypeNumber, Default: &numberDefault},
			"alive":        {ID: "alive", Name: "Alive", Type: StatTypeBoolean, Default: &booleanDefault},
			"заметка.ＧＭ":   {ID: "заметка.ＧＭ", Name: "Заметка", Type: StatTypeString, Default: &stringDefault},
		},
		Actions: map[string]ActionDefinition{
			"bite": {
				ID: "bite", Name: "Укус", Description: "Атака волка", Tags: []string{"attack", "melee"},
				Rolls: []RollSpec{{ID: "attack", Name: "Попадание", Count: 1, Sides: 20, ModifierStat: "strength_mod", ModifierFixed: -1}},
			},
		},
		Presets: map[string]CharacterPresetDefinition{
			"рыжий.волк": {
				ID: "рыжий.волк", Name: "Рыжий волк", Kind: "monster", AvatarAssetID: "avatar-1",
				Stats: map[string]StatValue{
					"strength_mod": IntegerStatValue(2), "speed": NumberStatValue(12.5),
					"alive": BooleanStatValue(false), "заметка.ＧＭ": StringStatValue(""),
				},
				ActionIDs: []string{"bite"},
			},
		},
	}
}

func TestDefinitionsTOMLRoundTripAllKindsAndTypes(t *testing.T) {
	want := selfContainedTOMLRegistry()
	data, err := ExportDefinitionsTOML(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`actions = ['bite']`)) {
		t.Fatalf("TOML actionIds mapping is not explicit:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`'рыжий.волк'`)) || !bytes.Contains(data, []byte(`'заметка.ＧＭ'`)) {
		t.Fatalf("keys requiring quoting were not preserved:\n%s", data)
	}
	got, err := ParseDefinitionsTOML(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip differs\ngot:  %#v\nwant: %#v\nTOML:\n%s", got, want, data)
	}
}

func TestDefinitionsTOMLExportIsCanonicalAcrossMapOrder(t *testing.T) {
	one := selfContainedTOMLRegistry()
	two := CampaignRegistry{Stats: map[string]StatDefinition{}, Actions: map[string]ActionDefinition{}, Presets: map[string]CharacterPresetDefinition{}}
	for _, id := range []string{"заметка.ＧＭ", "alive", "speed", "strength_mod"} {
		two.Stats[id] = cloneStatDefinition(one.Stats[id])
	}
	two.Presets["рыжий.волк"] = clonePresetDefinition(one.Presets["рыжий.волк"])
	two.Actions["bite"] = cloneActionDefinition(one.Actions["bite"])

	first, err := ExportDefinitionsTOML(one)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExportDefinitionsTOML(two)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("canonical output depends on map insertion order:\n%s\n---\n%s", first, second)
	}
}

func TestRulesetSnapshotTOMLExportRoundTrip(t *testing.T) {
	registry := selfContainedTOMLRegistry()
	for id, preset := range registry.Presets {
		preset.AvatarAssetID = ""
		registry.Presets[id] = preset
	}
	want := RulesetSnapshot{Metadata: RulesetMetadata{ID: "ядро", Name: "Ядро правил", Version: "1.0"}, Registry: RulesetRegistry{Stats: registry.Stats, Actions: registry.Actions, Presets: registry.Presets}}
	data, err := ExportRulesetSnapshotTOML(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseRulesetTOML(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ruleset snapshot round trip differs\ngot: %#v\nwant: %#v\n%s", got, want, data)
	}
}

func TestDefinitionsTOMLRejectsInvalidDocuments(t *testing.T) {
	tests := map[string]string{
		"missing schema": `[stats.hp]
type = "integer"`,
		"wrong schema": `schema_version = 2`,
		"unknown field": `schema_version = 1
[stats.hp]
type = "integer"
typo = true`,
		"duplicate": `schema_version = 1
[stats.hp]
type = "integer"
[stats.hp]
type = "integer"`,
		"invalid stat type": `schema_version = 1
[stats.hp]
type = "object"`,
		"invalid stat value type": `schema_version = 1
[stats.hp]
type = "integer"
[presets.wolf]
name = "Wolf"
[presets.wolf.stats]
hp = true`,
		"missing modifier stat": `schema_version = 1
[actions.bite]
name = "Bite"
[[actions.bite.rolls]]
id = "attack"
name = "Attack"
count = 1
sides = 20
modifier_stat = "strength_mod"`,
		"missing action": `schema_version = 1
[presets.wolf]
name = "Wolf"
actions = ["bite"]`,
		"control in id": "schema_version = 1\n[stats.\"bad\\tkey\"]\ntype = \"integer\"",
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDefinitionsTOML([]byte(document)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDefinitionsTOMLRejectsOversizeInput(t *testing.T) {
	data := []byte("schema_version = 1\n#" + strings.Repeat("x", MaxDefinitionDocumentBytes))
	if _, err := ParseDefinitionsTOML(data); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size error, got %v", err)
	}
}

func TestPreviewCampaignDefinitionsImportUpsertsWithoutDeletion(t *testing.T) {
	ruleset := selfContainedTOMLRegistry()
	registries := DefinitionRegistries{
		Ruleset: RulesetRegistry{Stats: ruleset.Stats, Actions: ruleset.Actions, Presets: ruleset.Presets},
		Campaign: CampaignRegistry{
			Stats: map[string]StatDefinition{
				"legacy": {ID: "legacy", Name: "Legacy", Type: StatTypeString},
				"speed":  {ID: "speed", Name: "Campaign speed", Type: StatTypeNumber},
			},
			Actions: map[string]ActionDefinition{},
			Presets: map[string]CharacterPresetDefinition{},
		},
	}
	document := []byte(`schema_version = 1

[stats.speed]
name = "Movement speed"
type = "number"

[stats.armor]
name = "Armor"
type = "integer"

[actions.guard]
name = "Guard"

[presets.guard_wolf]
name = "Guard wolf"
kind = "monster"
actions = ["bite", "guard"]

[presets.guard_wolf.stats]
strength_mod = 1
armor = 14
`)
	original := cloneCampaignRegistry(registries.Campaign)
	preview, err := PreviewCampaignDefinitionsImport(registries, document)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(registries.Campaign, original) {
		t.Fatal("preview mutated current campaign registry")
	}
	if _, exists := preview.Registry.Stats["legacy"]; !exists {
		t.Fatal("omitted definition was deleted")
	}
	if preview.Registry.Presets["guard_wolf"].ActionIDs[0] != "bite" {
		t.Fatal("TOML actions were not mapped to runtime action IDs")
	}
	if !reflect.DeepEqual(preview.Diff.Stats.Added, []string{"armor"}) || !reflect.DeepEqual(preview.Diff.Stats.Updated, []string{"speed"}) || !reflect.DeepEqual(preview.Diff.Actions.Added, []string{"guard"}) || !reflect.DeepEqual(preview.Diff.Presets.Added, []string{"guard_wolf"}) {
		t.Fatalf("unexpected preview diff: %#v", preview.Diff)
	}
}

func TestPreviewCampaignDefinitionsImportIsAllOrNothing(t *testing.T) {
	ruleset := selfContainedTOMLRegistry()
	registries := DefinitionRegistries{
		Ruleset: RulesetRegistry{Stats: ruleset.Stats, Actions: ruleset.Actions, Presets: ruleset.Presets},
		Campaign: CampaignRegistry{
			Stats:   map[string]StatDefinition{"legacy": {ID: "legacy", Name: "Legacy", Type: StatTypeString}},
			Actions: map[string]ActionDefinition{}, Presets: map[string]CharacterPresetDefinition{},
		},
	}
	original := cloneCampaignRegistry(registries.Campaign)
	bad := []byte(`schema_version = 1
[stats.new_stat]
type = "integer"
[presets.broken]
name = "Broken"
actions = ["missing"]
`)
	preview, err := PreviewCampaignDefinitionsImport(registries, bad)
	if err == nil {
		t.Fatal("expected invalid reference error")
	}
	if !reflect.DeepEqual(preview, CampaignImportPreview{}) {
		t.Fatalf("failed preview returned partial state: %#v", preview)
	}
	if !reflect.DeepEqual(registries.Campaign, original) {
		t.Fatal("failed preview mutated current campaign registry")
	}
}

func TestPreviewCampaignDefinitionsImportRejectsReferencedTypeChange(t *testing.T) {
	registries := DefinitionRegistries{Campaign: CampaignRegistry{
		Stats: map[string]StatDefinition{"modifier": {ID: "modifier", Type: StatTypeNumber}},
		Actions: map[string]ActionDefinition{"attack": {
			ID: "attack", Name: "Attack", Rolls: []RollSpec{{ID: "roll", Name: "Roll", Count: 1, Sides: 20, ModifierStat: "modifier"}},
		}},
		Presets: map[string]CharacterPresetDefinition{},
	}}
	document := []byte(`schema_version = 1
[stats.modifier]
type = "integer"
`)
	if _, err := PreviewCampaignDefinitionsImport(registries, document); err == nil || !strings.Contains(err.Error(), "referenced stat") {
		t.Fatalf("expected referenced type change error, got %v", err)
	}
}

func TestExportCampaignDefinitionsAllowsRulesetReferences(t *testing.T) {
	ruleset := selfContainedTOMLRegistry()
	registries := DefinitionRegistries{
		Ruleset: RulesetRegistry{Stats: ruleset.Stats, Actions: ruleset.Actions, Presets: ruleset.Presets},
		Campaign: CampaignRegistry{
			Stats: map[string]StatDefinition{}, Actions: map[string]ActionDefinition{},
			Presets: map[string]CharacterPresetDefinition{
				"local": {ID: "local", Name: "Local", Stats: map[string]StatValue{"strength_mod": IntegerStatValue(3)}, ActionIDs: []string{"bite"}},
			},
		},
	}
	data, err := ExportCampaignDefinitionsTOML(registries)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewCampaignDefinitionsImport(DefinitionRegistries{Ruleset: registries.Ruleset}, data)
	if err != nil || preview.Registry.Presets["local"].ActionIDs[0] != "bite" {
		t.Fatalf("campaign export cannot be imported against ruleset: %v\n%s", err, data)
	}
}


func TestCompileRulesetFilesMergesFragmentsAndRejectsDuplicateIDs(t *testing.T) {
	core := `schema_version = 1
[ruleset]
id = "dnd"
name = "D&D"
version = "1"

[stats.strength]
name = "Strength"
type = "integer"
default = 10

[presets.hero]
name = "Hero"
kind = "character"
actions = ["strength_check"]

[presets.hero.stats]
strength = 16
`
	actions := `schema_version = 1
[actions.strength_check]
name = "Strength check"

[[actions.strength_check.rolls]]
id = "check"
name = "Check"
count = 1
sides = 20
modifier_stat = "strength"
`

	snapshot, err := CompileRulesetFiles(map[string]string{"core.toml": core, "actions.toml": actions})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Files) != 2 || !snapshot.Files["core.toml"].HasMetadata || snapshot.Files["actions.toml"].HasMetadata {
		t.Fatalf("unexpected ruleset file manifests: %#v", snapshot.Files)
	}
	if snapshot.Registry.Actions["strength_check"].ID == "" || snapshot.Registry.Presets["hero"].Stats["strength"].Integer() != 16 {
		t.Fatalf("fragments were not compiled into one registry: %#v", snapshot.Registry)
	}

	duplicate := actions + `
[stats.strength]
name = "Duplicate strength"
type = "integer"
`
	if _, err := CompileRulesetFiles(map[string]string{"core.toml": core, "actions.toml": duplicate}); err == nil || !strings.Contains(err.Error(), "duplicate stat") {
		t.Fatalf("duplicate ID across ruleset files was accepted: %v", err)
	}
}

func TestReplaceRulesetFileRecompilesWholeRulesetAndKeepsOldSnapshotOnFailure(t *testing.T) {
	core := `schema_version = 1
[ruleset]
id = "dnd"
name = "D&D"
version = "1"

[stats.strength]
name = "Strength"
type = "integer"

[presets.hero]
name = "Hero"
kind = "character"
actions = ["strength_check"]

[presets.hero.stats]
strength = 16
`
	actions := `schema_version = 1
[actions.strength_check]
name = "Strength check"

[[actions.strength_check.rolls]]
id = "check"
name = "Check"
count = 1
sides = 20
modifier_stat = "strength"
`
	current, err := CompileRulesetFiles(map[string]string{"core.toml": core, "actions.toml": actions})
	if err != nil {
		t.Fatal(err)
	}
	original := cloneRulesetSnapshot(current)

	updated := strings.Replace(actions, `name = "Strength check"`, `name = "Updated strength check"`, 1)
	next, err := ReplaceRulesetFile(current, "actions.toml", updated)
	if err != nil {
		t.Fatal(err)
	}
	if next.Registry.Actions["strength_check"].Name != "Updated strength check" || next.Files["actions.toml"].Digest == current.Files["actions.toml"].Digest {
		t.Fatalf("single-file replacement did not rebuild the ruleset: %#v", next)
	}

	broken := `schema_version = 1
[actions.other]
name = "Other"
`
	if _, err := ReplaceRulesetFile(current, "actions.toml", broken); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("replacement that breaks another fragment was accepted: %v", err)
	}
	if !reflect.DeepEqual(current, original) {
		t.Fatal("failed single-file replacement mutated the current snapshot")
	}
}
