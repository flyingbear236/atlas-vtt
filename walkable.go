package main

import (
	"sort"
)

type componentIDGenerator func() string

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
