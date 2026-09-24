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
	sceneModelVersion  = 2
)

const (
	layerKindVisual   = "visual"
	layerKindTokens   = "tokens"
	layerKindWalkable = "walkable"
)

const (
	assetKeep        = "keep"
	assetReclaimable = "reclaimable"
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
	ID                         string  `json:"id"`
	Name                       string  `json:"name"`
	Order                      int     `json:"order"`
	Opacity                    float64 `json:"opacity"`
	OpacityWhenViewedFromBelow float64 `json:"opacityWhenViewedFromBelow"`
}

type Layer struct {
	ID             string          `json:"id"`
	FloorID        string          `json:"floorId"`
	Name           string          `json:"name"`
	Kind           string          `json:"kind"`
	Order          int             `json:"order"`
	Visible        bool            `json:"visible"`
	Opacity        float64         `json:"opacity"`
	Locked         bool            `json:"locked"`
	WalkableBounds *WalkableBounds `json:"walkableBounds,omitempty"`
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

func defaultWalkableBounds(bounds SceneBounds) *WalkableBounds {
	return &WalkableBounds{Width: bounds.Width, Height: bounds.Height}
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

func walkableBoundsForFloor(scene *Scene, floorID string) (WalkableBounds, bool) {
	layer := scene.Layers[layerIDByKind(scene, floorID, layerKindWalkable)]
	if layer.WalkableBounds == nil || !validWalkableBounds(*layer.WalkableBounds) {
		return WalkableBounds{}, false
	}
	return *layer.WalkableBounds, true
}

func positionInWalkable(scene *Scene, floorID string, x, y float64) bool {
	bounds, ok := walkableBoundsForFloor(scene, floorID)
	return ok && x >= bounds.X && y >= bounds.Y && x <= bounds.X+bounds.Width && y <= bounds.Y+bounds.Height
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
	if member == nil || member.Role == "gm" {
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
	if scene == nil || member == nil || member.Role != "player" || tokenID == "" {
		return Token{}, false
	}
	token, ok := scene.Tokens[tokenID]
	return token, ok && token.Owner == member.ID && !token.Hidden
}

func currentFloorForPeer(peer *peer, scene *Scene) string {
	if peer == nil {
		return firstFloorID(scene)
	}
	if peer.member == nil || peer.member.Role == "gm" {
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

func elementIntersectsSceneBounds(element SceneElement, bounds SceneBounds) bool {
	return elementIntersectsRegion(element, SceneRegion{Left: 0, Top: 0, Right: bounds.Width, Bottom: bounds.Height})
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
		maxOrder++
	}
	if layerIDByKind(scene, floorID, layerKindWalkable) == "" {
		walkableID := id()
		scene.Layers[walkableID] = Layer{ID: walkableID, FloorID: floorID, Name: "Игровая область", Kind: layerKindWalkable, Order: maxOrder + 1, Visible: true, Opacity: 1, WalkableBounds: defaultWalkableBounds(scene.Bounds)}
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
		Floors:       map[string]Floor{floorID: {ID: floorID, Name: "Этаж 1", Order: 0, Opacity: 1}},
		Layers:       map[string]Layer{},
		Elements:     map[string]SceneElement{},
		Tokens:       map[string]Token{},
		Transitions:  map[string]Transition{},
	}
	scene.Layers[baseID] = Layer{ID: baseID, FloorID: floorID, Name: "Основа", Kind: layerKindVisual, Order: 0, Visible: true, Opacity: 1}
	scene.Layers[objectsID] = Layer{ID: objectsID, FloorID: floorID, Name: "Объекты", Kind: layerKindVisual, Order: 1, Visible: true, Opacity: 1}
	addFloorLayers(scene, floorID)
	return scene
}

// ensureSceneStructure upgrades the immediately preceding Floor/Layer model.
// Maps already remain ordinary SceneElements; no singleton map path exists.
func ensureSceneStructure(scene *Scene) bool {
	changed := false
	legacyModel := scene.ModelVersion < sceneModelVersion
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
		scene.Floors[floorID] = Floor{ID: floorID, Name: "Этаж 1", Order: 0, Opacity: 1}
		changed = true
	}
	floorID := firstFloorID(scene)
	if len(scene.Layers) == 0 {
		baseID, objectsID := id(), id()
		scene.Layers[baseID] = Layer{ID: baseID, FloorID: floorID, Name: "Основа", Kind: layerKindVisual, Order: 0, Visible: true, Opacity: 1}
		scene.Layers[objectsID] = Layer{ID: objectsID, FloorID: floorID, Name: "Объекты", Kind: layerKindVisual, Order: 1, Visible: true, Opacity: 1}
		changed = true
	}
	for floorKey, floor := range scene.Floors {
		if legacyModel {
			floor.Opacity = 1
			floor.OpacityWhenViewedFromBelow = 0
			scene.Floors[floorKey] = floor
			changed = true
		}
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
	for layerID, layer := range scene.Layers {
		if layer.Kind == layerKindWalkable && (layer.WalkableBounds == nil || !validWalkableBounds(*layer.WalkableBounds)) {
			layer.WalkableBounds = defaultWalkableBounds(scene.Bounds)
			scene.Layers[layerID] = layer
			changed = true
		}
	}
	if legacyModel {
		scene.ModelVersion = sceneModelVersion
		changed = true
	}
	return changed
}

func validateSceneStructure(scene *Scene, assets map[string]Asset, members map[string]*Member) bool {
	if scene == nil || scene.ID == "" || strings.TrimSpace(scene.Name) == "" || scene.ModelVersion != sceneModelVersion || !validSceneBounds(scene.Bounds) || len(scene.Floors) == 0 || len(scene.Floors) > maxSceneFloors || scene.Tokens == nil || scene.Elements == nil || scene.Layers == nil || scene.Transitions == nil {
		return false
	}
	for floorID, floor := range scene.Floors {
		if floor.ID != floorID || strings.TrimSpace(floor.Name) == "" || !validNumber(floor.Opacity) || floor.Opacity < 0 || floor.Opacity > 1 || !validNumber(floor.OpacityWhenViewedFromBelow) || floor.OpacityWhenViewedFromBelow < 0 || floor.OpacityWhenViewedFromBelow > 1 {
			return false
		}
		if layerIDByKind(scene, floorID, layerKindTokens) == "" || layerIDByKind(scene, floorID, layerKindWalkable) == "" {
			return false
		}
	}
	kindCounts := map[string]map[string]int{}
	for layerID, layer := range scene.Layers {
		if layer.ID != layerID || scene.Floors[layer.FloorID].ID == "" || strings.TrimSpace(layer.Name) == "" || (layer.Kind != layerKindVisual && layer.Kind != layerKindTokens && layer.Kind != layerKindWalkable) || layer.Opacity < 0 || layer.Opacity > 1 {
			return false
		}
		if kindCounts[layer.FloorID] == nil {
			kindCounts[layer.FloorID] = map[string]int{}
		}
		kindCounts[layer.FloorID][layer.Kind]++
		if layer.Kind == layerKindWalkable {
			if layer.WalkableBounds == nil || !validWalkableBounds(*layer.WalkableBounds) {
				return false
			}
		} else if layer.WalkableBounds != nil {
			return false
		}
	}
	for floorID := range scene.Floors {
		if kindCounts[floorID][layerKindTokens] != 1 || kindCounts[floorID][layerKindWalkable] != 1 {
			return false
		}
	}
	for elementID, element := range scene.Elements {
		layer := scene.Layers[element.LayerID]
		if element.ID != elementID || layer.ID == "" || layer.Kind != layerKindVisual || element.FloorID != layer.FloorID || assets[element.AssetID].ID == "" || !validTransform(element.Transform) || element.Opacity < 0 || element.Opacity > 1 {
			return false
		}
	}
	for tokenID, token := range scene.Tokens {
		layer := scene.Layers[token.LayerID]
		if token.ID != tokenID || scene.Floors[token.FloorID].ID == "" || layer.Kind != layerKindTokens || layer.FloorID != token.FloorID || !validNumber(token.X) || !validNumber(token.Y) || token.Size < 16 || token.Size > 1024 || (token.Owner != "" && members[token.Owner] == nil) || (token.Asset != "" && assets[token.Asset].ID == "") {
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
		if !positionInWalkable(scene, endpoint.FloorID, endpoint.Position.X, endpoint.Position.Y) {
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
