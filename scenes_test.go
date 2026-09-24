package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCampaignScenesVisibilityAndIndependentRevision(t *testing.T) {
	s := testServer(t, t.TempDir())
	tsrv := httptest.NewServer(s.routes())
	defer tsrv.Close()
	gm := post(t, tsrv.URL+"/api/sessions", map[string]string{"name": "Campaign"})
	player := post(t, tsrv.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})
	gc := dial(t, tsrv.URL, gm)
	read(t, gc, "snapshot")
	pc := dial(t, tsrv.URL, player)
	snap := read(t, pc, "snapshot")
	var initial SceneMetadata
	json.Unmarshal(snap["scene"], &initial)
	if initial.ID == "" || !initial.Published {
		t.Fatal("published scene missing")
	}
	name := "GM only"
	gc.WriteJSON(Command{Type: "sceneCreate", Client: "gm", Seq: 1, SceneName: name})
	read(t, gc, "campaignSnapshot")
	read(t, gc, "ack")
	pc.WriteJSON(Command{Type: "sync"})
	home := read(t, pc, "snapshot")
	if string(home["scene"]) == "" {
		t.Fatal("scene snapshot missing")
	}
	s.mu.Lock()
	var hidden string
	for id, scene := range s.sessions[gm["session"]].Scenes {
		if scene.Name == name {
			hidden = id
		}
	}
	s.mu.Unlock()
	pc.WriteJSON(Command{Type: "subscribe", SceneID: hidden})
	errMsg := read(t, pc, "error")
	if string(errMsg["message"]) == "" {
		t.Fatal("unpublished scene opened")
	}
	gc.WriteJSON(Command{Type: "subscribe", SceneID: hidden})
	hiddenSnap := read(t, gc, "snapshot")
	if string(hiddenSnap["scene"]) == "" {
		t.Fatal("GM scene snapshot missing")
	}
	gc.WriteJSON(Command{Type: "sceneUpdate", SceneID: hidden, Published: func() *bool { v := true; return &v }(), Client: "gm", Seq: 2})
	read(t, gc, "campaignSnapshot")
	read(t, gc, "ack")
	pc.WriteJSON(Command{Type: "subscribe", SceneID: hidden})
	read(t, pc, "snapshot")
}

func TestCampaignHomeContainsOnlySceneSummaries(t *testing.T) {
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Campaign"})
	s.mu.Lock()
	ss := s.sessions[gm["session"]]
	scene := firstScene(ss)
	asset := Asset{ID: "secret-asset", Width: 10, Height: 10, Levels: 1, Kind: "token"}
	ss.Assets[asset.ID] = asset
	scene.Revision = 42
	scene.Tokens["secret-token"] = Token{ID: "secret-token", Name: "Secret", Size: 80, Asset: asset.ID}
	s.mu.Unlock()

	ws := dialRaw(t, ts.URL, gm)
	home := read(t, ws, "campaignSnapshot")
	encoded, err := json.Marshal(home)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"secret-asset", "secret-token", `"revision"`, `"tokens"`, `"map"`, `"assets"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("campaign home leaked %s: %s", forbidden, text)
		}
	}
}

func TestSceneStateAssetsAndEventsAreIsolated(t *testing.T) {
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Campaign"})
	gc := dial(t, ts.URL, gm)
	first := read(t, gc, "snapshot")
	var firstMeta SceneMetadata
	json.Unmarshal(first["scene"], &firstMeta)

	gc.WriteJSON(Command{Type: "sceneCreate", Client: "scenes", Seq: 1, SceneName: "Other"})
	created := read(t, gc, "campaignSnapshot")
	read(t, gc, "ack")
	var summaries []SceneSummary
	json.Unmarshal(created["scenes"], &summaries)
	var otherID string
	for _, summary := range summaries {
		if summary.Name == "Other" {
			otherID = summary.ID
		}
	}
	if otherID == "" {
		t.Fatal("created scene missing")
	}
	subscribeSceneLoaded(t, gc, otherID)
	gc.WriteJSON(Command{Type: "create", SceneID: otherID, Token: Token{Name: "Only other", Size: 80}})
	createdEvent := read(t, gc, "upsert")
	var eventSceneID string
	json.Unmarshal(createdEvent["sceneId"], &eventSceneID)
	if eventSceneID != otherID {
		t.Fatal("scene event has no stable scene identity")
	}
	createdToken := tokenFrom(t, createdEvent)

	firstAgain := subscribeSceneLoaded(t, gc, firstMeta.ID)
	var tokens map[string]Token
	json.Unmarshal(firstAgain["tokens"], &tokens)
	if _, leaked := tokens[createdToken.ID]; leaked {
		t.Fatal("token leaked between scenes")
	}
	s.mu.Lock()
	ss := s.sessions[gm["session"]]
	if ss.Scenes[firstMeta.ID].Revision != firstMeta.Revision || ss.Scenes[otherID].Revision != 1 {
		t.Fatal("scene revisions are not independent")
	}
	s.mu.Unlock()
}

func TestClientsCanWorkInDifferentScenes(t *testing.T) {
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Campaign"})
	player := post(t, ts.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})

	gc := dialRaw(t, ts.URL, gm)
	gmHome := read(t, gc, "campaignSnapshot")
	var initial []SceneSummary
	json.Unmarshal(gmHome["scenes"], &initial)
	firstID := initial[0].ID
	gc.WriteJSON(Command{Type: "sceneCreate", Client: "split-scenes", Seq: 1, SceneName: "GM workshop"})
	gmHome = read(t, gc, "campaignSnapshot")
	read(t, gc, "ack")
	var all []SceneSummary
	json.Unmarshal(gmHome["scenes"], &all)
	var hiddenID string
	for _, scene := range all {
		if scene.Name == "GM workshop" {
			hiddenID = scene.ID
		}
	}
	subscribeSceneLoaded(t, gc, hiddenID)

	pc := dialRaw(t, ts.URL, player)
	playerHome := read(t, pc, "campaignSnapshot")
	if strings.Contains(string(playerHome["scenes"]), hiddenID) {
		t.Fatal("hidden scene leaked to player")
	}
	playerScene := subscribeSceneLoaded(t, pc, firstID)
	var who Member
	json.Unmarshal(playerScene["you"], &who)

	gc.WriteJSON(Command{Type: "create", SceneID: hiddenID, Token: Token{Name: "GM only", Size: 80}})
	read(t, gc, "upsert")
	gc.WriteJSON(Command{Type: "create", SceneID: firstID, Token: Token{Name: "Player token", Size: 80, Owner: who.ID}})
	playerToken := tokenFrom(t, read(t, pc, "upsert"))
	pc.WriteJSON(Command{Type: "move", SceneID: firstID, Token: Token{ID: playerToken.ID, X: 123, Y: 456}})
	read(t, pc, "move")

	gc.WriteJSON(Command{Type: "sync"})
	hiddenSnapshot := read(t, gc, "snapshot")
	var hiddenTokens map[string]Token
	json.Unmarshal(hiddenSnapshot["tokens"], &hiddenTokens)
	if len(hiddenTokens) != 1 || hiddenTokens[playerToken.ID].ID != "" {
		t.Fatalf("states from different scene subscriptions mixed: %#v", hiddenTokens)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[gm["session"]].Scenes[firstID].Tokens[playerToken.ID].X != 123 {
		t.Fatal("player work in published scene was lost")
	}
}

func TestSceneSnapshotDiscoversOnlyRequiredAssets(t *testing.T) {
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Campaign"})
	player := post(t, ts.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})
	s.mu.Lock()
	ss := s.sessions[gm["session"]]
	scene := firstScene(ss)
	for _, assetID := range []string{"visible", "hidden", "unused"} {
		ss.Assets[assetID] = Asset{ID: assetID, Width: 32, Height: 32, Kind: "token"}
	}
	scene.Tokens["visible"] = Token{ID: "visible", Name: "Visible", Size: 80, Asset: "visible"}
	scene.Tokens["hidden"] = Token{ID: "hidden", Name: "Hidden", Size: 80, Asset: "hidden", Hidden: true}
	s.mu.Unlock()

	gc := dial(t, ts.URL, gm)
	gmSnapshot := read(t, gc, "snapshot")
	var gmAssets map[string]Asset
	json.Unmarshal(gmSnapshot["assets"], &gmAssets)
	if len(gmAssets) != 2 || gmAssets["unused"].ID != "" {
		t.Fatalf("GM received campaign catalog: %#v", gmAssets)
	}
	pc := dial(t, ts.URL, player)
	playerSnapshot := read(t, pc, "snapshot")
	var playerAssets map[string]Asset
	json.Unmarshal(playerSnapshot["assets"], &playerAssets)
	if len(playerAssets) != 1 || playerAssets["visible"].ID == "" || playerAssets["hidden"].ID != "" {
		t.Fatalf("player asset discovery leaked hidden state: %#v", playerAssets)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/asset/hidden/token.png?session="+gm["session"]+"&scene="+scene.ID, nil)
	req.Header.Set("Authorization", "Bearer "+player["key"])
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("hidden asset returned %s", res.Status)
	}
}

func TestUnpublishAndDeleteReturnSubscribersHome(t *testing.T) {
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Campaign"})
	player := post(t, ts.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})
	gc := dial(t, ts.URL, gm)
	read(t, gc, "snapshot")
	pc := dial(t, ts.URL, player)
	read(t, pc, "snapshot")

	gc.WriteJSON(Command{Type: "sceneCreate", Client: "scene-admin", Seq: 1, SceneName: "Temporary"})
	home := read(t, gc, "campaignSnapshot")
	read(t, gc, "ack")
	var scenes []SceneSummary
	json.Unmarshal(home["scenes"], &scenes)
	var sceneID string
	for _, scene := range scenes {
		if scene.Name == "Temporary" {
			sceneID = scene.ID
		}
	}
	if sceneID == "" {
		t.Fatal("created scene missing")
	}
	s.mu.Lock()
	ss := s.sessions[gm["session"]]
	ss.Assets["shared"] = Asset{ID: "shared", Width: 32, Height: 32, Kind: "token"}
	ss.Scenes[sceneID].Tokens["shared"] = Token{ID: "shared", Name: "Shared", Size: 80, Asset: "shared"}
	s.mu.Unlock()
	published := true
	gc.WriteJSON(Command{Type: "sceneUpdate", Client: "scene-admin", Seq: 2, SceneID: sceneID, Published: &published})
	read(t, gc, "campaignSnapshot")
	read(t, gc, "ack")
	pc.WriteJSON(Command{Type: "subscribe", SceneID: sceneID})
	read(t, pc, "snapshot")

	published = false
	gc.WriteJSON(Command{Type: "sceneUpdate", Client: "scene-admin", Seq: 3, SceneID: sceneID, Published: &published})
	playerHome := read(t, pc, "campaignSnapshot")
	read(t, gc, "campaignSnapshot")
	read(t, gc, "ack")
	if strings.Contains(string(playerHome["scenes"]), sceneID) {
		t.Fatal("unpublished scene leaked in player home")
	}

	published = true
	gc.WriteJSON(Command{Type: "sceneUpdate", Client: "scene-admin", Seq: 4, SceneID: sceneID, Published: &published})
	read(t, pc, "campaignSnapshot")
	read(t, gc, "campaignSnapshot")
	read(t, gc, "ack")
	pc.WriteJSON(Command{Type: "subscribe", SceneID: sceneID})
	read(t, pc, "snapshot")
	gc.WriteJSON(Command{Type: "subscribe", SceneID: sceneID})
	read(t, gc, "snapshot")
	gc.WriteJSON(Command{Type: "sceneDelete", Client: "scene-admin", Seq: 5, SceneID: sceneID})
	playerDeletedHome := read(t, pc, "campaignSnapshot")
	deletedHome := read(t, gc, "campaignSnapshot")
	read(t, gc, "ack")
	if strings.Contains(string(deletedHome["scenes"]), sceneID) || strings.Contains(string(playerDeletedHome["scenes"]), sceneID) {
		t.Fatal("deleted scene remains in campaign home")
	}
	s.mu.Lock()
	_, assetKept := s.sessions[gm["session"]].Assets["shared"]
	s.mu.Unlock()
	if !assetKept {
		t.Fatal("scene deletion removed campaign asset")
	}
}

func TestLegacySingleSceneSaveIsRejected(t *testing.T) {
	root := t.TempDir()
	legacy := `{"old":{"id":"old","name":"Old","revision":1,"tokens":{},"members":{"gm":{"id":"gm","name":"GM","role":"gm"}},"assets":{},"keys":{"key":"gm"}}}`
	if err := os.WriteFile(filepath.Join(root, "sessions.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newServer(root); err == nil {
		t.Fatal("legacy save was silently migrated")
	}
}

func TestCampaignCanHaveNoScenes(t *testing.T) {
	root := t.TempDir()
	s := testServer(t, root)
	ts := httptest.NewServer(s.routes())
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Empty campaign"})
	gc := dialRaw(t, ts.URL, gm)
	home := read(t, gc, "campaignSnapshot")
	var scenes []SceneSummary
	json.Unmarshal(home["scenes"], &scenes)
	gc.WriteJSON(Command{Type: "sceneDelete", Client: "delete-last", Seq: 1, SceneID: scenes[0].ID})
	home = read(t, gc, "campaignSnapshot")
	read(t, gc, "ack")
	json.Unmarshal(home["scenes"], &scenes)
	if len(scenes) != 0 {
		t.Fatal("last scene was not deleted")
	}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := newServer(root); err != nil {
		t.Fatalf("empty campaign did not survive restart: %v", err)
	}
}

func TestSceneCommandReceiptSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	s := testServer(t, root)
	ts := httptest.NewServer(s.routes())
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Campaign"})
	gc := dial(t, ts.URL, gm)
	read(t, gc, "snapshot")
	command := Command{Type: "sceneCreate", Client: "durable-scenes", Seq: 1, SceneName: "Stable"}
	gc.WriteJSON(command)
	read(t, gc, "campaignSnapshot")
	read(t, gc, "ack")
	gc.Close()
	ts.Close()

	restarted := testServer(t, root)
	ts2 := httptest.NewServer(restarted.routes())
	defer ts2.Close()
	gc2 := dial(t, ts2.URL, gm)
	read(t, gc2, "snapshot")
	gc2.WriteJSON(command)
	read(t, gc2, "ack")
	restarted.mu.Lock()
	defer restarted.mu.Unlock()
	count := 0
	for _, scene := range restarted.sessions[gm["session"]].Scenes {
		if scene.Name == "Stable" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("retry created %d scenes", count)
	}
}

func TestHiddenTokenEventsDoNotRevealIdentifiersOrCreateDeliveryGaps(t *testing.T) {
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Hidden events"})
	player := post(t, ts.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})
	gc := dial(t, ts.URL, gm)
	read(t, gc, "snapshot")
	pc := dial(t, ts.URL, player)
	initial := read(t, pc, "snapshot")
	var delivery uint64
	json.Unmarshal(initial["delivery"], &delivery)
	if delivery != 0 {
		t.Fatalf("initial delivery = %d", delivery)
	}

	gc.WriteJSON(Command{Type: "create", Token: Token{Name: "Hidden", Size: 80, Hidden: true}})
	hidden := tokenFrom(t, read(t, gc, "upsert"))
	gc.WriteJSON(Command{Type: "create", Token: Token{Name: "Visible", Size: 80}})
	visible := tokenFrom(t, read(t, gc, "upsert"))
	playerEvent := read(t, pc, "upsert")
	if strings.Contains(fmt.Sprint(playerEvent), hidden.ID) {
		t.Fatal("hidden token identifier leaked")
	}
	json.Unmarshal(playerEvent["delivery"], &delivery)
	if delivery != 1 || tokenFrom(t, playerEvent).ID != visible.ID {
		t.Fatalf("filtered event created delivery gap: delivery=%d event=%v", delivery, playerEvent)
	}

	renamed := "Still hidden"
	gc.WriteJSON(Command{Type: "properties", Token: Token{ID: hidden.ID}, Properties: Properties{Name: &renamed}})
	read(t, gc, "upsert")
	hide := true
	gc.WriteJSON(Command{Type: "properties", Token: Token{ID: visible.ID}, Properties: Properties{Hidden: &hide}})
	read(t, gc, "upsert")
	deleted := read(t, pc, "delete")
	json.Unmarshal(deleted["delivery"], &delivery)
	if delivery != 2 || string(deleted["id"]) != `"`+visible.ID+`"` {
		t.Fatalf("visible to hidden transition was not delivered: %v", deleted)
	}

	gc.WriteJSON(Command{Type: "properties", Token: Token{ID: visible.ID}, Properties: Properties{Name: &renamed}})
	read(t, gc, "upsert")
	hide = false
	gc.WriteJSON(Command{Type: "properties", Token: Token{ID: visible.ID}, Properties: Properties{Hidden: &hide}})
	read(t, gc, "upsert")
	revealed := read(t, pc, "upsert")
	json.Unmarshal(revealed["delivery"], &delivery)
	if delivery != 3 || tokenFrom(t, revealed).ID != visible.ID {
		t.Fatalf("hidden events disturbed delivery stream: %v", revealed)
	}
}

func TestRegionSnapshotLoadsOnlyRequestedStateAndAssets(t *testing.T) {
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Regions"})
	player := post(t, ts.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})

	s.mu.Lock()
	ss := s.sessions[gm["session"]]
	scene := firstScene(ss)
	for _, assetID := range []string{"near", "far", "hidden"} {
		ss.Assets[assetID] = Asset{ID: assetID, Width: 64, Height: 64, Kind: "token"}
	}
	scene.Tokens["near"] = Token{ID: "near", Name: "Near", X: 100, Y: 100, Size: 80, Asset: "near"}
	scene.Tokens["far"] = Token{ID: "far", Name: "Far", X: 20000, Y: 20000, Size: 80, Asset: "far"}
	scene.Tokens["hidden"] = Token{ID: "hidden", Name: "Hidden", X: 150, Y: 150, Size: 80, Asset: "hidden", Hidden: true}
	scene.Revision++
	scene.rebuildRuntime()
	s.mu.Unlock()

	pc := dialRaw(t, ts.URL, player)
	home := read(t, pc, "campaignSnapshot")
	var summaries []SceneSummary
	json.Unmarshal(home["scenes"], &summaries)
	pc.WriteJSON(Command{Type: "subscribe", SceneID: summaries[0].ID})
	initial := read(t, pc, "snapshot")
	var initialTokens map[string]Token
	json.Unmarshal(initial["tokens"], &initialTokens)
	if len(initialTokens) != 0 || string(initial["region"]) != "null" {
		t.Fatalf("scene open eagerly loaded state: tokens=%d region=%s", len(initialTokens), initial["region"])
	}
	var metadata SceneMetadata
	json.Unmarshal(initial["scene"], &metadata)

	region := SceneRegion{Left: -500, Top: -500, Right: 1000, Bottom: 1000}
	pc.WriteJSON(Command{Type: "view", SceneID: metadata.ID, Region: &region})
	loaded := read(t, pc, "snapshot")
	var tokens map[string]Token
	var assets map[string]Asset
	json.Unmarshal(loaded["tokens"], &tokens)
	json.Unmarshal(loaded["assets"], &assets)
	if len(tokens) != 1 || tokens["near"].ID == "" || tokens["far"].ID != "" || tokens["hidden"].ID != "" {
		t.Fatalf("region snapshot leaked unrelated state: %#v", tokens)
	}
	if len(assets) != 1 || assets["near"].ID == "" || assets["far"].ID != "" || assets["hidden"].ID != "" {
		t.Fatalf("region snapshot leaked unrelated asset metadata: %#v", assets)
	}

	tooWide := SceneRegion{Left: 0, Top: 0, Right: maxSceneRegionSpan + 1, Bottom: 100}
	pc.WriteJSON(Command{Type: "view", SceneID: metadata.ID, Region: &tooWide})
	read(t, pc, "error")
}

func TestRegionRealtimeTracksObjectsEnteringAndLeavingLoadedArea(t *testing.T) {
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Region realtime"})

	observer := dialRaw(t, ts.URL, gm)
	home := read(t, observer, "campaignSnapshot")
	var summaries []SceneSummary
	json.Unmarshal(home["scenes"], &summaries)
	observer.WriteJSON(Command{Type: "subscribe", SceneID: summaries[0].ID})
	initial := read(t, observer, "snapshot")
	var metadata SceneMetadata
	json.Unmarshal(initial["scene"], &metadata)
	region := SceneRegion{Left: -500, Top: -500, Right: 1000, Bottom: 1000}
	observer.WriteJSON(Command{Type: "view", SceneID: metadata.ID, Region: &region})
	read(t, observer, "snapshot")

	writer := dial(t, ts.URL, gm)
	read(t, writer, "snapshot")
	writer.WriteJSON(Command{Type: "create", Token: Token{Name: "Mover", X: 100, Y: 100, Size: 80}})
	created := read(t, writer, "upsert")
	tok := tokenFrom(t, created)
	entered := read(t, observer, "upsert")
	var delivery uint64
	json.Unmarshal(entered["delivery"], &delivery)
	if delivery != 1 || tokenFrom(t, entered).ID != tok.ID {
		t.Fatalf("region create delivery mismatch: %#v", entered)
	}

	writer.WriteJSON(Command{Type: "move", Token: Token{ID: tok.ID, X: 5000, Y: 5000}})
	read(t, writer, "move")
	left := read(t, observer, "delete")
	json.Unmarshal(left["delivery"], &delivery)
	if delivery != 2 {
		t.Fatalf("leaving region delivery=%d", delivery)
	}

	// A move fully outside the loaded region is filtered and must not consume a
	// delivery number. sync returns the same delivery sequence for this peer.
	writer.WriteJSON(Command{Type: "move", Token: Token{ID: tok.ID, X: 6000, Y: 6000}})
	read(t, writer, "move")
	observer.WriteJSON(Command{Type: "sync"})
	synced := read(t, observer, "snapshot")
	json.Unmarshal(synced["delivery"], &delivery)
	if delivery != 2 {
		t.Fatalf("filtered off-region event consumed delivery: %d", delivery)
	}

	writer.WriteJSON(Command{Type: "move", Token: Token{ID: tok.ID, X: 200, Y: 200}})
	read(t, writer, "move")
	reentered := read(t, observer, "upsert")
	json.Unmarshal(reentered["delivery"], &delivery)
	if delivery != 3 || tokenFrom(t, reentered).ID != tok.ID {
		t.Fatalf("re-entering region was not full upsert: %#v", reentered)
	}
}

func TestSceneEntryPointUsesVisibleOwnedToken(t *testing.T) {
	player := &Member{ID: "player", Role: "player"}
	scene := &Scene{Tokens: map[string]Token{
		"other":  {ID: "other", Owner: "someone-else", X: 10, Y: 20, Size: 80},
		"hidden": {ID: "hidden", Owner: player.ID, X: 30, Y: 40, Size: 80, Hidden: true},
		"owned":  {ID: "owned", Owner: player.ID, X: 5000, Y: 6000, Size: 80},
	}}
	entry := sceneEntryPoint(scene, player)
	if entry == nil || entry.X != 5000 || entry.Y != 6000 {
		t.Fatalf("entry point did not use visible owned token: %#v", entry)
	}
	delete(scene.Tokens, "owned")
	if entry := sceneEntryPoint(scene, player); entry != nil {
		t.Fatalf("hidden owned token leaked entry point: %#v", entry)
	}
}

func TestSceneSpatialRuntimeScalesByRegion(t *testing.T) {
	scene := &Scene{ID: "large", Name: "Large", Revision: 1, Tokens: map[string]Token{}}
	for i := 0; i < 50000; i++ {
		id := fmt.Sprintf("t-%05d", i)
		scene.Tokens[id] = Token{ID: id, X: float64((i % 500) * 100), Y: float64((i / 500) * 100), Size: 40}
	}
	rt := scene.ensureRuntime()
	got := rt.query(SceneRegion{Left: 0, Top: 0, Right: 500, Bottom: 500})
	if len(got) == 0 || len(got) > 100 {
		t.Fatalf("small viewport returned unexpected object count: %d", len(got))
	}
	if rt.indexed != 50000 {
		t.Fatalf("spatial runtime lost entries: %d", rt.indexed)
	}
}

func TestSceneRuntimeUpdatesSpatialAndAssetReferences(t *testing.T) {
	old := Token{ID: "token", X: 100, Y: 100, Size: 80, Asset: "portrait"}
	scene := &Scene{ID: "runtime", Name: "Runtime", Revision: 1, Tokens: map[string]Token{old.ID: old}}
	rt := scene.ensureRuntime()
	if rt.assetPublic["portrait"] != 1 || len(rt.query(SceneRegion{Left: 0, Top: 0, Right: 500, Bottom: 500})) != 1 {
		t.Fatal("initial runtime index is incomplete")
	}

	next := old
	next.X, next.Y, next.Hidden = 5000, 5000, true
	scene.Tokens[next.ID] = next
	scene.Revision++
	scene.applyTokenRuntimeChange(old, true, next, true)
	if scene.runtime.assetPublic["portrait"] != 0 || scene.runtime.assetAll["portrait"] != 1 {
		t.Fatal("visibility asset references were not updated")
	}
	if len(scene.runtime.query(SceneRegion{Left: 0, Top: 0, Right: 500, Bottom: 500})) != 0 || len(scene.runtime.query(SceneRegion{Left: 4500, Top: 4500, Right: 5500, Bottom: 5500})) != 1 {
		t.Fatal("spatial move left stale cells")
	}

	delete(scene.Tokens, next.ID)
	scene.Revision++
	scene.applyTokenRuntimeChange(next, true, Token{}, false)
	if scene.runtime.assetAll["portrait"] != 0 || scene.runtime.indexed != 0 {
		t.Fatal("delete left runtime references")
	}
}
