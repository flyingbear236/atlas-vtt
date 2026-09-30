package main

import (
	"math"
	"sort"
	"strings"
	"time"
)

const (
	defaultSceneWidth  = 4000.0
	defaultSceneHeight = 3000.0
	maxSceneDimension  = 1_000_000.0
	maxSceneElements   = 100_000
	maxSceneFloors     = 2
	sceneModelVersion  = 3
)

const (
	layerKindVisual = "visual"
	layerKindTokens = "tokens"
)

const legacyLayerKindWalkable = "walkable"

const (
	assetKeep        = "keep"
	assetReclaimable = "reclaimable"
)

const (
	walkableModeUnrestricted = "unrestricted"
	walkableModeRestricted   = "restricted"
)

// Transform is the shared world-space representation used by visual elements
// and by token helpers. It deliberately contains no renderer-specific state.
type Transform struct {
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
	Rotation float64 `json:"rotation"`
}

type SceneBounds struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type WalkableBounds struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type Floor struct {
	ID                         string              `json:"id"`
	Name                       string              `json:"name"`
	Order                      int                 `json:"order"`
	Opacity                    float64             `json:"opacity"`
	OpacityWhenViewedFromBelow float64             `json:"opacityWhenViewedFromBelow"`
	WalkableMode               string              `json:"walkableMode"`
	WalkableComponents         []WalkableComponent `json:"walkableComponents"`
	RenderBounds               *Polygon            `json:"renderBounds"`
	GeometryRevision           uint64              `json:"geometryRevision"`
}

type Layer struct {
	ID      string  `json:"id"`
	FloorID string  `json:"floorId"`
	Name    string  `json:"name"`
	Kind    string  `json:"kind"`
	Order   int     `json:"order"`
	Visible bool    `json:"visible"`
	Opacity float64 `json:"opacity"`
	Locked  bool    `json:"locked"`
}

type SceneElement struct {
	ID        string    `json:"id"`
	FloorID   string    `json:"floorId"`
	LayerID   string    `json:"layerId"`
	AssetID   string    `json:"assetId"`
	Name      string    `json:"name"`
	Transform Transform `json:"transform"`
	ZOrder    int       `json:"zOrder"`
	Visible   bool      `json:"visible"`
	Locked    bool      `json:"locked"`
	Opacity   float64   `json:"opacity"`
}

type TransitionEndpoint struct {
	FloorID  string     `json:"floorId"`
	Position ScenePoint `json:"position"`
	Radius   float64    `json:"radius"`
}

type Transition struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	EndpointA TransitionEndpoint `json:"endpointA"`
	EndpointB TransitionEndpoint `json:"endpointB"`
	Direction string             `json:"direction"`
}

func validSceneBounds(bounds SceneBounds) bool {
	return validNumber(bounds.Width) && validNumber(bounds.Height) && bounds.Width >= 1 && bounds.Height >= 1 && bounds.Width <= maxSceneDimension && bounds.Height <= maxSceneDimension
}

func validWalkableBounds(bounds WalkableBounds) bool {
	return validNumber(bounds.X) && validNumber(bounds.Y) && validNumber(bounds.Width) && validNumber(bounds.Height) &&
		bounds.Width > 0 && bounds.Height > 0 && bounds.Width <= maxSceneDimension && bounds.Height <= maxSceneDimension
}

func validTransform(transform Transform) bool {
	return validNumber(transform.X) && validNumber(transform.Y) && validNumber(transform.Width) && validNumber(transform.Height) && validNumber(transform.Rotation) &&
		transform.Width > 0 && transform.Height > 0 && transform.Width <= maxSceneDimension && transform.Height <= maxSceneDimension
}

func orderedFloors(scene *Scene) []Floor {
	floors := make([]Floor, 0, len(scene.Floors))
	for _, floor := range scene.Floors {
		floors = append(floors, floor)
	}
	sort.Slice(floors, func(i, j int) bool {
		if floors[i].Order != floors[j].Order {
			return floors[i].Order < floors[j].Order
		}
		return floors[i].ID < floors[j].ID
	})
	return floors
}

func orderedLayers(scene *Scene, floorID string) []Layer {
	layers := make([]Layer, 0, len(scene.Layers))
	for _, layer := range scene.Layers {
		if layer.FloorID == floorID {
			layers = append(layers, layer)
		}
	}
	sort.Slice(layers, func(i, j int) bool {
		if layers[i].Order != layers[j].Order {
			return layers[i].Order < layers[j].Order
		}
		return layers[i].ID < layers[j].ID
	})
	return layers
}

func firstFloorID(scene *Scene) string {
	floors := orderedFloors(scene)
	if len(floors) == 0 {
		return ""
	}
	return floors[0].ID
}

func firstLayerID(scene *Scene, floorID string) string {
	layers := orderedLayers(scene, floorID)
	for _, layer := range layers {
		if layer.Kind == layerKindVisual {
			return layer.ID
		}
	}
	return ""
}

func layerIDByKind(scene *Scene, floorID, kind string) string {
	for _, layer := range scene.Layers {
		if layer.FloorID == floorID && layer.Kind == kind {
			return layer.ID
		}
	}
	return ""
}

func tokenFloorID(scene *Scene, token Token) string {
	if scene.Floors[token.FloorID].ID != "" {
		return token.FloorID
	}
	return firstFloorID(scene)
}

type floorView struct {
	Floor Floor
	Alpha float64
}

// visibleFloorViews is the shared compositing rule used by delivery,
// authorization and rendering. The current floor is always materialized even
// at alpha zero; the other floor is omitted when it cannot affect the image.
func visibleFloorViews(scene *Scene, currentFloorID string) []floorView {
	floors := orderedFloors(scene)
	if len(floors) == 0 {
		return nil
	}
	current := 0
	for i := range floors {
		if floors[i].ID == currentFloorID {
			current = i
			break
		}
	}
	if current == 0 {
		views := []floorView{{Floor: floors[0], Alpha: floors[0].Opacity}}
		if len(floors) > 1 {
			alpha := floors[1].Opacity * floors[1].OpacityWhenViewedFromBelow
			if alpha > 0 {
				views = append(views, floorView{Floor: floors[1], Alpha: alpha})
			}
		}
		return views
	}
	views := make([]floorView, 0, 2)
	if floors[0].Opacity > 0 {
		views = append(views, floorView{Floor: floors[0], Alpha: floors[0].Opacity})
	}
	views = append(views, floorView{Floor: floors[current], Alpha: floors[current].Opacity})
	return views
}

func visibleFloorSet(scene *Scene, currentFloorID string) map[string]float64 {
	visible := make(map[string]float64, maxSceneFloors)
	for _, view := range visibleFloorViews(scene, currentFloorID) {
		visible[view.Floor.ID] = view.Alpha
	}
	return visible
}

func currentFloorForMember(scene *Scene, member *Member, preferred string) string {
	if scene == nil {
		return ""
	}
	if member == nil || memberIsGM(member) {
		if scene.Floors[preferred].ID != "" {
			return preferred
		}
		return firstFloorID(scene)
	}
	owned := make([]Token, 0)
	for _, token := range scene.Tokens {
		if token.Owner == member.ID && !token.Hidden {
			owned = append(owned, token)
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].ID < owned[j].ID })
	if len(owned) > 0 {
		return tokenFloorID(scene, owned[0])
	}
	if scene.Floors[preferred].ID != "" {
		return preferred
	}
	return firstFloorID(scene)
}

func activeTokenForMember(scene *Scene, member *Member, tokenID string) (Token, bool) {
	if scene == nil || member == nil || memberIsGM(member) || tokenID == "" {
		return Token{}, false
	}
	token, ok := scene.Tokens[tokenID]
	return token, ok && token.Owner == member.ID && !token.Hidden
}

func currentFloorForPeer(peer *peer, scene *Scene) string {
	if peer == nil {
		return firstFloorID(scene)
	}
	if peer.member == nil || memberIsGM(peer.member) {
		return currentFloorForMember(scene, peer.member, peer.floorID)
	}
	if token, ok := activeTokenForMember(scene, peer.member, peer.activeTokenID); ok {
		return tokenFloorID(scene, token)
	}
	owned := make([]Token, 0)
	for _, token := range scene.Tokens {
		if token.Owner == peer.member.ID && !token.Hidden {
			owned = append(owned, token)
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].ID < owned[j].ID })
	if len(owned) > 0 {
		peer.activeTokenID = owned[0].ID
		return tokenFloorID(scene, owned[0])
	}
	peer.activeTokenID = ""
	return currentFloorForMember(scene, peer.member, peer.floorID)
}

func elementIntersectsRenderBounds(element SceneElement, bounds *Polygon) bool {
	if bounds == nil {
		return true
	}
	t := element.Transform
	cx, cy := t.X+t.Width/2, t.Y+t.Height/2
	angle := t.Rotation * math.Pi / 180
	c, sine := math.Cos(angle), math.Sin(angle)
	ring := make([]ScenePoint, 0, 4)
	for _, corner := range [][2]float64{{-t.Width / 2, -t.Height / 2}, {t.Width / 2, -t.Height / 2}, {t.Width / 2, t.Height / 2}, {-t.Width / 2, t.Height / 2}} {
		ring = append(ring, ScenePoint{X: cx + corner[0]*c - corner[1]*sine, Y: cy + corner[0]*sine + corner[1]*c})
	}
	elementBounds := AABB{MinX: ring[0].X, MinY: ring[0].Y, MaxX: ring[0].X, MaxY: ring[0].Y}
	for _, point := range ring[1:] {
		elementBounds.MinX = math.Min(elementBounds.MinX, point.X)
		elementBounds.MinY = math.Min(elementBounds.MinY, point.Y)
		elementBounds.MaxX = math.Max(elementBounds.MaxX, point.X)
		elementBounds.MaxY = math.Max(elementBounds.MaxY, point.Y)
	}
	if !elementBounds.intersects(polygonBounds(*bounds)) {
		return false
	}
	if ringsIntersect(ring, bounds.Outer) || pointInPolygon(*bounds, ring[0]) {
		return true
	}
	return ringLocation(ring, bounds.Outer[0]) != pointOutside
}

func initializeFloorRenderBounds(scene *Scene, floorID string, width, height float64) bool {
	floor, ok := scene.Floors[floorID]
	if !ok || floor.RenderBounds != nil {
		return false
	}
	for _, element := range scene.Elements {
		if element.FloorID == floorID {
			return false
		}
	}
	bounds, err := rectanglePolygon(AABB{MaxX: width, MaxY: height})
	if err != nil {
		return false
	}
	floor.RenderBounds = &bounds
	floor.GeometryRevision++
	scene.Floors[floorID] = floor
	return true
}

func addFloorLayers(scene *Scene, floorID string) {
	maxOrder := -1
	for _, layer := range scene.Layers {
		if layer.FloorID == floorID && layer.Order > maxOrder {
			maxOrder = layer.Order
		}
	}
	if firstLayerID(scene, floorID) == "" {
		baseID := id()
		scene.Layers[baseID] = Layer{ID: baseID, FloorID: floorID, Name: "Основа", Kind: layerKindVisual, Order: maxOrder + 1, Visible: true, Opacity: 1}
		maxOrder++
	}
	if layerIDByKind(scene, floorID, layerKindTokens) == "" {
		tokenLayerID := id()
		scene.Layers[tokenLayerID] = Layer{ID: tokenLayerID, FloorID: floorID, Name: "Токены", Kind: layerKindTokens, Order: maxOrder + 1, Visible: true, Opacity: 1}
	}
}

func newScene(sceneID, name string) *Scene {
	floorID := id()
	baseID, objectsID := id(), id()
	scene := &Scene{
		ID:           sceneID,
		Name:         name,
		ModelVersion: sceneModelVersion,
		Bounds:       SceneBounds{Width: defaultSceneWidth, Height: defaultSceneHeight},
		Floors: map[string]Floor{floorID: {
			ID: floorID, Name: "Этаж 1", Order: 0, Opacity: 1,
			WalkableMode: walkableModeUnrestricted, WalkableComponents: []WalkableComponent{},
		}},
		Layers:      map[string]Layer{},
		Elements:    map[string]SceneElement{},
		Tokens:      map[string]Token{},
		Transitions: map[string]Transition{},
	}
	scene.Layers[baseID] = Layer{ID: baseID, FloorID: floorID, Name: "Основа", Kind: layerKindVisual, Order: 0, Visible: true, Opacity: 1}
	scene.Layers[objectsID] = Layer{ID: objectsID, FloorID: floorID, Name: "Объекты", Kind: layerKindVisual, Order: 1, Visible: true, Opacity: 1}
	addFloorLayers(scene, floorID)
	return scene
}

func removeLegacyWalkableLayers(scene *Scene) bool {
	if scene == nil {
		return false
	}
	changed := false
	for layerID, layer := range scene.Layers {
		if layer.Kind == legacyLayerKindWalkable {
			delete(scene.Layers, layerID)
			changed = true
		}
	}
	return changed
}

// ensureSceneStructure repairs optional structure only within the current model.
// Older model versions are rejected by newServer instead of being guessed into
// the new authoritative walkable representation.
func ensureSceneStructure(scene *Scene) bool {
	if scene == nil || scene.ModelVersion != sceneModelVersion {
		return false
	}
	changed := removeLegacyWalkableLayers(scene)
	if scene.Tokens == nil {
		scene.Tokens = map[string]Token{}
		changed = true
	}
	if scene.Floors == nil {
		scene.Floors = map[string]Floor{}
	}
	if scene.Layers == nil {
		scene.Layers = map[string]Layer{}
	}
	if scene.Elements == nil {
		scene.Elements = map[string]SceneElement{}
	}
	if scene.Transitions == nil {
		scene.Transitions = map[string]Transition{}
	}
	if len(scene.Floors) == 0 {
		floorID := id()
		scene.Floors[floorID] = Floor{ID: floorID, Name: "Этаж 1", Order: 0, Opacity: 1, WalkableMode: walkableModeUnrestricted, WalkableComponents: []WalkableComponent{}}
		changed = true
	}
	floorID := firstFloorID(scene)
	if len(scene.Layers) == 0 {
		baseID, objectsID := id(), id()
		scene.Layers[baseID] = Layer{ID: baseID, FloorID: floorID, Name: "Основа", Kind: layerKindVisual, Order: 0, Visible: true, Opacity: 1}
		scene.Layers[objectsID] = Layer{ID: objectsID, FloorID: floorID, Name: "Объекты", Kind: layerKindVisual, Order: 1, Visible: true, Opacity: 1}
		changed = true
	}
	for layerID, layer := range scene.Layers {
		if layer.Kind == "" {
			layer.Kind = layerKindVisual
			scene.Layers[layerID] = layer
			changed = true
		}
	}
	for floorKey := range scene.Floors {
		before := len(scene.Layers)
		addFloorLayers(scene, floorKey)
		if len(scene.Layers) != before {
			changed = true
		}
	}
	for tokenID, token := range scene.Tokens {
		if token.FloorID == "" {
			token.FloorID = floorID
			changed = true
		}
		tokenLayerID := layerIDByKind(scene, token.FloorID, layerKindTokens)
		if token.LayerID != tokenLayerID {
			token.LayerID = tokenLayerID
			changed = true
		}
		scene.Tokens[tokenID] = token
	}
	if !validSceneBounds(scene.Bounds) {
		width, height := defaultSceneWidth, defaultSceneHeight
		for _, token := range scene.Tokens {
			width = math.Max(width, token.X+token.Size)
			height = math.Max(height, token.Y+token.Size)
		}
		scene.Bounds = SceneBounds{Width: width, Height: height}
		changed = true
	}
	return changed
}

func validateSceneStructure(scene *Scene, assets map[string]Asset, members map[string]*Member) bool {
	if scene == nil || scene.ID == "" || strings.TrimSpace(scene.Name) == "" || scene.ModelVersion != sceneModelVersion || !validSceneBounds(scene.Bounds) || len(scene.Floors) == 0 || len(scene.Floors) > maxSceneFloors || scene.Tokens == nil || scene.Elements == nil || scene.Layers == nil || scene.Transitions == nil {
		return false
	}
	for floorID, floor := range scene.Floors {
		if floor.ID != floorID || strings.TrimSpace(floor.Name) == "" || !validNumber(floor.Opacity) || floor.Opacity < 0 || floor.Opacity > 1 || !validNumber(floor.OpacityWhenViewedFromBelow) || floor.OpacityWhenViewedFromBelow < 0 || floor.OpacityWhenViewedFromBelow > 1 || !validFloorGeometry(floor) {
			return false
		}
		if layerIDByKind(scene, floorID, layerKindTokens) == "" {
			return false
		}
	}
	kindCounts := map[string]map[string]int{}
	for layerID, layer := range scene.Layers {
		if layer.ID != layerID || scene.Floors[layer.FloorID].ID == "" || strings.TrimSpace(layer.Name) == "" || (layer.Kind != layerKindVisual && layer.Kind != layerKindTokens) || layer.Opacity < 0 || layer.Opacity > 1 {
			return false
		}
		if kindCounts[layer.FloorID] == nil {
			kindCounts[layer.FloorID] = map[string]int{}
		}
		kindCounts[layer.FloorID][layer.Kind]++
	}
	for floorID := range scene.Floors {
		if kindCounts[floorID][layerKindTokens] != 1 {
			return false
		}
	}
	for elementID, element := range scene.Elements {
		layer := scene.Layers[element.LayerID]
		asset := assets[element.AssetID]
		if element.ID != elementID || layer.ID == "" || layer.Kind != layerKindVisual || element.FloorID != layer.FloorID || asset.ID == "" || !isSceneRasterKind(asset.Kind) || !validTransform(element.Transform) || element.Opacity < 0 || element.Opacity > 1 {
			return false
		}
	}
	for tokenID, token := range scene.Tokens {
		layer := scene.Layers[token.LayerID]
		if token.ID != tokenID || scene.Floors[token.FloorID].ID == "" || layer.Kind != layerKindTokens || layer.FloorID != token.FloorID || !validNumber(token.X) || !validNumber(token.Y) || token.Size < 16 || token.Size > 1024 || (token.Owner != "" && members[token.Owner] == nil) || (token.Asset != "" && (assets[token.Asset].ID == "" || assets[token.Asset].Kind != assetKindToken)) {
			return false
		}
	}
	for transitionID, transition := range scene.Transitions {
		if transition.ID != transitionID || !validTransition(scene, transition) {
			return false
		}
	}
	return true
}

func validFloorGeometry(floor Floor) bool {
	if (floor.WalkableMode != walkableModeUnrestricted && floor.WalkableMode != walkableModeRestricted) || floor.WalkableComponents == nil || !walkableGeometryWithinLimits(floor.WalkableComponents) {
		return false
	}
	ids := make(map[string]bool, len(floor.WalkableComponents))
	previousID := ""
	for _, component := range floor.WalkableComponents {
		if component.ID == "" || ids[component.ID] || previousID > component.ID {
			return false
		}
		normalized, err := normalizePolygon(component.Polygon)
		if err != nil || !polygonsEqual(normalized, component.Polygon) {
			return false
		}
		ids[component.ID] = true
		previousID = component.ID
	}
	if floor.RenderBounds != nil {
		if len(floor.RenderBounds.Holes) != 0 {
			return false
		}
		normalized, err := normalizePolygon(*floor.RenderBounds)
		if err != nil || !polygonsEqual(normalized, *floor.RenderBounds) {
			return false
		}
	}
	return true
}

func elementIntersectsRegion(element SceneElement, region SceneRegion) bool {
	// Exact OBB/AABB separating-axis test keeps rotated maps spatially lazy and
	// prevents an out-of-bounds element's asset metadata from leaking to Player.
	t := element.Transform
	cx, cy := t.X+t.Width/2, t.Y+t.Height/2
	rx, ry := (region.Left+region.Right)/2, (region.Top+region.Bottom)/2
	dx, dy := cx-rx, cy-ry
	hw, hh := t.Width/2, t.Height/2
	rw, rh := (region.Right-region.Left)/2, (region.Bottom-region.Top)/2
	angle := t.Rotation * math.Pi / 180
	c, sine := math.Cos(angle), math.Sin(angle)
	if math.Abs(dx) > rw+math.Abs(c)*hw+math.Abs(sine)*hh || math.Abs(dy) > rh+math.Abs(sine)*hw+math.Abs(c)*hh {
		return false
	}
	if math.Abs(dx*c+dy*sine) > hw+rw*math.Abs(c)+rh*math.Abs(sine) {
		return false
	}
	return math.Abs(-dx*sine+dy*c) <= hh+rw*math.Abs(sine)+rh*math.Abs(c)
}

func transitionContains(endpoint TransitionEndpoint, point ScenePoint) bool {
	return endpoint.Radius > 0 && math.Hypot(point.X-endpoint.Position.X, point.Y-endpoint.Position.Y) <= endpoint.Radius
}

func validTransition(scene *Scene, transition Transition) bool {
	if transition.Direction != "bidirectional" && transition.Direction != "AToB" && transition.Direction != "BToA" {
		return false
	}
	for _, endpoint := range []TransitionEndpoint{transition.EndpointA, transition.EndpointB} {
		if scene.Floors[endpoint.FloorID].ID == "" || !validNumber(endpoint.Position.X) || !validNumber(endpoint.Position.Y) || !validNumber(endpoint.Radius) || endpoint.Radius < 8 || endpoint.Radius > maxSceneDimension {
			return false
		}
		if !CanOccupyTokenPoint(scene, endpoint.FloorID, endpoint.Position) {
			return false
		}
	}
	return true
}

func transitionForMove(scene *Scene, token Token, next ScenePoint) (TransitionEndpoint, bool) {
	previous := ScenePoint{X: token.X, Y: token.Y}
	matchedID := ""
	destination := TransitionEndpoint{}
	for transitionID, transition := range scene.Transitions {
		var candidate TransitionEndpoint
		matched := false
		if token.FloorID == transition.EndpointA.FloorID && transition.Direction != "BToA" && !transitionContains(transition.EndpointA, previous) && transitionContains(transition.EndpointA, next) {
			candidate, matched = transition.EndpointB, true
		} else if token.FloorID == transition.EndpointB.FloorID && transition.Direction != "AToB" && !transitionContains(transition.EndpointB, previous) && transitionContains(transition.EndpointB, next) {
			candidate, matched = transition.EndpointA, true
		}
		if matched && !validTransition(scene, transition) {
			continue
		}
		if matched && (matchedID == "" || transitionID < matchedID) {
			matchedID, destination = transitionID, candidate
		}
	}
	return destination, matchedID != ""
}

// refreshAssetOrphans is the single campaign-wide reference tracker. Future
// Actor/handout references belong here rather than in independent GC passes.
func refreshAssetOrphans(session *Session, now time.Time) bool {
	references := make(map[string]int, len(session.Assets))
	for _, scene := range session.Scenes {
		for _, element := range scene.Elements {
			references[element.AssetID]++
		}
		for _, token := range scene.Tokens {
			if token.Asset != "" {
				references[token.Asset]++
			}
		}
	}
	// A live derived representation keeps its source chain alive. Provenance is
	// not a client-visible asset reference, but it is a retention/GC reference.
	queue := make([]string, 0, len(references))
	seen := make(map[string]bool, len(references))
	for assetID, count := range references {
		if count > 0 {
			queue = append(queue, assetID)
		}
	}
	for len(queue) > 0 {
		assetID := queue[0]
		queue = queue[1:]
		if seen[assetID] {
			continue
		}
		seen[assetID] = true
		if sourceID := session.Assets[assetID].ProvenanceSourceID(); sourceID != "" {
			references[sourceID]++
			queue = append(queue, sourceID)
		}
	}
	changed := false
	stamp := now.Unix()
	for assetID, asset := range session.Assets {
		if asset.RetentionPolicy == "" {
			asset.RetentionPolicy = assetReclaimable
			changed = true
		}
		orphan := asset.RetentionPolicy == assetReclaimable && references[assetID] == 0
		if orphan && asset.OrphanSince == nil {
			asset.OrphanSince = &stamp
			changed = true
		} else if !orphan && asset.OrphanSince != nil {
			asset.OrphanSince = nil
			changed = true
		}
		session.Assets[assetID] = asset
	}
	return changed
}

func (asset Asset) ProvenanceSourceID() string {
	if asset.Provenance == nil {
		return ""
	}
	return asset.Provenance.SourceAssetID
}
