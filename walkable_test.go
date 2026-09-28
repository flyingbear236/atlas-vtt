package main

import "testing"

func rectComponent(t *testing.T, componentID string, minX, minY, maxX, maxY float64) WalkableComponent {
	t.Helper()
	return WalkableComponent{ID: componentID, Polygon: mustRectangle(t, minX, minY, maxX, maxY)}
}

func sequenceIDs(values ...string) componentIDGenerator {
	index := 0
	return func() string {
		value := values[index]
		index++
		return value
	}
}

func TestAddWalkableRectMergesLShapeAndSharedEdge(t *testing.T) {
	result, changed, err := addWalkableRect(
		[]WalkableComponent{rectComponent(t, "existing", 0, 0, 10, 5)},
		AABB{MinX: 0, MinY: 0, MaxX: 5, MaxY: 10}, sequenceIDs("unused"),
	)
	if err != nil || !changed || len(result) != 1 || result[0].ID != "existing" || len(result[0].Polygon.Outer) != 6 {
		t.Fatalf("L union: %#v, changed=%v, err=%v", result, changed, err)
	}

	result, changed, err = addWalkableRect(result, AABB{MinX: 10, MinY: 0, MaxX: 15, MaxY: 5}, sequenceIDs("unused"))
	if err != nil || !changed || len(result) != 1 || result[0].ID != "existing" {
		t.Fatalf("shared-edge union: %#v, changed=%v, err=%v", result, changed, err)
	}
}

func TestAddWalkableRectCornerAndDistantStaySeparate(t *testing.T) {
	for name, bounds := range map[string]AABB{
		"corner":  {MinX: 10, MinY: 10, MaxX: 20, MaxY: 20},
		"distant": {MinX: 30, MinY: 30, MaxX: 40, MaxY: 40},
	} {
		t.Run(name, func(t *testing.T) {
			result, changed, err := addWalkableRect([]WalkableComponent{rectComponent(t, "old", 0, 0, 10, 10)}, bounds, sequenceIDs("new"))
			if err != nil || !changed || len(result) != 2 || result[0].ID != "new" || result[1].ID != "old" {
				t.Fatalf("result: %#v, changed=%v, err=%v", result, changed, err)
			}
		})
	}
}

func TestAddWalkableRectContainedIsNoOp(t *testing.T) {
	called := false
	components := []WalkableComponent{rectComponent(t, "old", 0, 0, 20, 20)}
	result, changed, err := addWalkableRect(components, AABB{MinX: 5, MinY: 5, MaxX: 10, MaxY: 10}, func() string {
		called = true
		return "new"
	})
	if err != nil || changed || called || !walkableComponentSetsEqual(result, components) {
		t.Fatalf("contained add changed state: %#v, changed=%v, called=%v, err=%v", result, changed, called, err)
	}
}

func TestAddWalkableRectInsideHoleCreatesIsland(t *testing.T) {
	outer := mustRectangle(t, 0, 0, 30, 30)
	outer.Holes = [][]ScenePoint{mustRectangle(t, 5, 5, 25, 25).Outer}
	components := []WalkableComponent{{ID: "outer", Polygon: outer}}
	result, changed, err := addWalkableRect(components, AABB{MinX: 10, MinY: 10, MaxX: 15, MaxY: 15}, sequenceIDs("island"))
	if err != nil || !changed || len(result) != 2 {
		t.Fatalf("hole add: %#v, changed=%v, err=%v", result, changed, err)
	}
	ids := map[string]bool{result[0].ID: true, result[1].ID: true}
	if !ids["outer"] || !ids["island"] {
		t.Fatalf("component IDs changed: %#v", result)
	}
}

func TestAddWalkableRectMergeKeepsDeterministicID(t *testing.T) {
	components := []WalkableComponent{
		rectComponent(t, "z-component", 0, 0, 5, 5),
		rectComponent(t, "a-component", 10, 0, 15, 5),
	}
	result, changed, err := addWalkableRect(components, AABB{MinX: 5, MinY: 0, MaxX: 10, MaxY: 5}, sequenceIDs("unused"))
	if err != nil || !changed || len(result) != 1 || result[0].ID != "a-component" {
		t.Fatalf("merge ID: %#v, changed=%v, err=%v", result, changed, err)
	}
}

func TestAddWalkableRectRejectsAtomically(t *testing.T) {
	components := []WalkableComponent{rectComponent(t, "old", 0, 0, 10, 10)}
	before := append([]ScenePoint(nil), components[0].Polygon.Outer...)
	if _, _, err := addWalkableRect(components, AABB{MinX: 5, MinY: 5, MaxX: 5, MaxY: 10}, sequenceIDs("new")); err == nil {
		t.Fatal("zero-width rectangle accepted")
	}
	if !ringsEqual(before, components[0].Polygon.Outer) {
		t.Fatal("failed operation mutated input")
	}
}

func TestSubtractWalkableRectCreatesHoleAndCutsEdge(t *testing.T) {
	components := []WalkableComponent{rectComponent(t, "area", 0, 0, 20, 20)}
	withHole, changed, err := subtractWalkableRect(components, AABB{MinX: 5, MinY: 5, MaxX: 15, MaxY: 15}, sequenceIDs("unused"))
	if err != nil || !changed || len(withHole) != 1 || withHole[0].ID != "area" || len(withHole[0].Polygon.Holes) != 1 {
		t.Fatalf("hole subtraction: %#v, changed=%v, err=%v", withHole, changed, err)
	}

	cutEdge, changed, err := subtractWalkableRect(components, AABB{MinX: 10, MinY: 10, MaxX: 25, MaxY: 25}, sequenceIDs("unused"))
	if err != nil || !changed || len(cutEdge) != 1 || cutEdge[0].ID != "area" || len(cutEdge[0].Polygon.Outer) != 6 {
		t.Fatalf("edge subtraction: %#v, changed=%v, err=%v", cutEdge, changed, err)
	}
}

func TestSubtractWalkableRectSplitAssignsDeterministicIDs(t *testing.T) {
	components := []WalkableComponent{rectComponent(t, "area", 0, 0, 20, 10)}
	result, changed, err := subtractWalkableRect(components, AABB{MinX: 9, MinY: -1, MaxX: 11, MaxY: 11}, sequenceIDs("new-piece"))
	if err != nil || !changed || len(result) != 2 {
		t.Fatalf("split: %#v, changed=%v, err=%v", result, changed, err)
	}
	byID := map[string]Polygon{result[0].ID: result[0].Polygon, result[1].ID: result[1].Polygon}
	if polygonBounds(byID["area"]).MinX != 0 || polygonBounds(byID["new-piece"]).MinX != 11 {
		t.Fatalf("old ID must remain on lexicographically first piece: %#v", result)
	}
}

func TestSubtractWalkableRectFullDeleteAndOutsideNoOp(t *testing.T) {
	components := []WalkableComponent{rectComponent(t, "area", 0, 0, 10, 10)}
	removed, changed, err := subtractWalkableRect(components, AABB{MinX: -1, MinY: -1, MaxX: 11, MaxY: 11}, sequenceIDs("unused"))
	if err != nil || !changed || len(removed) != 0 {
		t.Fatalf("full delete: %#v, changed=%v, err=%v", removed, changed, err)
	}

	called := false
	unchanged, changed, err := subtractWalkableRect(components, AABB{MinX: 20, MinY: 20, MaxX: 30, MaxY: 30}, func() string {
		called = true
		return "unused"
	})
	if err != nil || changed || called || !walkableComponentSetsEqual(components, unchanged) {
		t.Fatalf("outside subtraction: %#v, changed=%v, called=%v, err=%v", unchanged, changed, called, err)
	}
}

func TestSubtractWalkableRectAffectsSeveralComponents(t *testing.T) {
	components := []WalkableComponent{
		rectComponent(t, "left", 0, 0, 10, 10),
		rectComponent(t, "right", 20, 0, 30, 10),
	}
	result, changed, err := subtractWalkableRect(components, AABB{MinX: 5, MinY: -1, MaxX: 25, MaxY: 11}, sequenceIDs("unused"))
	if err != nil || !changed || len(result) != 2 {
		t.Fatalf("multi-component subtraction: %#v, changed=%v, err=%v", result, changed, err)
	}
	for _, component := range result {
		bounds := polygonBounds(component.Polygon)
		if bounds.MaxX-bounds.MinX != 5 {
			t.Fatalf("component %s was not cut once: %#v", component.ID, bounds)
		}
	}
}

func TestMoveWalkableComponentWithoutMergeMovesHoles(t *testing.T) {
	polygon := mustRectangle(t, 0, 0, 20, 20)
	polygon.Holes = [][]ScenePoint{mustRectangle(t, 5, 5, 10, 10).Outer}
	components := []WalkableComponent{
		{ID: "moving", Polygon: polygon},
		rectComponent(t, "still", 100, 100, 110, 110),
	}
	result, changed, err := moveWalkableComponent(components, "moving", 30, 40)
	if err != nil || !changed || len(result) != 2 {
		t.Fatalf("move: %#v, changed=%v, err=%v", result, changed, err)
	}
	byID := map[string]Polygon{result[0].ID: result[0].Polygon, result[1].ID: result[1].Polygon}
	moved := byID["moving"]
	if bounds := polygonBounds(moved); bounds.MinX != 30 || bounds.MinY != 40 || len(moved.Holes) != 1 {
		t.Fatalf("outer was not moved: %#v", moved)
	}
	if holeBounds := polygonBounds(Polygon{Outer: moved.Holes[0]}); holeBounds.MinX != 35 || holeBounds.MinY != 45 {
		t.Fatalf("hole was not moved with outer: %#v", moved.Holes[0])
	}
	if bounds := polygonBounds(byID["still"]); bounds.MinX != 100 || bounds.MinY != 100 {
		t.Fatalf("unrelated component moved: %#v", byID["still"])
	}
}

func TestMoveWalkableComponentMergesAtDestination(t *testing.T) {
	components := []WalkableComponent{
		rectComponent(t, "z-moving", 0, 0, 5, 5),
		rectComponent(t, "a-target", 10, 0, 15, 5),
	}
	result, changed, err := moveWalkableComponent(components, "z-moving", 5, 0)
	if err != nil || !changed || len(result) != 1 || result[0].ID != "a-target" {
		t.Fatalf("merged move: %#v, changed=%v, err=%v", result, changed, err)
	}
}

func TestMoveWalkableComponentContainedMergeUsesStableMinimumID(t *testing.T) {
	components := []WalkableComponent{
		rectComponent(t, "a-moving", 0, 0, 2, 2),
		rectComponent(t, "z-target", 10, 10, 20, 20),
	}
	result, changed, err := moveWalkableComponent(components, "a-moving", 12, 12)
	if err != nil || !changed || len(result) != 1 || result[0].ID != "a-moving" {
		t.Fatalf("contained merge ID: %#v, changed=%v, err=%v", result, changed, err)
	}
}

func TestMoveWalkableComponentCornerTouchDoesNotMerge(t *testing.T) {
	components := []WalkableComponent{
		rectComponent(t, "moving", 0, 0, 5, 5),
		rectComponent(t, "target", 10, 10, 15, 15),
	}
	result, changed, err := moveWalkableComponent(components, "moving", 5, 5)
	if err != nil || !changed || len(result) != 2 || !walkableComponentSetsEqual(result, []WalkableComponent{
		rectComponent(t, "moving", 5, 5, 10, 10),
		components[1],
	}) {
		t.Fatalf("corner-touch move: %#v, changed=%v, err=%v", result, changed, err)
	}
}

func TestMoveWalkableComponentZeroDeltaIsNoOp(t *testing.T) {
	components := []WalkableComponent{rectComponent(t, "area", 0, 0, 10, 10)}
	result, changed, err := moveWalkableComponent(components, "area", geometryQuantum/10, -geometryQuantum/10)
	if err != nil || changed || !walkableComponentSetsEqual(components, result) {
		t.Fatalf("quantized zero move: %#v, changed=%v, err=%v", result, changed, err)
	}
}

func TestDeleteLastWalkableComponentKeepsRestrictedMode(t *testing.T) {
	floor := Floor{
		ID:                 "floor",
		WalkableMode:       walkableModeRestricted,
		WalkableComponents: []WalkableComponent{rectComponent(t, "last", 0, 0, 10, 10)},
	}
	components, changed, err := deleteWalkableComponent(floor.WalkableComponents, "last")
	if err != nil || !changed || components == nil || len(components) != 0 {
		t.Fatalf("delete: %#v, changed=%v, err=%v", components, changed, err)
	}
	floor.WalkableComponents = components
	if floor.WalkableMode != walkableModeRestricted {
		t.Fatal("deleting the last component enabled unrestricted movement")
	}
}

func TestSetWalkableModeIsExplicitAndPreservesGeometry(t *testing.T) {
	floor := Floor{WalkableMode: walkableModeUnrestricted, WalkableComponents: []WalkableComponent{rectComponent(t, "area", 0, 0, 10, 10)}}
	restricted, changed, err := setWalkableMode(floor, walkableModeRestricted)
	if err != nil || !changed || restricted.WalkableMode != walkableModeRestricted || len(restricted.WalkableComponents) != 1 {
		t.Fatalf("set restricted: %#v, changed=%v, err=%v", restricted, changed, err)
	}
	if _, _, err := setWalkableMode(restricted, "automatic"); err == nil {
		t.Fatal("invalid mode accepted")
	}
}
