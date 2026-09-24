package main

import (
	"math"
	"sort"
)

const (
	maxSceneTokens     = 100000
	sceneSpatialCell   = 2048.0
	maxSceneRegionSpan = 524288.0
)

// Scene owns mutable play state. Assets themselves remain campaign-owned and
// are referenced by ID, so sharing an image never duplicates its storage.
type Scene struct {
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	Published    bool                    `json:"published"`
	Revision     uint64                  `json:"revision"`
	ModelVersion int                     `json:"modelVersion"`
	Bounds       SceneBounds             `json:"bounds"`
	Floors       map[string]Floor        `json:"floors"`
	Layers       map[string]Layer        `json:"layers"`
	Elements     map[string]SceneElement `json:"elements"`
	Tokens       map[string]Token        `json:"tokens"`
	Transitions  map[string]Transition   `json:"transitions"`

	// runtime is rebuilt from Tokens after startup and is never persisted. It
	// keeps normal viewport work proportional to the requested region instead of
	// forcing every peer or asset request to scan the complete Scene.
	runtime *sceneRuntime
}

// SceneSummary is deliberately small. Campaign Home must not reveal scene
// contents, revisions, map references, or asset identifiers.
type SceneSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Published bool   `json:"published"`
}

type SceneMetadata struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Published bool        `json:"published"`
	Revision  uint64      `json:"revision"`
	Bounds    SceneBounds `json:"bounds"`
}

type ScenePoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// SceneRegion is the bounded piece of Scene state materialized by region-aware
// clients. The client asks for viewport + prefetch; the server independently
// caps the span so one request cannot accidentally turn back into a full-scene
// snapshot.
type SceneRegion struct {
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Right  float64 `json:"right"`
	Bottom float64 `json:"bottom"`
}

func regionNumber(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= 1e9 }

func (r SceneRegion) valid() bool {
	return regionNumber(r.Left) && regionNumber(r.Top) && regionNumber(r.Right) && regionNumber(r.Bottom) &&
		r.Right > r.Left && r.Bottom > r.Top &&
		r.Right-r.Left <= maxSceneRegionSpan && r.Bottom-r.Top <= maxSceneRegionSpan
}

func tokenIntersectsRegion(t Token, r SceneRegion) bool {
	half := t.Size / 2
	return t.X+half >= r.Left && t.X-half <= r.Right && t.Y+half >= r.Top && t.Y-half <= r.Bottom
}

type spatialCell struct{ X, Y int }

type sceneRuntime struct {
	scene              *Scene
	buckets            map[spatialCell]map[string]struct{}
	cells              map[string][]spatialCell
	elementBuckets     map[spatialCell]map[string]struct{}
	elementCells       map[string][]spatialCell
	assetAll           map[string]int
	assetPublic        map[string]int
	assetAllByFloor    map[string]map[string]int
	assetPublicByFloor map[string]map[string]int
	indexed            int
	elementIndexed     int
	revision           uint64
}

func newSceneRuntime(scene *Scene) *sceneRuntime {
	rt := &sceneRuntime{
		scene:              scene,
		buckets:            map[spatialCell]map[string]struct{}{},
		cells:              make(map[string][]spatialCell, len(scene.Tokens)),
		elementBuckets:     map[spatialCell]map[string]struct{}{},
		elementCells:       make(map[string][]spatialCell, len(scene.Elements)),
		assetAll:           map[string]int{},
		assetPublic:        map[string]int{},
		assetAllByFloor:    map[string]map[string]int{},
		assetPublicByFloor: map[string]map[string]int{},
		revision:           scene.Revision,
	}
	for _, token := range scene.Tokens {
		rt.add(token)
	}
	for _, element := range scene.Elements {
		rt.addElement(element)
	}
	return rt
}

func (scene *Scene) ensureRuntime() *sceneRuntime {
	// Count/revision checks also keep tests and maintenance code that replace a
	// whole token map from silently leaving the ephemeral index stale.
	if scene.runtime == nil || scene.runtime.revision != scene.Revision || scene.runtime.indexed != len(scene.Tokens) || scene.runtime.elementIndexed != len(scene.Elements) {
		scene.runtime = newSceneRuntime(scene)
	}
	return scene.runtime
}

func (scene *Scene) rebuildRuntime() { scene.runtime = newSceneRuntime(scene) }

func (scene *Scene) applyTokenRuntimeChange(old Token, existed bool, next Token, nextExists bool) {
	previousRevision := uint64(0)
	if scene.Revision > 0 {
		previousRevision = scene.Revision - 1
	}
	if scene.runtime == nil || scene.runtime.revision != previousRevision {
		// A rare out-of-band replacement (tests/maintenance/map revision) is
		// cheaper and safer to repair once than to keep a subtly stale index.
		scene.rebuildRuntime()
		return
	}
	if existed {
		scene.runtime.remove(old)
	}
	if nextExists {
		scene.runtime.add(next)
	}
	scene.runtime.revision = scene.Revision
}

func (scene *Scene) applyElementRuntimeChange(old SceneElement, existed bool, next SceneElement, nextExists bool) {
	previousRevision := uint64(0)
	if scene.Revision > 0 {
		previousRevision = scene.Revision - 1
	}
	if scene.runtime == nil || scene.runtime.revision != previousRevision {
		scene.rebuildRuntime()
		return
	}
	if existed {
		scene.runtime.removeElement(old)
	}
	if nextExists {
		scene.runtime.addElement(next)
	}
	scene.runtime.revision = scene.Revision
}

func (rt *sceneRuntime) add(token Token) {
	rt.indexed++
	half := token.Size / 2
	x0, x1 := int(math.Floor((token.X-half)/sceneSpatialCell)), int(math.Floor((token.X+half)/sceneSpatialCell))
	y0, y1 := int(math.Floor((token.Y-half)/sceneSpatialCell)), int(math.Floor((token.Y+half)/sceneSpatialCell))
	keys := make([]spatialCell, 0, (x1-x0+1)*(y1-y0+1))
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			key := spatialCell{X: x, Y: y}
			bucket := rt.buckets[key]
			if bucket == nil {
				bucket = map[string]struct{}{}
				rt.buckets[key] = bucket
			}
			bucket[token.ID] = struct{}{}
			keys = append(keys, key)
		}
	}
	rt.cells[token.ID] = keys
	if token.Asset != "" {
		rt.assetAll[token.Asset]++
		incrementFloorAsset(rt.assetAllByFloor, tokenFloorID(rt.scene, token), token.Asset)
		if !token.Hidden {
			rt.assetPublic[token.Asset]++
			incrementFloorAsset(rt.assetPublicByFloor, tokenFloorID(rt.scene, token), token.Asset)
		}
	}
}

func (rt *sceneRuntime) remove(token Token) {
	if _, ok := rt.cells[token.ID]; !ok {
		return
	}
	for _, key := range rt.cells[token.ID] {
		bucket := rt.buckets[key]
		delete(bucket, token.ID)
		if len(bucket) == 0 {
			delete(rt.buckets, key)
		}
	}
	delete(rt.cells, token.ID)
	rt.indexed--
	if token.Asset != "" {
		if rt.assetAll[token.Asset] <= 1 {
			delete(rt.assetAll, token.Asset)
		} else {
			rt.assetAll[token.Asset]--
		}
		decrementFloorAsset(rt.assetAllByFloor, tokenFloorID(rt.scene, token), token.Asset)
		if !token.Hidden {
			if rt.assetPublic[token.Asset] <= 1 {
				delete(rt.assetPublic, token.Asset)
			} else {
				rt.assetPublic[token.Asset]--
			}
			decrementFloorAsset(rt.assetPublicByFloor, tokenFloorID(rt.scene, token), token.Asset)
		}
	}
}

func elementCells(element SceneElement) []spatialCell {
	half := math.Hypot(element.Transform.Width, element.Transform.Height) / 2
	cx := element.Transform.X + element.Transform.Width/2
	cy := element.Transform.Y + element.Transform.Height/2
	x0, x1 := int(math.Floor((cx-half)/sceneSpatialCell)), int(math.Floor((cx+half)/sceneSpatialCell))
	y0, y1 := int(math.Floor((cy-half)/sceneSpatialCell)), int(math.Floor((cy+half)/sceneSpatialCell))
	keys := make([]spatialCell, 0, (x1-x0+1)*(y1-y0+1))
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			keys = append(keys, spatialCell{X: x, Y: y})
		}
	}
	return keys
}

func (rt *sceneRuntime) elementPublic(element SceneElement) bool {
	layer, ok := rt.scene.Layers[element.LayerID]
	return ok && layer.Kind == layerKindVisual && element.Visible && layer.Visible && element.Opacity > 0 && layer.Opacity > 0 && elementIntersectsSceneBounds(element, rt.scene.Bounds)
}

func incrementFloorAsset(index map[string]map[string]int, floorID, assetID string) {
	if index[floorID] == nil {
		index[floorID] = map[string]int{}
	}
	index[floorID][assetID]++
}

func decrementFloorAsset(index map[string]map[string]int, floorID, assetID string) {
	assets := index[floorID]
	if assets[assetID] <= 1 {
		delete(assets, assetID)
	} else {
		assets[assetID]--
	}
	if len(assets) == 0 {
		delete(index, floorID)
	}
}

func (rt *sceneRuntime) addElement(element SceneElement) {
	rt.elementIndexed++
	keys := elementCells(element)
	for _, key := range keys {
		bucket := rt.elementBuckets[key]
		if bucket == nil {
			bucket = map[string]struct{}{}
			rt.elementBuckets[key] = bucket
		}
		bucket[element.ID] = struct{}{}
	}
	rt.elementCells[element.ID] = keys
	if element.AssetID != "" {
		rt.assetAll[element.AssetID]++
		incrementFloorAsset(rt.assetAllByFloor, element.FloorID, element.AssetID)
		if rt.elementPublic(element) {
			rt.assetPublic[element.AssetID]++
			incrementFloorAsset(rt.assetPublicByFloor, element.FloorID, element.AssetID)
		}
	}
}

func (rt *sceneRuntime) removeElement(element SceneElement) {
	if _, ok := rt.elementCells[element.ID]; !ok {
		return
	}
	for _, key := range rt.elementCells[element.ID] {
		bucket := rt.elementBuckets[key]
		delete(bucket, element.ID)
		if len(bucket) == 0 {
			delete(rt.elementBuckets, key)
		}
	}
	delete(rt.elementCells, element.ID)
	rt.elementIndexed--
	if element.AssetID != "" {
		if rt.assetAll[element.AssetID] <= 1 {
			delete(rt.assetAll, element.AssetID)
		} else {
			rt.assetAll[element.AssetID]--
		}
		decrementFloorAsset(rt.assetAllByFloor, element.FloorID, element.AssetID)
		if rt.elementPublic(element) {
			if rt.assetPublic[element.AssetID] <= 1 {
				delete(rt.assetPublic, element.AssetID)
			} else {
				rt.assetPublic[element.AssetID]--
			}
			decrementFloorAsset(rt.assetPublicByFloor, element.FloorID, element.AssetID)
		}
	}
}

func (rt *sceneRuntime) query(region SceneRegion) []Token {
	x0, x1 := int(math.Floor(region.Left/sceneSpatialCell)), int(math.Floor(region.Right/sceneSpatialCell))
	y0, y1 := int(math.Floor(region.Top/sceneSpatialCell)), int(math.Floor(region.Bottom/sceneSpatialCell))
	ids := map[string]struct{}{}
	cellCount := (x1 - x0 + 1) * (y1 - y0 + 1)
	if cellCount > len(rt.buckets) {
		for key, bucket := range rt.buckets {
			if key.X < x0 || key.X > x1 || key.Y < y0 || key.Y > y1 {
				continue
			}
			for id := range bucket {
				ids[id] = struct{}{}
			}
		}
	} else {
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				for id := range rt.buckets[spatialCell{X: x, Y: y}] {
					ids[id] = struct{}{}
				}
			}
		}
	}
	out := make([]Token, 0, len(ids))
	for id := range ids {
		if token, ok := rt.scene.Tokens[id]; ok && tokenIntersectsRegion(token, region) {
			out = append(out, token)
		}
	}
	return out
}

func (rt *sceneRuntime) queryElements(region SceneRegion) []SceneElement {
	x0, x1 := int(math.Floor(region.Left/sceneSpatialCell)), int(math.Floor(region.Right/sceneSpatialCell))
	y0, y1 := int(math.Floor(region.Top/sceneSpatialCell)), int(math.Floor(region.Bottom/sceneSpatialCell))
	ids := map[string]struct{}{}
	cellCount := (x1 - x0 + 1) * (y1 - y0 + 1)
	if cellCount > len(rt.elementBuckets) {
		for key, bucket := range rt.elementBuckets {
			if key.X < x0 || key.X > x1 || key.Y < y0 || key.Y > y1 {
				continue
			}
			for elementID := range bucket {
				ids[elementID] = struct{}{}
			}
		}
	} else {
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				for elementID := range rt.elementBuckets[spatialCell{X: x, Y: y}] {
					ids[elementID] = struct{}{}
				}
			}
		}
	}
	out := make([]SceneElement, 0, len(ids))
	for elementID := range ids {
		if element, ok := rt.scene.Elements[elementID]; ok && elementIntersectsRegion(element, region) {
			out = append(out, element)
		}
	}
	return out
}

func firstScene(ss *Session) *Scene {
	var first *Scene
	for _, scene := range ss.Scenes {
		if first == nil || scene.ID < first.ID {
			first = scene
		}
	}
	return first
}

func ssScene(ss *Session, sceneID string) *Scene {
	if ss == nil || sceneID == "" {
		return nil
	}
	return ss.Scenes[sceneID]
}

func sceneVisible(scene *Scene, member *Member) bool {
	return scene != nil && (member.Role == "gm" || scene.Published)
}

func (s *Server) sceneList(ss *Session, member *Member) []SceneSummary {
	out := make([]SceneSummary, 0, len(ss.Scenes))
	for _, scene := range ss.Scenes {
		if sceneVisible(scene, member) {
			out = append(out, SceneSummary{ID: scene.ID, Name: scene.Name, Published: scene.Published})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Server) snapshotCampaign(ss *Session, member *Member) any {
	return map[string]any{
		"type":             "campaignSnapshot",
		"id":               ss.ID,
		"name":             ss.Name,
		"campaignRevision": ss.CampaignRevision,
		"scenes":           s.sceneList(ss, member),
		"you":              *member,
	}
}

func sceneEntryPoint(scene *Scene, member *Member) *ScenePoint {
	if scene == nil || member == nil {
		return nil
	}
	for _, token := range scene.Tokens {
		if token.Owner == member.ID && (member.Role == "gm" || !token.Hidden) {
			return &ScenePoint{X: token.X, Y: token.Y}
		}
	}
	return nil
}

func ownedTokenLocators(scene *Scene, member *Member) map[string]Token {
	owned := map[string]Token{}
	if scene == nil || member == nil || member.Role != "player" {
		return owned
	}
	for tokenID, token := range scene.Tokens {
		if token.Owner != member.ID || token.Hidden {
			continue
		}
		// Navigation needs identity, floor and geometry, but an unopened Floor
		// must not disclose or start loading its image asset.
		token.Asset = ""
		owned[tokenID] = token
	}
	return owned
}

func (s *Server) snapshotScene(ss *Session, member *Member, sceneID string, region *SceneRegion) any {
	scene := ss.Scenes[sceneID]
	return s.snapshotSceneAtFloor(ss, member, sceneID, region, currentFloorForMember(scene, member, ""))
}

func (s *Server) snapshotSceneAtFloor(ss *Session, member *Member, sceneID string, region *SceneRegion, floorID string) any {
	scene := ss.Scenes[sceneID]
	if !sceneVisible(scene, member) {
		return s.snapshotCampaign(ss, member)
	}
	if scene.Floors[floorID].ID == "" {
		floorID = currentFloorForMember(scene, member, "")
	}
	visibleFloors := visibleFloorSet(scene, floorID)
	tokens := map[string]Token{}
	elements := map[string]SceneElement{}
	assets := make(map[string]Asset)

	if region != nil {
		runtime := scene.ensureRuntime()
		for _, token := range runtime.query(*region) {
			if _, visible := visibleFloors[tokenFloorID(scene, token)]; visible && (member.Role == "gm" || !token.Hidden) {
				tokens[token.ID] = token
				if asset, ok := ss.Assets[token.Asset]; ok {
					assets[asset.ID] = asset
				}
			}
		}
		for _, element := range runtime.queryElements(*region) {
			if _, visible := visibleFloors[element.FloorID]; !visible {
				continue
			}
			if member.Role != "gm" && !runtime.elementPublic(element) {
				continue
			}
			elements[element.ID] = element
			if asset, ok := ss.Assets[element.AssetID]; ok {
				assets[asset.ID] = asset
			}
		}
	}

	members := make(map[string]Member, len(ss.Members))
	for memberID, campaignMember := range ss.Members {
		members[memberID] = *campaignMember
	}
	online := map[string]bool{}
	for p := range s.peers {
		if p.session == ss.ID {
			online[p.member.ID] = true
		}
	}
	metadata := SceneMetadata{ID: scene.ID, Name: scene.Name, Published: scene.Published, Revision: scene.Revision, Bounds: scene.Bounds}
	var entry *ScenePoint
	if region == nil {
		entry = sceneEntryPoint(scene, member)
	}
	layers := make(map[string]Layer, len(scene.Layers))
	for layerID, layer := range scene.Layers {
		if member.Role == "gm" {
			layers[layerID] = layer
		} else if _, visible := visibleFloors[layer.FloorID]; visible && layer.Kind != layerKindWalkable {
			layer.WalkableBounds = nil
			layers[layerID] = layer
		}
	}
	transitions := scene.Transitions
	if member.Role != "gm" {
		// Transition geometry is editor state. Runtime triggering is authoritative
		// on the server, so players do not need endpoint metadata.
		transitions = map[string]Transition{}
	}
	movementBounds, _ := walkableBoundsForFloor(scene, floorID)
	return map[string]any{
		"type":           "snapshot",
		"id":             ss.ID,
		"name":           ss.Name,
		"scene":          metadata,
		"revision":       scene.Revision,
		"currentFloorId": floorID,
		"movementBounds": movementBounds,
		"floors":         scene.Floors,
		"layers":         layers,
		"elements":       elements,
		"tokens":         tokens,
		"ownedTokens":    ownedTokenLocators(scene, member),
		"transitions":    transitions,
		"assets":         assets,
		"members":        members,
		"you":            *member,
		"online":         online,
		"region":         region,
		"entry":          entry,
	}
}

// Delivery is local to one Scene subscription. Scene revision describes the
// authoritative state; delivery describes only events actually sent to this
// peer, so filtered region-local events do not look like packet loss.
func (s *Server) snapshotSceneForPeer(ss *Session, p *peer) any {
	scene := ss.Scenes[p.sceneID]
	p.floorID = currentFloorForPeer(p, scene)
	snapshot := s.snapshotSceneAtFloor(ss, p.member, p.sceneID, p.region, p.floorID)
	if value, ok := snapshot.(map[string]any); ok && value["type"] == "snapshot" {
		value["delivery"] = p.delivery
		value["activeTokenId"] = p.activeTokenID
		if token, ok := activeTokenForMember(scene, p.member, p.activeTokenID); ok && p.region == nil {
			value["entry"] = &ScenePoint{X: token.X, Y: token.Y}
		}
	}
	return snapshot
}

func (s *Server) publishCampaign(ss *Session) {
	for p := range s.peers {
		if p.session == ss.ID {
			s.send(p, s.snapshotCampaign(ss, p.member))
		}
	}
}

func (s *Server) publishSceneSnapshot(ss *Session, sceneID string) {
	for p := range s.peers {
		if p.session == ss.ID && p.sceneID == sceneID {
			s.send(p, s.snapshotSceneForPeer(ss, p))
		}
	}
}

// Asset access is derived from currently accessible scene state. Knowing a
// campaign asset ID alone is not authorization. Runtime reference counts avoid
// rescanning all Scene objects for every binary tile/image request.
func assetVisibleTo(ss *Session, member *Member, sceneID, assetID string) bool {
	return assetVisibleToAtToken(ss, member, sceneID, assetID, "")
}

func assetVisibleToAtToken(ss *Session, member *Member, sceneID, assetID, activeTokenID string) bool {
	if _, ok := ss.Assets[assetID]; !ok {
		return false
	}
	scene := ss.Scenes[sceneID]
	if !sceneVisible(scene, member) {
		return false
	}
	rt := scene.ensureRuntime()
	if member.Role == "gm" {
		return rt.assetAll[assetID] > 0
	}
	floorID := currentFloorForMember(scene, member, "")
	if token, ok := activeTokenForMember(scene, member, activeTokenID); ok {
		floorID = tokenFloorID(scene, token)
	}
	for visibleFloorID := range visibleFloorSet(scene, floorID) {
		if rt.assetPublicByFloor[visibleFloorID][assetID] > 0 {
			return true
		}
	}
	return false
}
