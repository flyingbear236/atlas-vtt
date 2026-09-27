package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestGMElementCatalogIsCompleteButAssetsStaySpatial(t *testing.T) {
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Catalog"})
	player := post(t, ts.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})

	s.mu.Lock()
	ss := s.sessions[gm["session"]]
	scene := firstScene(ss)
	floorID := firstFloorID(scene)
	layerID := firstLayerID(scene, floorID)
	for _, assetID := range []string{"near-asset", "far-asset"} {
		ss.Assets[assetID] = Asset{ID: assetID, Kind: "scene", Width: 64, Height: 64, Levels: 1}
	}
	scene.Elements["near"] = SceneElement{ID: "near", Name: "Near", AssetID: "near-asset", FloorID: floorID, LayerID: layerID, Transform: Transform{X: 100, Y: 100, Width: 64, Height: 64}, Visible: true, Opacity: 1}
	scene.Elements["far"] = SceneElement{ID: "far", Name: "Far", AssetID: "far-asset", FloorID: floorID, LayerID: layerID, Transform: Transform{X: 20_000, Y: 20_000, Width: 64, Height: 64}, Visible: true, Opacity: 1}
	scene.rebuildRuntime()
	sceneID := scene.ID
	s.mu.Unlock()

	gmWS := dialRaw(t, ts.URL, gm)
	read(t, gmWS, "campaignSnapshot")
	gmWS.WriteJSON(Command{Type: "subscribe", SceneID: sceneID})
	initialGM := read(t, gmWS, "snapshot")
	var catalog, initialElements map[string]SceneElement
	var initialAssets map[string]Asset
	json.Unmarshal(initialGM["elementCatalog"], &catalog)
	json.Unmarshal(initialGM["elements"], &initialElements)
	json.Unmarshal(initialGM["assets"], &initialAssets)
	if len(catalog) != 2 || len(initialElements) != 0 || len(initialAssets) != 0 {
		t.Fatalf("initial editor state was not catalog-only: catalog=%d elements=%d assets=%d", len(catalog), len(initialElements), len(initialAssets))
	}
	if catalog["far"].AssetID != "" || catalog["near"].AssetID != "" {
		t.Fatal("catalog authorized offscreen asset references")
	}

	playerWS := dialRaw(t, ts.URL, player)
	read(t, playerWS, "campaignSnapshot")
	playerWS.WriteJSON(Command{Type: "subscribe", SceneID: sceneID})
	initialPlayer := read(t, playerWS, "snapshot")
	if _, leaked := initialPlayer["elementCatalog"]; leaked {
		t.Fatal("editor catalog leaked to player")
	}

	region := SceneRegion{Left: 0, Top: 0, Right: 1000, Bottom: 1000}
	gmWS.WriteJSON(Command{Type: "view", SceneID: sceneID, Region: &region})
	gmRegion := read(t, gmWS, "snapshot")
	if _, repeated := gmRegion["elementCatalog"]; repeated {
		t.Fatal("viewport refresh resent the full editor catalog")
	}
	playerWS.WriteJSON(Command{Type: "view", SceneID: sceneID, Region: &region})
	read(t, playerWS, "snapshot")

	name := "Far renamed"
	gmWS.WriteJSON(Command{Type: "elementUpdate", Client: "catalog", Seq: 1, SceneID: sceneID, Element: SceneElement{ID: "far"}, ElementProperties: ElementProperties{Name: &name}})
	catalogEvent := read(t, gmWS, "elementCatalogUpsert")
	var catalogUpdate SceneElement
	json.Unmarshal(catalogEvent["catalogElement"], &catalogUpdate)
	if catalogUpdate.Name != name || catalogUpdate.AssetID != "" {
		t.Fatalf("bad catalog update: %+v", catalogUpdate)
	}
	read(t, gmWS, "ack")
	playerWS.WriteJSON(Command{Type: "sync"})
	playerSync := read(t, playerWS, "snapshot")
	var delivery uint64
	json.Unmarshal(playerSync["delivery"], &delivery)
	if delivery != 0 {
		t.Fatalf("offscreen editor update reached player delivery stream: %d", delivery)
	}

	nearTransform := Transform{X: 200, Y: 200, Width: 64, Height: 64}
	gmWS.WriteJSON(Command{Type: "elementTransform", Client: "catalog-positions", Seq: 1, SceneID: sceneID, Element: SceneElement{ID: "far", Transform: nearTransform}})
	read(t, gmWS, "elementUpsert")
	entered := read(t, playerWS, "elementUpsert")
	var enteredElement SceneElement
	var enteredAsset Asset
	json.Unmarshal(entered["element"], &enteredElement)
	json.Unmarshal(entered["asset"], &enteredAsset)
	if enteredElement.ID != "far" || enteredElement.Transform != nearTransform || enteredAsset.ID != "far-asset" {
		t.Fatalf("element entering player viewport was incomplete: element=%+v asset=%+v", enteredElement, enteredAsset)
	}
	read(t, gmWS, "ack")
}

func TestCampaignSupportsMultipleGMFlags(t *testing.T) {
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Multiple GMs"})
	player := post(t, ts.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Second GM"})

	gmWS := dialRaw(t, ts.URL, gm)
	gmHome := read(t, gmWS, "campaignSnapshot")
	playerWS := dialRaw(t, ts.URL, player)
	playerHome := read(t, playerWS, "campaignSnapshot")
	var firstGM, secondGM Member
	json.Unmarshal(gmHome["you"], &firstGM)
	json.Unmarshal(playerHome["you"], &secondGM)

	promote := true
	gmWS.WriteJSON(Command{Type: "memberUpdate", Client: "members", Seq: 1, MemberID: secondGM.ID, GM: &promote})
	read(t, gmWS, "campaignSnapshot")
	read(t, gmWS, "ack")
	promotedHome := read(t, playerWS, "campaignSnapshot")
	var promoted Member
	json.Unmarshal(promotedHome["you"], &promoted)
	if !promoted.GM || promoted.Role != "gm" {
		t.Fatalf("member was not promoted: %+v", promoted)
	}

	playerWS.WriteJSON(Command{Type: "sceneCreate", Client: "second-gm", Seq: 1, SceneName: "Workshop"})
	read(t, playerWS, "campaignSnapshot")
	createdAck := read(t, playerWS, "ack")
	var issue string
	json.Unmarshal(createdAck["error"], &issue)
	if issue != "" {
		t.Fatalf("second GM could not administer campaign: %s", issue)
	}

	demote := false
	playerWS.WriteJSON(Command{Type: "memberUpdate", Client: "second-gm", Seq: 2, MemberID: firstGM.ID, GM: &demote})
	read(t, playerWS, "campaignSnapshot")
	read(t, playerWS, "ack")
	playerWS.WriteJSON(Command{Type: "memberUpdate", Client: "second-gm", Seq: 3, MemberID: secondGM.ID, GM: &demote})
	lastGMAck := read(t, playerWS, "ack")
	json.Unmarshal(lastGMAck["error"], &issue)
	if issue == "" {
		t.Fatal("server allowed removing the last GM")
	}
}
