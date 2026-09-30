package main

import "testing"

func movementTestScene(t *testing.T, mode string, polygons ...Polygon) (*Scene, string) {
	t.Helper()
	floorID := "floor"
	components := make([]WalkableComponent, len(polygons))
	for i, input := range polygons {
		polygon, err := normalizePolygon(input)
		if err != nil {
			t.Fatal(err)
		}
		components[i] = WalkableComponent{ID: string(rune('a' + i)), Polygon: polygon}
	}
	return &Scene{
		Floors: map[string]Floor{floorID: {ID: floorID, WalkableMode: mode, WalkableComponents: components}},
		Tokens: map[string]Token{},
	}, floorID
}

func rectangleInput(left, top, right, bottom float64) Polygon {
	return Polygon{Outer: []ScenePoint{{left, top}, {right, top}, {right, bottom}, {left, bottom}}}
}

func TestCanMoveTokenSegmentWalkableGeometry(t *testing.T) {
	t.Run("unrestricted", func(t *testing.T) {
		scene, floorID := movementTestScene(t, walkableModeUnrestricted)
		if !CanMoveTokenSegment(scene, floorID, ScenePoint{-100, -100}, ScenePoint{100, 100}) {
			t.Fatal("unrestricted floor rejected movement")
		}
	})

	t.Run("inside", func(t *testing.T) {
		scene, floorID := movementTestScene(t, walkableModeRestricted, rectangleInput(0, 0, 100, 100))
		if !CanMoveTokenSegment(scene, floorID, ScenePoint{10, 10}, ScenePoint{90, 90}) {
			t.Fatal("inside segment was rejected")
		}
	})

	t.Run("hole", func(t *testing.T) {
		polygon := rectangleInput(0, 0, 100, 100)
		polygon.Holes = [][]ScenePoint{rectangleInput(40, 40, 60, 60).Outer}
		scene, floorID := movementTestScene(t, walkableModeRestricted, polygon)
		if CanMoveTokenSegment(scene, floorID, ScenePoint{10, 50}, ScenePoint{90, 50}) {
			t.Fatal("segment through a hole was accepted")
		}
	})

	t.Run("concave gap", func(t *testing.T) {
		scene, floorID := movementTestScene(t, walkableModeRestricted, Polygon{Outer: []ScenePoint{{0, 0}, {100, 0}, {100, 30}, {30, 30}, {30, 100}, {0, 100}}})
		if CanMoveTokenSegment(scene, floorID, ScenePoint{15, 80}, ScenePoint{80, 15}) {
			t.Fatal("segment crossing a concave notch was accepted")
		}
	})

	t.Run("separate components", func(t *testing.T) {
		scene, floorID := movementTestScene(t, walkableModeRestricted, rectangleInput(0, 0, 40, 40), rectangleInput(60, 0, 100, 40))
		if CanMoveTokenSegment(scene, floorID, ScenePoint{20, 20}, ScenePoint{80, 20}) {
			t.Fatal("movement across a gap was accepted")
		}
	})

	t.Run("shared edge", func(t *testing.T) {
		scene, floorID := movementTestScene(t, walkableModeRestricted, rectangleInput(0, 0, 50, 50), rectangleInput(50, 0, 100, 50))
		if !CanMoveTokenSegment(scene, floorID, ScenePoint{25, 25}, ScenePoint{75, 25}) {
			t.Fatal("movement across a shared edge was rejected")
		}
	})

	t.Run("corner contact", func(t *testing.T) {
		scene, floorID := movementTestScene(t, walkableModeRestricted, rectangleInput(0, 0, 50, 50), rectangleInput(50, 50, 100, 100))
		if CanMoveTokenSegment(scene, floorID, ScenePoint{25, 25}, ScenePoint{75, 75}) {
			t.Fatal("zero-width corner passage was accepted")
		}
	})

	t.Run("starts outside", func(t *testing.T) {
		scene, floorID := movementTestScene(t, walkableModeRestricted, rectangleInput(0, 0, 100, 100))
		if CanMoveTokenSegment(scene, floorID, ScenePoint{-1, 50}, ScenePoint{50, 50}) {
			t.Fatal("token already outside walkable area was allowed to move")
		}
	})
}

func TestCanMoveTokenSegmentCombinesRenderBoundsIndependently(t *testing.T) {
	tests := []struct {
		name         string
		walkable     Polygon
		renderBounds *Polygon
		from, to     ScenePoint
		want         bool
	}{
		{
			name:     "nil render bounds",
			walkable: rectangleInput(0, 0, 100, 100),
			from:     ScenePoint{10, 50}, to: ScenePoint{90, 50}, want: true,
		},
		{
			name:         "render bounds broader than walkable",
			walkable:     rectangleInput(0, 0, 100, 100),
			renderBounds: polygonPointer(rectangleInput(-100, -100, 200, 200)),
			from:         ScenePoint{10, 50}, to: ScenePoint{90, 50}, want: true,
		},
		{
			name:         "render bounds narrower than walkable",
			walkable:     rectangleInput(0, 0, 100, 100),
			renderBounds: polygonPointer(rectangleInput(20, 20, 80, 80)),
			from:         ScenePoint{30, 50}, to: ScenePoint{90, 50}, want: false,
		},
		{
			name:         "partially overlapping restrictions",
			walkable:     rectangleInput(0, 0, 100, 100),
			renderBounds: polygonPointer(rectangleInput(50, -20, 150, 120)),
			from:         ScenePoint{60, 50}, to: ScenePoint{40, 50}, want: false,
		},
		{
			name:         "render bounds without walkable restriction",
			walkable:     Polygon{},
			renderBounds: polygonPointer(rectangleInput(0, 0, 100, 100)),
			from:         ScenePoint{10, 50}, to: ScenePoint{110, 50}, want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode := walkableModeRestricted
			polygons := []Polygon{test.walkable}
			if len(test.walkable.Outer) == 0 {
				mode, polygons = walkableModeUnrestricted, nil
			}
			scene, floorID := movementTestScene(t, mode, polygons...)
			floor := scene.Floors[floorID]
			if test.renderBounds != nil {
				normalized, err := normalizePolygon(*test.renderBounds)
				if err != nil {
					t.Fatal(err)
				}
				floor.RenderBounds = &normalized
				scene.Floors[floorID] = floor
			}
			if got := CanMoveTokenSegment(scene, floorID, test.from, test.to); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func polygonPointer(polygon Polygon) *Polygon { return &polygon }

func TestCanOccupyTokenPointUsesWalkableAndRenderBounds(t *testing.T) {
	walkable := rectangleInput(0, 0, 100, 100)
	walkable.Holes = [][]ScenePoint{rectangleInput(40, 40, 60, 60).Outer}
	scene, floorID := movementTestScene(t, walkableModeRestricted, walkable)
	floor := scene.Floors[floorID]
	renderBounds, err := normalizePolygon(rectangleInput(0, 0, 80, 80))
	if err != nil {
		t.Fatal(err)
	}
	floor.RenderBounds = &renderBounds
	scene.Floors[floorID] = floor

	for _, test := range []struct {
		name  string
		point ScenePoint
		want  bool
	}{
		{name: "inside", point: ScenePoint{20, 20}, want: true},
		{name: "walkable hole", point: ScenePoint{50, 50}, want: false},
		{name: "outside render bounds", point: ScenePoint{90, 20}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := CanOccupyTokenPoint(scene, floorID, test.point); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestMovementReusesPreparedWalkableBoundsAcrossTokenRevisions(t *testing.T) {
	scene, floorID := movementTestScene(t, walkableModeRestricted, rectangleInput(0, 0, 100, 100))
	token := Token{ID: "token", FloorID: floorID, LayerID: layerIDByKind(scene, floorID, layerKindTokens), X: 10, Y: 10, Size: 32}
	scene.Tokens[token.ID] = token
	scene.rebuildRuntime()
	if !CanMoveTokenSegment(scene, floorID, ScenePoint{10, 10}, ScenePoint{20, 20}) {
		t.Fatal("initial move was rejected")
	}
	runtime := scene.ensureRuntime()
	prepared, ok := runtime.preparedWalkable[floorID]
	if !ok || len(prepared.Components) != 1 {
		t.Fatal("walkable bounds were not prepared")
	}
	firstBacking := &prepared.Components[0]

	// Normal token updates advance Scene.Revision without changing floor geometry;
	// applyTokenRuntimeChange keeps the runtime alive, so prepared geometry should
	// remain reusable instead of rescanning every polygon on each pointer move.
	next := token
	next.X, next.Y = 20, 20
	scene.Tokens[token.ID] = next
	scene.Revision++
	scene.applyTokenRuntimeChange(token, true, next, true)
	if !CanMoveTokenSegment(scene, floorID, ScenePoint{20, 20}, ScenePoint{30, 30}) {
		t.Fatal("second move was rejected")
	}
	reused := scene.ensureRuntime().preparedWalkable[floorID]
	if len(reused.Components) != 1 || &reused.Components[0] != firstBacking {
		t.Fatal("prepared walkable bounds were rebuilt without a geometry change")
	}

	floor := scene.Floors[floorID]
	floor.GeometryRevision++
	scene.Floors[floorID] = floor
	if !CanMoveTokenSegment(scene, floorID, ScenePoint{30, 30}, ScenePoint{40, 40}) {
		t.Fatal("move after geometry revision was rejected")
	}
	refreshed := scene.ensureRuntime().preparedWalkable[floorID]
	if refreshed.Revision != floor.GeometryRevision {
		t.Fatal("prepared walkable bounds were not refreshed for new geometry revision")
	}
}
