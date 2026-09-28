package main

import (
	"math"
	"sort"
)

const segmentParameterEpsilon = 1e-9

// CanMoveTokenSegment validates the token centre along the complete movement
// segment. GM-only teleports and transitions remain separate command paths.
func CanMoveTokenSegment(scene *Scene, floorID string, from, to ScenePoint) bool {
	floor, ok := scene.Floors[floorID]
	if !ok {
		return false
	}
	if !walkableAllowsSegment(floor, from, to) {
		return false
	}
	if floor.RenderBounds == nil {
		return true
	}
	return segmentContainedInPolygon(from, to, *floor.RenderBounds)
}

func walkableAllowsSegment(floor Floor, from, to ScenePoint) bool {
	if floor.WalkableMode == walkableModeUnrestricted {
		return true
	}
	if floor.WalkableMode != walkableModeRestricted || len(floor.WalkableComponents) == 0 {
		return false
	}
	polygons := make([]Polygon, 0, len(floor.WalkableComponents))
	segmentBounds := AABB{
		MinX: math.Min(from.X, to.X), MinY: math.Min(from.Y, to.Y),
		MaxX: math.Max(from.X, to.X), MaxY: math.Max(from.Y, to.Y),
	}
	for _, component := range floor.WalkableComponents {
		if polygonBounds(component.Polygon).intersects(segmentBounds) {
			polygons = append(polygons, component.Polygon)
		}
	}
	return segmentContainedInPolygonUnion(from, to, polygons)
}

func segmentContainedInPolygon(from, to ScenePoint, polygon Polygon) bool {
	return segmentContainedInPolygonUnion(from, to, []Polygon{polygon})
}

// segmentContainedInPolygonUnion splits a segment at every boundary crossing
// and tests each open interval. This catches holes, concave notches and gaps;
// testing endpoints alone would miss all three.
func segmentContainedInPolygonUnion(from, to ScenePoint, polygons []Polygon) bool {
	if len(polygons) == 0 || len(polygonOwnersAt(polygons, from)) == 0 || len(polygonOwnersAt(polygons, to)) == 0 {
		return false
	}
	if samePoint(from, to) {
		return true
	}

	parameters := []float64{0, 1}
	for _, polygon := range polygons {
		parameters = appendRingIntersectionParameters(parameters, from, to, polygon.Outer)
		for _, hole := range polygon.Holes {
			parameters = appendRingIntersectionParameters(parameters, from, to, hole)
		}
	}
	sort.Float64s(parameters)
	parameters = uniqueParameters(parameters)

	var previousOwners []int
	for i := 0; i+1 < len(parameters); i++ {
		if parameters[i+1]-parameters[i] <= segmentParameterEpsilon {
			continue
		}
		midpoint := interpolatePoint(from, to, (parameters[i]+parameters[i+1])/2)
		owners := polygonOwnersAt(polygons, midpoint)
		if len(owners) == 0 {
			return false
		}
		if len(previousOwners) > 0 && !ownerSetsConnect(previousOwners, owners, polygons) {
			// Components touching only at a point do not form a passage.
			return false
		}
		previousOwners = owners
	}
	return true
}

func appendRingIntersectionParameters(parameters []float64, from, to ScenePoint, ring []ScenePoint) []float64 {
	for i, start := range ring {
		parameters = append(parameters, segmentEdgeParameters(from, to, start, ring[(i+1)%len(ring)])...)
	}
	return parameters
}

func segmentEdgeParameters(a, b, c, d ScenePoint) []float64 {
	rx, ry := b.X-a.X, b.Y-a.Y
	sx, sy := d.X-c.X, d.Y-c.Y
	denominator := rx*sy - ry*sx
	qx, qy := c.X-a.X, c.Y-a.Y
	if math.Abs(denominator) > segmentParameterEpsilon {
		t := (qx*sy - qy*sx) / denominator
		u := (qx*ry - qy*rx) / denominator
		if t >= -segmentParameterEpsilon && t <= 1+segmentParameterEpsilon && u >= -segmentParameterEpsilon && u <= 1+segmentParameterEpsilon {
			return []float64{min(1, max(0, t))}
		}
		return nil
	}
	if math.Abs(qx*ry-qy*rx) > segmentParameterEpsilon {
		return nil
	}
	lengthSquared := rx*rx + ry*ry
	if lengthSquared == 0 {
		return nil
	}
	t0 := ((c.X-a.X)*rx + (c.Y-a.Y)*ry) / lengthSquared
	t1 := ((d.X-a.X)*rx + (d.Y-a.Y)*ry) / lengthSquared
	result := make([]float64, 0, 2)
	for _, value := range []float64{t0, t1} {
		if value >= -segmentParameterEpsilon && value <= 1+segmentParameterEpsilon {
			result = append(result, min(1, max(0, value)))
		}
	}
	return result
}

func uniqueParameters(sorted []float64) []float64 {
	result := sorted[:0]
	for _, value := range sorted {
		if len(result) == 0 || value-result[len(result)-1] > segmentParameterEpsilon {
			result = append(result, value)
		}
	}
	return result
}

func interpolatePoint(from, to ScenePoint, parameter float64) ScenePoint {
	return ScenePoint{X: from.X + (to.X-from.X)*parameter, Y: from.Y + (to.Y-from.Y)*parameter}
}

func polygonOwnersAt(polygons []Polygon, point ScenePoint) []int {
	owners := make([]int, 0, 1)
	for i, polygon := range polygons {
		if pointInPolygon(polygon, point) {
			owners = append(owners, i)
		}
	}
	return owners
}

func ownerSetsConnect(previous, next []int, polygons []Polygon) bool {
	for _, left := range previous {
		for _, right := range next {
			if left == right || polygonsShareBoundarySegment(polygons[left], polygons[right]) {
				return true
			}
		}
	}
	return false
}

func polygonsShareBoundarySegment(left, right Polygon) bool {
	leftRings := append([][]ScenePoint{left.Outer}, left.Holes...)
	rightRings := append([][]ScenePoint{right.Outer}, right.Holes...)
	for _, a := range leftRings {
		for i, a0 := range a {
			a1 := a[(i+1)%len(a)]
			for _, b := range rightRings {
				for j, b0 := range b {
					if collinearOverlapHasLength(a0, a1, b0, b[(j+1)%len(b)]) {
						return true
					}
				}
			}
		}
	}
	return false
}

func collinearOverlapHasLength(a, b, c, d ScenePoint) bool {
	ga, gb, gc, gd := pointGrid(a), pointGrid(b), pointGrid(c), pointGrid(d)
	if cross(ga, gb, gc) != 0 || cross(ga, gb, gd) != 0 {
		return false
	}
	if abs64(gb.X-ga.X) >= abs64(gb.Y-ga.Y) {
		return min(gb.X, ga.X) < max(gc.X, gd.X) && min(gc.X, gd.X) < max(gb.X, ga.X)
	}
	return min(gb.Y, ga.Y) < max(gc.Y, gd.Y) && min(gc.Y, gd.Y) < max(gb.Y, ga.Y)
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
