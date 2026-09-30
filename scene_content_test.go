package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFloorGeometryJSONRoundTrip(t *testing.T) {
	restricted := Floor{
		ID:                 "restricted",
		Name:               "Restricted",
		WalkableMode:       walkableModeRestricted,
		WalkableComponents: []WalkableComponent{},
		RenderBounds:       nil,
		GeometryRevision:   4,
	}
	unrestricted := Floor{ID: "unrestricted", Name: "Unrestricted", WalkableMode: walkableModeUnrestricted}

	encoded, err := json.Marshal([]Floor{restricted, unrestricted})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"walkableComponents":[]`)) || !bytes.Contains(encoded, []byte(`"renderBounds":null`)) {
		t.Fatalf("geometry zero values lost in JSON: %s", encoded)
	}

	var decoded []Floor
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded[0].WalkableMode != walkableModeRestricted || decoded[0].WalkableComponents == nil || len(decoded[0].WalkableComponents) != 0 {
		t.Fatalf("restricted empty geometry was not preserved: %#v", decoded[0])
	}
	if decoded[1].WalkableMode != walkableModeUnrestricted || decoded[0].RenderBounds != nil {
		t.Fatalf("floor modes or null render bounds changed: %#v", decoded)
	}
}

func TestRenderBoundsReplaceLegacySceneRectangleSemantics(t *testing.T) {
	bounds, err := normalizePolygon(Polygon{Outer: []ScenePoint{{X: 0, Y: 0}, {X: 100, Y: 0}, {X: 100, Y: 30}, {X: 30, Y: 30}, {X: 30, Y: 100}, {X: 0, Y: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	inside := SceneElement{Transform: Transform{X: 80, Y: 5, Width: 40, Height: 20}}
	notch := SceneElement{Transform: Transform{X: 45, Y: 45, Width: 20, Height: 20}}
	rotatedEdge := SceneElement{Transform: Transform{X: 20, Y: 20, Width: 30, Height: 30, Rotation: 45}}
	if !elementIntersectsRenderBounds(inside, &bounds) || elementIntersectsRenderBounds(notch, &bounds) || !elementIntersectsRenderBounds(rotatedEdge, &bounds) || !elementIntersectsRenderBounds(notch, nil) {
		t.Fatal("visual element filtering does not follow floor render bounds")
	}
	if sceneContentCommand("boundsUpdate") {
		t.Fatal("legacy editable scene bounds command is still accepted")
	}
}

func TestRemoveLegacyWalkableLayerBeforeValidation(t *testing.T) {
	scene := newScene("scene", "Legacy cleanup")
	floorID := firstFloorID(scene)
	legacyID := "legacy-walkable"
	scene.Layers[legacyID] = Layer{ID: legacyID, FloorID: floorID, Name: "Игровая область", Kind: legacyLayerKindWalkable, Visible: true, Opacity: 1}
	if validateSceneStructure(scene, map[string]Asset{}, map[string]*Member{}) {
		t.Fatal("legacy walkable layer was accepted as part of the current model")
	}
	if !removeLegacyWalkableLayers(scene) {
		t.Fatal("legacy walkable layer was not reported as migrated")
	}
	if _, ok := scene.Layers[legacyID]; ok {
		t.Fatal("legacy walkable layer remained in current model")
	}
	if !validateSceneStructure(scene, map[string]Asset{}, map[string]*Member{}) {
		t.Fatal("scene became invalid after removing legacy walkable layer")
	}
}

func TestTransitionUsesCurrentPlayableGeometry(t *testing.T) {
	scene := newScene("scene", "Transitions")
	sourceID := firstFloorID(scene)
	destinationID := "upper"
	scene.Floors[destinationID] = Floor{ID: destinationID, Name: "Upper", Order: 1, Opacity: 1, WalkableMode: walkableModeRestricted, WalkableComponents: []WalkableComponent{}}
	addFloorLayers(scene, destinationID)
	destinationPolygon, err := normalizePolygon(rectangleInput(0, 0, 100, 100))
	if err != nil {
		t.Fatal(err)
	}
	destination := scene.Floors[destinationID]
	destination.WalkableComponents = []WalkableComponent{{ID: "walkable", Polygon: destinationPolygon}}
	destination.RenderBounds = polygonPointer(destinationPolygon)
	scene.Floors[destinationID] = destination
	transition := Transition{ID: "stairs", Name: "Stairs", EndpointA: TransitionEndpoint{FloorID: sourceID, Position: ScenePoint{X: 10, Y: 10}, Radius: 20}, EndpointB: TransitionEndpoint{FloorID: destinationID, Position: ScenePoint{X: 50, Y: 50}, Radius: 20}, Direction: "bidirectional"}
	if !validTransition(scene, transition) {
		t.Fatal("valid transition was rejected by playable geometry")
	}
	scene.Transitions[transition.ID] = transition

	// Later geometry edits may invalidate an existing transition. It must stop
	// teleporting until the GM moves the endpoint back into playable geometry.
	destination.RenderBounds = polygonPointer(rectangleInput(0, 0, 40, 40))
	destination.GeometryRevision++
	scene.Floors[destinationID] = destination
	if validTransition(scene, transition) {
		t.Fatal("transition outside current render bounds remained valid")
	}
	token := Token{FloorID: sourceID, X: 40, Y: 10}
	if _, ok := transitionForMove(scene, token, ScenePoint{X: 10, Y: 10}); ok {
		t.Fatal("movement triggered a transition whose endpoint became invalid")
	}
}

func TestFirstMapInitializesOnlyItsFloorRenderBounds(t *testing.T) {
	scene := newScene("scene", "Map defaults")
	lowerID := firstFloorID(scene)
	if !initializeFloorRenderBounds(scene, lowerID, 1200, 800) {
		t.Fatal("first floor map did not initialize render bounds")
	}
	lower := scene.Floors[lowerID]
	if lower.RenderBounds == nil || polygonBounds(*lower.RenderBounds) != (AABB{MaxX: 1200, MaxY: 800}) || lower.GeometryRevision != 1 {
		t.Fatalf("unexpected first-map render bounds: %#v", lower)
	}
	if initializeFloorRenderBounds(scene, lowerID, 2000, 1600) || polygonBounds(*scene.Floors[lowerID].RenderBounds) != (AABB{MaxX: 1200, MaxY: 800}) {
		t.Fatal("later map changed established render bounds")
	}
	upperID := "upper"
	scene.Floors[upperID] = Floor{ID: upperID, Name: "Upper"}
	addFloorLayers(scene, upperID)
	if !initializeFloorRenderBounds(scene, upperID, 600, 400) || polygonBounds(*scene.Floors[upperID].RenderBounds) != (AABB{MaxX: 600, MaxY: 400}) {
		t.Fatal("first map on a new floor did not get independent render bounds")
	}
}

func elementFrom(t *testing.T, message map[string]json.RawMessage) SceneElement {
	t.Helper()
	var element SceneElement
	if err := json.Unmarshal(message["element"], &element); err != nil {
		t.Fatal(err)
	}
	return element
}

func TestSceneStructureSeparatesRasterAndTokenAssetRoles(t *testing.T) {
	scene := newScene("scene", "Asset roles")
	floorID := firstFloorID(scene)
	visualLayerID := firstLayerID(scene, floorID)
	tokenLayerID := layerIDByKind(scene, floorID, layerKindTokens)
	assets := map[string]Asset{
		"raster": {ID: "raster", Kind: assetKindScene, Width: 64, Height: 64, RenderMode: renderModeBitmap},
		"token":  {ID: "token", Kind: assetKindToken, Width: 64, Height: 64, RenderMode: renderModeBitmap},
	}
	scene.Elements["element"] = SceneElement{ID: "element", FloorID: floorID, LayerID: visualLayerID, AssetID: "raster", Transform: Transform{Width: 64, Height: 64}, Visible: true, Opacity: 1}
	scene.Tokens["piece"] = Token{ID: "piece", FloorID: floorID, LayerID: tokenLayerID, Size: 64, Asset: "token"}
	if !validateSceneStructure(scene, assets, map[string]*Member{}) {
		t.Fatal("valid raster element and token portrait were rejected")
	}

	element := scene.Elements["element"]
	element.AssetID = "token"
	scene.Elements["element"] = element
	if validateSceneStructure(scene, assets, map[string]*Member{}) {
		t.Fatal("token portrait was accepted as a visual SceneElement")
	}
	element.AssetID = "raster"
	scene.Elements["element"] = element
	token := scene.Tokens["piece"]
	token.Asset = "raster"
	scene.Tokens["piece"] = token
	if validateSceneStructure(scene, assets, map[string]*Member{}) {
		t.Fatal("SceneElement raster was accepted as a token portrait")
	}
}

func TestPreviousSceneModelIsRejectedExplicitly(t *testing.T) {
	root := t.TempDir()
	mapID := "existing-map"
	member := &Member{ID: "gm", Name: "GM", Role: "gm"}
	floorID, layerID := "floor", "base"
	scene := &Scene{ID: "scene", Name: "Current scene", Published: true, Revision: 7, Bounds: SceneBounds{Width: 12000, Height: 8000}, Floors: map[string]Floor{floorID: {ID: floorID, Name: "Этаж 1"}}, Layers: map[string]Layer{layerID: {ID: layerID, FloorID: floorID, Name: "Основа", Visible: true, Opacity: 1}}, Elements: map[string]SceneElement{"map": {ID: "map", FloorID: floorID, LayerID: layerID, AssetID: mapID, Name: "Карта", Transform: Transform{Width: 12000, Height: 8000}, Visible: true, Opacity: 1}}, Tokens: map[string]Token{"hero": {ID: "hero", Name: "Hero", Size: 80}}, Transitions: map[string]Transition{}}
	session := &Session{ID: "campaign", Name: "Campaign", Invite: "invite", Members: map[string]*Member{member.ID: member}, Assets: map[string]Asset{mapID: {ID: mapID, Width: 12000, Height: 8000, Levels: 6, Kind: "map"}}, Keys: map[string]string{"secret": member.ID}, CampaignRevision: 1, Scenes: map[string]*Scene{scene.ID: scene}}
	encoded, err := json.Marshal(map[string]*Session{session.ID: session})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "sessions.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}

	if _, err = testServerNoFatal(root); err == nil || !strings.Contains(err.Error(), "unsupported scene model version") {
		t.Fatalf("old scene model was not rejected explicitly: %v", err)
	}
}

func TestTwoFloorInvariantWalkableAndFloorAwareDelivery(t *testing.T) {
	server := testServer(t, t.TempDir())
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Two floors"})
	player := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})

	server.mu.Lock()
	session := server.sessions[gm["session"]]
	scene := firstScene(session)
	lowerID := firstFloorID(scene)
	upperID := "upper"
	scene.Floors[upperID] = Floor{ID: upperID, Name: "Этаж 2", Order: 1, Opacity: 1, WalkableMode: walkableModeUnrestricted, WalkableComponents: []WalkableComponent{}}
	addFloorLayers(scene, upperID)
	lower := scene.Floors[lowerID]
	lower.WalkableMode = walkableModeRestricted
	walkablePolygon, err := rectanglePolygon(AABB{MinX: 0, MinY: 0, MaxX: scene.Bounds.Width, MaxY: scene.Bounds.Height})
	if err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	lower.WalkableComponents = []WalkableComponent{{ID: "main", Polygon: walkablePolygon}}
	lower.RenderBounds = &walkablePolygon
	scene.Floors[lowerID] = lower
	var playerMember *Member
	for _, member := range session.Members {
		if member.Role == "player" {
			playerMember = member
		}
	}
	assets := []string{"lower-asset", "upper-asset", "outside-asset"}
	for _, assetID := range assets {
		session.Assets[assetID] = Asset{ID: assetID, Width: 64, Height: 64, Kind: "map", RenderMode: "bitmap"}
	}
	scene.Elements["lower"] = SceneElement{ID: "lower", FloorID: lowerID, LayerID: firstLayerID(scene, lowerID), AssetID: "lower-asset", Transform: Transform{X: 10, Y: 10, Width: 64, Height: 64}, Visible: true, Opacity: 1}
	scene.Elements["upper"] = SceneElement{ID: "upper", FloorID: upperID, LayerID: firstLayerID(scene, upperID), AssetID: "upper-asset", Transform: Transform{X: 20, Y: 20, Width: 64, Height: 64}, Visible: true, Opacity: 1}
	scene.Elements["outside"] = SceneElement{ID: "outside", FloorID: lowerID, LayerID: firstLayerID(scene, lowerID), AssetID: "outside-asset", Transform: Transform{X: scene.Bounds.Width + 100, Y: 10, Width: 64, Height: 64}, Visible: true, Opacity: 1}
	scene.Tokens["hero"] = Token{ID: "hero", Name: "Hero", FloorID: lowerID, LayerID: layerIDByKind(scene, lowerID, layerKindTokens), X: 50, Y: 50, Size: 80, Owner: playerMember.ID}
	scene.Tokens["zz-upper"] = Token{ID: "zz-upper", Name: "Upper", FloorID: upperID, LayerID: layerIDByKind(scene, upperID, layerKindTokens), X: 60, Y: 60, Size: 80, Owner: playerMember.ID}
	scene.Revision++
	scene.rebuildRuntime()
	region := SceneRegion{Left: 0, Top: 0, Right: scene.Bounds.Width + 1000, Bottom: 1000}
	snapshot := server.snapshotSceneAtFloor(session, playerMember, scene.ID, &region, lowerID).(map[string]any)
	elements := snapshot["elements"].(map[string]SceneElement)
	layers := snapshot["layers"].(map[string]Layer)
	if elements["lower"].ID == "" || elements["upper"].ID != "" || elements["outside"].ID != "" {
		t.Fatalf("zero-alpha or out-of-bounds floor content leaked: %#v", elements)
	}
	for _, layer := range layers {
		if layer.Kind != layerKindVisual && layer.Kind != layerKindTokens {
			t.Fatalf("legacy layer leaked to player: %#v", layer)
		}
	}
	if assetVisibleTo(session, playerMember, scene.ID, "upper-asset") || assetVisibleTo(session, playerMember, scene.ID, "outside-asset") {
		t.Fatal("asset authorization ignored compositing floor or render bounds")
	}
	if !assetVisibleToAtToken(session, playerMember, scene.ID, "upper-asset", "zz-upper") {
		t.Fatal("asset authorization ignored the validated active token floor")
	}
	upper := scene.Floors[upperID]
	upper.OpacityWhenViewedFromBelow = .5
	scene.Floors[upperID] = upper
	scene.Revision++
	scene.rebuildRuntime()
	snapshot = server.snapshotSceneAtFloor(session, playerMember, scene.ID, &region, lowerID).(map[string]any)
	elements = snapshot["elements"].(map[string]SceneElement)
	if elements["upper"].ID == "" || !assetVisibleTo(session, playerMember, scene.ID, "upper-asset") {
		t.Fatal("visible upper floor was not delivered and authorized")
	}
	lower = scene.Floors[lowerID]
	lower.Opacity = 0
	scene.Floors[lowerID] = lower
	hero := scene.Tokens["hero"]
	hero.FloorID, hero.LayerID = upperID, layerIDByKind(scene, upperID, layerKindTokens)
	scene.Tokens[hero.ID] = hero
	scene.Revision++
	scene.rebuildRuntime()
	snapshot = server.snapshotSceneAtFloor(session, playerMember, scene.ID, &region, upperID).(map[string]any)
	elements = snapshot["elements"].(map[string]SceneElement)
	if elements["upper"].ID == "" || elements["lower"].ID != "" || assetVisibleTo(session, playerMember, scene.ID, "lower-asset") {
		t.Fatal("zero-alpha lower floor was delivered while viewing the upper floor")
	}
	lower.Opacity = 1
	scene.Floors[lowerID] = lower
	hero.FloorID, hero.LayerID = lowerID, layerIDByKind(scene, lowerID, layerKindTokens)
	scene.Tokens[hero.ID] = hero
	scene.Revision++
	scene.rebuildRuntime()
	server.mu.Unlock()

	gmWS := dial(t, host.URL, gm)
	read(t, gmWS, "snapshot")
	gmWS.WriteJSON(Command{Type: "floorCreate", Client: "floor-limit", Seq: 1, SceneID: scene.ID, Floor: Floor{Name: "Этаж 3", Order: 2}})
	ack := read(t, gmWS, "ack")
	var issue string
	json.Unmarshal(ack["error"], &issue)
	if issue == "" {
		t.Fatal("third floor was accepted")
	}
	tokenLayerID := layerIDByKind(scene, lowerID, layerKindTokens)
	gmWS.WriteJSON(Command{Type: "layerDelete", Client: "special-layer", Seq: 1, SceneID: scene.ID, Layer: Layer{ID: tokenLayerID}})
	ack = read(t, gmWS, "ack")
	json.Unmarshal(ack["error"], &issue)
	if issue == "" {
		t.Fatal("token layer was deleted")
	}
	gmWS.WriteJSON(Command{Type: "elementCreate", Client: "special-layer", Seq: 2, SceneID: scene.ID, Element: SceneElement{FloorID: lowerID, LayerID: tokenLayerID, AssetID: "lower-asset", Transform: Transform{Width: 64, Height: 64}, Visible: true, Opacity: 1}})
	ack = read(t, gmWS, "ack")
	json.Unmarshal(ack["error"], &issue)
	if issue == "" {
		t.Fatal("scene element was placed into token layer")
	}

	gmWS.WriteJSON(Command{Type: "elementFixRotation", Client: "fix-rotation-disabled", Seq: 1, SceneID: scene.ID, Element: SceneElement{ID: "lower"}})
	ack = read(t, gmWS, "ack")
	json.Unmarshal(ack["error"], &issue)
	if issue != "Фиксация ротации отключена" {
		t.Fatalf("disabled fix rotation returned unexpected error: %q", issue)
	}
	server.mu.Lock()
	if len(server.rotationJobs) != 0 {
		server.mu.Unlock()
		t.Fatal("disabled fix rotation started a background job")
	}
	server.mu.Unlock()

	playerWS := dial(t, host.URL, player)
	read(t, playerWS, "snapshot")
	playerWS.WriteJSON(Command{Type: "move", SceneID: scene.ID, Token: Token{ID: "hero", X: scene.Bounds.Width + 500, Y: 50}})
	blocked := read(t, playerWS, "error")
	var operation, errorCode, blockedSceneID, blockedID string
	var blockedX, blockedY float64
	json.Unmarshal(blocked["operation"], &operation)
	json.Unmarshal(blocked["errorCode"], &errorCode)
	json.Unmarshal(blocked["sceneId"], &blockedSceneID)
	json.Unmarshal(blocked["id"], &blockedID)
	json.Unmarshal(blocked["x"], &blockedX)
	json.Unmarshal(blocked["y"], &blockedY)
	if operation != "move" || errorCode != movementBlockedErrorCode || blockedSceneID != scene.ID || blockedID != "hero" || blockedX != 50 || blockedY != 50 {
		t.Fatalf("preview rejection did not return compact authoritative correction: %#v", blocked)
	}
	playerWS.WriteJSON(Command{Type: "final", Client: "other-floor", Seq: 1, SceneID: scene.ID, Token: Token{ID: "zz-upper", X: 100, Y: 100}})
	ack = read(t, playerWS, "ack")
	json.Unmarshal(ack["error"], &issue)
	if issue == "" {
		t.Fatal("player moved a controlled token on the other floor")
	}
	playerWS.WriteJSON(Command{Type: "final", Client: "walkable-player", Seq: 1, SceneID: scene.ID, Token: Token{ID: "hero", X: scene.Bounds.Width + 500, Y: 50}})
	ack = read(t, playerWS, "ack")
	json.Unmarshal(ack["error"], &issue)
	if issue == "" {
		t.Fatal("player moved token outside walkable bounds")
	}
	json.Unmarshal(ack["errorCode"], &errorCode)
	if errorCode != movementBlockedErrorCode {
		t.Fatalf("reliable movement rejection was not typed: %q", errorCode)
	}
	server.mu.Lock()
	if token := scene.Tokens["hero"]; token.X != 50 || token.Y != 50 {
		t.Fatalf("rejected movement changed state: %#v", token)
	}
	server.mu.Unlock()

	gmWS.WriteJSON(Command{Type: "final", Client: "walkable-gm", Seq: 1, SceneID: scene.ID, Token: Token{ID: "hero", X: scene.Bounds.Width + 500, Y: 50}})
	read(t, gmWS, "upsert")
	ack = read(t, gmWS, "ack")
	json.Unmarshal(ack["error"], &issue)
	if issue != "" {
		t.Fatalf("GM administrative movement was rejected: %s", issue)
	}
	playerWS.WriteJSON(Command{Type: "final", Client: "walkable-player", Seq: 2, SceneID: scene.ID, Token: Token{ID: "hero", X: 50, Y: 50}})
	ack = read(t, playerWS, "ack")
	json.Unmarshal(ack["error"], &issue)
	if issue == "" {
		t.Fatal("player moved a token that already started outside walkable geometry")
	}
}

func testServerNoFatal(root string) (*Server, error) { return newServer(root) }

func TestSceneElementRegionVisibilityAndAssetAccess(t *testing.T) {
	server := testServer(t, t.TempDir())
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Element visibility"})
	player := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})

	server.mu.Lock()
	session := server.sessions[gm["session"]]
	scene := firstScene(session)
	floorID := firstFloorID(scene)
	visibleLayerID := firstLayerID(scene, floorID)
	hiddenLayerID := "hidden-layer"
	scene.Layers[hiddenLayerID] = Layer{ID: hiddenLayerID, FloorID: floorID, Name: "GM only", Order: 10, Visible: false, Opacity: 1}
	for _, assetID := range []string{"near-element", "far-element", "hidden-element"} {
		session.Assets[assetID] = Asset{ID: assetID, Filename: assetID + ".png", MimeType: "image/png", Width: 64, Height: 64, Levels: 1, Kind: "token", RetentionPolicy: assetReclaimable}
	}
	scene.Elements["near"] = SceneElement{ID: "near", AssetID: "near-element", FloorID: floorID, LayerID: visibleLayerID, Transform: Transform{X: 100, Y: 100, Width: 64, Height: 64}, Visible: true, Opacity: 1}
	scene.Elements["far"] = SceneElement{ID: "far", AssetID: "far-element", FloorID: floorID, LayerID: visibleLayerID, Transform: Transform{X: 20000, Y: 20000, Width: 64, Height: 64}, Visible: true, Opacity: 1}
	scene.Elements["hidden"] = SceneElement{ID: "hidden", AssetID: "hidden-element", FloorID: floorID, LayerID: hiddenLayerID, Transform: Transform{X: 120, Y: 120, Width: 64, Height: 64}, Visible: true, Opacity: 1}
	scene.Revision++
	scene.rebuildRuntime()
	server.mu.Unlock()

	ws := dialRaw(t, host.URL, player)
	home := read(t, ws, "campaignSnapshot")
	var summaries []SceneSummary
	json.Unmarshal(home["scenes"], &summaries)
	ws.WriteJSON(Command{Type: "subscribe", SceneID: summaries[0].ID})
	read(t, ws, "snapshot")
	region := SceneRegion{Left: 0, Top: 0, Right: 1000, Bottom: 1000}
	ws.WriteJSON(Command{Type: "view", SceneID: scene.ID, Region: &region})
	loaded := read(t, ws, "snapshot")
	var elements map[string]SceneElement
	var assets map[string]Asset
	json.Unmarshal(loaded["elements"], &elements)
	json.Unmarshal(loaded["assets"], &assets)
	if len(elements) != 1 || elements["near"].ID == "" || elements["far"].ID != "" || elements["hidden"].ID != "" {
		t.Fatalf("region snapshot leaked element state: %#v", elements)
	}
	if len(assets) != 1 || assets["near-element"].ID == "" || assets["far-element"].ID != "" || assets["hidden-element"].ID != "" {
		t.Fatalf("region snapshot leaked element asset metadata: %#v", assets)
	}
	req, _ := http.NewRequest(http.MethodGet, host.URL+"/api/asset/hidden-element/token.png?session="+gm["session"]+"&scene="+scene.ID, nil)
	req.Header.Set("Authorization", "Bearer "+player["key"])
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("hidden layer asset returned %s", res.Status)
	}
}

func TestSceneContentCRUDTransformReuseAndRetention(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Content"})
	ws := dial(t, host.URL, gm)
	read(t, ws, "snapshot")

	server.mu.Lock()
	session := server.sessions[gm["session"]]
	scene := firstScene(session)
	asset := Asset{ID: "shared-image", Filename: "chair.png", MimeType: "image/png", Width: 300, Height: 400, Levels: 1, Kind: "map", RetentionPolicy: assetReclaimable}
	session.Assets[asset.ID] = asset
	refreshAssetOrphans(session, time.Unix(100, 0))
	server.dirty = true
	if err := server.saveLocked(); err != nil {
		t.Fatal(err)
	}
	writes := server.writes
	server.mu.Unlock()

	create := func(seq uint64, x float64) SceneElement {
		ws.WriteJSON(Command{Type: "elementCreate", Client: "content", Seq: seq, SceneID: scene.ID, Element: SceneElement{AssetID: asset.ID, FloorID: firstFloorID(scene), LayerID: firstLayerID(scene, firstFloorID(scene)), Name: "Chair", Transform: Transform{X: x, Y: 20, Width: 150, Height: 200}, Visible: true, Opacity: 1}})
		element := elementFrom(t, read(t, ws, "elementUpsert"))
		if ack := read(t, ws, "ack"); string(ack["error"]) != "null" && string(ack["error"]) != `""` {
			t.Fatalf("create rejected: %s", ack["error"])
		}
		return element
	}
	first := create(1, 10)
	second := create(2, 300)
	if first.ID == second.ID || first.AssetID != second.AssetID {
		t.Fatalf("asset was not reused: %+v %+v", first, second)
	}
	otherLayer := ""
	for layerID, layer := range scene.Layers {
		if layerID != first.LayerID && layer.FloorID == first.FloorID && layer.Kind == layerKindVisual {
			otherLayer = layerID
		}
	}
	ws.WriteJSON(Command{Type: "elementUpdate", Client: "content", Seq: 3, SceneID: scene.ID, Element: SceneElement{ID: first.ID}, ElementProperties: ElementProperties{FloorID: &first.FloorID, LayerID: &otherLayer, ZOrder: func() *int { value := 12; return &value }()}})
	movedLayer := elementFrom(t, read(t, ws, "elementUpsert"))
	read(t, ws, "ack")
	if movedLayer.LayerID != otherLayer || movedLayer.ZOrder != 12 {
		t.Fatalf("element layer/z-order update failed: %+v", movedLayer)
	}
	order := 50
	ws.WriteJSON(Command{Type: "layerUpdate", Client: "content", Seq: 4, SceneID: scene.ID, Layer: Layer{ID: otherLayer}, LayerProperties: LayerProperties{Order: &order}})
	read(t, ws, "snapshot")
	read(t, ws, "ack")
	if scene.Layers[otherLayer].Order != order {
		t.Fatal("layer order was not updated")
	}

	transform := Transform{X: 900, Y: 700, Width: 240, Height: 320, Rotation: 37}
	server.mu.Lock()
	beforeTransformWrites := server.writes
	server.mu.Unlock()
	ws.WriteJSON(Command{Type: "elementTransform", Client: "positions", Seq: 1, SceneID: scene.ID, Element: SceneElement{ID: first.ID, Transform: transform}})
	updated := elementFrom(t, read(t, ws, "elementUpsert"))
	read(t, ws, "ack")
	server.mu.Lock()
	if server.writes != beforeTransformWrites {
		t.Fatal("coalesced element transform wrote synchronously")
	}
	if scene.Elements[first.ID].Transform != transform {
		t.Fatal("authoritative transform was not updated")
	}
	revisionAfterTransform := scene.Revision
	server.mu.Unlock()
	ws.WriteJSON(Command{Type: "elementTransform", Client: "positions", Seq: 2, SceneID: scene.ID, Element: SceneElement{ID: first.ID, Transform: transform}})
	read(t, ws, "ack")
	server.mu.Lock()
	if server.writes != beforeTransformWrites || scene.Revision != revisionAfterTransform {
		t.Fatal("no-op transform created a revision or disk write")
	}
	server.mu.Unlock()

	floorID := firstFloorID(scene)
	expectedGeometryRevision := scene.Floors[floorID].GeometryRevision
	renderBounds, err := rectanglePolygon(AABB{MinX: -100, MinY: -200, MaxX: 20000, MaxY: 18000})
	if err != nil {
		t.Fatal(err)
	}
	ws.WriteJSON(Command{Type: "setRenderBounds", Client: "content", Seq: 5, SceneID: scene.ID, FloorID: floorID, ExpectedGeometryRevision: &expectedGeometryRevision, RenderBounds: &renderBounds})
	read(t, ws, "snapshot")
	read(t, ws, "ack")
	server.mu.Lock()
	if scene.Floors[floorID].RenderBounds == nil || !polygonsEqual(*scene.Floors[floorID].RenderBounds, renderBounds) || scene.Elements[first.ID].Transform != updated.Transform || server.writes <= writes {
		t.Fatal("render bounds update moved content or did not persist pending transform")
	}
	server.mu.Unlock()
	restarted, err := newServer(root)
	if err != nil {
		t.Fatal(err)
	}
	persisted := restarted.sessions[gm["session"]].Scenes[scene.ID]
	if persisted.Floors[floorID].RenderBounds == nil || !polygonsEqual(*persisted.Floors[floorID].RenderBounds, renderBounds) || persisted.Elements[first.ID].Transform != transform || persisted.Elements[first.ID].LayerID != otherLayer {
		t.Fatal("scene hierarchy/transform did not survive restart")
	}

	ws.WriteJSON(Command{Type: "elementDelete", Client: "content", Seq: 6, SceneID: scene.ID, Element: SceneElement{ID: first.ID}})
	read(t, ws, "elementDelete")
	read(t, ws, "ack")
	ws.WriteJSON(Command{Type: "elementDelete", Client: "content", Seq: 7, SceneID: scene.ID, Element: SceneElement{ID: second.ID}})
	read(t, ws, "elementDelete")
	read(t, ws, "ack")
	server.mu.Lock()
	if session.Assets[asset.ID].OrphanSince == nil {
		t.Fatal("unreferenced reclaimable asset was not marked orphan")
	}
	server.mu.Unlock()

	ws.WriteJSON(Command{Type: "assetRetention", Client: "content", Seq: 8, SceneID: scene.ID, AssetID: asset.ID, RetentionPolicy: assetKeep})
	read(t, ws, "snapshot")
	read(t, ws, "ack")
	server.mu.Lock()
	kept := session.Assets[asset.ID]
	server.mu.Unlock()
	if kept.RetentionPolicy != assetKeep || kept.OrphanSince != nil {
		t.Fatalf("keep asset remained orphan: %+v", kept)
	}
}

func TestFloorTransitionAndPlayerPermissions(t *testing.T) {
	server := testServer(t, t.TempDir())
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Floors"})
	player := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})
	gmWS := dial(t, host.URL, gm)
	read(t, gmWS, "snapshot")
	playerWS := dial(t, host.URL, player)
	playerSnapshot := read(t, playerWS, "snapshot")
	var playerMember Member
	json.Unmarshal(playerSnapshot["you"], &playerMember)

	server.mu.Lock()
	scene := firstScene(server.sessions[gm["session"]])
	sourceFloor := firstFloorID(scene)
	server.mu.Unlock()
	gmWS.WriteJSON(Command{Type: "floorCreate", Client: "gm-content", Seq: 1, SceneID: scene.ID, Floor: Floor{Name: "Этаж 2", Order: 1}})
	read(t, gmWS, "snapshot")
	read(t, gmWS, "ack")
	server.mu.Lock()
	destinationFloor := ""
	for floorID := range scene.Floors {
		if floorID != sourceFloor {
			destinationFloor = floorID
		}
	}
	server.mu.Unlock()

	gmWS.WriteJSON(Command{Type: "create", Client: "gm-content", Seq: 2, SceneID: scene.ID, Token: Token{Name: "Climber", FloorID: sourceFloor, X: 50, Y: 50, Size: 80, Owner: playerMember.ID}})
	token := tokenFrom(t, read(t, gmWS, "upsert"))
	read(t, gmWS, "ack")
	read(t, playerWS, "upsert")
	transition := Transition{Name: "Stairs", EndpointA: TransitionEndpoint{FloorID: sourceFloor, Position: ScenePoint{X: 100, Y: 100}, Radius: 40}, EndpointB: TransitionEndpoint{FloorID: destinationFloor, Position: ScenePoint{X: 500, Y: 600}, Radius: 60}, Direction: "bidirectional"}
	gmWS.WriteJSON(Command{Type: "transitionCreate", Client: "gm-content", Seq: 3, SceneID: scene.ID, Transition: transition})
	transitionSnapshot := read(t, gmWS, "snapshot")
	read(t, gmWS, "ack")
	read(t, playerWS, "snapshot")
	var transitions map[string]Transition
	json.Unmarshal(transitionSnapshot["transitions"], &transitions)
	for _, current := range transitions {
		transition = current
	}

	playerWS.WriteJSON(Command{Type: "final", Client: "player-content", Seq: 1, SceneID: scene.ID, Token: Token{ID: token.ID, FloorID: sourceFloor, X: 100, Y: 100}})
	movedSnapshot := read(t, playerWS, "snapshot")
	read(t, playerWS, "ack")
	var movedTokens map[string]Token
	json.Unmarshal(movedSnapshot["tokens"], &movedTokens)
	moved := movedTokens[token.ID]
	if moved.FloorID != destinationFloor || moved.X != 500 || moved.Y != 600 {
		t.Fatalf("transition failed: %+v", moved)
	}

	// A final event queued before the teleport must not continue the old drag on
	// the destination floor. It is acknowledged as an idempotent no-op so a
	// normal transition does not produce a misleading client error.
	playerWS.WriteJSON(Command{Type: "final", Client: "player-content", Seq: 2, SceneID: scene.ID, Token: Token{ID: token.ID, FloorID: sourceFloor, X: 130, Y: 100}})
	ack := read(t, playerWS, "ack")
	var issue string
	json.Unmarshal(ack["error"], &issue)
	if issue != "" {
		t.Fatalf("stale final after transition returned an error: %s", issue)
	}
	server.mu.Lock()
	afterStale := scene.Tokens[token.ID]
	server.mu.Unlock()
	if afterStale.FloorID != destinationFloor || afterStale.X != 500 || afterStale.Y != 600 {
		t.Fatalf("stale final moved teleported token: %+v", afterStale)
	}

	// Being placed inside B does not immediately trigger B -> A. The token must
	// leave the zone first and then enter it again.
	playerWS.WriteJSON(Command{Type: "final", Client: "player-content", Seq: 3, SceneID: scene.ID, Token: Token{ID: token.ID, FloorID: destinationFloor, X: 600, Y: 600}})
	read(t, playerWS, "upsert")
	read(t, playerWS, "ack")
	playerWS.WriteJSON(Command{Type: "final", Client: "player-content", Seq: 4, SceneID: scene.ID, Token: Token{ID: token.ID, FloorID: destinationFloor, X: 540, Y: 600}})
	backSnapshot := read(t, playerWS, "snapshot")
	read(t, playerWS, "ack")
	json.Unmarshal(backSnapshot["tokens"], &movedTokens)
	backAtA := movedTokens[token.ID]
	if backAtA.FloorID != sourceFloor || backAtA.X != 100 || backAtA.Y != 100 {
		t.Fatalf("reverse transition failed: %+v", backAtA)
	}

	back := destinationFloor
	playerWS.WriteJSON(Command{Type: "properties", Client: "player-content", Seq: 5, SceneID: scene.ID, Token: Token{ID: token.ID}, Properties: Properties{FloorID: &back}})
	ack = read(t, playerWS, "ack")
	json.Unmarshal(ack["error"], &issue)
	if issue == "" {
		t.Fatal("player changed token floor without transition")
	}
	server.mu.Lock()
	current := scene.Tokens[token.ID]
	server.mu.Unlock()
	if current.FloorID != sourceFloor {
		t.Fatal("rejected floor change mutated token")
	}
}

func TestPlayerActiveTokenControlsFloorAndNavigation(t *testing.T) {
	server := testServer(t, t.TempDir())
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Active token"})
	player := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})

	server.mu.Lock()
	session := server.sessions[gm["session"]]
	member := session.Members[session.Keys[player["key"]]]
	scene := firstScene(session)
	lowerFloor := firstFloorID(scene)
	upperFloor := id()
	scene.Floors[upperFloor] = Floor{ID: upperFloor, Name: "Upper", Order: 1, Opacity: 1, WalkableMode: walkableModeUnrestricted, WalkableComponents: []WalkableComponent{}}
	addFloorLayers(scene, upperFloor)
	lower := Token{ID: "owned-lower", Name: "Lower", Owner: member.ID, FloorID: lowerFloor, LayerID: layerIDByKind(scene, lowerFloor, layerKindTokens), X: 100, Y: 120, Size: 80, Asset: "lower-secret"}
	upper := Token{ID: "owned-upper", Name: "Upper", Owner: member.ID, FloorID: upperFloor, LayerID: layerIDByKind(scene, upperFloor, layerKindTokens), X: 2400, Y: 1800, Size: 80, Asset: "upper-secret"}
	foreign := Token{ID: "foreign-upper", Name: "Foreign", Owner: "someone-else", FloorID: upperFloor, LayerID: upper.LayerID, X: 2450, Y: 1800, Size: 80}
	scene.Tokens[lower.ID], scene.Tokens[upper.ID], scene.Tokens[foreign.ID] = lower, upper, foreign
	scene.Revision++
	scene.rebuildRuntime()
	server.mu.Unlock()

	ws := dialRaw(t, host.URL, player)
	read(t, ws, "campaignSnapshot")
	if err := ws.WriteJSON(Command{Type: "subscribe", SceneID: scene.ID, ActiveTokenID: upper.ID}); err != nil {
		t.Fatal(err)
	}
	initial := read(t, ws, "snapshot")
	var currentFloor, activeToken string
	json.Unmarshal(initial["currentFloorId"], &currentFloor)
	json.Unmarshal(initial["activeTokenId"], &activeToken)
	if currentFloor != upperFloor || activeToken != upper.ID {
		t.Fatalf("saved active token was not accepted: floor=%s token=%s", currentFloor, activeToken)
	}
	var owned map[string]Token
	json.Unmarshal(initial["ownedTokens"], &owned)
	if len(owned) != 2 || owned[lower.ID].ID == "" || owned[upper.ID].ID == "" || owned[foreign.ID].ID != "" {
		t.Fatalf("wrong navigation token set: %+v", owned)
	}
	if owned[lower.ID].Asset != "" || owned[upper.ID].Asset != "" {
		t.Fatal("navigation list disclosed assets from an unopened floor")
	}

	region := SceneRegion{Left: 0, Top: 0, Right: 800, Bottom: 600}
	if err := ws.WriteJSON(Command{Type: "activeToken", SceneID: scene.ID, ActiveTokenID: lower.ID, Focus: true, Region: &region}); err != nil {
		t.Fatal(err)
	}
	lowerSnapshot := read(t, ws, "snapshot")
	json.Unmarshal(lowerSnapshot["currentFloorId"], &currentFloor)
	if currentFloor != lowerFloor {
		t.Fatalf("active lower token selected floor %s", currentFloor)
	}
	var focused SceneRegion
	json.Unmarshal(lowerSnapshot["region"], &focused)
	if (focused.Left+focused.Right)/2 != lower.X || (focused.Top+focused.Bottom)/2 != lower.Y {
		t.Fatalf("focused region is not centered on token: %+v", focused)
	}

	// WebSocket ordering makes the active-token switch authoritative before the
	// immediately following drag update, including a token on an unseen Floor.
	ws.WriteJSON(Command{Type: "activeToken", SceneID: scene.ID, ActiveTokenID: upper.ID, Focus: true, Region: &region})
	ws.WriteJSON(Command{Type: "move", SceneID: scene.ID, Token: Token{ID: upper.ID, X: 2500, Y: 1850}})
	read(t, ws, "snapshot")
	move := read(t, ws, "move")
	var movedX, movedY float64
	json.Unmarshal(move["x"], &movedX)
	json.Unmarshal(move["y"], &movedY)
	if movedX != 2500 || movedY != 1850 {
		t.Fatalf("move after active switch was lost: %v %v", movedX, movedY)
	}
	otherWS := dialRaw(t, host.URL, player)
	read(t, otherWS, "campaignSnapshot")
	otherWS.WriteJSON(Command{Type: "subscribe", SceneID: scene.ID, ActiveTokenID: lower.ID})
	otherSnapshot := read(t, otherWS, "snapshot")
	json.Unmarshal(otherSnapshot["currentFloorId"], &currentFloor)
	if currentFloor != lowerFloor {
		t.Fatalf("second connection did not keep its own active token: %s", currentFloor)
	}
	ws.WriteJSON(Command{Type: "sync"})
	firstSnapshot := read(t, ws, "snapshot")
	json.Unmarshal(firstSnapshot["activeTokenId"], &activeToken)
	if activeToken != upper.ID {
		t.Fatalf("second connection changed first connection active token to %s", activeToken)
	}

	ws.WriteJSON(Command{Type: "activeToken", SceneID: scene.ID, ActiveTokenID: foreign.ID, Region: &region})
	rejected := read(t, ws, "error")
	var operation string
	json.Unmarshal(rejected["operation"], &operation)
	if operation != "activeToken" {
		t.Fatalf("active token rejection was not typed: %q", operation)
	}
	ws.WriteJSON(Command{Type: "sync"})
	afterReject := read(t, ws, "snapshot")
	json.Unmarshal(afterReject["activeTokenId"], &activeToken)
	if activeToken != upper.ID {
		t.Fatalf("rejected token changed active selection to %s", activeToken)
	}
}

func benchmarkSceneRuntime() (*Scene, Floor, Layer) {
	floor := Floor{ID: "floor", Name: "Floor", Opacity: 1}
	layer := Layer{ID: "visual", FloorID: floor.ID, Name: "Visual", Kind: layerKindVisual, Visible: true, Opacity: 1}
	scene := &Scene{
		ID: "benchmark", Revision: 1, Bounds: SceneBounds{Width: maxSceneDimension, Height: maxSceneDimension},
		Floors: map[string]Floor{floor.ID: floor}, Layers: map[string]Layer{layer.ID: layer},
		Elements: map[string]SceneElement{}, Tokens: map[string]Token{}, Transitions: map[string]Transition{},
	}
	return scene, floor, layer
}

func TestSceneElementSpatialIndexBoundsOversizedEntries(t *testing.T) {
	scene, floor, layer := benchmarkSceneRuntime()
	long := SceneElement{ID: "long", FloorID: floor.ID, LayerID: layer.ID, Visible: true, Opacity: 1, Transform: Transform{X: -20_000, Y: -50, Width: 50_000, Height: 100}}
	scene.Elements[long.ID] = long
	runtime := newSceneRuntime(scene)
	scene.runtime = runtime
	if _, oversized := runtime.oversizedElements[long.ID]; oversized {
		t.Fatal("axis-aligned thin element was classified as oversized")
	}
	if got := len(runtime.elementCells[long.ID]); got > maxElementSpatialCells || got >= 100 {
		t.Fatalf("thin element used %d cells", got)
	}
	if got := runtime.queryElements(SceneRegion{Left: -20_010, Top: -60, Right: -19_990, Bottom: 60}); len(got) != 1 || got[0].ID != long.ID {
		t.Fatalf("negative-coordinate boundary query missed element: %+v", got)
	}

	rotated := long
	rotated.Transform.Rotation = 45
	scene.Elements[long.ID] = rotated
	scene.Revision++
	scene.applyElementRuntimeChange(long, true, rotated, true)
	if _, oversized := runtime.oversizedElements[long.ID]; !oversized {
		t.Fatal("large rotated AABB did not use oversized storage")
	}
	if len(runtime.elementCells[long.ID]) != 0 {
		t.Fatal("oversized element retained grid cells")
	}
	scene.Elements[long.ID] = long
	scene.Revision++
	scene.applyElementRuntimeChange(rotated, true, long, true)
	if _, oversized := runtime.oversizedElements[long.ID]; oversized || len(runtime.elementCells[long.ID]) == 0 {
		t.Fatal("resized element did not return to grid storage")
	}
	scene.Elements[long.ID] = rotated
	scene.Revision++
	scene.applyElementRuntimeChange(long, true, rotated, true)

	giant := SceneElement{ID: "giant", FloorID: floor.ID, LayerID: layer.ID, Visible: true, Opacity: 1, Transform: Transform{X: 0, Y: 0, Width: maxSceneDimension, Height: maxSceneDimension, Rotation: 13}}
	scene.Elements[giant.ID] = giant
	scene.Revision++
	scene.applyElementRuntimeChange(SceneElement{}, false, giant, true)
	if _, oversized := runtime.oversizedElements[giant.ID]; !oversized || len(runtime.elementCells[giant.ID]) != 0 {
		t.Fatal("maximum element allocated spatial cells")
	}
	if got := runtime.queryElements(SceneRegion{Left: 499_900, Top: 499_900, Right: 500_100, Bottom: 500_100}); len(got) == 0 {
		t.Fatal("oversized element was not queryable")
	}

	delete(scene.Elements, long.ID)
	scene.Revision++
	scene.applyElementRuntimeChange(rotated, true, SceneElement{}, false)
	if _, ok := runtime.oversizedElements[long.ID]; ok {
		t.Fatal("delete retained oversized entry")
	}
	scene.rebuildRuntime()
	if _, ok := scene.runtime.oversizedElements[giant.ID]; !ok || scene.runtime.elementIndexed != 1 {
		t.Fatal("runtime rebuild lost oversized state")
	}
}

func BenchmarkSceneElementSpatialIndex(b *testing.B) {
	scene, floor, layer := benchmarkSceneRuntime()
	element := SceneElement{ID: "long", FloorID: floor.ID, LayerID: layer.ID, Visible: true, Opacity: 1, Transform: Transform{X: 100, Y: 100, Width: 50_000, Height: 100}}
	runtime := newSceneRuntime(scene)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runtime.addElement(element)
		runtime.removeElement(element)
	}
}

func BenchmarkOversizedElementQuery(b *testing.B) {
	scene, floor, layer := benchmarkSceneRuntime()
	for i := 0; i < 1000; i++ {
		element := SceneElement{ID: fmt.Sprintf("oversized-%d", i), FloorID: floor.ID, LayerID: layer.ID, Visible: true, Opacity: 1, Transform: Transform{X: 0, Y: 0, Width: maxSceneDimension, Height: maxSceneDimension, Rotation: float64(i % 90)}}
		scene.Elements[element.ID] = element
	}
	scene.rebuildRuntime()
	region := SceneRegion{Left: 499_900, Top: 499_900, Right: 500_100, Bottom: 500_100}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := len(scene.runtime.queryElements(region)); got != len(scene.Elements) {
			b.Fatalf("query returned %d of %d oversized elements", got, len(scene.Elements))
		}
	}
}

func BenchmarkTokenPositionIndexUpdate(b *testing.B) {
	scene, floor, _ := benchmarkSceneRuntime()
	tokenLayer := Layer{ID: "tokens", FloorID: floor.ID, Name: "Tokens", Kind: layerKindTokens, Visible: true, Opacity: 1}
	scene.Layers[tokenLayer.ID] = tokenLayer
	token := Token{ID: "moving", FloorID: floor.ID, LayerID: tokenLayer.ID, X: 100, Y: 100, Size: 80}
	scene.Tokens[token.ID] = token
	scene.rebuildRuntime()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		old := token
		token.X = 100 + float64(i%100)
		scene.Tokens[token.ID] = token
		scene.Revision++
		scene.applyTokenRuntimeChange(old, true, token, true)
	}
}
