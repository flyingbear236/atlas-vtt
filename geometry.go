package main

import (
	"errors"
	"math"
	"sort"
)

const (
	// One millimetre in the existing world coordinate system is precise enough
	// for editor geometry and keeps exact integer predicates safe at ±1e6.
	geometryQuantum = 0.001
	// Input is tighter because simple-ring validation is quadratic. Persisted
	// results may be larger, but remain bounded for snapshots and later edits.
	maxGeometryInputVertices          = 512
	maxGeometryRingVertices           = 4_096
	maxGeometryResultVertices         = 8_192
	maxGeometryOperationInputVertices = maxGeometryResultVertices + maxGeometryInputVertices
	maxWalkableComponents             = 512
	maxGeometryOperationComponents    = maxWalkableComponents + 1 // stored components plus the edited rectangle
	maxGeometryCommandBytes           = 12 << 10
)

var (
	errInvalidGeometry       = errors.New("invalid geometry")
	errGeometryLimitExceeded = errors.New("geometry limit exceeded")
)

// Polygon is stored in Floor world coordinates. Rings are implicitly closed;
// the first point is not repeated at the end.
type Polygon struct {
	Outer []ScenePoint   `json:"outer"`
	Holes [][]ScenePoint `json:"holes"`
}

// WalkableComponent is one connected piece of walkable geometry. Its stable
// ID lets later editor operations move or delete the component independently.
type WalkableComponent struct {
	ID      string  `json:"id"`
	Polygon Polygon `json:"polygon"`
}

type AABB struct {
	MinX float64
	MinY float64
	MaxX float64
	MaxY float64
}

type gridPoint struct {
	X int64
	Y int64
}

type pointLocation uint8

const (
	pointOutside pointLocation = iota
	pointInside
	pointBoundary
)

func quantizeCoordinate(value float64) (float64, error) {
	if !validNumber(value) || math.Abs(value) > maxSceneDimension {
		return 0, errInvalidGeometry
	}
	value = math.Round(value/geometryQuantum) * geometryQuantum
	if value == 0 {
		value = 0
	}
	return value, nil
}

func quantizePoint(point ScenePoint) (ScenePoint, error) {
	x, err := quantizeCoordinate(point.X)
	if err != nil {
		return ScenePoint{}, err
	}
	y, err := quantizeCoordinate(point.Y)
	if err != nil {
		return ScenePoint{}, err
	}
	return ScenePoint{X: x, Y: y}, nil
}

func pointGrid(point ScenePoint) gridPoint {
	return gridPoint{X: int64(math.Round(point.X / geometryQuantum)), Y: int64(math.Round(point.Y / geometryQuantum))}
}

func samePoint(a, b ScenePoint) bool { return pointGrid(a) == pointGrid(b) }

func cross(a, b, c gridPoint) int64 {
	return (b.X-a.X)*(c.Y-a.Y) - (b.Y-a.Y)*(c.X-a.X)
}

func between(a, b, c gridPoint) bool {
	return b.X >= min(a.X, c.X) && b.X <= max(a.X, c.X) && b.Y >= min(a.Y, c.Y) && b.Y <= max(a.Y, c.Y)
}

func onSegment(a, b, point gridPoint) bool {
	return cross(a, b, point) == 0 && between(a, point, b)
}

func segmentsIntersect(a, b, c, d ScenePoint) bool {
	ga, gb, gc, gd := pointGrid(a), pointGrid(b), pointGrid(c), pointGrid(d)
	o1, o2 := cross(ga, gb, gc), cross(ga, gb, gd)
	o3, o4 := cross(gc, gd, ga), cross(gc, gd, gb)
	if o1 == 0 && onSegment(ga, gb, gc) || o2 == 0 && onSegment(ga, gb, gd) ||
		o3 == 0 && onSegment(gc, gd, ga) || o4 == 0 && onSegment(gc, gd, gb) {
		return true
	}
	return (o1 < 0) != (o2 < 0) && (o3 < 0) != (o4 < 0)
}

func signedRingArea(ring []ScenePoint) float64 {
	area := 0.0
	for i, point := range ring {
		next := ring[(i+1)%len(ring)]
		area += point.X*next.Y - next.X*point.Y
	}
	return area / 2
}

func ringSimple(ring []ScenePoint) bool {
	if len(ring) < 3 {
		return false
	}
	for i := range ring {
		a, b := ring[i], ring[(i+1)%len(ring)]
		if samePoint(a, b) {
			return false
		}
		for j := i + 1; j < len(ring); j++ {
			if j == i+1 || i == 0 && j == len(ring)-1 {
				continue
			}
			c, d := ring[j], ring[(j+1)%len(ring)]
			if segmentsIntersect(a, b, c, d) {
				return false
			}
		}
	}
	return signedRingArea(ring) != 0
}

func normalizeRing(input []ScenePoint, counterClockwise bool) ([]ScenePoint, error) {
	if len(input) > maxGeometryRingVertices {
		return nil, errGeometryLimitExceeded
	}
	ring := make([]ScenePoint, 0, len(input))
	for _, raw := range input {
		point, err := quantizePoint(raw)
		if err != nil {
			return nil, err
		}
		if len(ring) == 0 || !samePoint(ring[len(ring)-1], point) {
			ring = append(ring, point)
		}
	}
	if len(ring) > 1 && samePoint(ring[0], ring[len(ring)-1]) {
		ring = ring[:len(ring)-1]
	}

	for changed := true; changed && len(ring) >= 3; {
		changed = false
		for i := 0; i < len(ring); i++ {
			a := pointGrid(ring[(i+len(ring)-1)%len(ring)])
			b := pointGrid(ring[i])
			c := pointGrid(ring[(i+1)%len(ring)])
			if cross(a, b, c) == 0 && between(a, b, c) {
				ring = append(ring[:i], ring[i+1:]...)
				changed = true
				break
			}
		}
	}
	if !ringSimple(ring) {
		return nil, errInvalidGeometry
	}
	if (signedRingArea(ring) > 0) != counterClockwise {
		reverseRing(ring)
	}
	rotateRingToMinimum(ring)
	return ring, nil
}

func reverseRing(ring []ScenePoint) {
	for left, right := 0, len(ring)-1; left < right; left, right = left+1, right-1 {
		ring[left], ring[right] = ring[right], ring[left]
	}
}

func rotateRingToMinimum(ring []ScenePoint) {
	minimum := 0
	for i := 1; i < len(ring); i++ {
		if pointLess(ring[i], ring[minimum]) {
			minimum = i
		}
	}
	if minimum == 0 {
		return
	}
	rotated := append(append(make([]ScenePoint, 0, len(ring)), ring[minimum:]...), ring[:minimum]...)
	copy(ring, rotated)
}

func pointLess(a, b ScenePoint) bool {
	ga, gb := pointGrid(a), pointGrid(b)
	return ga.X < gb.X || ga.X == gb.X && ga.Y < gb.Y
}

func ringLocation(ring []ScenePoint, point ScenePoint) pointLocation {
	gp := pointGrid(point)
	inside := false
	for i, a := range ring {
		b := ring[(i+1)%len(ring)]
		ga, gb := pointGrid(a), pointGrid(b)
		if onSegment(ga, gb, gp) {
			return pointBoundary
		}
		if (ga.Y > gp.Y) != (gb.Y > gp.Y) {
			x := float64(gb.X-ga.X)*float64(gp.Y-ga.Y)/float64(gb.Y-ga.Y) + float64(ga.X)
			if float64(gp.X) < x {
				inside = !inside
			}
		}
	}
	if inside {
		return pointInside
	}
	return pointOutside
}

func pointInPolygon(polygon Polygon, point ScenePoint) bool {
	outer := ringLocation(polygon.Outer, point)
	if outer == pointOutside {
		return false
	}
	if outer == pointBoundary {
		return true
	}
	for _, hole := range polygon.Holes {
		switch ringLocation(hole, point) {
		case pointInside:
			return false
		case pointBoundary:
			return true
		}
	}
	return true
}

func ringsIntersect(a, b []ScenePoint) bool {
	for i, p := range a {
		p2 := a[(i+1)%len(a)]
		for j, q := range b {
			if segmentsIntersect(p, p2, q, b[(j+1)%len(b)]) {
				return true
			}
		}
	}
	return false
}

func normalizePolygon(input Polygon) (Polygon, error) {
	if _, ok := polygonVertexCountWithin(input, maxGeometryResultVertices); !ok {
		return Polygon{}, errGeometryLimitExceeded
	}
	outer, err := normalizeRing(input.Outer, true)
	if err != nil {
		return Polygon{}, err
	}
	holes := make([][]ScenePoint, len(input.Holes))
	for i, inputHole := range input.Holes {
		hole, err := normalizeRing(inputHole, false)
		if err != nil {
			return Polygon{}, err
		}
		if ringLocation(outer, hole[0]) != pointInside || ringsIntersect(outer, hole) {
			return Polygon{}, errInvalidGeometry
		}
		holes[i] = hole
	}
	for i := range holes {
		for j := i + 1; j < len(holes); j++ {
			if ringsIntersect(holes[i], holes[j]) || ringLocation(holes[i], holes[j][0]) != pointOutside || ringLocation(holes[j], holes[i][0]) != pointOutside {
				return Polygon{}, errInvalidGeometry
			}
		}
	}
	sort.Slice(holes, func(i, j int) bool { return ringLess(holes[i], holes[j]) })
	return Polygon{Outer: outer, Holes: holes}, nil
}

func validateGeometryInputPolygon(polygon Polygon) error {
	if _, ok := polygonVertexCountWithin(polygon, maxGeometryInputVertices); !ok {
		return errGeometryLimitExceeded
	}
	return nil
}

func polygonVertexCountWithin(polygon Polygon, limit int) (int, bool) {
	if limit < 0 || len(polygon.Outer) > limit {
		return 0, false
	}
	total := len(polygon.Outer)
	for _, ring := range polygon.Holes {
		if len(ring) > limit-total {
			return 0, false
		}
		total += len(ring)
	}
	return total, true
}

func ringVertexCount(rings [][]ScenePoint) int {
	total := 0
	for _, ring := range rings {
		total += len(ring)
	}
	return total
}

func ringLess(a, b []ScenePoint) bool {
	for i := 0; i < min(len(a), len(b)); i++ {
		if samePoint(a[i], b[i]) {
			continue
		}
		return pointLess(a[i], b[i])
	}
	return len(a) < len(b)
}

func polygonBounds(polygon Polygon) AABB {
	bounds := AABB{MinX: polygon.Outer[0].X, MinY: polygon.Outer[0].Y, MaxX: polygon.Outer[0].X, MaxY: polygon.Outer[0].Y}
	for _, point := range polygon.Outer[1:] {
		bounds.MinX = math.Min(bounds.MinX, point.X)
		bounds.MinY = math.Min(bounds.MinY, point.Y)
		bounds.MaxX = math.Max(bounds.MaxX, point.X)
		bounds.MaxY = math.Max(bounds.MaxY, point.Y)
	}
	return bounds
}

func (bounds AABB) valid() bool {
	_, x1 := quantizeCoordinate(bounds.MinX)
	_, y1 := quantizeCoordinate(bounds.MinY)
	_, x2 := quantizeCoordinate(bounds.MaxX)
	_, y2 := quantizeCoordinate(bounds.MaxY)
	return x1 == nil && y1 == nil && x2 == nil && y2 == nil && bounds.MaxX > bounds.MinX && bounds.MaxY > bounds.MinY
}

func (bounds AABB) intersects(other AABB) bool {
	return bounds.MinX <= other.MaxX && bounds.MaxX >= other.MinX && bounds.MinY <= other.MaxY && bounds.MaxY >= other.MinY
}

func rectanglePolygon(bounds AABB) (Polygon, error) {
	if !bounds.valid() {
		return Polygon{}, errInvalidGeometry
	}
	return normalizePolygon(Polygon{Outer: []ScenePoint{
		{X: bounds.MinX, Y: bounds.MinY},
		{X: bounds.MaxX, Y: bounds.MinY},
		{X: bounds.MaxX, Y: bounds.MaxY},
		{X: bounds.MinX, Y: bounds.MaxY},
	}})
}
