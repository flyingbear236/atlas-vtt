package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// A portrait is presentation metadata of a character instance. It never
// participates in token avatar resolution or in the mechanical projection.
func characterPortraitAsset(ss *Session, c CharacterInstance) string {
	id := c.PortraitAssetID
	if id == "" && c.PortraitPath != "" {
		id = ss.Ruleset.Media[c.PortraitPath]
	}
	if id != "" && universalMedia(ss.Assets[id]) {
		return id
	}
	return ""
}

func (s *Server) setCharacterPortrait(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CharacterID string `json:"characterId"`
		AssetID     string `json:"assetId"`
		Text        string `json:"text"`
		// When only editing the text, preserve unresolved portable paths too.
		KeepImage bool `json:"keepImage"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || validateText("portrait text", req.Text, 8192, true) != nil {
		fail(w, 400, "Некорректный артворк или слишком длинное описание")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, m := s.auth(r)
	if !memberCanAccessCharacter(ss, m, req.CharacterID) {
		fail(w, 403, "Персонаж недоступен")
		return
	}
	old := ss.CharacterInstances[req.CharacterID]
	next := old
	next.PortraitText = strings.TrimSpace(req.Text)
	if !req.KeepImage {
		if req.AssetID != "" && (!universalMedia(ss.Assets[req.AssetID]) || !(req.AssetID == characterPortraitAsset(ss, old) || avatarAssetVisibleTo(ss, m, req.AssetID))) {
			fail(w, 403, "Изображение недоступно")
			return
		}
		next.PortraitAssetID, next.PortraitPath = req.AssetID, ""
	}
	if next.PortraitAssetID == old.PortraitAssetID && next.PortraitPath == old.PortraitPath && next.PortraitText == old.PortraitText {
		reply(w, map[string]any{"assetId": characterPortraitAsset(ss, old), "text": old.PortraitText})
		return
	}
	assets, rev, dirty := cloneAssetRegistry(ss.Assets), ss.CharacterRevision, s.dirty
	ss.CharacterInstances[req.CharacterID] = next
	ss.CharacterRevision++
	refreshAssetOrphans(ss, time.Now())
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		ss.CharacterInstances[req.CharacterID], ss.Assets, ss.CharacterRevision, s.dirty = old, assets, rev, dirty
		fail(w, 500, "Ошибка сохранения артворка")
		return
	}
	s.refreshCharacterWatches(ss, map[string]bool{req.CharacterID: false}, false)
	reply(w, map[string]any{"assetId": characterPortraitAsset(ss, next), "text": next.PortraitText})
}

// Only the specific loaded, visible token can expose its public handout.
// A character ID supplied by the client is never sufficient authorization.
func visibleCharacterHover(ss *Session, p *peer, tokenID string) (CharacterInstance, bool) {
	scene := ss.Scenes[p.sceneID]
	if !sceneVisible(scene, p.member) || p.playerPreview && !scene.Published {
		return CharacterInstance{}, false
	}
	token, ok := scene.Tokens[tokenID]
	if !ok || !tokenLoadedForPeer(p, scene, token) {
		return CharacterInstance{}, false
	}
	if visibleFloorSet(scene, currentFloorForPeer(p, scene))[tokenFloorID(scene, token)] <= 0 {
		return CharacterInstance{}, false
	}
	c, ok := ss.CharacterInstances[token.CharacterInstanceID]
	return c, ok
}

func (s *Server) sendCharacterHover(ss *Session, p *peer, c Command) {
	response := map[string]any{"type": "objectHover", "sceneId": p.sceneID, "seq": c.Seq, "id": "character:" + c.TokenID}
	defer func() { s.send(p, response) }()
	if c.SceneID != p.sceneID {
		return
	}
	instance, ok := visibleCharacterHover(ss, p, c.TokenID)
	if !ok {
		return
	}
	response["text"] = instance.PortraitText
	if id := characterPortraitAsset(ss, instance); id != "" {
		response["media"] = objectMediaPayload(ss, p.member, id)
	}
}
