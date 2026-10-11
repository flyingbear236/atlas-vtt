package main

import (
	"net/http"
	"sort"
	"time"
)

// Campaign media are physical assets shared by the scene and logical ruleset
// references. Uploading an asset does not edit the committed ruleset.
func (s *Server) uploadCampaignMedia(w http.ResponseWriter, request *http.Request) {
	s.mu.Lock()
	session, member := s.auth(request)
	allowed := memberIsGM(member)
	s.mu.Unlock()
	if !allowed { fail(w, http.StatusForbidden, "Нужны права ведущего"); return }
	select {
	case s.imageJobs <- struct{}{}:
		defer func(){ <-s.imageJobs }()
	default:
		fail(w, http.StatusTooManyRequests, "Другое изображение уже обрабатывается")
		return
	}
	request.Body = http.MaxBytesReader(w, request.Body, avatarUploadLimit)
	asset, err := prepareReader(request.Context(), s.root, request.Body, assetKindAvatar)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, currentMember := s.auth(request)
	if request.Context().Err() != nil || current != session || !memberIsGM(currentMember) || s.stopping {
		s.discardPreparedAssetLocked(asset.ID)
		fail(w, http.StatusConflict, "Права на загрузку изменились")
		return
	}
	oldAssets, oldDirty := cloneAssetRegistry(session.Assets), s.dirty
	if previous, exists := session.Assets[asset.ID]; exists {
		asset = previous
	}
	// Common campaign media stay available for future picker selections.
	asset.RetentionPolicy = assetKeep
	asset.OrphanSince = nil
	session.Assets[asset.ID] = asset
	refreshAssetOrphans(session, time.Now())
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		session.Assets, s.dirty = oldAssets, oldDirty
		s.discardPreparedAssetLocked(asset.ID)
		fail(w, http.StatusInternalServerError, "Ошибка сохранения media")
		return
	}
	reply(w, asset)
}

type campaignMediaItem struct {
	ID string `json:"id"`
	Filename string `json:"filename"`
	Kind string `json:"kind"`
}

func (s *Server) readCampaignMedia(w http.ResponseWriter, request *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, member := s.auth(request)
	if !memberIsGM(member) { fail(w, http.StatusForbidden, "Нужны права ведущего"); return }
	items := make([]campaignMediaItem, 0)
	for _, asset := range session.Assets {
		if isCharacterArtworkAsset(asset) {
			items = append(items, campaignMediaItem{ID: asset.ID, Filename: asset.Filename, Kind: asset.Kind})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	reply(w, items)
}

