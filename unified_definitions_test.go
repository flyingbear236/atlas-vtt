package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUnifiedDefinitionsOwnerEditsAndFreshSession(t *testing.T) {
	original := `schema_version = 1
# Original notes stay here
[stats.hp]
name = "Hit points" # annotation
type = "integer"
kind = "resource"

[actions.hit]
name = "Hit"

[presets.hero]
name = "Hero"
actions = ["hit"]
[presets.hero.stats]
hp = 10
`
	snapshot, err := CompileRulesetFiles(map[string]string{"rules.toml": original})
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{Ruleset: snapshot, RegistryRevision: 1, CharacterInstances: map[string]CharacterInstance{}, Assets: map[string]Asset{}}
	rev := session.RegistryRevision
	hp := snapshot.Registry.Stats["hp"]
	hp.Name = "Health"
	changed, yes, err := applyDefinitionCommand(session, Command{Type: "definitionUpdate", DefinitionKind: definitionKindStat, DefinitionID: "hp", StatDefinition: &hp, ExpectedRegistryRevision: &rev})
	if err != nil || !yes {
		t.Fatalf("update failed: %v, changed %t", err, yes)
	}
	session.Ruleset = changed
	edited, err := RenderRulesetFileTOML(session.Ruleset, "rules.toml")
	if err != nil || !strings.Contains(edited, `name = "Health" # annotation`) || !strings.Contains(edited, "# Original notes stay here") {
		t.Fatalf("source-preserving update: %v\n%s", err, edited)
	}
	if session.Ruleset.Files["rules.toml"].Source != original {
		t.Fatal("original TOML source overwritten")
	}

	mana := StatDefinition{ID: "mana", Name: "Mana", Type: StatTypeInteger, Kind: StatKindResource}
	changed, yes, err = applyDefinitionCommand(session, Command{Type: "definitionCreate", DefinitionKind: definitionKindStat, DefinitionID: "mana", StatDefinition: &mana, ExpectedRegistryRevision: &rev})
	if err != nil || !yes {
		t.Fatalf("create failed: %v", err)
	}
	session.Ruleset = changed
	if owner, err := definitionFileOwner(session.Ruleset, definitionKindStat, "mana"); err != nil || owner != generatedDefinitionsFile {
		t.Fatalf("generated source: %q, %v", owner, err)
	}
	if _, yes, err := applyDefinitionCommand(session, Command{Type: "definitionCreate", DefinitionKind: definitionKindStat, DefinitionID: "hp", StatDefinition: &hp, ExpectedRegistryRevision: &rev}); err == nil || yes {
		t.Fatalf("duplicate id was accepted: %v", err)
	}

	action := ActionDefinition{ID: "heal", Name: "Heal"}
	added, yes, err := applyDefinitionCommand(session, Command{Type: "definitionCreate", DefinitionKind: definitionKindAction, DefinitionID: "heal", ActionDefinition: &action, ExpectedRegistryRevision: &rev})
	if err != nil || !yes {
		t.Fatalf("new action failed: %v", err)
	}
	session.Ruleset = added
	preset := CharacterPresetDefinition{ID: "cleric", Name: "Cleric", ActionIDs: []string{"heal"}}
	added, yes, err = applyDefinitionCommand(session, Command{Type: "definitionCreate", DefinitionKind: definitionKindPreset, DefinitionID: "cleric", PresetDefinition: &preset, ExpectedRegistryRevision: &rev})
	if err != nil || !yes {
		t.Fatalf("new preset failed: %v", err)
	}
	session.Ruleset = added

	text, err := RenderRulesetFileTOML(session.Ruleset, generatedDefinitionsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "mana") || !strings.Contains(text, "heal") || !strings.Contains(text, "cleric") {
		t.Fatalf("generated TOML incomplete: %s", text)
	}
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		t.Fatal(err)
	}
	hero := registry.Presets["hero"]
	character := NewCharacterInstance("character-1", &hero)
	character.ResourceCurrent = map[string]StatValue{"hp": IntegerStatValue(7)}
	before := EffectiveCharacterState(character, &hero, registry)
	if before.Resources["hp"].Initial != IntegerStatValue(10) || before.Resources["hp"].Current != IntegerStatValue(7) {
		t.Fatal("linked resource initial/current changed")
	}
	materialized := MaterializeCharacter(character, &hero, registry)
	after := EffectiveCharacterState(materialized, &hero, registry)
	if after.Resources["hp"] != before.Resources["hp"] {
		t.Fatal("materialization altered resource")
	}
	session.CharacterInstances[character.ID] = materialized
	if _, yes, err := applyDefinitionCommand(session, Command{Type: "definitionDelete", DefinitionKind: definitionKindStat, DefinitionID: "hp", ExpectedRegistryRevision: &rev}); err == nil || yes {
		t.Fatal("referenced resource could be deleted")
	}
	blob, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "campaignDefinitions") {
		t.Fatal("legacy layer persisted")
	}
	var loaded Session
	if err := json.Unmarshal(blob, &loaded); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRulesetSnapshot(loaded.Ruleset); err != nil {
		t.Fatal(err)
	}
	if !equalRulesetRegistries(loaded.Ruleset.Registry, session.Ruleset.Registry) {
		t.Fatal("registry changed after reload")
	}
}

func TestUnifiedDefinitionsImportRejectsCrossFileDuplicate(t *testing.T) {
	rules, err := CompileRulesetFiles(map[string]string{"core.toml": "schema_version = 1\n[stats.hp]\nname = 'HP'\ntype = 'integer'\n"})
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{Ruleset: rules, CharacterInstances: map[string]CharacterInstance{}, Assets: map[string]Asset{}}
	imported := []byte("schema_version = 1\n# imported source\n[stats.focus]\nname = 'Focus'\ntype = 'integer'\n")
	next, _, err := prepareDefinitionsImport(session, imported)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := definitionFileOwner(next, definitionKindStat, "focus")
	if err != nil || owner != "imported-definitions.toml" {
		t.Fatalf("owner %q: %v", owner, err)
	}
	text, err := RenderRulesetFileTOML(next, owner)
	if err != nil || text != string(imported) {
		t.Fatalf("source changed: %v\n%s", err, text)
	}
	session.Ruleset = next
	if _, _, err := prepareDefinitionsImport(session, imported); err == nil {
		t.Fatal("duplicate import should be rejected")
	}
	if _, _, err := prepareDefinitionsImport(session, []byte("schema_version = 1\n[stats.hp]\nname = 'Other'\ntype = 'integer'\n")); err == nil {
		t.Fatal("existing original-file id was overridden")
	}
}
