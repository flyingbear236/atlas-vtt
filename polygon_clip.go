package main

import (
	"sort"

	polyclip "github.com/ctessum/polyclip-go"
)

// unionPolygons and differencePolygons are the only places where Atlas knows
// the clipping library API. Inputs and outputs are normalized on Atlas' fixed
// coordinate grid so the rest of the server never depends on library details.
func unionPolygons(polygons []Polygon) ([]Polygon, error) {
	return clipPolygons(polygons, nil, polyclip.UNION)
}

func differencePolygons(subject, clipping []Polygon) ([]Polygon, error) {
	return clipPolygons(subject, clipping, polyclip.DIFFERENCE)
}

func clipPolygons(subject, clipping []Polygon, operation polyclip.Op) ([]Polygon, error) {
	if len(subject)+len(clipping) > maxWalkableComponents {
		return nil, errGeometryLimitExceeded
	}
	subjectClip, err := atlasPolygonsToClip(subject)
	if err != nil {
		return nil, err
	}
	if operation == polyclip.UNION {
		return clipPolygonToAtlas(subjectClip)
	}
	clippingClip, err := atlasPolygonsToClip(clipping)
	if err != nil {
		return nil, err
	}
	if len(subjectClip) == 0 || len(clippingClip) == 0 {
		return clipPolygonToAtlas(subjectClip)
	}
	return clipPolygonToAtlas(subjectClip.Construct(operation, clippingClip))
}

func atlasPolygonsToClip(polygons []Polygon) (polyclip.Polygon, error) {
	var result polyclip.Polygon
	total := 0
	for _, polygon := range polygons {
		normalized, err := normalizePolygon(polygon)
		if err != nil {
			return nil, err
		}
		total += len(normalized.Outer) + ringVertexCount(normalized.Holes)
		if total > maxGeometryTotalVertices {
			return nil, errGeometryLimitExceeded
		}
		current := polyclip.Polygon{ringToClip(normalized.Outer)}
		for _, hole := range normalized.Holes {
			current = append(current, ringToClip(hole))
		}
		if len(result) == 0 {
			result = current
		} else {
			result = result.Construct(polyclip.UNION, current)
		}
	}
	return result, nil
}

func ringToClip(ring []ScenePoint) polyclip.Contour {
	contour := make(polyclip.Contour, len(ring))
	for i, point := range ring {
		contour[i] = polyclip.Point{X: point.X, Y: point.Y}
	}
	return contour
}

type clipContour struct {
	ring   []ScenePoint
	area   float64
	parent int
	depth  int
}

func clipPolygonToAtlas(clipped polyclip.Polygon) ([]Polygon, error) {
	if len(clipped) > maxWalkableComponents*2 {
		return nil, errGeometryLimitExceeded
	}
	contours := make([]clipContour, 0, len(clipped))
	total := 0
	for _, contour := range clipped {
		raw := make([]ScenePoint, len(contour))
		for i, point := range contour {
			raw[i] = ScenePoint{X: point.X, Y: point.Y}
		}
		ring, err := normalizeRing(raw, true)
		if err != nil {
			return nil, err
		}
		total += len(ring)
		if total > maxGeometryTotalVertices {
			return nil, errGeometryLimitExceeded
		}
		contours = append(contours, clipContour{ring: ring, area: signedRingArea(ring), parent: -1})
	}

	for i := range contours {
		parentArea := mathMaxFloat
		for j := range contours {
			if i == j || contours[j].area <= contours[i].area || ringLocation(contours[j].ring, contours[i].ring[0]) != pointInside {
				continue
			}
			if contours[j].area < parentArea {
				contours[i].parent = j
				parentArea = contours[j].area
			}
		}
	}
	for i := range contours {
		depth := 0
		for parent := contours[i].parent; parent >= 0; parent = contours[parent].parent {
			depth++
			if depth > len(contours) {
				return nil, errInvalidGeometry
			}
		}
		contours[i].depth = depth
	}

	owners := make(map[int]int)
	result := make([]Polygon, 0, len(contours))
	for i, contour := range contours {
		if contour.depth%2 == 0 {
			owners[i] = len(result)
			result = append(result, Polygon{Outer: contour.ring, Holes: [][]ScenePoint{}})
		}
	}
	for _, contour := range contours {
		if contour.depth%2 == 0 {
			continue
		}
		parent := contour.parent
		for parent >= 0 && contours[parent].depth%2 != 0 {
			parent = contours[parent].parent
		}
		owner, ok := owners[parent]
		if !ok {
			return nil, errInvalidGeometry
		}
		hole := append([]ScenePoint(nil), contour.ring...)
		reverseRing(hole)
		rotateRingToMinimum(hole)
		result[owner].Holes = append(result[owner].Holes, hole)
	}
	for i := range result {
		normalized, err := normalizePolygon(result[i])
		if err != nil {
			return nil, err
		}
		result[i] = normalized
	}
	if len(result) > maxWalkableComponents {
		return nil, errGeometryLimitExceeded
	}
	sort.Slice(result, func(i, j int) bool { return ringLess(result[i].Outer, result[j].Outer) })
	return result, nil
}

const mathMaxFloat = 1.7976931348623157e+308
