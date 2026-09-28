package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWalkableCommandVersionRetryAndDigest(t *testing.T) {
	server := testServer(t, t.TempDir())
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Walkable reliable"})
	ws := dial(t, host.URL, gm)
	read(t, ws, "snapshot")

	server.mu.Lock()
	scene := firstScene(server.sessions[gm["session"]])
	sceneID, floorID := scene.ID, firstFloorID(scene)
	server.mu.Unlock()
	expected := uint64(0)
	command := Command{
		Type: "addWalkableRect", Client: "walkable", Seq: 1, SceneID: sceneID,
		FloorID: floorID, ExpectedGeometryRevision: &expected,
		WalkableBounds: &WalkableBounds{X: 10, Y: 20, Width: 100, Height: 80},
	}
	if err := ws.WriteJSON(command); err != nil {
		t.Fatal(err)
	}
	read(t, ws, "snapshot")
	ack := read(t, ws, "ack")
	if string(ack["error"]) != `""` {
		t.Fatalf("add rejected: %s", ack["error"])
	}

	server.mu.Lock()
	floor := server.sessions[gm["session"]].Scenes[sceneID].Floors[floorID]
	writes := server.writes
	server.mu.Unlock()
	if floor.GeometryRevision != 1 || len(floor.WalkableComponents) != 1 {
		t.Fatalf("geometry was not committed: %#v", floor)
	}

	// An exact retry is acknowledged from the persisted receipt and does not
	// execute clipping or write the state again.
	if err := ws.WriteJSON(command); err != nil {
		t.Fatal(err)
	}
	ack = read(t, ws, "ack")
	if string(ack["error"]) != `""` {
		t.Fatalf("exact retry rejected: %s", ack["error"])
	}
	server.mu.Lock()
	if server.writes != writes || server.sessions[gm["session"]].Scenes[sceneID].Floors[floorID].GeometryRevision != 1 {
		t.Fatal("exact retry executed the operation again")
	}
	server.mu.Unlock()

	command.WalkableBounds.Width = 101
	if err := ws.WriteJSON(command); err != nil {
		t.Fatal(err)
	}
	read(t, ws, "fatal")
}

func TestWalkableCommandRejectsStaleRevisionAndResyncs(t *testing.T) {
	server := testServer(t, t.TempDir())
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Walkable conflict"})
	ws := dial(t, host.URL, gm)
	read(t, ws, "snapshot")

	server.mu.Lock()
	scene := firstScene(server.sessions[gm["session"]])
	sceneID, floorID := scene.ID, firstFloorID(scene)
	server.mu.Unlock()
	zero := uint64(0)
	first := Command{Type: "addWalkableRect", Client: "walkable", Seq: 1, SceneID: sceneID, FloorID: floorID, ExpectedGeometryRevision: &zero, WalkableBounds: &WalkableBounds{Width: 100, Height: 100}}
	ws.WriteJSON(first)
	read(t, ws, "snapshot")
	read(t, ws, "ack")

	stale := Command{Type: "addWalkableRect", Client: "walkable", Seq: 2, SceneID: sceneID, FloorID: floorID, ExpectedGeometryRevision: &zero, WalkableBounds: &WalkableBounds{X: 200, Width: 100, Height: 100}}
	if err := ws.WriteJSON(stale); err != nil {
		t.Fatal(err)
	}
	snapshot := read(t, ws, "snapshot")
	var floors map[string]Floor
	if err := json.Unmarshal(snapshot["floors"], &floors); err != nil {
		t.Fatal(err)
	}
	ack := read(t, ws, "ack")
	var issue string
	if err := json.Unmarshal(ack["error"], &issue); err != nil || issue == "" {
		t.Fatalf("stale command was not rejected: %s", ack["error"])
	}
	if floors[floorID].GeometryRevision != 1 || len(floors[floorID].WalkableComponents) != 1 {
		t.Fatalf("conflict snapshot is not authoritative: %#v", floors[floorID])
	}
	server.mu.Lock()
	floor := server.sessions[gm["session"]].Scenes[sceneID].Floors[floorID]
	server.mu.Unlock()
	if floor.GeometryRevision != 1 || len(floor.WalkableComponents) != 1 {
		t.Fatalf("stale command partially changed state: %#v", floor)
	}
}

func TestWalkableCommandLimitRejectionIsAtomic(t *testing.T) {
	server := testServer(t, t.TempDir())
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Walkable limits"})
	ws := dial(t, host.URL, gm)
	read(t, ws, "snapshot")

	server.mu.Lock()
	scene := firstScene(server.sessions[gm["session"]])
	sceneID, floorID := scene.ID, firstFloorID(scene)
	floor := scene.Floors[floorID]
	floor.WalkableMode = walkableModeRestricted
	floor.WalkableComponents = make([]WalkableComponent, 0, maxWalkableComponents)
	for index := range maxWalkableComponents {
		x := float64(index * 20)
		polygon, err := rectanglePolygon(AABB{MinX: x, MinY: 0, MaxX: x + 10, MaxY: 10})
		if err != nil {
			server.mu.Unlock()
			t.Fatal(err)
		}
		floor.WalkableComponents = append(floor.WalkableComponents, WalkableComponent{ID: id(), Polygon: polygon})
	}
	scene.Floors[floorID] = floor
	initialSceneRevision := scene.Revision
	server.mu.Unlock()

	expected := uint64(0)
	command := Command{Type: "addWalkableRect", Client: "walkable-limit", Seq: 1, SceneID: sceneID, FloorID: floorID, ExpectedGeometryRevision: &expected, WalkableBounds: &WalkableBounds{X: 100_000, Width: 10, Height: 10}}
	if err := ws.WriteJSON(command); err != nil {
		t.Fatal(err)
	}
	read(t, ws, "snapshot")
	ack := read(t, ws, "ack")
	var issue string
	if err := json.Unmarshal(ack["error"], &issue); err != nil || issue == "" {
		t.Fatalf("limit command was not rejected: %s", ack["error"])
	}
	server.mu.Lock()
	floor = server.sessions[gm["session"]].Scenes[sceneID].Floors[floorID]
	currentSceneRevision := server.sessions[gm["session"]].Scenes[sceneID].Revision
	server.mu.Unlock()
	if len(floor.WalkableComponents) != maxWalkableComponents || floor.GeometryRevision != 0 || currentSceneRevision != initialSceneRevision {
		t.Fatalf("limit rejection partially changed state: components=%d geometryRevision=%d sceneRevision=%d", len(floor.WalkableComponents), floor.GeometryRevision, currentSceneRevision)
	}
}

func TestWalkablePersistenceRoundTrip(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Walkable persistence"})
	ws := dial(t, host.URL, gm)
	read(t, ws, "snapshot")

	server.mu.Lock()
	scene := firstScene(server.sessions[gm["session"]])
	sceneID, floorID := scene.ID, firstFloorID(scene)
	server.mu.Unlock()
	revision0, revision1, revision2 := uint64(0), uint64(1), uint64(2)
	commands := []Command{
		{Type: "setWalkableMode", Client: "persist-walkable", Seq: 1, SceneID: sceneID, FloorID: floorID, ExpectedGeometryRevision: &revision0, WalkableMode: walkableModeRestricted},
		{Type: "addWalkableRect", Client: "persist-walkable", Seq: 2, SceneID: sceneID, FloorID: floorID, ExpectedGeometryRevision: &revision1, WalkableBounds: &WalkableBounds{Width: 100, Height: 100}},
		{Type: "subtractWalkableRect", Client: "persist-walkable", Seq: 3, SceneID: sceneID, FloorID: floorID, ExpectedGeometryRevision: &revision2, WalkableBounds: &WalkableBounds{X: 20, Y: 20, Width: 20, Height: 20}},
	}
	for _, command := range commands {
		if err := ws.WriteJSON(command); err != nil {
			t.Fatal(err)
		}
		read(t, ws, "snapshot")
		ack := read(t, ws, "ack")
		if string(ack["error"]) != `""` {
			t.Fatalf("%s rejected: %s", command.Type, ack["error"])
		}
	}

	server.mu.Lock()
	before := server.sessions[gm["session"]].Scenes[sceneID].Floors[floorID]
	server.mu.Unlock()
	restarted, err := testServerNoFatal(root)
	if err != nil {
		t.Fatal(err)
	}
	after := restarted.sessions[gm["session"]].Scenes[sceneID].Floors[floorID]
	if before.WalkableMode != walkableModeRestricted || before.GeometryRevision != 3 || len(before.WalkableComponents) != 1 || len(before.WalkableComponents[0].Polygon.Holes) != 1 {
		t.Fatalf("unexpected committed geometry: %#v", before)
	}
	if after.WalkableMode != before.WalkableMode || after.GeometryRevision != before.GeometryRevision || !walkableComponentSetsEqual(after.WalkableComponents, before.WalkableComponents) {
		t.Fatalf("walkable state changed after restart: before=%#v after=%#v", before, after)
	}
}

func TestWalkablePersistenceRejectsNonNormalizedGeometry(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Broken walkable"})
	host.Close()

	server.mu.Lock()
	scene := firstScene(server.sessions[gm["session"]])
	floorID := firstFloorID(scene)
	floor := scene.Floors[floorID]
	floor.WalkableMode = walkableModeRestricted
	floor.WalkableComponents = []WalkableComponent{{
		ID:      "clockwise",
		Polygon: Polygon{Outer: []ScenePoint{{X: 0, Y: 0}, {X: 0, Y: 10}, {X: 10, Y: 10}, {X: 10, Y: 0}}},
	}}
	scene.Floors[floorID] = floor
	server.dirty = true
	if err := server.saveLocked(); err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	server.mu.Unlock()

	if _, err := testServerNoFatal(root); err == nil || !strings.Contains(err.Error(), "invalid scene") {
		t.Fatalf("non-normalized geometry was accepted: %v", err)
	}
}

func TestRenderBoundsCommandsPersistAndRejectStaleRevision(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Render bounds"})
	ws := dial(t, host.URL, gm)
	read(t, ws, "snapshot")

	server.mu.Lock()
	scene := firstScene(server.sessions[gm["session"]])
	sceneID, floorID := scene.ID, firstFloorID(scene)
	server.mu.Unlock()
	zero := uint64(0)
	polygon := Polygon{Outer: []ScenePoint{{0, 0}, {200, 0}, {200, 100}, {0, 100}}}
	set := Command{Type: "setRenderBounds", Client: "render-bounds", Seq: 1, SceneID: sceneID, FloorID: floorID, ExpectedGeometryRevision: &zero, RenderBounds: &polygon}
	if err := ws.WriteJSON(set); err != nil {
		t.Fatal(err)
	}
	read(t, ws, "snapshot")
	if ack := read(t, ws, "ack"); string(ack["error"]) != `""` {
		t.Fatalf("set rejected: %s", ack["error"])
	}

	server.mu.Lock()
	floor := server.sessions[gm["session"]].Scenes[sceneID].Floors[floorID]
	server.mu.Unlock()
	if floor.RenderBounds == nil || floor.GeometryRevision != 1 {
		t.Fatalf("render bounds were not committed: %#v", floor)
	}
	restarted, err := testServerNoFatal(root)
	if err != nil {
		t.Fatal(err)
	}
	restored := restarted.sessions[gm["session"]].Scenes[sceneID].Floors[floorID]
	if restored.RenderBounds == nil || !polygonsEqual(*restored.RenderBounds, *floor.RenderBounds) || restored.GeometryRevision != 1 {
		t.Fatalf("render bounds did not survive restart: %#v", restored)
	}

	stale := Command{Type: "clearRenderBounds", Client: "render-bounds", Seq: 2, SceneID: sceneID, FloorID: floorID, ExpectedGeometryRevision: &zero}
	if err := ws.WriteJSON(stale); err != nil {
		t.Fatal(err)
	}
	read(t, ws, "snapshot")
	ack := read(t, ws, "ack")
	var issue string
	if err := json.Unmarshal(ack["error"], &issue); err != nil || issue == "" {
		t.Fatalf("stale clear was not rejected: %s", ack["error"])
	}
	server.mu.Lock()
	floor = server.sessions[gm["session"]].Scenes[sceneID].Floors[floorID]
	server.mu.Unlock()
	if floor.RenderBounds == nil || floor.GeometryRevision != 1 {
		t.Fatalf("stale clear changed state: %#v", floor)
	}

	one := uint64(1)
	clear := Command{Type: "clearRenderBounds", Client: "render-bounds", Seq: 3, SceneID: sceneID, FloorID: floorID, ExpectedGeometryRevision: &one}
	if err := ws.WriteJSON(clear); err != nil {
		t.Fatal(err)
	}
	read(t, ws, "snapshot")
	if ack = read(t, ws, "ack"); string(ack["error"]) != `""` {
		t.Fatalf("clear rejected: %s", ack["error"])
	}
	server.mu.Lock()
	floor = server.sessions[gm["session"]].Scenes[sceneID].Floors[floorID]
	server.mu.Unlock()
	if floor.RenderBounds != nil || floor.GeometryRevision != 2 {
		t.Fatalf("clear was not committed: %#v", floor)
	}
}
