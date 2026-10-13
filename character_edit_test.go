package main

import (
	"strings"
	"testing"
)

func TestCharacterEditAtomicReplaceAndIsolatedConflict(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	fixture.server.mu.Lock()
	defer fixture.server.mu.Unlock()
	session := fixture.session
	gm := session.Members[session.Keys[fixture.gm["key"]]]
	alice := session.Members[session.Keys[fixture.alice["key"]]]
	preset := session.CampaignDefinitions.Presets["wolf"]
	original := NewCharacterInstance("sheet-edit-test", &preset)
	session.CharacterInstances[original.ID] = original
	tracked := StatDefinition{ID: "energy_edit", Name: "Energy", Kind: StatKindResource, Type: StatTypeInteger}
	action := ActionDefinition{ID: "rest_edit", Name: "Rest"}
	edit := &CharacterEditPayload{
		ExpectedCharacterRevision: session.CharacterRevision,
		ExpectedRegistryRevision:  session.RegistryRevision,
		ExpectedInstance:          ptrCharacter(original),
		Name:                      "Edited Wolf",
		Stats:                     map[string]StatValue{"dexterity": IntegerStatValue(18)},
		ResourceInitial:           map[string]StatValue{tracked.ID: IntegerStatValue(10)},
		ResourceCurrent:           map[string]StatValue{tracked.ID: IntegerStatValue(4)},
		ActionIDs:                 []string{action.ID},
		Definitions:               []StatDefinition{tracked},
		NewActions:                []ActionDefinition{action},
	}
	command := Command{Type: "characterEdit", CharacterID: original.ID, CharacterEdit: edit}
	mutation := newCharacterMutation(session)
	if err := applyCharacterCommand(session, gm, fixture.sceneA.ID, command, mutation); err != nil {
		t.Fatal(err)
	}
	after := session.CharacterInstances[original.ID]
	if after.Name != "Edited Wolf" || after.Materialized == nil || after.Materialized.Stats["dexterity"].Integer() != 18 || after.ResourceCurrent[tracked.ID].Integer() != 4 {
		t.Fatalf("batch not applied: %+v", after)
	}
	if _, ok := after.Materialized.Stats["hp"]; ok {
		t.Fatal("removed inherited characteristic remains")
	}
	if session.CampaignDefinitions.Presets["wolf"].Stats["hp"].Integer() != 11 {
		t.Fatal("original preset changed")
	}
	if _, ok := session.CampaignDefinitions.Stats[tracked.ID]; !ok {
		t.Fatal("new definition missing")
	}
	// Replaying an old snapshot after an unrelated revision increment is a conflict
	// only if this exact character changed.
	stale := newCharacterMutation(session)
	if err := applyCharacterCommand(session, gm, fixture.sceneA.ID, command, stale); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale batch: %v", err)
	}
	stale.rollback()
	denied := newCharacterMutation(session)
	if err := applyCharacterCommand(session, alice, fixture.sceneA.ID, Command{Type: "characterEdit", CharacterID: after.ID, CharacterEdit: edit}, denied); err == nil {
		t.Fatal("player edited mechanical state")
	}
	denied.rollback()
}

func TestCharacterEditInvalidDefinitionDoesNotModifyCharacter(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	fixture.server.mu.Lock()
	defer fixture.server.mu.Unlock()
	session := fixture.session
	gm := session.Members[session.Keys[fixture.gm["key"]]]
	original := NewCharacterInstance("invalid-edit-test", nil)
	session.CharacterInstances[original.ID] = original
	edit := &CharacterEditPayload{
		ExpectedRegistryRevision: session.RegistryRevision, ExpectedInstance: ptrCharacter(original), Name: "Try",
		Stats:       map[string]StatValue{"fractional_new": NumberStatValue(1.5)},
		Definitions: []StatDefinition{{ID: "fractional_new", Name: "Decimal", Type: StatTypeNumber}},
	}
	mutation := newCharacterMutation(session)
	if err := applyCharacterCommand(session, gm, fixture.sceneA.ID, Command{Type: "characterEdit", CharacterID: original.ID, CharacterEdit: edit}, mutation); err == nil {
		t.Fatal("float definition accepted")
	}
	mutation.rollback()
	if session.CharacterInstances[original.ID].Name != original.Name {
		t.Fatal("character was modified")
	}
	if _, exists := session.CampaignDefinitions.Stats["fractional_new"]; exists {
		t.Fatal("invalid definition was registered")
	}
}

func ptrCharacter(c CharacterInstance) *CharacterInstance { return &c }

func TestCharacterEditNameOnlyKeepsPresetInheritance(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	fixture.server.mu.Lock()
	defer fixture.server.mu.Unlock()
	session := fixture.session
	member := session.Members[session.Keys[fixture.gm["key"]]]
	preset := session.CampaignDefinitions.Presets["wolf"]
	linked := NewCharacterInstance("edit-name-only", &preset)
	session.CharacterInstances[linked.ID] = linked
	draft := &CharacterEditPayload{
		ExpectedRegistryRevision: session.RegistryRevision, ExpectedInstance: ptrCharacter(linked),
		Name: "Renamed", Stats: cloneStatValues(preset.Stats), ActionIDs: append([]string(nil), preset.ActionIDs...),
	}
	mutation := newCharacterMutation(session)
	if err := applyCharacterCommand(session, member, fixture.sceneA.ID, Command{Type: "characterEdit", CharacterID: linked.ID, CharacterEdit: draft}, mutation); err != nil {
		t.Fatal(err)
	}
	after := session.CharacterInstances[linked.ID]
	if after.Materialized != nil || after.Name != "Renamed" || after.PresetID != preset.ID {
		t.Fatalf("name-only edit materialized character: %+v", after)
	}
}
