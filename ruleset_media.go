package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

const maxRulesetMediaEntries = 1024

type ArtworkResolution struct {
	AssetID string `json:"assetId,omitempty"`
	Source  string `json:"source"` // character, preset, or placeholder
	Status  string `json:"status"` // resolved, missing, or placeholder
	Path    string `json:"path,omitempty"`
}

type ArtworkWarning struct {
	PresetID string `json:"presetId"`
	Path     string `json:"path"`
}

func validateRulesetMediaPath(name string) error {
	if len(name) < len("media/a.png") || len(name) > 255 || !strings.HasPrefix(name, "media/") || name != path.Clean(name) || strings.ContainsAny(name, "\\:") {
		return fmt.Errorf("invalid ruleset media path %q", name)
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "." || segment == ".." || segment == "" || strings.HasPrefix(segment, ".") {
			return fmt.Errorf("invalid ruleset media path %q", name)
		}
		for _, ch := range segment {
			if unicode.IsControl(ch) {
				return fmt.Errorf("invalid ruleset media path %q", name)
			}
		}
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp":
		return nil
	}
	return fmt.Errorf("unsupported ruleset media path %q", name)
}

func validateRulesetMediaAssets(session *Session, snapshot RulesetSnapshot) error {
	for name, id := range snapshot.Media {
		asset, ok := session.Assets[id]
		if !ok || !isCharacterArtworkAsset(asset) {
			return fmt.Errorf("ruleset media %q references unavailable campaign image %q", name, id)
		}
	}
	return nil
}

func compileRulesetPackage(pkg rulesetPackage) (RulesetSnapshot, error) {
	snapshot, err := CompileRulesetFiles(pkg.Files)
	if err != nil {
		return RulesetSnapshot{}, err
	}
	if len(pkg.Media) != 0 {
		snapshot.Media = make(map[string]string, len(pkg.Media))
		for name, id := range pkg.Media {
			snapshot.Media[name] = id
		}
	}
	if err = ValidateRulesetSnapshot(snapshot); err != nil {
		return RulesetSnapshot{}, err
	}
	return snapshot, nil
}

// Keep the existing digest unchanged when no media mapping is supplied so old
// TOML-only clients remain compatible with the preview/apply handshake.
func rulesetPackageDigest(pkg rulesetPackage) string {
	base := rulesetDocumentsDigest(pkg.Files)
	if len(pkg.Media) == 0 {
		return base
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, base)
	names := make([]string, 0, len(pkg.Media))
	for name := range pkg.Media {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		_, _ = hash.Write([]byte{0})
		_, _ = io.WriteString(hash, name)
		_, _ = hash.Write([]byte{0})
		_, _ = io.WriteString(hash, pkg.Media[name])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func resolvePresetArtwork(preset *CharacterPresetDefinition, snapshot RulesetSnapshot, assets map[string]Asset) ArtworkResolution {
	empty := ArtworkResolution{Source: "placeholder", Status: "placeholder"}
	if preset == nil {
		return empty
	}
	if preset.Artwork != "" {
		result := ArtworkResolution{Source: "preset", Status: "missing", Path: preset.Artwork}
		if assetID, exists := snapshot.Media[preset.Artwork]; exists && isCharacterArtworkAsset(assets[assetID]) {
			result.AssetID, result.Status = assetID, "resolved"
		}
		return result
	}
	if preset.AvatarAssetID != "" {
		if isCharacterArtworkAsset(assets[preset.AvatarAssetID]) {
			return ArtworkResolution{Source: "preset", Status: "resolved", AssetID: preset.AvatarAssetID}
		}
		return ArtworkResolution{Source: "preset", Status: "missing"}
	}
	return empty
}

func resolveCharacterArtwork(session *Session, instance CharacterInstance, preset *CharacterPresetDefinition, snapshot RulesetSnapshot) ArtworkResolution {
	if instance.ArtworkPath != "" {
		result := ArtworkResolution{Source: "character", Status: "missing", Path: instance.ArtworkPath}
		if assetID, ok := snapshot.Media[instance.ArtworkPath]; ok && isCharacterArtworkAsset(session.Assets[assetID]) {
			result.AssetID = assetID
			result.Status = "resolved"
		}
		return result
	}
	if instance.AvatarAssetID != nil {
		if *instance.AvatarAssetID == "" {
			return ArtworkResolution{Source: "placeholder", Status: "placeholder"}
		}
		if isCharacterArtworkAsset(session.Assets[*instance.AvatarAssetID]) {
			return ArtworkResolution{Source: "character", Status: "resolved", AssetID: *instance.AvatarAssetID}
		}
		return ArtworkResolution{Source: "character", Status: "missing"}
	}
	return resolvePresetArtwork(preset, snapshot, session.Assets)
}

func rulesetArtworkWarnings(snapshot RulesetSnapshot, assets map[string]Asset) []ArtworkWarning {
	var warnings []ArtworkWarning
	for id, preset := range snapshot.Registry.Presets {
		if preset.Artwork != "" && resolvePresetArtwork(&preset, snapshot, assets).Status == "missing" {
			warnings = append(warnings, ArtworkWarning{PresetID: id, Path: preset.Artwork})
		}
	}
	sort.Slice(warnings, func(i, j int) bool { return warnings[i].PresetID < warnings[j].PresetID })
	return warnings
}

func suggestMediaPath(filename string, existing map[string]string) string {
	filename = strings.ReplaceAll(filename, "\\", "/")
	filename = path.Base(filename)
	ext := strings.ToLower(path.Ext(filename))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp":
	default:
		ext = ".png"
	}
	base := strings.TrimSuffix(filename, path.Ext(filename))
	var cleaned strings.Builder
	for _, ch := range strings.ToLower(base) {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '-' || ch == '_' {
			cleaned.WriteRune(ch)
		} else if cleaned.Len() > 0 && !strings.HasSuffix(cleaned.String(), "-") {
			cleaned.WriteByte('-')
		}
	}
	base = strings.Trim(cleaned.String(), "-_")
	if base == "" {
		base = "image"
	}
	if len(base) > 180 {
		runes := []rune(base)
		for len(string(runes)) > 180 {
			runes = runes[:len(runes)-1]
		}
		base = string(runes)
	}
	candidate := "media/" + base + ext
	for n := 2; ; n++ {
		if _, used := existing[candidate]; !used {
			return candidate
		}
		candidate = fmt.Sprintf("media/%s-%d%s", base, n, ext)
	}
}

func (s *Server) listRulesetMediaAssets(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, member := s.auth(r)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	assets := make([]Asset, 0)
	for _, asset := range session.Assets {
		if isCharacterArtworkAsset(asset) {
			assets = append(assets, asset)
		}
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].ID < assets[j].ID })
	reply(w, map[string]any{"assets": assets, "rulesetMedia": session.Ruleset.Media})
}

func (s *Server) suggestRulesetMediaPath(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, member := s.auth(r)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	name := r.URL.Query().Get("filename")
	if len(name) > 255 {
		fail(w, http.StatusBadRequest, "Filename too long")
		return
	}
	existing := make(map[string]string, len(session.Ruleset.Media))
	for path, assetID := range session.Ruleset.Media {
		existing[path] = assetID
	}
	for _, path := range r.URL.Query()["used"] {
		if err := validateRulesetMediaPath(path); err == nil {
			existing[path] = ""
		}
	}
	reply(w, map[string]string{"path": suggestMediaPath(name, existing)})
}

// Upload registers an immutable image in the shared campaign asset store.
// The resulting ID can be included in a later candidate Media map; upload
// itself never changes ruleset ownership or mechanical definitions.
func (s *Server) uploadRulesetMedia(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	session, member := s.auth(r)
	allowed := memberIsGM(member) && !s.stopping
	s.mu.Unlock()
	if !allowed {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	select {
	case s.imageJobs <- struct{}{}:
		defer func() { <-s.imageJobs }()
	default:
		fail(w, http.StatusTooManyRequests, "Другое изображение уже обрабатывается")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, avatarUploadLimit)
	asset, err := prepareReader(r.Context(), s.root, r.Body, assetKindAvatar)
	if err != nil {
		var tooLarge *http.MaxBytesError
		status := http.StatusBadRequest
		if errors.As(err, &tooLarge) || errors.Is(err, errAvatarUploadSize) {
			status = http.StatusRequestEntityTooLarge
		}
		if errors.Is(err, errVipsMissing) {
			status = http.StatusServiceUnavailable
		}
		fail(w, status, err.Error())
		return
	}
	s.mu.Lock()
	currentSession, currentMember := s.auth(r)
	if s.stopping || r.Context().Err() != nil || currentSession != session || !memberIsGM(currentMember) {
		s.discardPreparedAssetLocked(asset.ID)
		s.mu.Unlock()
		fail(w, http.StatusConflict, "Права изменились во время загрузки")
		return
	}
	oldAssets, oldDirty := cloneAssetRegistry(session.Assets), s.dirty
	if previous, ok := session.Assets[asset.ID]; ok {
		asset.RetentionPolicy, asset.CreatedAt, asset.OrphanSince = previous.RetentionPolicy, previous.CreatedAt, previous.OrphanSince
	}
	session.Assets[asset.ID] = asset
	refreshAssetOrphans(session, time.Now())
	s.dirty = true
	if err = s.saveLocked(); err != nil {
		session.Assets, s.dirty = oldAssets, oldDirty
		s.discardPreparedAssetLocked(asset.ID)
		s.mu.Unlock()
		fail(w, http.StatusInternalServerError, "Ошибка сохранения media")
		return
	}
	path := suggestMediaPath(r.URL.Query().Get("filename"), session.Ruleset.Media)
	s.mu.Unlock()
	reply(w, map[string]any{"asset": asset, "suggestedPath": path})
}

// The shared campaign picker can display any registered image to a GM, even
// if the image is not yet referenced by a token, character or ruleset.
func (s *Server) readRulesetMediaImage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	session, member := s.auth(r)
	assetID := r.PathValue("id")
	allowed := memberIsGM(member) && isCharacterArtworkAsset(session.Assets[assetID])
	asset := Asset{}
	if session != nil {
		asset = session.Assets[assetID]
	}
	s.mu.Unlock()
	if !allowed {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	if err := validateID(assetID); err != nil {
		fail(w, http.StatusBadRequest, "Некорректный asset ID")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	filename := "avatar.png"
	if asset.Kind == assetKindToken {
		filename = "token.png"
	}
	http.ServeFile(w, r, filepath.Join(s.root, "assets", assetID, filename))
}

// attachRulesetMedia assigns an existing shared asset a stable logical path.
// It does not rewrite user TOML or make a copy of the image.
func (s *Server) attachRulesetMedia(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req struct {
		AssetID  string `json:"assetId"`
		Filename string `json:"filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Некорректный media request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, member := s.auth(r)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	if !isCharacterArtworkAsset(session.Assets[req.AssetID]) {
		fail(w, http.StatusBadRequest, "Изображение недоступно")
		return
	}
	var existing []string
	for name, id := range session.Ruleset.Media {
		if id == req.AssetID {
			existing = append(existing, name)
		}
	}
	if len(existing) != 0 {
		sort.Strings(existing)
		reply(w, map[string]any{"path": existing[0], "registryRevision": session.RegistryRevision})
		return
	}
	if len(session.Ruleset.Media) >= maxRulesetMediaEntries {
		fail(w, http.StatusBadRequest, "Media library is full")
		return
	}
	if len(req.Filename) > 255 {
		fail(w, http.StatusBadRequest, "Filename too long")
		return
	}
	filename := strings.TrimSuffix(req.Filename, path.Ext(req.Filename)) + ".png"
	name := suggestMediaPath(filename, session.Ruleset.Media)
	previous := cloneRulesetSnapshot(session.Ruleset)
	oldAssets, oldDirty, oldRevision := cloneAssetRegistry(session.Assets), s.dirty, session.RegistryRevision
	if session.Ruleset.Media == nil {
		session.Ruleset.Media = map[string]string{}
	}
	session.Ruleset.Media[name] = req.AssetID
	if err := ValidateRulesetSnapshot(session.Ruleset); err != nil {
		session.Ruleset = previous
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	refreshAssetOrphans(session, time.Now())
	session.RegistryRevision++
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		session.Ruleset = previous
		session.Assets = oldAssets
		session.RegistryRevision = oldRevision
		s.dirty = oldDirty
		fail(w, http.StatusInternalServerError, "Ошибка сохранения media")
		return
	}
	s.publishRegistryChanged(session)
	s.refreshCharacterWatches(session, nil, true)
	reply(w, map[string]any{"path": name, "registryRevision": session.RegistryRevision})
}
