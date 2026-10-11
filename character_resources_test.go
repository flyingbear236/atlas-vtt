package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func resourceStatDefinition(id string, statType StatType) StatDefinition {
	return StatDefinition{ID: id, Name: id, Type: statType, Kind: StatKindResource}
}

func TestResourceStatTOMLKindAndBackwardCompatibility(t *testing.T) {
	legacy, err := ParseDefinitionsTOML([]byte(`
schema_version = 1

[stats.hp]
name = "HP"
type = "integer"
`))
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Stats["hp"].Kind != "" || legacy.Stats["hp"].IsResource() {
		t.Fatalf("stat without kind changed semantics: %#v", legacy.Stats["hp"])
	}

	compiled, err := CompileRulesetFiles(map[string]string{"resources.toml": `
schema_version = 1

[[stats]]
id = "hp"
name = "HP"
type = "integer"
kind = "resource"
`})
	if err != nil {
		t.Fatal(err)
	}
	if definition := compiled.Registry.Stats["hp"]; definition.Kind != StatKindResource || definition.Type != StatTypeInteger {
		t.Fatalf("resource stat did not compile: %#v", definition)
	}
}

func TestEffectiveResourceCurrentInitialAndScalarTypes(t *testing.T) {
	registry := EffectiveRegistry{
		Stats: map[string]StatDefinition{
			"hp":        resourceStatDefinition("hp", StatTypeInteger),
			"enabled":   resourceStatDefinition("enabled", StatTypeBoolean),
			"note":      resourceStatDefinition("note", StatTypeString),
			"stamina":   resourceStatDefinition("stamina", StatTypeNumber),
			"unchanged": resourceStatDefinition("unchanged", StatTypeInteger),
			"dexterity": {ID: "dexterity", Name: "Dexterity", Type: StatTypeInteger},
		},
		Actions: map[string]ActionDefinition{"bite": {ID: "bite", Name: "Bite"}},
		Presets: map[string]CharacterPresetDefinition{},
	}
	preset := CharacterPresetDefinition{
		ID: "wolf", Name: "Wolf",
		Stats: map[string]StatValue{
			"hp": IntegerStatValue(55), "enabled": BooleanStatValue(true), "note": StringStatValue("preset"),
			"stamina": NumberStatValue(12.5), "unchanged": IntegerStatValue(7), "dexterity": IntegerStatValue(15),
		},
		ActionIDs: []string{"bite"},
	}
	registry.Presets[preset.ID] = preset
	instance := NewCharacterInstance("linked", &preset)

	effective := EffectiveCharacterState(instance, &preset, registry)
	if effective.Resources["hp"].Current.Integer() != 55 || effective.Resources["hp"].Initial.Integer() != 55 {
		t.Fatalf("missing current did not fall back to initial: %#v", effective.Resources["hp"])
	}
	if _, exists := effective.Stats["hp"]; exists || effective.Stats["dexterity"].Integer() != 15 {
		t.Fatalf("resource and ordinary stat classification is wrong: %#v", effective)
	}

	instance = WithResourceCurrent(instance, "hp", IntegerStatValue(44))
	instance = WithResourceCurrent(instance, "enabled", BooleanStatValue(false))
	instance = WithResourceCurrent(instance, "note", StringStatValue(""))
	instance = WithResourceCurrent(instance, "stamina", NumberStatValue(0))
	if instance.Materialized != nil {
		t.Fatal("changing current materialized a linked character")
	}
	if value, exists := instance.ResourceCurrent["enabled"]; !exists || value.Type() != StatTypeBoolean || value.Boolean() {
		t.Fatalf("explicit false was lost: value=%#v exists=%v", value, exists)
	}
	if value, exists := instance.ResourceCurrent["note"]; !exists || value.Type() != StatTypeString || value.String() != "" {
		t.Fatalf("explicit empty string was lost: value=%#v exists=%v", value, exists)
	}
	if value, exists := instance.ResourceCurrent["stamina"]; !exists || value.Type() != StatTypeNumber || value.Number() != 0 {
		t.Fatalf("explicit number zero was lost: value=%#v exists=%v", value, exists)
	}
	if value, exists := instance.ResourceCurrent["hp"]; !exists || value.Type() != StatTypeInteger || value.Integer() != 44 {
		t.Fatalf("explicit integer current was lost: value=%#v exists=%v", value, exists)
	}

	preset.Stats["hp"] = IntegerStatValue(100)
	preset.Stats["unchanged"] = IntegerStatValue(9)
	registry.Presets[preset.ID] = preset
	effective = EffectiveCharacterState(instance, &preset, registry)
	if effective.Resources["hp"].Current.Integer() != 44 || effective.Resources["hp"].Initial.Integer() != 100 {
		t.Fatalf("explicit current did not survive preset initial change: %#v", effective.Resources["hp"])
	}
	if _, exists := instance.ResourceCurrent["unchanged"]; exists || effective.Resources["unchanged"].Current.Integer() != 9 || effective.Resources["unchanged"].Initial.Integer() != 9 {
		t.Fatalf("missing current did not track new preset initial: %#v", effective.Resources["unchanged"])
	}
	if effective.Resources["enabled"].Current.Boolean() || effective.Resources["note"].Current.String() != "" || effective.Resources["stamina"].Current.Type() != StatTypeNumber {
		t.Fatalf("explicit scalar resource values changed: %#v", effective.Resources)
	}

	materialized := MaterializeCharacter(instance, &preset, registry)
	if materialized.Materialized == nil || materialized.Materialized.ResourceInitial["hp"].Integer() != 100 || materialized.Materialized.Stats["dexterity"].Integer() != 15 || !reflect.DeepEqual(materialized.Materialized.ActionIDs, []string{"bite"}) {
		t.Fatalf("mechanical materialization did not include complete state: %#v", materialized.Materialized)
	}
	preset.Stats["hp"] = IntegerStatValue(200)
	registry.Presets[preset.ID] = preset
	effective = EffectiveCharacterState(materialized, &preset, registry)
	if effective.Resources["hp"].Current.Integer() != 44 || effective.Resources["hp"].Initial.Integer() != 100 {
		t.Fatalf("materialized resource initial followed preset: %#v", effective.Resources["hp"])
	}
}

func TestResourceCommandsMaterializationAndPermissions(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	fixture.server.mu.Lock()
	hp := fixture.session.CampaignDefinitions.Stats["hp"]
	hp.Kind = StatKindResource
	fixture.session.CampaignDefinitions.Stats["hp"] = hp
	fixture.server.dirty = true
	if err := fixture.server.saveLocked(); err != nil {
		fixture.server.mu.Unlock()
		t.Fatal(err)
	}
	fixture.server.mu.Unlock()

	gmWS := dialRaw(t, fixture.host.URL, fixture.gm)
	read(t, gmWS, "campaignSnapshot")
	aliceWS := dialRaw(t, fixture.host.URL, fixture.alice)
	read(t, aliceWS, "campaignSnapshot")
	charlieWS := dialRaw(t, fixture.host.URL, fixture.charlie)
	read(t, charlieWS, "campaignSnapshot")

	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "resource-create", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["wolf1"], PresetID: "wolf"})
	characterID := characterIDFromACK(t, read(t, gmWS, "ack"))

	set44 := Command{Type: "characterResourceCurrentSet", Client: "resource-current", Seq: 1, CharacterID: characterID, StatID: "hp", StatValue: statValuePointer(IntegerStatValue(44))}
	writeCharacterCommand(t, aliceWS, set44)
	if issue := ackError(t, read(t, aliceWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	fixture.server.mu.Lock()
	instance := fixture.session.CharacterInstances[characterID]
	fixture.server.mu.Unlock()
	if instance.Materialized != nil || instance.ResourceCurrent["hp"].Integer() != 44 {
		t.Fatalf("current update changed mechanical ownership: %#v", instance)
	}

	writeCharacterCommand(t, charlieWS, Command{Type: "characterResourceCurrentSet", Client: "resource-denied", Seq: 1, CharacterID: characterID, StatID: "hp", StatValue: statValuePointer(IntegerStatValue(1))})
	if issue := ackError(t, read(t, charlieWS, "ack")); !strings.Contains(issue, "access denied") {
		t.Fatalf("unrelated player changed resource current: %q", issue)
	}
	writeCharacterCommand(t, aliceWS, Command{Type: "characterResourceInitialSet", Client: "resource-initial-denied", Seq: 1, CharacterID: characterID, StatID: "hp", StatValue: statValuePointer(IntegerStatValue(100))})
	if issue := ackError(t, read(t, aliceWS, "ack")); !strings.Contains(issue, "GM") {
		t.Fatalf("ordinary player changed resource initial: %q", issue)
	}

	writeCharacterCommand(t, gmWS, Command{Type: "characterResourceInitialSet", Client: "resource-initial", Seq: 1, CharacterID: characterID, StatID: "hp", StatValue: statValuePointer(IntegerStatValue(100))})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	fixture.server.mu.Lock()
	registry, err := MergeDefinitionRegistries(sessionRegistries(fixture.session))
	if err != nil {
		fixture.server.mu.Unlock()
		t.Fatal(err)
	}
	instance = fixture.session.CharacterInstances[characterID]
	preset := registry.Presets[instance.PresetID]
	effective := EffectiveCharacterState(instance, &preset, registry)
	fixture.server.mu.Unlock()
	if instance.Materialized == nil || instance.Materialized.ResourceInitial["hp"].Integer() != 100 || instance.Materialized.Stats["dexterity"].Integer() != 15 || !reflect.DeepEqual(instance.Materialized.ActionIDs, []string{"bite"}) {
		t.Fatalf("initial update did not materialize complete effective mechanics: %#v", instance.Materialized)
	}
	if instance.ResourceCurrent["hp"].Integer() != 44 || effective.Resources["hp"].Current.Integer() != 44 || effective.Resources["hp"].Initial.Integer() != 100 {
		t.Fatalf("initial update changed current: instance=%#v effective=%#v", instance, effective.Resources["hp"])
	}
}

func TestResourceRulesetCompatibilityAndPresetRemoval(t *testing.T) {
	old := RulesetSnapshot{Registry: RulesetRegistry{
		Stats: map[string]StatDefinition{
			"hp":        resourceStatDefinition("hp", StatTypeInteger),
			"dexterity": {ID: "dexterity", Name: "Dexterity", Type: StatTypeInteger},
		},
		Actions: map[string]ActionDefinition{"bite": {ID: "bite", Name: "Bite"}},
		Presets: map[string]CharacterPresetDefinition{
			"wolf": {ID: "wolf", Name: "Wolf", Stats: map[string]StatValue{"hp": IntegerStatValue(55), "dexterity": IntegerStatValue(15)}, ActionIDs: []string{"bite"}},
		},
	}}
	preset := old.Registry.Presets["wolf"]
	linked := NewCharacterInstance("linked", &preset)
	linked = WithResourceCurrent(linked, "hp", IntegerStatValue(44))
	session := &Session{
		Ruleset:             old,
		CampaignDefinitions: emptyCampaignRegistry(),
		CharacterInstances:  map[string]CharacterInstance{"linked": linked},
		Assets:              map[string]Asset{},
	}

	incompatible := cloneRulesetSnapshot(old)
	definition := incompatible.Registry.Stats["hp"]
	definition.Type = StatTypeNumber
	incompatible.Registry.Stats["hp"] = definition
	changedPreset := incompatible.Registry.Presets["wolf"]
	changedPreset.Stats["hp"] = NumberStatValue(55)
	incompatible.Registry.Presets["wolf"] = changedPreset
	if _, err := prepareDefinitionCharacters(session, incompatible, emptyCampaignRegistry()); err == nil || !strings.Contains(err.Error(), "requires \"number\"") {
		t.Fatalf("incompatible resource type was accepted: %v", err)
	}
	if session.Ruleset.Registry.Stats["hp"].Type != StatTypeInteger || session.CharacterInstances["linked"].ResourceCurrent["hp"].Integer() != 44 || session.CharacterInstances["linked"].Materialized != nil {
		t.Fatal("candidate validation mutated committed state")
	}

	removed := cloneRulesetSnapshot(old)
	delete(removed.Registry.Presets, "wolf")
	updates, err := prepareDefinitionCharacters(session, removed, emptyCampaignRegistry())
	if err != nil {
		t.Fatal(err)
	}
	materialized, exists := updates["linked"]
	if !exists || materialized.Materialized == nil {
		t.Fatalf("preset removal did not materialize linked character: %#v", updates)
	}
	if materialized.Materialized.ResourceInitial["hp"].Integer() != 55 || materialized.Materialized.Stats["dexterity"].Integer() != 15 || !reflect.DeepEqual(materialized.Materialized.ActionIDs, []string{"bite"}) || materialized.ResourceCurrent["hp"].Integer() != 44 {
		t.Fatalf("preset removal lost mechanics or current: %#v", materialized)
	}
}

func TestResourceCurrentSaveFailureRollsBack(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	fixture.server.mu.Lock()
	hp := fixture.session.CampaignDefinitions.Stats["hp"]
	hp.Kind = StatKindResource
	fixture.session.CampaignDefinitions.Stats["hp"] = hp
	fixture.server.dirty = true
	if err := fixture.server.saveLocked(); err != nil {
		fixture.server.mu.Unlock()
		t.Fatal(err)
	}
	fixture.server.mu.Unlock()

	gmWS := dialRaw(t, fixture.host.URL, fixture.gm)
	read(t, gmWS, "campaignSnapshot")
	aliceWS := dialRaw(t, fixture.host.URL, fixture.alice)
	read(t, aliceWS, "campaignSnapshot")
	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "resource-rollback-create", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["wolf1"], PresetID: "wolf"})
	characterID := characterIDFromACK(t, read(t, gmWS, "ack"))

	blocker := filepath.Join(fixture.server.root, "sessions.json.tmp")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	command := Command{Type: "characterResourceCurrentSet", Client: "resource-rollback", Seq: 1, CharacterID: characterID, StatID: "hp", StatValue: statValuePointer(IntegerStatValue(20))}
	writeCharacterCommand(t, aliceWS, command)
	read(t, aliceWS, "saveError")
	fixture.server.mu.Lock()
	instance := fixture.session.CharacterInstances[characterID]
	aliceID := fixture.session.Keys[fixture.alice["key"]]
	receiptLeaked := fixture.session.Receipts[aliceID+":resource-rollback"].Seq != 0
	fixture.server.mu.Unlock()
	if _, exists := instance.ResourceCurrent["hp"]; exists || instance.Materialized != nil || receiptLeaked {
		t.Fatalf("failed current save leaked state or receipt: instance=%#v receipt=%v", instance, receiptLeaked)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	writeCharacterCommand(t, aliceWS, command)
	if issue := ackError(t, read(t, aliceWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	fixture.server.mu.Lock()
	instance = fixture.session.CharacterInstances[characterID]
	fixture.server.mu.Unlock()
	if instance.Materialized != nil || instance.ResourceCurrent["hp"].Integer() != 20 {
		t.Fatalf("retry did not persist current independently: %#v", instance)
	}
}

func TestNumericResourceCurrentCanDriveRollModifier(t *testing.T) {
	character := EffectiveCharacter{
		ID:    "resource-character",
		Stats: map[string]StatValue{},
		Resources: map[string]EffectiveResource{
			"stamina": {Current: NumberStatValue(3.5), Initial: NumberStatValue(10)},
		},
	}
	modifier, err := resolveRollModifier(character, RollSpec{ModifierStat: "stamina", ModifierFixed: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if modifier != 5 {
		t.Fatalf("resource modifier = %v, want 5", modifier)
	}
}
