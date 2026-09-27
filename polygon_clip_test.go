package main

import "testing"

func mustRectangle(t *testing.T, minX, minY, maxX, maxY float64) Polygon {
	t.Helper()
	polygon, err := rectanglePolygon(AABB{MinX: minX, MinY: minY, MaxX: maxX, MaxY: maxY})
	if err != nil {
		t.Fatal(err)
	}
	return polygon
}

func TestPolygonClipUnionTopology(t *testing.T) {
	for name, second := range map[string]Polygon{
		"overlap":     mustRectangle(t, 5, 5, 15, 15),
		"shared edge": mustRectangle(t, 10, 0, 20, 10),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := unionPolygons([]Polygon{mustRectangle(t, 0, 0, 10, 10), second})
			if err != nil || len(result) != 1 {
				t.Fatalf("got %d polygons, err %v", len(result), err)
			}
		})
	}
	result, err := unionPolygons([]Polygon{mustRectangle(t, 0, 0, 10, 10), mustRectangle(t, 10, 10, 20, 20)})
	if err != nil || len(result) != 2 {
		t.Fatalf("corner touch must stay disconnected: %d, %v", len(result), err)
	}
}

func TestPolygonClipDifferenceHoleAndSplit(t *testing.T) {
	hole, err := differencePolygons([]Polygon{mustRectangle(t, 0, 0, 20, 20)}, []Polygon{mustRectangle(t, 5, 5, 15, 15)})
	if err != nil || len(hole) != 1 || len(hole[0].Holes) != 1 {
		t.Fatalf("hole result: %#v, %v", hole, err)
	}
	split, err := differencePolygons([]Polygon{mustRectangle(t, 0, 0, 20, 20)}, []Polygon{mustRectangle(t, 9, -1, 11, 21)})
	if err != nil || len(split) != 2 {
		t.Fatalf("split result: %#v, %v", split, err)
	}
}

func TestPolygonClipPreservesExistingHole(t *testing.T) {
	polygon := mustRectangle(t, 0, 0, 20, 20)
	polygon.Holes = [][]ScenePoint{mustRectangle(t, 5, 5, 15, 15).Outer}
	result, err := unionPolygons([]Polygon{polygon})
	if err != nil || len(result) != 1 || len(result[0].Holes) != 1 {
		t.Fatalf("existing hole was not preserved: %#v, %v", result, err)
	}
}
