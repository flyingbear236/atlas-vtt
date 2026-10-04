package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSharedTokenOwnershipPermissionsAndRollback(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	defer host.Close()

	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Shared ownership"})
	alice := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Alice"})
	bob := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Bob"})
	charlie := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Charlie"})

	server.mu.Lock()
	session := server.sessions[gm["session"]]
	aliceID := session.Keys[alice["key"]]
	bobID := session.Keys[bob["key"]]
	charlieID := session.Keys[charlie["key"]]
	server.mu.Unlock()

	gmWS := dial(t, host.URL, gm)
	read(t, gmWS, "snapshot")
	aliceWS := dial(t, host.URL, alice)
	read(t, aliceWS, "snapshot")
	bobWS := dial(t, host.URL, bob)
	read(t, bobWS, "snapshot")
	charlieWS := dial(t, host.URL, charlie)
	read(t, charlieWS, "snapshot")

	gmWS.WriteJSON(Command{Type: "create", Token: Token{Name: "Shared", Size: 80, OwnerIDs: []string{aliceID, bobID, aliceID}}})
	token := tokenFrom(t, read(t, gmWS, "upsert"))
	if !slices.Equal(token.OwnerIDs, []string{aliceID, bobID}) {
		t.Fatalf("owners were not stably deduplicated: %v", token.OwnerIDs)
	}

	aliceWS.WriteJSON(Command{Type: "move", Token: Token{ID: token.ID, X: 10, Y: 20}})
	read(t, aliceWS, "move")
	bobWS.WriteJSON(Command{Type: "move", Token: Token{ID: token.ID, X: 30, Y: 40}})
	read(t, bobWS, "move")
	charlieWS.WriteJSON(Command{Type: "move", Token: Token{ID: token.ID, X: 50, Y: 60}})
	read(t, charlieWS, "error")
	gmWS.WriteJSON(Command{Type: "move", Token: Token{ID: token.ID, X: 70, Y: 80}})
	read(t, gmWS, "move")

	playerOwners := []string{charlieID}
	aliceWS.WriteJSON(Command{Type: "properties", Token: Token{ID: token.ID}, Properties: Properties{OwnerIDs: &playerOwners}})
	read(t, aliceWS, "error")

	unknown := []string{"missing-member"}
	gmWS.WriteJSON(Command{Type: "properties", Token: Token{ID: token.ID}, Properties: Properties{OwnerIDs: &unknown}})
	read(t, gmWS, "error")
	server.mu.Lock()
	if got := firstScene(session).Tokens[token.ID].OwnerIDs; !slices.Equal(got, []string{aliceID, bobID}) {
		server.mu.Unlock()
		t.Fatalf("rejected owners mutated token: %v", got)
	}
	server.mu.Unlock()

	blocker := filepath.Join(root, "sessions.json.tmp")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	failedOwners := []string{bobID}
	gmWS.WriteJSON(Command{Type: "properties", Client: "owners", Seq: 1, Token: Token{ID: token.ID}, Properties: Properties{OwnerIDs: &failedOwners}})
	read(t, gmWS, "saveError")
	rollback := read(t, gmWS, "snapshot")
	var tokens map[string]Token
	if err := json.Unmarshal(rollback["tokens"], &tokens); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tokens[token.ID].OwnerIDs, []string{aliceID, bobID}) {
		t.Fatalf("failed save did not restore independent owner slice: %v", tokens[token.ID].OwnerIDs)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}

	empty := []string{}
	gmWS.WriteJSON(Command{Type: "properties", Token: Token{ID: token.ID}, Properties: Properties{OwnerIDs: &empty}})
	read(t, gmWS, "upsert")
	aliceWS.WriteJSON(Command{Type: "move", Token: Token{ID: token.ID, X: 90, Y: 100}})
	read(t, aliceWS, "error")
	gmWS.WriteJSON(Command{Type: "move", Token: Token{ID: token.ID, X: 110, Y: 120}})
	read(t, gmWS, "move")
}

func TestLegacyOwnerMigrationAndExplicitOwnerIDsPrecedence(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Legacy owners"})
	alice := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Alice"})
	server.mu.Lock()
	aliceID := server.sessions[gm["session"]].Keys[alice["key"]]
	server.mu.Unlock()
	gmWS := dial(t, host.URL, gm)
	read(t, gmWS, "snapshot")
	gmWS.WriteJSON(Command{Type: "create", Client: "legacy-create", Seq: 1, Token: Token{Name: "Legacy", Size: 80}})
	legacyID := tokenFrom(t, read(t, gmWS, "upsert")).ID
	read(t, gmWS, "ack")
	gmWS.WriteJSON(Command{Type: "create", Client: "current-create", Seq: 1, Token: Token{Name: "Current", Size: 80}})
	currentID := tokenFrom(t, read(t, gmWS, "upsert")).ID
	read(t, gmWS, "ack")
	gmWS.Close()
	host.Close()

	path := filepath.Join(root, "sessions.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(original, &saved); err != nil {
		t.Fatal(err)
	}
	sessionJSON := saved[gm["session"]].(map[string]any)
	for _, sceneValue := range sessionJSON["scenes"].(map[string]any) {
		tokens := sceneValue.(map[string]any)["tokens"].(map[string]any)
		legacy := tokens[legacyID].(map[string]any)
		delete(legacy, "ownerIds")
		delete(legacy, "opacity")
		legacy["owner"] = aliceID
		current := tokens[currentID].(map[string]any)
		current["opacity"] = 0.0
		current["ownerIds"] = []any{}
		current["owner"] = aliceID
	}
	modified, _ := json.Marshal(saved)
	if err := os.WriteFile(path, modified, 0600); err != nil {
		t.Fatal(err)
	}

	restarted, err := newServer(root)
	if err != nil {
		t.Fatal(err)
	}
	scene := firstScene(restarted.sessions[gm["session"]])
	if !slices.Equal(scene.Tokens[legacyID].OwnerIDs, []string{aliceID}) {
		t.Fatalf("legacy owner was not migrated: %v", scene.Tokens[legacyID].OwnerIDs)
	}
	if len(scene.Tokens[currentID].OwnerIDs) != 0 {
		t.Fatalf("explicit ownerIds did not win over legacy owner: %v", scene.Tokens[currentID].OwnerIDs)
	}
	if scene.Tokens[legacyID].Opacity != 1 {
		t.Fatalf("legacy token opacity = %v, want 1", scene.Tokens[legacyID].Opacity)
	}
	if scene.Tokens[currentID].Opacity != 0 {
		t.Fatalf("explicit zero opacity was lost: %v", scene.Tokens[currentID].Opacity)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte(`"owner":`)) || !bytes.Contains(persisted, []byte(`"ownerIds":[]`)) || !bytes.Contains(persisted, []byte(`"opacity":1`)) || !bytes.Contains(persisted, []byte(`"opacity":0`)) {
		t.Fatalf("migration was not persisted in canonical ownership form: %s", persisted)
	}
	secondRestart, err := newServer(root)
	if err != nil {
		t.Fatal(err)
	}
	secondScene := firstScene(secondRestart.sessions[gm["session"]])
	if !slices.Equal(secondScene.Tokens[legacyID].OwnerIDs, []string{aliceID}) || len(secondScene.Tokens[currentID].OwnerIDs) != 0 {
		t.Fatal("canonical ownership migration did not survive a second restart")
	}
}

func TestUnknownSavedOwnerIsRejectedWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Invalid owner"})
	gmWS := dial(t, host.URL, gm)
	read(t, gmWS, "snapshot")
	gmWS.WriteJSON(Command{Type: "create", Client: "invalid-create", Seq: 1, Token: Token{Name: "Invalid", Size: 80}})
	tokenID := tokenFrom(t, read(t, gmWS, "upsert")).ID
	read(t, gmWS, "ack")
	gmWS.Close()
	host.Close()

	path := filepath.Join(root, "sessions.json")
	var saved map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	for _, sceneValue := range saved[gm["session"]].(map[string]any)["scenes"].(map[string]any) {
		token := sceneValue.(map[string]any)["tokens"].(map[string]any)[tokenID].(map[string]any)
		token["ownerIds"] = []any{"missing-member"}
	}
	corrupted, _ := json.Marshal(saved)
	if err := os.WriteFile(path, corrupted, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newServer(root); err == nil {
		t.Fatal("saved token with unknown owner was accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, corrupted) {
		t.Fatal("corrupted save was overwritten")
	}
}

func TestCharacterAccessUsesPublishedVisibleOwnership(t *testing.T) {
	gm := &Member{ID: "gm", Role: "gm"}
	alice := &Member{ID: "alice", Role: "player"}
	charlie := &Member{ID: "charlie", Role: "player"}
	token := Token{ID: "token", OwnerIDs: []string{alice.ID}, CharacterInstanceID: "character"}
	session := &Session{
		Members:            map[string]*Member{gm.ID: gm, alice.ID: alice, charlie.ID: charlie},
		CharacterInstances: map[string]CharacterInstance{"character": {ID: "character"}},
		Scenes:             map[string]*Scene{"scene": {ID: "scene", Published: true, Tokens: map[string]Token{token.ID: token}}},
	}
	rebuildCharacterReferences(session)
	if !memberCanAccessCharacter(session, alice, "character") || memberCanAccessCharacter(session, charlie, "character") || !memberCanAccessCharacter(session, gm, "character") {
		t.Fatal("visible published ownership produced wrong character access")
	}
	scene := session.Scenes["scene"]
	scene.Published = false
	if memberCanAccessCharacter(session, alice, "character") || !memberCanAccessCharacter(session, gm, "character") {
		t.Fatal("unpublished scene did not revoke only player character access")
	}
	scene.Published = true
	token.Hidden = true
	scene.Tokens[token.ID] = token
	if memberCanAccessCharacter(session, alice, "character") {
		t.Fatal("hidden token granted player character access")
	}
}
