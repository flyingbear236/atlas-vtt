package main

import (
	"sort"
)

type componentIDGenerator func() string

func commandWalkableAABB(bounds *WalkableBounds) (AABB, error) {
	if bounds == nil || !validWalkableBounds(*bounds) {
		return AABB{}, errInvalidGeometry
	}
	result := AABB{MinX: bounds.X, MinY: bounds.Y, MaxX: bounds.X + bounds.Width, MaxY: bounds.Y + bounds.Height}
	if !result.valid() {
		return AABB{}, errInvalidGeometry
	}
	return result, nil
}

// addWalkableRect returns an atomic replacement for the component slice. It
// assumes stored components were normalized when accepted; only AABB-selected
// candidates enter the comparatively expensive clipping operation.
func addWalkableRect(components []WalkableComponent, rectangle AABB, generateID componentIDGenerator) ([]WalkableComponent, bool, error) {
	if len(components) > maxWalkableComponents {
		return nil, false, errGeometryLimitExceeded
	}
	added, err := rectanglePolygon(rectangle)
	if err != nil {
		return nil, false, err
	}
	addedBounds := polygonBounds(added)

	existingIDs := make(map[string]bool, len(components)+1)
	unaffected := make([]WalkableComponent, 0, len(components))
	affected := make([]WalkableComponent, 0, 4)
	for _, component := range components {
		if component.ID == "" || existingIDs[component.ID] || len(component.Polygon.Outer) < 3 {
			return nil, false, errInvalidGeometry
		}
		existingIDs[component.ID] = true
		if polygonBounds(component.Polygon).intersects(addedBounds) {
			normalized, err := normalizePolygon(component.Polygon)
			if err != nil {
				return nil, false, err
			}
			affected = append(affected, WalkableComponent{ID: component.ID, Polygon: normalized})
		} else {
			unaffected = append(unaffected, component)
		}
	}

	inputs := make([]Polygon, 0, len(affected)+1)
	for _, component := range affected {
		inputs = append(inputs, component.Polygon)
	}
	inputs = append(inputs, added)
	union, err := unionPolygons(inputs)
	if err != nil {
		return nil, false, err
	}
	if len(unaffected)+len(union) > maxWalkableComponents {
		return nil, false, errGeometryLimitExceeded
	}

	result := append([]WalkableComponent(nil), unaffected...)
	usedIDs := make(map[string]bool, len(existingIDs)+1)
	for _, component := range unaffected {
		usedIDs[component.ID] = true
	}
	for _, polygon := range union {
		componentID := matchingComponentID(polygon, affected, usedIDs)
		if componentID == "" {
			componentID, err = freshComponentID(generateID, existingIDs)
			if err != nil {
				return nil, false, err
			}
			existingIDs[componentID] = true
		}
		usedIDs[componentID] = true
		result = append(result, WalkableComponent{ID: componentID, Polygon: polygon})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })

	if walkableComponentSetsEqual(components, result) {
		return append([]WalkableComponent(nil), components...), false, nil
	}
	return result, true, nil
}

// subtractWalkableRect applies one difference to every AABB candidate. A split
// keeps the old ID on the lexicographically first normalized piece; subsequent
// pieces receive fresh IDs. The input slice is never mutated.
func subtractWalkableRect(components []WalkableComponent, rectangle AABB, generateID componentIDGenerator) ([]WalkableComponent, bool, error) {
	if len(components) > maxWalkableComponents {
		return nil, false, errGeometryLimitExceeded
	}
	cut, err := rectanglePolygon(rectangle)
	if err != nil {
		return nil, false, err
	}
	cutBounds := polygonBounds(cut)
	existingIDs, err := componentIDs(components)
	if err != nil {
		return nil, false, err
	}

	result := make([]WalkableComponent, 0, len(components))
	for _, component := range components {
		if len(component.Polygon.Outer) < 3 {
			return nil, false, errInvalidGeometry
		}
		if !polygonBounds(component.Polygon).intersects(cutBounds) {
			result = append(result, component)
			continue
		}
		normalized, err := normalizePolygon(component.Polygon)
		if err != nil {
			return nil, false, err
		}
		pieces, err := differencePolygons([]Polygon{normalized}, []Polygon{cut})
		if err != nil {
			return nil, false, err
		}
		for index, piece := range pieces {
			componentID := component.ID
			if index > 0 {
				componentID, err = freshComponentID(generateID, existingIDs)
				if err != nil {
					return nil, false, err
				}
				existingIDs[componentID] = true
			}
			result = append(result, WalkableComponent{ID: componentID, Polygon: piece})
		}
	}
	if len(result) > maxWalkableComponents || walkableVertexCount(result) > maxGeometryTotalVertices {
		return nil, false, errGeometryLimitExceeded
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if walkableComponentSetsEqual(components, result) {
		return append([]WalkableComponent(nil), components...), false, nil
	}
	return result, true, nil
}

// moveWalkableComponent translates one complete polygon, including its holes,
// then unions it only with components near the destination. Tokens, assets and
// transitions are deliberately outside this pure geometry operation.
func moveWalkableComponent(components []WalkableComponent, componentID string, deltaX, deltaY float64) ([]WalkableComponent, bool, error) {
	if len(components) > maxWalkableComponents || componentID == "" {
		return nil, false, errInvalidGeometry
	}
	ids, err := componentIDs(components)
	if err != nil {
		return nil, false, err
	}
	if !ids[componentID] {
		return nil, false, errInvalidGeometry
	}
	deltaX, err = quantizeCoordinate(deltaX)
	if err != nil {
		return nil, false, err
	}
	deltaY, err = quantizeCoordinate(deltaY)
	if err != nil {
		return nil, false, err
	}
	if deltaX == 0 && deltaY == 0 {
		return append([]WalkableComponent(nil), components...), false, nil
	}

	var moved WalkableComponent
	remaining := make([]WalkableComponent, 0, len(components)-1)
	for _, component := range components {
		if component.ID == componentID {
			polygon, err := translatePolygon(component.Polygon, deltaX, deltaY)
			if err != nil {
				return nil, false, err
			}
			moved = WalkableComponent{ID: component.ID, Polygon: polygon}
		} else {
			remaining = append(remaining, component)
		}
	}
	movedBounds := polygonBounds(moved.Polygon)
	unaffected := make([]WalkableComponent, 0, len(remaining))
	affected := []WalkableComponent{moved}
	for _, component := range remaining {
		if len(component.Polygon.Outer) < 3 {
			return nil, false, errInvalidGeometry
		}
		if !polygonBounds(component.Polygon).intersects(movedBounds) {
			unaffected = append(unaffected, component)
			continue
		}
		normalized, err := normalizePolygon(component.Polygon)
		if err != nil {
			return nil, false, err
		}
		affected = append(affected, WalkableComponent{ID: component.ID, Polygon: normalized})
	}

	inputs := make([]Polygon, len(affected))
	for index, component := range affected {
		inputs[index] = component.Polygon
	}
	union, err := unionPolygons(inputs)
	if err != nil {
		return nil, false, err
	}
	result := append([]WalkableComponent(nil), unaffected...)
	usedIDs := make(map[string]bool, len(components))
	for _, component := range unaffected {
		usedIDs[component.ID] = true
	}
	for _, polygon := range union {
		resultID := matchingMovedComponentID(polygon, affected, usedIDs)
		if resultID == "" {
			return nil, false, errInvalidGeometry
		}
		usedIDs[resultID] = true
		result = append(result, WalkableComponent{ID: resultID, Polygon: polygon})
	}
	if len(result) > maxWalkableComponents || walkableVertexCount(result) > maxGeometryTotalVertices {
		return nil, false, errGeometryLimitExceeded
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, true, nil
}

func translatePolygon(polygon Polygon, deltaX, deltaY float64) (Polygon, error) {
	translated := Polygon{
		Outer: make([]ScenePoint, len(polygon.Outer)),
		Holes: make([][]ScenePoint, len(polygon.Holes)),
	}
	for index, point := range polygon.Outer {
		translated.Outer[index] = ScenePoint{X: point.X + deltaX, Y: point.Y + deltaY}
	}
	for holeIndex, hole := range polygon.Holes {
		translated.Holes[holeIndex] = make([]ScenePoint, len(hole))
		for pointIndex, point := range hole {
			translated.Holes[holeIndex][pointIndex] = ScenePoint{X: point.X + deltaX, Y: point.Y + deltaY}
		}
	}
	return normalizePolygon(translated)
}

func deleteWalkableComponent(components []WalkableComponent, componentID string) ([]WalkableComponent, bool, error) {
	if len(components) > maxWalkableComponents || componentID == "" {
		return nil, false, errInvalidGeometry
	}
	if _, err := componentIDs(components); err != nil {
		return nil, false, err
	}
	result := make([]WalkableComponent, 0, len(components))
	found := false
	for _, component := range components {
		if component.ID == componentID {
			found = true
			continue
		}
		result = append(result, component)
	}
	if !found {
		return nil, false, errInvalidGeometry
	}
	return result, true, nil
}

func setWalkableMode(floor Floor, mode string) (Floor, bool, error) {
	if mode != walkableModeUnrestricted && mode != walkableModeRestricted {
		return floor, false, errInvalidGeometry
	}
	if floor.WalkableMode == mode {
		return floor, false, nil
	}
	floor.WalkableMode = mode
	return floor, true, nil
}

func matchingMovedComponentID(polygon Polygon, affected []WalkableComponent, used map[string]bool) string {
	exact := make([]string, 0, 2)
	absorbed := make([]string, 0, len(affected))
	for _, component := range affected {
		if used[component.ID] {
			continue
		}
		if polygonsEqual(component.Polygon, polygon) {
			exact = append(exact, component.ID)
			continue
		}
		if polygonHasStrictlyInteriorPoint(polygon, component.Polygon) {
			absorbed = append(absorbed, component.ID)
		}
	}
	if len(exact) > 0 {
		exact = append(exact, absorbed...)
		sort.Strings(exact)
		return exact[0]
	}
	return matchingComponentID(polygon, affected, used)
}

func polygonHasStrictlyInteriorPoint(container, candidate Polygon) bool {
	points := make([]ScenePoint, 0, len(candidate.Outer)*2)
	for index, point := range candidate.Outer {
		next := candidate.Outer[(index+1)%len(candidate.Outer)]
		points = append(points, point, ScenePoint{X: (point.X + next.X) / 2, Y: (point.Y + next.Y) / 2})
	}
	for _, point := range points {
		if ringLocation(container.Outer, point) != pointInside {
			continue
		}
		insideHole := false
		for _, hole := range container.Holes {
			if ringLocation(hole, point) != pointOutside {
				insideHole = true
				break
			}
		}
		if !insideHole {
			return true
		}
	}
	return false
}

func componentIDs(components []WalkableComponent) (map[string]bool, error) {
	ids := make(map[string]bool, len(components)+1)
	for _, component := range components {
		if component.ID == "" || ids[component.ID] {
			return nil, errInvalidGeometry
		}
		ids[component.ID] = true
	}
	return ids, nil
}

func walkableVertexCount(components []WalkableComponent) int {
	total := 0
	for _, component := range components {
		total += len(component.Polygon.Outer) + ringVertexCount(component.Polygon.Holes)
	}
	return total
}

func matchingComponentID(polygon Polygon, affected []WalkableComponent, used map[string]bool) string {
	for _, component := range affected {
		if !used[component.ID] && polygonsEqual(component.Polygon, polygon) {
			return component.ID
		}
	}
	candidates := make([]string, 0, len(affected))
	for _, component := range affected {
		if !used[component.ID] && pointInPolygon(polygon, component.Polygon.Outer[0]) {
			candidates = append(candidates, component.ID)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Strings(candidates)
	return candidates[0]
}

func freshComponentID(generateID componentIDGenerator, existing map[string]bool) (string, error) {
	if generateID == nil {
		return "", errInvalidGeometry
	}
	for range 16 {
		candidate := generateID()
		if candidate != "" && !existing[candidate] {
			return candidate, nil
		}
	}
	return "", errInvalidGeometry
}

func walkableComponentSetsEqual(a, b []WalkableComponent) bool {
	if len(a) != len(b) {
		return false
	}
	byID := make(map[string]Polygon, len(a))
	for _, component := range a {
		byID[component.ID] = component.Polygon
	}
	for _, component := range b {
		polygon, ok := byID[component.ID]
		if !ok || !polygonsEqual(polygon, component.Polygon) {
			return false
		}
	}
	return true
}

func polygonsEqual(a, b Polygon) bool {
	if !ringsEqual(a.Outer, b.Outer) || len(a.Holes) != len(b.Holes) {
		return false
	}
	for i := range a.Holes {
		if !ringsEqual(a.Holes[i], b.Holes[i]) {
			return false
		}
	}
	return true
}

func ringsEqual(a, b []ScenePoint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !samePoint(a[i], b[i]) {
			return false
		}
	}
	return true
}
