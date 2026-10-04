package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestGMPlayerPreviewIsPerPeerFilteredAndMovementBound(t *testing.T) {
	server := testServer(t, t.TempDir())
	httpServer := httptest.NewServer(server.routes())
	defer httpServer.Close()

	gm := post(t, httpServer.URL+"/api/sessions", map[string]string{"name": "Player preview"})
	player := post(t, httpServer.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})

	server.mu.Lock()
	session := server.sessions[gm["session"]]
	scene := firstScene(session)
	floorID := firstFloorID(scene)
	floor := scene.Floors[floorID]
	walkable, err := rectanglePolygon(AABB{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100})
	if err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	renderBounds, err := rectanglePolygon(AABB{MinX: 0, MinY: 0, MaxX: 6000, MaxY: 6000})
	if err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	floor.WalkableMode = walkableModeRestricted
	floor.WalkableComponents = []WalkableComponent{{ID: "walkable", Polygon: walkable}}
	floor.RenderBounds = &renderBounds
	scene.Floors[floorID] = floor
	for _, assetID := range []string{"near-art", "far-art", "hidden-art", "public-background", "private-background"} {
		kind := assetKindToken
		if bytes.Contains([]byte(assetID), []byte("background")) {
			kind = assetKindScene
		}
		session.Assets[assetID] = Asset{ID: assetID, Width: 64, Height: 64, Kind: kind, RenderMode: renderModeBitmap}
	}
	tokenLayerID := layerIDByKind(scene, floorID, layerKindTokens)
	scene.Tokens["near"] = Token{ID: "near", Name: "Near", FloorID: floorID, LayerID: tokenLayerID, X: 20, Y: 20, Size: 40, Opacity: 1, Asset: "near-art"}
	scene.Tokens["far"] = Token{ID: "far", Name: "Far", FloorID: floorID, LayerID: tokenLayerID, X: 5000, Y: 5000, Size: 40, Opacity: 1, Asset: "far-art"}
	scene.Tokens["hidden"] = Token{ID: "hidden", Name: "Hidden", FloorID: floorID, LayerID: tokenLayerID, X: 5020, Y: 5020, Size: 40, Opacity: 1, Asset: "hidden-art", Hidden: true}
	visualLayerID := firstLayerID(scene, floorID)
	scene.Elements["public"] = SceneElement{ID: "public", FloorID: floorID, LayerID: visualLayerID, AssetID: "public-background", Transform: Transform{X: 10, Y: 10, Width: 20, Height: 20}, Visible: true, Opacity: 1}
	scene.Elements["private"] = SceneElement{ID: "private", FloorID: floorID, LayerID: visualLayerID, AssetID: "private-background", Transform: Transform{X: 4990, Y: 4990, Width: 40, Height: 40}, Visible: false, Opacity: 1}
	scene.Revision++
	scene.rebuildRuntime()
	server.mu.Unlock()

	preview := dialRaw(t, httpServer.URL, gm)
	read(t, preview, "campaignSnapshot")
	if err := preview.WriteJSON(Command{Type: "subscribe", SceneID: scene.ID, PreviewEnabled: boolPointer(true)}); err != nil {
		t.Fatal(err)
	}
	previewSnapshot := read(t, preview, "snapshot")
	var previewEnabled bool
	var previewRevision uint64
	var locators map[string]TokenLocator
	json.Unmarshal(previewSnapshot["playerPreview"], &previewEnabled)
	json.Unmarshal(previewSnapshot["previewRevision"], &previewRevision)
	json.Unmarshal(previewSnapshot["ownedTokens"], &locators)
	if !previewEnabled || previewRevision < 2 || len(locators) != 3 || !locators["hidden"].Hidden {
		t.Fatalf("preview context/locators mismatch: enabled=%v revision=%d locators=%#v", previewEnabled, previewRevision, locators)
	}
	if bytes.Contains(previewSnapshot["ownedTokens"], []byte("asset")) || bytes.Contains(previewSnapshot["ownedTokens"], []byte("characterInstanceId")) {
		t.Fatalf("preview locators disclosed heavy data: %s", previewSnapshot["ownedTokens"])
	}
	if string(previewSnapshot["tokens"]) != "{}" || string(previewSnapshot["assets"]) != "{}" {
		t.Fatalf("preview eagerly loaded scene content: tokens=%s assets=%s", previewSnapshot["tokens"], previewSnapshot["assets"])
	}
	server.mu.Lock()
	var previewPeer *peer
	for candidate := range server.peers {
		if candidate.session == session.ID && candidate.member.ID == session.Keys[gm["key"]] && candidate.playerPreview {
			previewPeer = candidate
			break
		}
	}
	if previewPeer == nil || !assetVisibleToPeer(session, previewPeer, "far-art") || assetVisibleToPeer(session, previewPeer, "hidden-art") || assetVisibleToPeer(session, previewPeer, "private-background") {
		server.mu.Unlock()
		t.Fatal("preview asset authorization does not match the player projection")
	}
	server.mu.Unlock()

	editor := dialRaw(t, httpServer.URL, gm)
	read(t, editor, "campaignSnapshot")
	if err := editor.WriteJSON(Command{Type: "subscribe", SceneID: scene.ID}); err != nil {
		t.Fatal(err)
	}
	editorSnapshot := read(t, editor, "snapshot")
	json.Unmarshal(editorSnapshot["playerPreview"], &previewEnabled)
	locators = nil
	json.Unmarshal(editorSnapshot["ownedTokens"], &locators)
	if previewEnabled || len(locators) != 0 || editorSnapshot["elementCatalog"] == nil {
		t.Fatalf("second GM peer inherited preview: enabled=%v locators=%#v catalog=%s", previewEnabled, locators, editorSnapshot["elementCatalog"])
	}

	playerWS := dialRaw(t, httpServer.URL, player)
	read(t, playerWS, "campaignSnapshot")
	if err := playerWS.WriteJSON(Command{Type: "subscribe", SceneID: scene.ID, PreviewEnabled: boolPointer(true)}); err != nil {
		t.Fatal(err)
	}
	denied := read(t, playerWS, "error")
	if !bytes.Contains(denied["operation"], []byte("playerPreview")) {
		t.Fatalf("player preview request was not rejected: %v", denied)
	}

	focusRegion := SceneRegion{Left: -100, Top: -100, Right: 100, Bottom: 100}
	if err := preview.WriteJSON(Command{Type: "activeToken", SceneID: scene.ID, ActiveTokenID: "hidden", Focus: true, Region: &focusRegion, PreviewRevision: previewRevision}); err != nil {
		t.Fatal(err)
	}
	hiddenSnapshot := read(t, preview, "snapshot")
	var activeTokenID string
	var loadedTokens map[string]Token
	var loadedAssets map[string]Asset
	json.Unmarshal(hiddenSnapshot["activeTokenId"], &activeTokenID)
	json.Unmarshal(hiddenSnapshot["tokens"], &loadedTokens)
	json.Unmarshal(hiddenSnapshot["assets"], &loadedAssets)
	if activeTokenID != "hidden" || loadedTokens["hidden"].ID != "" || loadedAssets["hidden-art"].ID != "" || loadedAssets["private-background"].ID != "" {
		t.Fatalf("hidden selection leaked player projection: active=%q tokens=%s assets=%s", activeTokenID, hiddenSnapshot["tokens"], hiddenSnapshot["assets"])
	}

	if err := preview.WriteJSON(Command{Type: "activeToken", SceneID: scene.ID, ActiveTokenID: "far", Focus: true, Region: &focusRegion, PreviewRevision: previewRevision}); err != nil {
		t.Fatal(err)
	}
	farSnapshot := read(t, preview, "snapshot")
	json.Unmarshal(farSnapshot["tokens"], &loadedTokens)
	json.Unmarshal(farSnapshot["assets"], &loadedAssets)
	if loadedTokens["far"].ID == "" || loadedAssets["far-art"].ID == "" || loadedTokens["near"].ID != "" || loadedAssets["near-art"].ID != "" || loadedAssets["private-background"].ID != "" {
		t.Fatalf("distant selection did not stay spatial/player-filtered: tokens=%s assets=%s", farSnapshot["tokens"], farSnapshot["assets"])
	}

	if err := preview.WriteJSON(Command{Type: "activeToken", SceneID: scene.ID, ActiveTokenID: "near", Focus: true, Region: &focusRegion, PreviewRevision: previewRevision}); err != nil {
		t.Fatal(err)
	}
	read(t, preview, "snapshot")
	if err := preview.WriteJSON(Command{Type: "move", SceneID: scene.ID, Token: Token{ID: "near", X: 150, Y: 20}, PreviewRevision: previewRevision}); err != nil {
		t.Fatal(err)
	}
	blocked := read(t, preview, "error")
	if !bytes.Contains(blocked["errorCode"], []byte(movementBlockedErrorCode)) {
		t.Fatalf("preview movement escaped walkable validation: %v", blocked)
	}

	if err := editor.WriteJSON(Command{Type: "move", SceneID: scene.ID, Token: Token{ID: "near", X: 150, Y: 20}}); err != nil {
		t.Fatal(err)
	}
	locatorMove := read(t, preview, "tokenLocatorMove")
	var locatorX float64
	json.Unmarshal(locatorMove["x"], &locatorX)
	if locatorX != 150 {
		t.Fatalf("preview locator did not follow an off-region move: %v", locatorX)
	}
	if err := editor.WriteJSON(Command{Type: "sync"}); err != nil {
		t.Fatal(err)
	}
	read(t, editor, "snapshot")
	server.mu.Lock()
	if got := scene.Tokens["near"].X; got != 150 {
		server.mu.Unlock()
		t.Fatalf("normal GM movement was constrained: x=%v", got)
	}
	server.mu.Unlock()

	if err := preview.WriteJSON(Command{Type: "playerPreview", SceneID: scene.ID, PreviewEnabled: boolPointer(false)}); err != nil {
		t.Fatal(err)
	}
	normalSnapshot := read(t, preview, "snapshot")
	var normalRevision uint64
	json.Unmarshal(normalSnapshot["previewRevision"], &normalRevision)
	if normalRevision <= previewRevision {
		t.Fatalf("preview generation did not advance: %d -> %d", previewRevision, normalRevision)
	}
	if err := preview.WriteJSON(Command{Type: "move", SceneID: scene.ID, Token: Token{ID: "near", X: 200, Y: 20}, PreviewRevision: previewRevision}); err != nil {
		t.Fatal(err)
	}
	stale := read(t, preview, "error")
	if !bytes.Contains(stale["errorCode"], []byte(previewContextChangedErrorCode)) {
		t.Fatalf("stale preview command was not rejected: %v", stale)
	}
	read(t, preview, "snapshot")
	server.mu.Lock()
	if got := scene.Tokens["near"].X; got != 150 {
		server.mu.Unlock()
		t.Fatalf("stale preview command changed token: x=%v", got)
	}
	server.mu.Unlock()
	if err := preview.WriteJSON(Command{Type: "final", Client: "stale-preview-final", Seq: 1, SceneID: scene.ID, Token: Token{ID: "near", X: 210, Y: 20}, PreviewRevision: previewRevision}); err != nil {
		t.Fatal(err)
	}
	staleFinal := read(t, preview, "ack")
	if !bytes.Contains(staleFinal["errorCode"], []byte(previewContextChangedErrorCode)) {
		t.Fatalf("stale final command was not rejected: %v", staleFinal)
	}
	if err := preview.WriteJSON(Command{Type: "final", Client: "stale-preview-final", Seq: 2, SceneID: scene.ID, Token: Token{ID: "near", X: 210, Y: 20}, PreviewRevision: normalRevision}); err != nil {
		t.Fatal(err)
	}
	acceptedFinal := read(t, preview, "ack")
	if string(acceptedFinal["error"]) != `""` {
		t.Fatalf("current normal-GM final command failed: %v", acceptedFinal)
	}
	server.mu.Lock()
	if got := scene.Tokens["near"].X; got != 210 {
		server.mu.Unlock()
		t.Fatalf("current projection final was not applied: x=%v", got)
	}
	server.mu.Unlock()

	reconnected := dialRaw(t, httpServer.URL, gm)
	read(t, reconnected, "campaignSnapshot")
	if err := reconnected.WriteJSON(Command{Type: "subscribe", SceneID: scene.ID, ActiveTokenID: "far", PreviewEnabled: boolPointer(true)}); err != nil {
		t.Fatal(err)
	}
	restored := read(t, reconnected, "snapshot")
	json.Unmarshal(restored["playerPreview"], &previewEnabled)
	json.Unmarshal(restored["activeTokenId"], &activeTokenID)
	if !previewEnabled || activeTokenID != "far" {
		t.Fatalf("explicit reconnect did not restore preview context: enabled=%v active=%q", previewEnabled, activeTokenID)
	}
}
