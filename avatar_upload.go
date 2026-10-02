package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type avatarUploadTarget struct {
	characterID string
	presetID    string
}

func avatarTargetFromRequest(request *http.Request) (avatarUploadTarget, error) {
	target := avatarUploadTarget{
		characterID: request.URL.Query().Get("characterId"),
		presetID:    request.URL.Query().Get("presetId"),
	}
	if (target.characterID == "") == (target.presetID == "") {
		return avatarUploadTarget{}, errors.New("Укажите ровно один characterId или presetId")
	}
	if target.characterID != "" {
		if err := validateID(target.characterID); err != nil {
			return avatarUploadTarget{}, errors.New("Некорректный characterId")
		}
	} else if err := validateID(target.presetID); err != nil {
		return avatarUploadTarget{}, errors.New("Некорректный presetId")
	}
	return target, nil
}

func avatarTargetAllowed(session *Session, member *Member, target avatarUploadTarget) bool {
	if session == nil || member == nil {
		return false
	}
	if target.characterID != "" {
		return memberCanAccessCharacter(session, member, target.characterID)
	}
	if !memberIsGM(member) {
		return false
	}
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		return false
	}
	_, exists := registry.Presets[target.presetID]
	return exists
}

func cloneAssetRegistry(assets map[string]Asset) map[string]Asset {
	result := make(map[string]Asset, len(assets))
	for assetID, asset := range assets {
		result[assetID] = asset
	}
	return result
}

// discardPreparedAssetLocked removes an immutable representation only when no
// campaign registered it. The upload limiter serializes preparation, while the
// scan protects content shared by another Session.
func (s *Server) discardPreparedAssetLocked(assetID string) {
	if assetID == "" {
		return
	}
	for _, session := range s.sessions {
		if _, exists := session.Assets[assetID]; exists {
			return
		}
	}
	_ = os.RemoveAll(filepath.Join(s.root, "assets", assetID))
}

func (s *Server) uploadAvatar(w http.ResponseWriter, request *http.Request) {
	target, err := avatarTargetFromRequest(request)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	session, member := s.auth(request)
	allowed := avatarTargetAllowed(session, member, target)
	s.mu.Unlock()
	if !allowed {
		fail(w, http.StatusForbidden, "Avatar недоступен")
		return
	}

	select {
	case s.imageJobs <- struct{}{}:
		defer func() { <-s.imageJobs }()
	default:
		fail(w, http.StatusTooManyRequests, "Другое изображение уже обрабатывается")
		return
	}
	request.Body = http.MaxBytesReader(w, request.Body, avatarUploadLimit)
	asset, err := prepareReader(request.Context(), s.root, request.Body, assetKindAvatar)
	if err != nil {
		status := http.StatusBadRequest
		var maxBytes *http.MaxBytesError
		if errors.Is(err, errAvatarUploadSize) || errors.As(err, &maxBytes) {
			status = http.StatusRequestEntityTooLarge
		}
		if errors.Is(err, errVipsMissing) {
			status = http.StatusServiceUnavailable
		}
		fail(w, status, err.Error())
		return
	}

	s.mu.Lock()
	if request.Context().Err() != nil {
		s.discardPreparedAssetLocked(asset.ID)
		s.mu.Unlock()
		return
	}
	currentSession, currentMember := s.auth(request)
	if s.stopping || currentSession != session || !avatarTargetAllowed(session, currentMember, target) {
		s.discardPreparedAssetLocked(asset.ID)
		s.mu.Unlock()
		fail(w, http.StatusConflict, "Права на avatar изменились во время загрузки")
		return
	}

	oldAssets := cloneAssetRegistry(session.Assets)
	oldCampaign := cloneCampaignRegistry(session.CampaignDefinitions)
	oldRegistryRevision := session.RegistryRevision
	oldCharacterRevision := session.CharacterRevision
	oldDirty := s.dirty
	var oldInstance CharacterInstance
	if target.characterID != "" {
		oldInstance = cloneCharacterInstance(session.CharacterInstances[target.characterID])
	}
	if previous, exists := session.Assets[asset.ID]; exists {
		asset.RetentionPolicy = previous.RetentionPolicy
		asset.CreatedAt = previous.CreatedAt
		asset.OrphanSince = previous.OrphanSince
	}
	session.Assets[asset.ID] = asset

	if target.characterID != "" {
		instance := cloneCharacterInstance(session.CharacterInstances[target.characterID])
		instance = WithAvatarOverride(instance, asset.ID)
		session.CharacterInstances[target.characterID] = instance
	} else {
		registry, mergeErr := MergeDefinitionRegistries(sessionRegistries(session))
		if mergeErr != nil {
			session.Assets = oldAssets
			s.discardPreparedAssetLocked(asset.ID)
			s.mu.Unlock()
			fail(w, http.StatusConflict, mergeErr.Error())
			return
		}
		preset := clonePresetDefinition(registry.Presets[target.presetID])
		preset.AvatarAssetID = asset.ID
		campaign := cloneCampaignRegistry(session.CampaignDefinitions)
		campaign.Presets[target.presetID] = preset
		session.CampaignDefinitions = campaign
		session.RegistryRevision++
	}
	session.CharacterRevision++
	refreshAssetOrphans(session, time.Now())
	if err = validateSessionCharacterState(session); err != nil {
		session.Assets = oldAssets
		session.CampaignDefinitions = oldCampaign
		if target.characterID != "" {
			session.CharacterInstances[target.characterID] = oldInstance
		}
		session.RegistryRevision = oldRegistryRevision
		session.CharacterRevision = oldCharacterRevision
		s.discardPreparedAssetLocked(asset.ID)
		s.mu.Unlock()
		fail(w, http.StatusConflict, err.Error())
		return
	}
	s.dirty = true
	if err = s.saveLocked(); err != nil {
		session.Assets = oldAssets
		session.CampaignDefinitions = oldCampaign
		if target.characterID != "" {
			session.CharacterInstances[target.characterID] = oldInstance
		}
		session.RegistryRevision = oldRegistryRevision
		session.CharacterRevision = oldCharacterRevision
		s.dirty = oldDirty
		s.discardPreparedAssetLocked(asset.ID)
		s.mu.Unlock()
		fail(w, http.StatusInternalServerError, "Ошибка сохранения avatar")
		return
	}
	if target.presetID != "" {
		s.publishRegistryChanged(session)
		s.refreshCharacterWatches(session, nil, true)
	} else {
		s.refreshCharacterWatches(session, map[string]bool{target.characterID: false}, false)
	}
	s.mu.Unlock()
	reply(w, asset)
}

func avatarAssetVisibleTo(session *Session, member *Member, assetID string) bool {
	asset, exists := session.Assets[assetID]
	if !exists || asset.Kind != assetKindAvatar {
		return false
	}
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		return false
	}
	if memberIsGM(member) {
		for _, preset := range session.CampaignDefinitions.Presets {
			if preset.AvatarAssetID == assetID {
				return true
			}
		}
		for _, instance := range session.CharacterInstances {
			if effectiveCharacterAvatar(instance, registry) == assetID {
				return true
			}
		}
		return false
	}
	for characterID, instance := range session.CharacterInstances {
		if memberCanAccessCharacter(session, member, characterID) && effectiveCharacterAvatar(instance, registry) == assetID {
			return true
		}
	}
	return false
}

func effectiveCharacterAvatar(instance CharacterInstance, registry EffectiveRegistry) string {
	if instance.AvatarAssetID != nil {
		return *instance.AvatarAssetID
	}
	return registry.Presets[instance.PresetID].AvatarAssetID
}
