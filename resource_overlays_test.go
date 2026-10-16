package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func overlay(statID, position string, slot int, mode string) ResourceOverlayConfig {
	return ResourceOverlayConfig{StatID: statID, Enabled: true, Position: position, Slot: slot, ShowName: true, Color: "#76d275", Mode: mode}
}

func TestResourceOverlayConfigValidation(t *testing.T) {
	registry := EffectiveRegistry{Stats: map[string]StatDefinition{
		"hp":       {ID: "hp", Name: "HP", Type: StatTypeInteger, Kind: StatKindResource},
		"mp":       {ID: "mp", Name: "MP", Type: StatTypeNumber, Kind: StatKindResource},
		"alive":    {ID: "alive", Name: "Alive", Type: StatTypeBoolean, Kind: StatKindResource},
		"note":     {ID: "note", Name: "Note", Type: StatTypeString, Kind: StatKindResource},
		"strength": {ID: "strength", Name: "Strength", Type: StatTypeInteger},
	}}
	valid := []ResourceOverlayConfig{
		overlay("hp", "top", 1, "bar"), overlay("mp", "top", 2, "text"),
		overlay("alive", "bottom", 1, "text"), overlay("note", "bottom", 2, "text"),
	}
	if err := validateResourceOverlayConfig(valid, registry); err != nil {
		t.Fatal(err)
	}
	for name, config := range map[string][]ResourceOverlayConfig{
		"duplicate slot": {overlay("hp", "top", 1, "bar"), overlay("mp", "top", 1, "text")},
		"ordinary stat":  {overlay("strength", "top", 1, "text")},
		"string bar":     {overlay("note", "top", 1, "bar")},
		"fifth overlay":  append(valid, overlay("hp", "top", 1, "text")),
	} {
		if err := validateResourceOverlayConfig(config, registry); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	definitions := resourceOverlayDefinitions(registry, valid, true)
	if len(definitions) != 4 || definitions["strength"].ID != "" {
		t.Fatalf("settings exposed non-resource stats: %#v", definitions)
	}
}

func TestCharacterDisplaysDeduplicateSharedCharacterAndConfiguredResources(t *testing.T) {
	registry := EffectiveRegistry{Stats: map[string]StatDefinition{
		"hp":     {ID: "hp", Name: "HP", Type: StatTypeInteger, Kind: StatKindResource},
		"unused": {ID: "unused", Name: "Unused", Type: StatTypeInteger, Kind: StatKindResource},
	}, Presets: map[string]CharacterPresetDefinition{
		"wolf": {ID: "wolf", Name: "Wolf", Stats: map[string]StatValue{"hp": IntegerStatValue(10), "unused": IntegerStatValue(99)}},
	}, Actions: map[string]ActionDefinition{}}
	preset := registry.Presets["wolf"]
	instance := NewCharacterInstance("character-shared", &preset)
	instance.ResourceCurrent = map[string]StatValue{"hp": IntegerStatValue(4)}
	session := &Session{CharacterInstances: map[string]CharacterInstance{instance.ID: instance}, ResourceOverlays: []ResourceOverlayConfig{overlay("hp", "top", 1, "text")}}
	tokens := map[string]Token{
		"a": {ID: "a", CharacterInstanceID: instance.ID},
		"b": {ID: "b", CharacterInstanceID: instance.ID},
	}
	displays := characterDisplaysForTokens(session, registry, tokens)
	if len(displays) != 1 {
		t.Fatalf("shared character was duplicated: %#v", displays)
	}
	resource := displays[instance.ID].Resources["hp"]
	if resource.Current.Integer() != 4 || resource.Initial.Integer() != 10 || len(displays[instance.ID].Resources) != 1 {
		t.Fatalf("compact resource display is wrong: %#v", displays[instance.ID])
	}
}

func TestResourceOverlayConfigPersistsAndResourceChangeUsesCompactEvent(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	fixture.server.mu.Lock()
	hp := fixture.session.Ruleset.Registry.Stats["hp"]
	hp.Kind = StatKindResource
	fixture.session.Ruleset.Registry.Stats["hp"] = hp
	fixture.session.ResourceOverlays = nil
	fixture.session.RegistryRevision++
	fixture.server.dirty = true
	if err := fixture.server.saveLocked(); err != nil {
		fixture.server.mu.Unlock()
		t.Fatal(err)
	}
	fixture.server.mu.Unlock()

	gmWS := dialRaw(t, fixture.host.URL, fixture.gm)
	defer gmWS.Close()
	read(t, gmWS, "campaignSnapshot")
	config := []ResourceOverlayConfig{overlay("hp", "top", 1, "bar")}
	writeCharacterCommand(t, gmWS, Command{Type: "resourceOverlayConfigSet", Client: "overlay-config", Seq: 1, ResourceOverlays: config})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}

	playerWS := dialRaw(t, fixture.host.URL, fixture.alice)
	defer playerWS.Close()
	read(t, playerWS, "campaignSnapshot")
	writeCharacterCommand(t, playerWS, Command{Type: "resourceOverlayConfigSet", Client: "overlay-denied", Seq: 1, ResourceOverlays: nil})
	if issue := ackError(t, read(t, playerWS, "ack")); issue == "" {
		t.Fatal("player changed resource overlay configuration")
	}

	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "overlay-character", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["wolf1"], PresetID: "wolf"})
	characterID := characterIDFromACK(t, read(t, gmWS, "ack"))
	writeCharacterCommand(t, gmWS, Command{Type: "characterLink", Client: "overlay-character", Seq: 2, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["wolf2"], CharacterID: characterID})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}

	observer := dialRaw(t, fixture.host.URL, fixture.gm)
	defer observer.Close()
	read(t, observer, "campaignSnapshot")
	snapshot := subscribeSceneLoaded(t, observer, fixture.sceneA.ID)
	var displays map[string]CharacterDisplay
	if err := json.Unmarshal(snapshot["characterDisplays"], &displays); err != nil {
		t.Fatal(err)
	}
	if len(displays) == 0 || displays[characterID].Resources["hp"].Initial.Integer() != 11 {
		t.Fatalf("scene snapshot omitted compact shared display: %#v", displays)
	}

	writeCharacterCommand(t, playerWS, Command{Type: "characterResourceCurrentSet", Client: "overlay-live", Seq: 1, CharacterID: characterID, StatID: "hp", StatValue: statValuePointer(IntegerStatValue(7))})
	if issue := ackError(t, read(t, playerWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	changed := read(t, observer, "characterDisplayChanged")
	var display CharacterDisplay
	if err := json.Unmarshal(changed["display"], &display); err != nil {
		t.Fatal(err)
	}
	if display.ID != characterID || display.Resources["hp"].Current.Integer() != 7 {
		t.Fatalf("live display event is wrong: %#v", display)
	}
	fixture.server.mu.Lock()
	for peer := range fixture.server.peers {
		if peer.session == fixture.session.ID && peer.member.ID == fixture.session.Keys[fixture.gm["key"]] && peer != nil && peer.characterID != "" {
			fixture.server.mu.Unlock()
			t.Fatal("resource overlay created a CharacterWatch")
		}
	}
	fixture.server.mu.Unlock()

	data, err := os.ReadFile(filepath.Join(fixture.server.root, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]*Session{}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored[fixture.session.ID].ResourceOverlays, config) {
		t.Fatalf("overlay config was not persisted: %#v", stored[fixture.session.ID].ResourceOverlays)
	}
}
