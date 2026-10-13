package main

import (
	"strings"
	"testing"
)

func TestCharacterParameterUnifiedAddRemoveAndPresetIsolation(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	fixture.server.mu.Lock()
	defer fixture.server.mu.Unlock()
	session := fixture.session
	preset := session.CampaignDefinitions.Presets["wolf"]
	member := session.Members[session.Keys[fixture.gm["key"]]]
	alice := session.Members[session.Keys[fixture.alice["key"]]]
	linked := NewCharacterInstance("unified-test", &preset)
	session.CharacterInstances[linked.ID] = linked
	apply := func(command Command) error {
		mutation := newCharacterMutation(session)
		err := applyCharacterCommand(session, member, fixture.sceneA.ID, command, mutation)
		if err != nil {
			mutation.rollback()
		}
		return err
	}
	stamina := StatDefinition{ID: "stamina", Name: "Stamina", Type: StatTypeInteger, Kind: StatKindResource}
	if err := apply(Command{Type: "characterParameterAdd", CharacterID: linked.ID, StatID: "stamina", StatValue: statValuePointer(IntegerStatValue(20)), StatDefinition: &stamina}); err != nil {
		t.Fatal(err)
	}
	instance := session.CharacterInstances[linked.ID]
	if instance.Materialized == nil || instance.Materialized.ResourceInitial["stamina"].Integer() != 20 || instance.Materialized.Stats["hp"].Integer() != 11 {
		t.Fatalf("add failed to materialize and retain preset values: %+v", instance)
	}
	if session.CampaignDefinitions.Stats["stamina"].Kind != StatKindResource {
		t.Fatal("missing resource definition")
	}
	instance.ResourceCurrent = map[string]StatValue{"stamina": IntegerStatValue(5)}
	session.CharacterInstances[linked.ID] = instance
	if err := apply(Command{Type: "characterParameterRemove", CharacterID: linked.ID, StatID: "stamina"}); err != nil {
		t.Fatal(err)
	}
	instance = session.CharacterInstances[linked.ID]
	if _, ok := instance.Materialized.ResourceInitial["stamina"]; ok {
		t.Fatal("initial resource survived removal")
	}
	if _, ok := instance.ResourceCurrent["stamina"]; ok {
		t.Fatal("current resource survived removal")
	}
	if _, ok := session.CampaignDefinitions.Stats["stamina"]; !ok {
		t.Fatal("removing value deleted global definition")
	}
	if err := apply(Command{Type: "characterParameterRemove", CharacterID: linked.ID, StatID: "hp"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := session.CharacterInstances[linked.ID].Materialized.Stats["hp"]; ok {
		t.Fatal("inherited stat survived removal")
	}
	if preset.Stats["hp"].Integer() != 11 || session.CampaignDefinitions.Presets["wolf"].Stats["hp"].Integer() != 11 {
		t.Fatal("character removal changed preset")
	}
	if err := apply(Command{Type: "characterParameterAdd", CharacterID: linked.ID, StatID: "hp", StatValue: statValuePointer(IntegerStatValue(8))}); err != nil {
		t.Fatal(err)
	}
	if session.CharacterInstances[linked.ID].Materialized.Stats["hp"].Integer() != 8 {
		t.Fatal("existing definition was not re-added")
	}
	if err := apply(Command{Type: "characterParameterAdd", CharacterID: linked.ID, StatID: "hp", StatValue: statValuePointer(IntegerStatValue(10))}); !strings.Contains(err.Error(), "already assigned") {
		t.Fatalf("duplicate parameter: %v", err)
	}
	mutation := newCharacterMutation(session)
	if err := applyCharacterCommand(session, alice, fixture.sceneA.ID, Command{Type: "characterParameterRemove", CharacterID: linked.ID, StatID: "hp"}, mutation); err == nil {
		t.Fatal("player removed parameter")
	}
	mutation.rollback()
}

func TestCharacterParameterNewFloatDefinitionRejectedAtomically(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	fixture.server.mu.Lock()
	defer fixture.server.mu.Unlock()
	session := fixture.session
	member := session.Members[session.Keys[fixture.gm["key"]]]
	instance := NewCharacterInstance("float-rejection-test", nil)
	session.CharacterInstances[instance.ID] = instance
	// A character without a preset uses the same add/remove flow.
	add := func(command Command) error {
		mutation := newCharacterMutation(session)
		err := applyCharacterCommand(session, member, fixture.sceneA.ID, command, mutation)
		if err != nil {
			mutation.rollback()
		}
		return err
	}
	if err := add(Command{Type: "characterParameterAdd", CharacterID: instance.ID, StatID: "dexterity", StatValue: statValuePointer(IntegerStatValue(9))}); err != nil {
		t.Fatal(err)
	}
	custom := StatDefinition{ID: "has_luck", Name: "Luck", Type: StatTypeBoolean}
	if err := add(Command{Type: "characterParameterAdd", CharacterID: instance.ID, StatID: "has_luck", StatValue: statValuePointer(BooleanStatValue(true)), StatDefinition: &custom}); err != nil {
		t.Fatal(err)
	}
	if !session.CharacterInstances[instance.ID].Materialized.Stats["has_luck"].Boolean() {
		t.Fatal("boolean parameter not added to empty character")
	}
	if err := add(Command{Type: "characterParameterRemove", CharacterID: instance.ID, StatID: "has_luck"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := session.CharacterInstances[instance.ID].Materialized.Stats["has_luck"]; ok {
		t.Fatal("boolean parameter not removed")
	}
	value := NumberStatValue(1.5)
	def := StatDefinition{ID: "decimal_new", Name: "Decimal", Type: StatTypeNumber}
	mutation := newCharacterMutation(session)
	if err := applyCharacterCommand(session, member, fixture.sceneA.ID, Command{Type: "characterParameterAdd", CharacterID: instance.ID, StatID: def.ID, StatValue: &value, StatDefinition: &def}, mutation); err == nil {
		t.Fatal("float definition accepted")
	}
	mutation.rollback()
	if _, ok := session.CampaignDefinitions.Stats[def.ID]; ok {
		t.Fatal("invalid definition leaked")
	}
	if _, ok := session.CharacterInstances[instance.ID].Materialized.Stats[def.ID]; ok {
		t.Fatal("invalid value leaked")
	}
}
