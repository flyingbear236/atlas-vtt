package main

import (
	"errors"
	"math"
	"testing"
)

func TestNormalizePolygonAndPredicates(t *testing.T) {
	polygon, err := normalizePolygon(Polygon{
		Outer: []ScenePoint{{0, 0}, {5, 0}, {10, 0}, {10, 10}, {5, 5}, {0, 10}, {0, 0}},
		Holes: [][]ScenePoint{{{2, 2}, {2, 4}, {4, 4}, {4, 2}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(polygon.Outer) != 5 || signedRingArea(polygon.Outer) <= 0 || signedRingArea(polygon.Holes[0]) >= 0 {
		t.Fatalf("polygon was not normalized: %#v", polygon)
	}
	for _, test := range []struct {
		point ScenePoint
		want  bool
	}{{ScenePoint{1, 1}, true}, {ScenePoint{3, 3}, false}, {ScenePoint{2, 3}, true}, {ScenePoint{7, 7}, true}, {ScenePoint{5, 8}, false}} {
		if got := pointInPolygon(polygon, test.point); got != test.want {
			t.Errorf("point %#v: got %v, want %v", test.point, got, test.want)
		}
	}
}

func TestNormalizePolygonRejectsInvalidGeometry(t *testing.T) {
	for name, polygon := range map[string]Polygon{
		"self intersection": {Outer: []ScenePoint{{0, 0}, {10, 10}, {0, 10}, {10, 0}}},
		"hole outside":      {Outer: []ScenePoint{{0, 0}, {10, 0}, {10, 10}, {0, 10}}, Holes: [][]ScenePoint{{{20, 20}, {21, 20}, {21, 21}, {20, 21}}}},
		"repeated vertex":   {Outer: []ScenePoint{{0, 0}, {10, 0}, {10, 10}, {0, 0}, {0, 10}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizePolygon(polygon); !errors.Is(err, errInvalidGeometry) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestSegmentsIntersectAtBoundary(t *testing.T) {
	if !segmentsIntersect(ScenePoint{0, 0}, ScenePoint{10, 0}, ScenePoint{10, 0}, ScenePoint{20, 0}) {
		t.Fatal("shared endpoint must intersect")
	}
	if segmentsIntersect(ScenePoint{0, 0}, ScenePoint{10, 0}, ScenePoint{11, 0}, ScenePoint{20, 0}) {
		t.Fatal("separated collinear segments intersect")
	}
}

func TestSetRenderBoundsValidation(t *testing.T) {
	floor := Floor{ID: "floor"}
	rectangle := rectangleInput(0, 0, 100, 80)
	updated, changed, err := setRenderBounds(floor, &rectangle)
	if err != nil || !changed || updated.RenderBounds == nil {
		t.Fatalf("rectangle rejected: changed=%v err=%v", changed, err)
	}
	if _, changed, err = setRenderBounds(updated, &rectangle); err != nil || changed {
		t.Fatalf("same bounds must be a no-op: changed=%v err=%v", changed, err)
	}

	concave := Polygon{Outer: []ScenePoint{{0, 0}, {100, 0}, {100, 40}, {40, 40}, {40, 100}, {0, 100}}}
	if _, changed, err = setRenderBounds(floor, &concave); err != nil || !changed {
		t.Fatalf("concave polygon rejected: changed=%v err=%v", changed, err)
	}

	invalid := map[string]Polygon{
		"self intersection": {Outer: []ScenePoint{{0, 0}, {100, 100}, {0, 100}, {100, 0}}},
		"zero area":         {Outer: []ScenePoint{{0, 0}, {50, 0}, {100, 0}}},
		"hole":              {Outer: rectangle.Outer, Holes: [][]ScenePoint{rectangleInput(20, 20, 40, 40).Outer}},
	}
	for name, polygon := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, _, err := setRenderBounds(floor, &polygon); !errors.Is(err, errInvalidGeometry) {
				t.Fatalf("got %v", err)
			}
		})
	}

	cleared, changed, err := clearRenderBounds(updated)
	if err != nil || !changed || cleared.RenderBounds != nil {
		t.Fatalf("clear failed: changed=%v err=%v", changed, err)
	}
	if _, changed, err = clearRenderBounds(cleared); err != nil || changed {
		t.Fatalf("repeated clear must be a no-op: changed=%v err=%v", changed, err)
	}
}

func TestGeometryCoordinateAndInputVertexLimits(t *testing.T) {
	if value, err := quantizeCoordinate(maxSceneDimension); err != nil || value != maxSceneDimension {
		t.Fatalf("maximum coordinate rejected: value=%v err=%v", value, err)
	}
	for _, value := range []float64{maxSceneDimension + geometryQuantum, -maxSceneDimension - geometryQuantum, math.Inf(1), math.NaN()} {
		if _, err := quantizeCoordinate(value); !errors.Is(err, errInvalidGeometry) {
			t.Fatalf("coordinate %v was accepted: %v", value, err)
		}
	}

	floor := Floor{ID: "floor"}
	tooMany := Polygon{Outer: make([]ScenePoint, maxGeometryInputVertices+1)}
	for index := range tooMany.Outer {
		angle := 2 * math.Pi * float64(index) / float64(len(tooMany.Outer))
		tooMany.Outer[index] = ScenePoint{X: 1000 * math.Cos(angle), Y: 1000 * math.Sin(angle)}
	}
	result, changed, err := setRenderBounds(floor, &tooMany)
	if !errors.Is(err, errGeometryLimitExceeded) || changed || result.RenderBounds != nil {
		t.Fatalf("input vertex limit was not atomic: changed=%v err=%v floor=%#v", changed, err, result)
	}
}
