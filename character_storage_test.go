package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testRulesetTOML(rulesetID, version string) []byte {
	return []byte(`schema_version = 1

[ruleset]
id = "` + rulesetID + `"
name = "Test ruleset"
version = "` + version + `"

[stats.hp]
name = "HP"
type = "integer"

[stats.strength_mod]
name = "Strength modifier"
type = "integer"

[actions.bite]
name = "Bite"

[[actions.bite.rolls]]
id = "attack"
name = "Attack"
count = 1
sides = 20
modifier_stat = "strength_mod"

[presets.wolf]
name = "Wolf"
kind = "monster"
actions = ["bite"]

[presets.wolf.stats]
hp = 11
strength_mod = 1
`)
}

func createSessionForStorageTest(t *testing.T, server *Server, name string) *Session {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{"name":"`+name+`"}`))
	response := httptest.NewRecorder()
	server.create(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("create session: %d %s", response.Code, response.Body.String())
	}
	var result map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return server.sessions[result["session"]]
}

func addPersistedCharacterFixture(t *testing.T, server *Server, session *Session) {
	t.Helper()
	session.CampaignDefinitions.Stats["speed"] = StatDefinition{ID: "speed", Name: "Speed", Type: StatTypeNumber}
	session.CharacterInstances["wolf-1"] = CharacterInstance{
		ID: "wolf-1", PresetID: "wolf", Name: "Wolf #1",
		StatOverrides:  map[string]StatValue{"hp": IntegerStatValue(4), "speed": NumberStatValue(12)},
		AddedActionIDs: []string{}, RemovedActionIDs: []string{},
	}
	first := firstScene(session)
	second := newScene("second-scene", "Second")
	session.Scenes[second.ID] = second
	for index, scene := range []*Scene{first, second} {
		floorID := firstFloorID(scene)
		tokenID := "wolf-token-" + string(rune('1'+index))
		scene.Tokens[tokenID] = Token{
			ID: tokenID, Name: "Wolf", FloorID: floorID, LayerID: layerIDByKind(scene, floorID, layerKindTokens),
			X: 100, Y: 100, Size: 80, CharacterInstanceID: "wolf-1",
		}
		scene.Revision++
		scene.rebuildRuntime()
	}
	rebuildCharacterReferences(session)
	server.dirty = true
	if err := server.save(); err != nil {
		t.Fatal(err)
	}
}

func TestRulesetTOMLRequiresMetadataAndIsInternallyClosed(t *testing.T) {
	snapshot, err := ParseRulesetTOML(testRulesetTOML("test", "1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Metadata.ID != "test" || snapshot.Registry.Presets["wolf"].ActionIDs[0] != "bite" {
		t.Fatalf("unexpected ruleset snapshot: %#v", snapshot)
	}
	if _, err := ParseRulesetTOML([]byte("schema_version = 1\n")); err == nil {
		t.Fatal("ruleset without metadata was accepted")
	}
	broken := []byte(`schema_version = 1
[ruleset]
id = "broken"
name = "Broken"
version = "1"
[presets.wolf]
name = "Wolf"
actions = ["missing"]
`)
	if _, err := ParseRulesetTOML(broken); err == nil {
		t.Fatal("ruleset with external reference was accepted")
	}
}

func TestCharacterStorageRoundTripAndRulesetDefaultSnapshot(t *testing.T) {
	root := t.TempDir()
	rulesetPath := filepath.Join(root, "default.toml")
	if err := os.WriteFile(rulesetPath, testRulesetTOML("rules-a", "1"), 0600); err != nil {
		t.Fatal(err)
	}
	rulesetA, err := loadRulesetSnapshotFile(rulesetPath)
	if err != nil {
		t.Fatal(err)
	}
	server, err := newServer(root, rulesetA)
	if err != nil {
		t.Fatal(err)
	}
	session := createSessionForStorageTest(t, server, "Current")
	addPersistedCharacterFixture(t, server, session)

	if err := os.WriteFile(rulesetPath, testRulesetTOML("rules-b", "2"), 0600); err != nil {
		t.Fatal(err)
	}
	rulesetB, err := loadRulesetSnapshotFile(rulesetPath)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := newServer(root, rulesetB)
	if err != nil {
		t.Fatal(err)
	}
	got := restored.sessions[session.ID]
	if got.Ruleset.Metadata.ID != "rules-a" || got.Ruleset.Metadata.Version != "1" {
		t.Fatalf("existing session rebound to changed default: %#v", got.Ruleset.Metadata)
	}
	instance := got.CharacterInstances["wolf-1"]
	if instance.StatOverrides["hp"].Type() != StatTypeInteger || instance.StatOverrides["hp"].Integer() != 4 || instance.StatOverrides["speed"].Type() != StatTypeNumber || instance.StatOverrides["speed"].Number() != 12 {
		t.Fatalf("typed instance values did not round trip: %#v", instance.StatOverrides)
	}
	if len(got.characterReferences["wolf-1"]) != 2 {
		t.Fatalf("cross-scene reference index was not restored: %#v", got.characterReferences)
	}
	newSession := createSessionForStorageTest(t, restored, "New with B")
	if newSession.Ruleset.Metadata.ID != "rules-b" || newSession.Ruleset.Metadata.Version != "2" {
		t.Fatalf("new session did not receive current default: %#v", newSession.Ruleset.Metadata)
	}
	newSession.Ruleset.Metadata.Name = "Changed in session"
	if restored.defaultRuleset.Metadata.Name == "Changed in session" {
		t.Fatal("new session shares mutable ruleset snapshot with server default")
	}
}

func TestLegacyStorageInitializesEmptyCharacterCollections(t *testing.T) {
	root := t.TempDir()
	server, err := newServer(root)
	if err != nil {
		t.Fatal(err)
	}
	session := createSessionForStorageTest(t, server, "Legacy")
	path := filepath.Join(root, "sessions.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	delete(document[session.ID], "rulesetSnapshot")
	delete(document[session.ID], "campaignDefinitions")
	delete(document[session.ID], "characterInstances")
	legacy, err := json.Marshal(document)
	if err != nil || os.WriteFile(path, legacy, 0600) != nil {
		t.Fatal("write legacy fixture", err)
	}

	restored, err := newServer(root)
	if err != nil {
		t.Fatal(err)
	}
	got := restored.sessions[session.ID]
	if got.CharacterInstances == nil || len(got.CharacterInstances) != 0 || got.CampaignDefinitions.Stats == nil || got.Ruleset.Registry.Stats == nil {
		t.Fatalf("legacy collections were not initialized: %#v", got)
	}
	for _, scene := range got.Scenes {
		for _, token := range scene.Tokens {
			if token.CharacterInstanceID != "" {
				t.Fatal("legacy token was assigned a character")
			}
		}
	}
}

func TestCorruptCharacterStorageIsRejectedWithoutOverwrite(t *testing.T) {
	mutations := map[string]func(map[string]any){
		"missing character reference": func(session map[string]any) {
			scenes := session["scenes"].(map[string]any)
			for _, rawScene := range scenes {
				tokens := rawScene.(map[string]any)["tokens"].(map[string]any)
				for _, rawToken := range tokens {
					rawToken.(map[string]any)["characterInstanceId"] = "missing"
					return
				}
			}
		},
		"wrong stat type": func(session map[string]any) {
			instances := session["characterInstances"].(map[string]any)
			wolf := instances["wolf-1"].(map[string]any)
			overrides := wolf["statOverrides"].(map[string]any)
			overrides["hp"] = map[string]any{"type": "boolean", "value": true}
		},
		"duplicate id mismatch": func(session map[string]any) {
			instances := session["characterInstances"].(map[string]any)
			instances["wolf-1"].(map[string]any)["id"] = "other-id"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			ruleset, err := ParseRulesetTOML(testRulesetTOML("rules", "1"))
			if err != nil {
				t.Fatal(err)
			}
			server, err := newServer(root, ruleset)
			if err != nil {
				t.Fatal(err)
			}
			session := createSessionForStorageTest(t, server, "Corrupt")
			addPersistedCharacterFixture(t, server, session)
			path := filepath.Join(root, "sessions.json")
			var document map[string]any
			data, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(data, &document) != nil {
				t.Fatal("read fixture", err)
			}
			mutate(document[session.ID].(map[string]any))
			corrupt, err := json.Marshal(document)
			if err != nil || os.WriteFile(path, corrupt, 0600) != nil {
				t.Fatal("write corrupt fixture", err)
			}
			if _, err := newServer(root); err == nil || (!strings.Contains(err.Error(), "character") && !strings.Contains(err.Error(), "stat")) {
				t.Fatalf("expected clear character validation error, got %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, corrupt) {
				t.Fatal("corrupt persistence was overwritten")
			}
		})
	}
}

func TestSaveRefusesInvalidCharacterState(t *testing.T) {
	root := t.TempDir()
	server, err := newServer(root)
	if err != nil {
		t.Fatal(err)
	}
	session := createSessionForStorageTest(t, server, "Invalid")
	before, err := os.ReadFile(filepath.Join(root, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	session.CharacterInstances["wrong-key"] = CharacterInstance{ID: "different", Name: "Broken", StatOverrides: map[string]StatValue{}}
	server.dirty = true
	if err := server.save(); err == nil {
		t.Fatal("invalid state was saved")
	}
	after, err := os.ReadFile(filepath.Join(root, "sessions.json"))
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("save modified persistence before validation")
	}
}

func TestStorageRejectsDuplicateJSONIDs(t *testing.T) {
	data := []byte(`{"sessions":{"same":{"id":"one"},"same":{"id":"two"}}}`)
	if err := rejectDuplicateJSONKeys(data); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("expected duplicate key error, got %v", err)
	}
}
