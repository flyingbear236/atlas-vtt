package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

func (s *Server) previewPortableImport(w http.ResponseWriter, r *http.Request) {
	// ZIP is read into an on-disk bounded upload. Validation completes while it
	// remains available; see withPortableImport below.
	s.withPortableImport(w, r, false)
}
func (s *Server) applyPortableImport(w http.ResponseWriter, r *http.Request) {
	s.withPortableImport(w, r, true)
}

func (s *Server) withPortableImport(w http.ResponseWriter, r *http.Request, apply bool) {
	s.mu.Lock()
	ss, member := s.auth(r)
	allowed := memberIsGM(member) && !s.stopping
	s.mu.Unlock()
	if !allowed {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	// A distinct temporary upload is required because a ZIP entry holds a
	// reference to its parent file until the end of this handler.
	r.Body = http.MaxBytesReader(w, r.Body, maxPortableImportCompressed+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()
	raw := r.FormValue("options")
	if len(raw) > 65536 {
		fail(w, http.StatusBadRequest, "Слишком много параметров импорта")
		return
	}
	var opts portableImportOptions
	if err := json.Unmarshal([]byte(raw), &opts); err != nil {
		fail(w, http.StatusBadRequest, "Некорректные параметры импорта")
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		fail(w, http.StatusBadRequest, "Загрузите ZIP")
		return
	}
	defer f.Close()
	tmp, err := os.CreateTemp(s.root, "atlas-import-*.zip")
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, digest), io.LimitReader(f, maxPortableImportCompressed+1))
	if err != nil || n > maxPortableImportCompressed {
		fail(w, http.StatusRequestEntityTooLarge, "ZIP превышает лимит")
		return
	}
	zipReader, err := zip.NewReader(tmp, n)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	archive, err := readPortableImportArchive(zipReader)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	hexDigest := hex.EncodeToString(digest.Sum(nil))
	s.mu.Lock()
	ss, member = s.auth(r)
	if ss == nil || !memberIsGM(member) || s.stopping {
		s.mu.Unlock()
		fail(w, http.StatusForbidden, "Доступ запрещён")
		return
	}
	base := cloneForPortableImport(ss)
	regRev, charRev, campaignRev, libRev := ss.RegistryRevision, ss.CharacterRevision, ss.CampaignRevision, ss.LibraryRevision
	s.mu.Unlock()
	if apply {
		expectedCampaign, e0 := strconv.ParseUint(r.FormValue("campaignRevision"), 10, 64)
		expectedReg, e1 := strconv.ParseUint(r.FormValue("registryRevision"), 10, 64)
		expectedChar, e2 := strconv.ParseUint(r.FormValue("characterRevision"), 10, 64)
		expectedLib, e3 := strconv.ParseUint(r.FormValue("libraryRevision"), 10, 64)
		if e0 != nil || expectedCampaign != campaignRev || e1 != nil || e2 != nil || e3 != nil || expectedLib != libRev || expectedReg != regRev || expectedChar != charRev || r.FormValue("digest") != hexDigest || r.FormValue("optionsDigest") != portableOptionsDigest(opts) {
			fail(w, http.StatusConflict, "Изменились ревизии или содержимое ZIP: повторите preview")
			return
		}
	}
	// Discover conflicts without preparing images. A preview never writes to
	// Asset Store; synthetic IDs have only ephemeral metadata in the candidate.
	snapshot, err := buildPortableImportCandidate(r.Context(), base, archive, opts, s.root, cloneAssetRegistry(base.Assets), nil, false)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot.Preview.Digest = hexDigest
	snapshot.Preview.CampaignRevision = campaignRev
	snapshot.Preview.RegistryRevision = regRev
	snapshot.Preview.CharacterRevision = charRev
	snapshot.Preview.LibraryRevision = libRev
	snapshot.Preview.OptionsDigest = portableOptionsDigest(opts)
	if !apply {
		reply(w, snapshot.Preview)
		return
	}
	// Acquiring the existing upload semaphore limits simultaneous vips jobs.
	if len(archive.Media) > 0 {
		select {
		case s.imageJobs <- struct{}{}:
			defer func() { <-s.imageJobs }()
		case <-r.Context().Done():
			fail(w, http.StatusRequestTimeout, "Загрузка отменена")
			return
		default:
			fail(w, http.StatusTooManyRequests, "Другое изображение уже обрабатывается")
			return
		}
	}
	prepared := map[string]Asset{}
	preparedPaths := map[string]string{}
	cleanup := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for id := range prepared {
			s.discardPreparedAssetLocked(id)
		}
	}
	committed := false
	defer func() {
		if !committed {
			cleanup()
		}
	}()
	names := make([]string, 0, len(archive.Media))
	for name := range archive.Media {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		entry := archive.Media[name]
		if entry == nil {
			continue
		}
		oldID := base.Ruleset.Media[name]
		if oldID != "" {
			if opts.Media[name] == "keep" || equalImportedMedia(r.Context(), s.root, entry, base.Assets[oldID]) {
				continue
			}
		}
		asset, e := preparePortableImportMedia(r.Context(), s.root, entry)
		if e != nil {
			fail(w, http.StatusBadRequest, fmt.Sprintf("Медиа %s: %v", name, e))
			return
		}
		prepared[asset.ID] = asset
		preparedPaths[name] = asset.ID
	}
	allAssets := cloneAssetRegistry(base.Assets)
	for id, asset := range prepared {
		allAssets[id] = asset
	}
	next, err := buildPortableImportCandidate(r.Context(), base, archive, opts, s.root, allAssets, preparedPaths, true)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	s.mu.Lock()
	live, actor := s.auth(r)
	if live != ss || !memberIsGM(actor) || s.stopping || ss.RegistryRevision != regRev || ss.CharacterRevision != charRev || ss.CampaignRevision != campaignRev || ss.LibraryRevision != libRev {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "Состояние кампании изменилось: повторите preview")
		return
	}
	oldRuleset := ss.Ruleset
	oldChars := ss.CharacterInstances
	oldSources := ss.CharacterSources
	oldAssets := ss.Assets
	oldUnknown, oldDirs := ss.UnknownTOML, ss.LibraryDirectories
	oldReg, oldChar, oldLibrary, oldDirty := ss.RegistryRevision, ss.CharacterRevision, ss.LibraryRevision, s.dirty
	ss.Ruleset = next.Ruleset
	ss.CharacterInstances = next.Characters
	ss.CharacterSources = next.Sources
	ss.Assets = next.Assets
	ss.UnknownTOML, ss.LibraryDirectories = next.Unknown, next.Directories
	ss.RegistryRevision++
	ss.CharacterRevision++
	ss.LibraryRevision++
	refreshAssetOrphans(ss, time.Now())
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		ss.Ruleset, ss.CharacterInstances, ss.CharacterSources, ss.Assets = oldRuleset, oldChars, oldSources, oldAssets
		ss.UnknownTOML, ss.LibraryDirectories = oldUnknown, oldDirs
		ss.RegistryRevision, ss.CharacterRevision, ss.LibraryRevision, s.dirty = oldReg, oldChar, oldLibrary, oldDirty
		s.mu.Unlock()
		fail(w, http.StatusInternalServerError, "Ошибка сохранения импорта: "+err.Error())
		return
	}
	committed = true
	s.publishRegistryChanged(ss)
	s.refreshCharacterWatches(ss, nil, true)
	s.mu.Unlock()
	reply(w, map[string]any{"registryRevision": ss.RegistryRevision, "characterRevision": ss.CharacterRevision, "importedCharacters": len(next.Preview.AddedCharacters), "replacedFiles": len(next.Preview.ReplacedFiles)})
}

func preparePortableImportMedia(ctx context.Context, root string, f *zip.File) (Asset, error) {
	r, err := f.Open()
	if err != nil {
		return Asset{}, err
	}
	defer r.Close()
	switch strings.ToLower(filepath.Ext(f.Name)) {
	case ".png", ".jpg", ".jpeg":
		return prepareReader(ctx, root, io.LimitReader(r, int64(maxPortableImportImage)+1), assetKindAvatar)
	case ".webp":
		tool, e := findVips()
		if e != nil {
			return Asset{}, e
		}
		stage, e := os.MkdirTemp(root, "atlas-import-webp-*")
		if e != nil {
			return Asset{}, e
		}
		defer os.RemoveAll(stage)
		src := filepath.Join(stage, "image.webp")
		dst := filepath.Join(stage, "image.png")
		out, e := os.Create(src)
		if e != nil {
			return Asset{}, e
		}
		n, e := io.Copy(out, io.LimitReader(r, int64(maxPortableImportImage)+1))
		closeErr := out.Close()
		if e != nil {
			return Asset{}, e
		}
		if closeErr != nil {
			return Asset{}, closeErr
		}
		if n > int64(maxPortableImportImage) {
			return Asset{}, errors.New("image too large")
		}
		if e = runVips(ctx, tool, stage, "copy", src, dst); e != nil {
			return Asset{}, e
		}
		in, e := os.Open(dst)
		if e != nil {
			return Asset{}, e
		}
		defer in.Close()
		return prepareReader(ctx, root, in, assetKindAvatar)
	}
	return Asset{}, fmt.Errorf("unsupported media format %q", f.Name)
}
