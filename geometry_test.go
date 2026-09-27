package main

import (
	"errors"
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
