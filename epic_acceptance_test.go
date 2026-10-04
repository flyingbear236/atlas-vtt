package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"reflect"
	"sync"
	"testing"
)

func TestEpicExistingCampaignRulesetRestartAndOverlay(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Existing empty campaign"})
	document := testRulesetTOML("acceptance-core", "1.0.0")

	status, body := definitionHTTP(t, host.URL, "/api/definitions/ruleset/preview", gm, document, nil)
	if status != http.StatusOK {
		t.Fatalf("ruleset preview: %d %s", status, body)
	}
	var preview definitionPreviewResponse
	if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatal(err)
	}
	apply := url.Values{
		"expectedRevision": {fmt.Sprint(preview.RegistryRevision)},
		"digest":           {preview.Digest},
		"key":              {"acceptance-install"},
	}
	if status, body = definitionHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, document, apply); status != http.StatusOK {
		t.Fatalf("ruleset apply: %d %s", status, body)
	}
	host.Close()

	restarted := testServer(t, root)
	session := restarted.sessions[gm["session"]]
	if session.Ruleset.Metadata.ID != "acceptance-core" || session.Ruleset.Registry.Presets["wolf"].Stats["hp"].Integer() != 11 {
		t.Fatalf("installed snapshot did not survive restart: %#v", session.Ruleset)
	}
	restartedHost := httptest.NewServer(restarted.routes())
	gmWS := dialRaw(t, restartedHost.URL, gm)
	read(t, gmWS, "campaignSnapshot")
	overlay := clonePresetDefinition(session.Ruleset.Registry.Presets["wolf"])
	overlay.Name = "Campaign wolf"
	overlay.Stats["hp"] = IntegerStatValue(17)
	command := Command{
		Type: "definitionCreate", Client: "acceptance-overlay", Seq: 1,
		DefinitionKind: definitionKindPreset, DefinitionID: "wolf", PresetDefinition: &overlay,
		ExpectedRegistryRevision: registryRevisionPointer(session.RegistryRevision),
	}
	writeCharacterCommand(t, gmWS, command)
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	gmWS.Close()
	restartedHost.Close()

	secondRestart := testServer(t, root)
	session = secondRestart.sessions[gm["session"]]
	effective, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		t.Fatal(err)
	}
	if session.Ruleset.Registry.Presets["wolf"].Stats["hp"].Integer() != 11 || session.CampaignDefinitions.Presets["wolf"].Stats["hp"].Integer() != 17 || effective.Presets["wolf"].Stats["hp"].Integer() != 17 {
		t.Fatalf("ruleset snapshot or campaign overlay changed across restart: ruleset=%#v campaign=%#v effective=%#v", session.Ruleset.Registry.Presets["wolf"], session.CampaignDefinitions.Presets["wolf"], effective.Presets["wolf"])
	}
}

func TestEpicCoOwnersConcurrentStatPatchesReconnect(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	gmWS := dialRaw(t, fixture.host.URL, fixture.gm)
	read(t, gmWS, "campaignSnapshot")
	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "acceptance-owner-setup", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["wolf1"], PresetID: "wolf"})
	characterID := characterIDFromACK(t, read(t, gmWS, "ack"))

	aliceWS := dialRaw(t, fixture.host.URL, fixture.alice)
	read(t, aliceWS, "campaignSnapshot")
	bobWS := dialRaw(t, fixture.host.URL, fixture.bob)
	read(t, bobWS, "campaignSnapshot")
	aliceCommand := Command{Type: "characterStatSet", Client: "acceptance-alice", Seq: 1, CharacterID: characterID, StatID: "hp", StatValue: statValuePointer(IntegerStatValue(3))}
	bobCommand := Command{Type: "characterStatSet", Client: "acceptance-bob", Seq: 1, CharacterID: characterID, StatID: "dexterity", StatValue: statValuePointer(IntegerStatValue(18))}
	start := make(chan struct{})
	var writes sync.WaitGroup
	for _, entry := range []struct {
		connection interface{ WriteJSON(any) error }
		command    Command
	}{{aliceWS, aliceCommand}, {bobWS, bobCommand}} {
		writes.Add(1)
		go func() {
			defer writes.Done()
			<-start
			if err := entry.connection.WriteJSON(entry.command); err != nil {
				t.Errorf("write concurrent stat patch: %v", err)
			}
		}()
	}
	close(start)
	writes.Wait()
	if issue := ackError(t, read(t, aliceWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	if issue := ackError(t, read(t, bobWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	aliceWS.Close()
	bobWS.Close()

	reconnected := dialRaw(t, fixture.host.URL, fixture.alice)
	read(t, reconnected, "campaignSnapshot")
	if err := reconnected.WriteJSON(Command{Type: "characterWatch", CharacterID: characterID, CharacterWatch: 1}); err != nil {
		t.Fatal(err)
	}
	snapshot := decodeCharacterSnapshot(t, read(t, reconnected, "characterSnapshot"))
	if snapshot.Effective.Stats["hp"].Integer() != 3 || snapshot.Effective.Stats["dexterity"].Integer() != 18 {
		t.Fatalf("concurrent field patches were not both retained after reconnect: %#v", snapshot.Effective.Stats)
	}
	fixture.server.mu.Lock()
	instance := fixture.session.CharacterInstances[characterID]
	fixture.server.mu.Unlock()
	want := map[string]StatValue{"hp": IntegerStatValue(3), "dexterity": IntegerStatValue(18)}
	if !reflect.DeepEqual(instance.StatOverrides, want) {
		t.Fatalf("field patches replaced the override map: got=%#v want=%#v", instance.StatOverrides, want)
	}
}

func TestEpicAvatarUploadRejectsCharacterDeletedDuringWorker(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	gmWS := dialRaw(t, fixture.host.URL, fixture.gm)
	read(t, gmWS, "campaignSnapshot")
	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "acceptance-avatar", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["red"], CharacterName: "Temporary"})
	characterID := characterIDFromACK(t, read(t, gmWS, "ack"))

	started, release := make(chan struct{}), make(chan struct{})
	imageWorkerObserver = func(*exec.Cmd) func() {
		close(started)
		<-release
		return nil
	}
	defer func() { imageWorkerObserver = nil }()
	type uploadResult struct {
		status  int
		message string
	}
	result := make(chan uploadResult, 1)
	go func() {
		status, _, message := uploadAvatarRequest(t, fixture, fixture.alice, "characterId", characterID, pipelinePNG(t, 96, 96))
		result <- uploadResult{status: status, message: message}
	}()
	<-started
	writeCharacterCommand(t, gmWS, Command{Type: "characterUnlink", Client: "acceptance-avatar", Seq: 2, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["red"]})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	fixture.server.mu.Lock()
	_, exists := fixture.session.CharacterInstances[characterID]
	fixture.server.mu.Unlock()
	if exists {
		t.Fatal("transient character survived unlink before avatar worker completed")
	}
	close(release)
	upload := <-result
	if upload.status != http.StatusConflict {
		t.Fatalf("deleted character avatar upload: status=%d body=%s", upload.status, upload.message)
	}
	assertNoPartialAssets(t, fixture.server.root)
}
