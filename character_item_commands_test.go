package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func itemCommandSession(t *testing.T) *Session {
	t.Helper()
	ruleset, err := CompileRulesetFiles(map[string]string{"items.toml": inventoryRules, "character.toml": inventoryHero})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: ruleset.Registry})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := decodeNestedCharacter("hero", ruleset.Characters["hero"], registry)
	if err != nil {
		t.Fatal(err)
	}
	return &Session{Ruleset: ruleset, RegistryRevision: 1, CharacterRevision: 1, CharacterInstances: map[string]CharacterInstance{"hero": instance}, CharacterSources: map[string]CharacterTOMLSource{}}
}

func TestItemGrantInstancesAreIndependentAndProtected(t *testing.T) {
	session := itemCommandSession(t)
	original := len(session.CharacterInstances["hero"].Inventory)
	grant := Command{Type: "characterItemAdd", CharacterID: "hero", ObjectID: "ring", ItemCount: 2, Equipped: boolPointer(false)}
	if err := applyCharacterCommand(session, &Member{Role: "player"}, "", grant, newCharacterMutation(session)); err == nil || !strings.Contains(err.Error(), "GM") {
		t.Fatalf("player was allowed: %v", err)
	}
	if err := applyCharacterCommand(session, &Member{GM: true}, "", grant, newCharacterMutation(session)); err != nil {
		t.Fatal(err)
	}
	inventory := session.CharacterInstances["hero"].Inventory
	if len(inventory) != original+2 || inventory[original].ID == inventory[original+1].ID || inventory[original].Equipped || inventory[original+1].ObjectID != "ring" {
		t.Fatalf("bad grant: %+v", inventory[original:])
	}
	if err := ValidateRulesetSnapshot(session.Ruleset); err != nil {
		t.Fatal(err)
	}
}

func TestCreateObjectWithGrantsRoundTripAndRollback(t *testing.T) {
	session := itemCommandSession(t)
	original := len(session.Ruleset.Registry.Objects)
	cmd := Command{Type: "characterItemCreate", CharacterID: "hero", ItemCount: 1, Equipped: boolPointer(true), ObjectDraft: &CharacterObjectDraft{
		Name: "Настой стойкости", Description: "Усиливает носителя", Properties: map[string]string{"damage": "1d8", "charges": "3"}, Actions: []string{"weapon.attack.hit"}, Effects: []string{"ring_strength"},
	}}
	mutation := newCharacterMutation(session)
	if err := applyCharacterItemAdd(session, cmd, mutation); err != nil {
		t.Fatal(err)
	}
	if len(session.Ruleset.Registry.Objects) != original+1 {
		t.Fatal("new object is missing")
	}
	item := session.CharacterInstances["hero"].Inventory[len(session.CharacterInstances["hero"].Inventory)-1]
	if !item.Equipped || !strings.HasPrefix(item.ObjectID, "campaign.item_") {
		t.Fatalf("wrong item: %+v", item)
	}
	object := session.Ruleset.Registry.Objects[item.ObjectID]
	if object == nil || object.values["name"] != "Настой стойкости" || object.values["damage"] != "1d8" || object.values["charges"] != int64(3) {
		t.Fatalf("incorrect object: %+v", object)
	}
	saved, err := json.Marshal(session.Ruleset)
	if err != nil {
		t.Fatal(err)
	}
	var loaded RulesetSnapshot
	if err := json.Unmarshal(saved, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Registry.Objects[item.ObjectID] == nil || loaded.Registry.mechanics.objects[item.ObjectID].effects[0] != "ring_strength" {
		t.Fatal("object mechanics lost on restart")
	}
	mutation.rollback()
	if len(session.Ruleset.Registry.Objects) != original || len(session.CharacterInstances["hero"].Inventory) != 6 {
		t.Fatal("rollback retained object or item")
	}
}

func TestCreateObjectRejectsInvalidDraftAndKeepsRuleset(t *testing.T) {
	session := itemCommandSession(t)
	objects := len(session.Ruleset.Registry.Objects)
	for _, draft := range []*CharacterObjectDraft{
		{Name: "Bad effect", Effects: []string{"missing"}},
		{Name: "Bad property", Properties: map[string]string{"actions": "bypass"}},
		{Name: "Duplicate", Actions: []string{"weapon.attack.hit", "weapon.attack.hit"}},
	} {
		cmd := Command{Type: "characterItemCreate", CharacterID: "hero", ItemCount: 1, ObjectDraft: draft}
		if err := applyCharacterItemAdd(session, cmd, newCharacterMutation(session)); err == nil {
			t.Fatalf("accepted invalid draft %+v", draft)
		}
		if len(session.Ruleset.Registry.Objects) != objects {
			t.Fatal("ruleset mutated after validation failure")
		}
	}
	if err := applyCharacterItemAdd(session, Command{Type: "characterItemAdd", CharacterID: "hero", ObjectID: "ring", ItemCount: 21}, newCharacterMutation(session)); err == nil {
		t.Fatal("unbounded quantity accepted")
	}
}
