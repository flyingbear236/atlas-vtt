package main

func setRenderBounds(floor Floor, input *Polygon) (Floor, bool, error) {
	if input == nil || len(input.Holes) != 0 {
		return floor, false, errInvalidGeometry
	}
	if err := validateGeometryInputPolygon(*input); err != nil {
		return floor, false, err
	}
	polygon, err := normalizePolygon(*input)
	if err != nil {
		return floor, false, err
	}
	if floor.RenderBounds != nil && polygonsEqual(*floor.RenderBounds, polygon) {
		return floor, false, nil
	}
	floor.RenderBounds = &polygon
	return floor, true, nil
}

func clearRenderBounds(floor Floor) (Floor, bool, error) {
	if floor.RenderBounds == nil {
		return floor, false, nil
	}
	floor.RenderBounds = nil
	return floor, true, nil
}
