package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Scene objects reuse the visual element identity and raster representation.
// The interaction payload is immutable after publication; edits replace it.
type ObjectMessage struct {
	Text         string `json:"text,omitempty"`
	ImageAssetID string `json:"imageAssetId,omitempty"`
}
type ObjectReaction struct {
	Any     ObjectMessage `json:"any,omitempty"`
	Success ObjectMessage `json:"success,omitempty"`
	Fail    ObjectMessage `json:"fail,omitempty"`
}
type ObjectInteraction struct {
	Disabled  bool                      `json:"disabled,omitempty"`
	Hover     ObjectMessage             `json:"hover,omitempty"`
	Reactions map[string]ObjectReaction `json:"reactions,omitempty"`
}
type ObjectGlowSettings struct {
	Mode  string `json:"mode"`
	Color string `json:"color"`
}

func normalizedObjectGlow(g ObjectGlowSettings) ObjectGlowSettings {
	if g.Mode == "" {
		return ObjectGlowSettings{Mode: "hover", Color: "#ffbc93"}
	}
	return g
}
func validObjectGlow(g ObjectGlowSettings) bool {
	if g.Mode != "always" && g.Mode != "hover" && g.Mode != "never" {
		return false
	}
	if len(g.Color) != 7 || g.Color[0] != '#' {
		return false
	}
	_, err := hex.DecodeString(g.Color[1:])
	return err == nil
}
func activeObject(e SceneElement) bool { return e.Interaction != nil && !e.Interaction.Disabled }
func objectName(e SceneElement) string {
	if name := strings.TrimSpace(e.Name); name != "" {
		return name
	}
	return "Объект"
}
func projectObjectElement(e SceneElement) SceneElement {
	// Neither snapshots nor incremental scene deltas carry unrevealed messages.
	if e.Interaction != nil {
		e.Interaction = &ObjectInteraction{Disabled: e.Interaction.Disabled}
	}
	return e
}
func objectMediaIDs(i *ObjectInteraction) []string {
	if i == nil {
		return nil
	}
	ids := []string{}
	add := func(m ObjectMessage) {
		if m.ImageAssetID != "" {
			ids = append(ids, m.ImageAssetID)
		}
	}
	add(i.Hover)
	for _, r := range i.Reactions {
		add(r.Any)
		add(r.Success)
		add(r.Fail)
	}
	return ids
}
func validateObjectInteraction(ss *Session, i *ObjectInteraction) error {
	if i == nil {
		return nil
	}
	if len(i.Reactions) > 512 {
		return fmt.Errorf("Слишком много реакций объекта")
	}
	total := 0
	validate := func(m ObjectMessage) error {
		total += len(m.Text)
		if utf8.RuneCountInString(m.Text) > 8192 || !utf8.ValidString(m.Text) || total > 256<<10 {
			return fmt.Errorf("Слишком длинное описание объекта")
		}
		if m.ImageAssetID != "" {
			a, ok := ss.Assets[m.ImageAssetID]
			if !ok || !isSceneRasterKind(a.Kind) {
				return fmt.Errorf("Изображение реакции не найдено")
			}
		}
		return nil
	}
	if err := validate(i.Hover); err != nil {
		return err
	}
	for key, r := range i.Reactions {
		if key == "" || len(key) > 512 {
			return fmt.Errorf("Некорректный ID действия")
		}
		for _, m := range []ObjectMessage{r.Any, r.Success, r.Fail} {
			if err := validate(m); err != nil {
				return err
			}
		}
	}
	return nil
}
func nonemptyObjectMessage(m ObjectMessage) bool {
	return strings.TrimSpace(m.Text) != "" || m.ImageAssetID != ""
}
func objectReaction(e SceneElement, action string, success *bool) ObjectMessage {
	if e.Interaction != nil {
		r := e.Interaction.Reactions[action]
		if success != nil {
			m := r.Fail
			if *success {
				m = r.Success
			}
			if nonemptyObjectMessage(m) {
				m.Text = strings.TrimSpace(m.Text)
				return m
			}
		}
		if nonemptyObjectMessage(r.Any) {
			m := r.Any
			m.Text = strings.TrimSpace(m.Text)
			return m
		}
	}
	return ObjectMessage{Text: objectName(e) + " молчит."}
}
func (a actionAudience) seesObjectOnFloor(id string) bool {
	if a.scene == nil || !sceneVisible(a.scene, a.peer.member) || a.peer.playerPreview && !a.scene.Published {
		return false
	}
	e, ok := a.scene.Elements[id]
	if !ok || !activeObject(e) || !a.scene.ensureRuntime().elementPublic(e) {
		return false
	}
	// The selected floor and its composed floors are shared with the renderer.
	if alpha, ok := visibleFloorSet(a.scene, currentFloorForPeer(a.peer, a.scene))[e.FloorID]; !ok || alpha <= 0 {
		return false
	}
	return true
}
func (a actionAudience) seesObject(id string) bool {
	return a.seesObjectOnFloor(id) && a.peer.region != nil && elementIntersectsRegion(a.scene.Elements[id], *a.peer.region)
}
func (a actionAudience) seesHistoricalTarget(ref ActionInstanceRef) bool {
	if ref.ElementID != "" {
		return a.seesObjectOnFloor(ref.ElementID)
	}
	return a.seesCharacter(ref.CharacterID, ref.ItemID != "")
}
func (a actionAudience) seesActionTarget(ref ActionInstanceRef) bool {
	if ref.ElementID != "" {
		return ref.CharacterID == "" && ref.ItemID == "" && a.seesObject(ref.ElementID)
	}
	return a.seesCharacter(ref.CharacterID, ref.ItemID != "")
}

// Short-lived capabilities are signed by a process-local secret unknown to all
// campaign members. They are scoped to one authenticated member and one asset.
var objectMediaSecret = func() [32]byte {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		panic(err)
	}
	return key
}()

func objectGrantMAC(ss *Session, m *Member, assetID, expiry string) []byte {
	h := hmac.New(sha256.New, objectMediaSecret[:])
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s", ss.ID, m.ID, assetID, expiry)
	return h.Sum(nil)
}
func objectMediaPayload(ss *Session, m *Member, assetID string) map[string]any {
	a, ok := ss.Assets[assetID]
	if !ok {
		return nil
	}
	expiry := strconv.FormatInt(time.Now().Add(5*time.Minute).Unix(), 10)
	return map[string]any{"asset": publicAsset(a), "grant": expiry + "." + hex.EncodeToString(objectGrantMAC(ss, m, assetID, expiry))}
}
func validObjectMediaGrant(ss *Session, m *Member, assetID, grant string) bool {
	expiry, signature, ok := strings.Cut(grant, ".")
	if !ok || len(signature) != 64 {
		return false
	}
	until, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil || until < time.Now().Unix() {
		return false
	}
	raw, err := hex.DecodeString(signature)
	return err == nil && hmac.Equal(raw, objectGrantMAC(ss, m, assetID, expiry))
}
func (s *Server) sendObjectHover(ss *Session, p *peer, c Command) {
	response := map[string]any{"type": "objectHover", "sceneId": p.sceneID, "seq": c.Seq, "id": c.Element.ID}
	defer func() { s.send(p, response) }()
	if c.SceneID != p.sceneID || !audienceForAction(ss, p).seesObject(c.Element.ID) {
		return
	}
	e := ss.Scenes[p.sceneID].Elements[c.Element.ID]
	response["text"] = e.Interaction.Hover.Text
	if assetID := e.Interaction.Hover.ImageAssetID; assetID != "" {
		response["media"] = objectMediaPayload(ss, p.member, assetID)
	}
}
func (s *Server) readSceneObject(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, m := s.auth(r)
	if ss == nil || !memberIsGM(m) {
		fail(w, 403, "Нужны права ведущего")
		return
	}
	scene := ss.Scenes[r.URL.Query().Get("scene")]
	if scene == nil {
		fail(w, 404, "Сцена не найдена")
		return
	}
	e, ok := scene.Elements[r.URL.Query().Get("id")]
	if !ok {
		fail(w, 404, "Элемент не найден")
		return
	}
	type option struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	actions := []option{}
	for id, plan := range ss.Ruleset.actionPlans {
		if plan.selector == "select(object)" {
			name := ss.Ruleset.Registry.Actions[id].Name
			if name == "" {
				name = id
			}
			actions = append(actions, option{id, name})
		}
	}
	slices.SortFunc(actions, func(a, b option) int {
		if n := strings.Compare(a.Name, b.Name); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
	assets := map[string]Asset{}
	for _, id := range append(objectMediaIDs(e.Interaction), e.AssetID) {
		if a, ok := ss.Assets[id]; ok {
			assets[id] = publicAsset(a)
		}
	}
	reply(w, map[string]any{"element": e, "actions": actions, "assets": assets})
}
func (s *Server) setObjectGlow(w http.ResponseWriter, r *http.Request) {
	var g ObjectGlowSettings
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&g) != nil || !validObjectGlow(g) {
		fail(w, 400, "Некорректные настройки подсветки")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, m := s.auth(r)
	if ss == nil || !memberIsGM(m) {
		fail(w, 403, "Нужны права ведущего")
		return
	}
	old, rev, dirty := ss.ObjectGlow, ss.CampaignRevision, s.dirty
	ss.ObjectGlow = g
	ss.CampaignRevision++
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		ss.ObjectGlow, ss.CampaignRevision, s.dirty = old, rev, dirty
		fail(w, 500, "Ошибка сохранения")
		return
	}
	for p := range s.peers {
		if p.session == ss.ID {
			s.send(p, map[string]any{"type": "objectGlowChanged", "objectGlow": g, "campaignRevision": ss.CampaignRevision})
		}
	}
	reply(w, g)
}

// Matrix edits use HTTP because a full set of reactions can exceed the WS
// command limit. Geometry and interaction toggles keep the normal scene path.
func (s *Server) saveSceneObject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string             `json:"name"`
		AssetID     string             `json:"assetId"`
		Interaction *ObjectInteraction `json:"interaction"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.Interaction == nil || utf8.RuneCountInString(req.Name) > 120 {
		fail(w, 400, "Некорректные настройки объекта")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, m := s.auth(r)
	if ss == nil || !memberIsGM(m) {
		fail(w, 403, "Нужны права ведущего")
		return
	}
	scene := ss.Scenes[r.URL.Query().Get("scene")]
	if scene == nil {
		fail(w, 404, "Сцена не найдена")
		return
	}
	old, ok := scene.Elements[r.URL.Query().Get("id")]
	if !ok {
		fail(w, 404, "Элемент не найден")
		return
	}
	a, ok := ss.Assets[req.AssetID]
	if !ok || !isSceneRasterKind(a.Kind) {
		fail(w, 400, "Изображение не найдено")
		return
	}
	if err := validateObjectInteraction(ss, req.Interaction); err != nil {
		fail(w, 400, err.Error())
		return
	}
	next := old
	next.Name = strings.TrimSpace(req.Name)
	if next.Name == "" {
		next.Name = objectName(old)
	}
	next.AssetID = req.AssetID
	next.Interaction = req.Interaction
	oldAssets := make(map[string]Asset, len(ss.Assets))
	for k, v := range ss.Assets {
		oldAssets[k] = v
	}
	oldRevision, oldDirty := scene.Revision, s.dirty
	scene.Elements[next.ID] = next
	scene.Revision++
	refreshAssetOrphans(ss, time.Now())
	scene.applyElementRuntimeChange(old, true, next, true)
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		scene.Elements[old.ID] = old
		scene.Revision = oldRevision
		ss.Assets = oldAssets
		s.dirty = oldDirty
		scene.rebuildRuntime()
		fail(w, 500, "Ошибка сохранения объекта")
		return
	}
	s.publishElement(ss, scene.ID, old, true, next, true, false)
	reply(w, map[string]any{"element": projectObjectElement(next), "revision": scene.Revision})
}
