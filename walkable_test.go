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
