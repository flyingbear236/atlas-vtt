package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func universalMedia(a Asset) bool {
	return a.RepresentationVersion == universalRepresentationVersion && isSceneRasterKind(a.Kind)
}
func mediaPyramidLevels(w, h int) int {
	levels := 1
	for w > 512 || h > 512 {
		w = (w + 1) / 2
		h = (h + 1) / 2
		levels++
	}
	return levels
}
func generateMediaThumbnail(ctx context.Context, tool, stage, original, name string, side int) error {
	return runVips(ctx, tool, stage, "thumbnail", original, filepath.Join(stage, name)+"[compression=6,keep=none]", strconv.Itoa(side), "--height="+strconv.Itoa(side), "--size=down", "--no-rotate", "--fail-on=error")
}
func mediaPartSide(name string) int {
	switch name {
	case "media-thumb.png":
		return 192
	case "hover.png":
		return 320
	case "avatar.png":
		return 512
	case "media-view.png":
		return 1024
	case "artwork.png":
		return 1536
	}
	return 0
}

// If the original is already smaller than a requested derivative, share the
// smallest lossless representation covering the original dimensions. A tiny
// sprite need not occupy five identical PNG files merely because it is used
// in several UI contexts.
func universalPartName(a Asset, name string) string {
	if !universalMedia(a) {
		return name
	}
	if name == "token.png" {
		name = "avatar.png"
	}
	if (name == "scene" || name == "image.png") && a.RenderMode == renderModeBitmap {
		if max(a.Width, a.Height) <= 1536 {
			name = "artwork.png"
		}
	}
	side := mediaPartSide(name)
	if side == 0 {
		return name
	}
	needed := min(side, max(a.Width, a.Height))
	for _, candidate := range []string{"media-thumb.png", "hover.png", "avatar.png", "media-view.png", "artwork.png"} {
		if mediaPartSide(candidate) >= needed {
			return candidate
		}
	}
	return name
}
func mediaReady(root string, a Asset, name string) bool {
	name = universalPartName(a, name)
	if name == "scene" {
		name = ".scene-ready"
	}
	info, err := os.Stat(filepath.Join(root, "assets", a.ID, name))
	return err == nil && info.Mode().IsRegular()
}

// Caller owns imageJobs. Originals are immutable; stage a complete representation
// before publishing. Failed conversions never replace a usable derivative.
func ensureUniversalPartLocked(ctx context.Context, root string, a Asset, name string) error {
	if !universalMedia(a) {
		return nil
	}
	if mediaReady(root, a, name) {
		return nil
	}
	name = universalPartName(a, name)
	side := mediaPartSide(name)
	if name != "scene" && side == 0 {
		return errors.New("Неизвестное представление")
	}
	dir := filepath.Join(root, "assets", a.ID)
	tool, err := findVips()
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(dir, ".representation-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	original := filepath.Join(dir, "original")
	if name == "scene" {
		copy := a
		if err = prepareRepresentationFiles(ctx, tool, stage, original, &copy); err != nil {
			return err
		}
		if copy.Levels != a.Levels {
			return fmt.Errorf("Несовпадение уровней представления")
		}
	} else if err = generateMediaThumbnail(ctx, tool, stage, original, name, side); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		dst := filepath.Join(dir, entry.Name())
		if _, e := os.Stat(dst); e == nil {
			continue
		}
		if err = os.Rename(filepath.Join(stage, entry.Name()), dst); err != nil {
			return err
		}
	}
	if name == "scene" {
		return os.WriteFile(filepath.Join(dir, ".scene-ready"), []byte("1"), 0600)
	}
	return nil
}
func (s *Server) ensureUniversalPart(ctx context.Context, a Asset, name string) error {
	if !universalMedia(a) || mediaReady(s.root, a, name) {
		return nil
	}
	if s.mediaWaiters != nil {
		select {
		case s.mediaWaiters <- struct{}{}:
			defer func() { <-s.mediaWaiters }()
		default:
			return errors.New("Очередь обработки изображений заполнена, повторите запрос")
		}
	}
	// Only one decoder at a time, shared with imports and rotations. Bound the
	// wait and cancel queued work when the client disconnects.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	select {
	case s.imageJobs <- struct{}{}:
		defer func() { <-s.imageJobs }()
	case <-ctx.Done():
		return ctx.Err()
	case <-s.jobContext.Done():
		return errors.New("Сервер останавливается")
	}
	return ensureUniversalPartLocked(ctx, s.root, a, name)
}

// Resolve the catalog reference server-side; never accept a client filesystem path.
func (s *Server) prepareCampaignMediaUse(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ss, m := s.auth(r)
	if !memberIsGM(m) {
		s.mu.Unlock()
		fail(w, 403, "Нужны права ведущего")
		return
	}
	a := ss.Assets[ss.MediaLibrary.Files[r.URL.Query().Get("path")].AssetID]
	s.mu.Unlock()
	if a.ID == "" {
		fail(w, 404, "Изображение не найдено")
		return
	}
	variant := r.URL.Query().Get("variant")
	switch variant {
	case "scene", "avatar.png", "artwork.png", "hover.png", "media-thumb.png":
	default:
		fail(w, 400, "Неизвестное назначение")
		return
	}
	if err := s.ensureUniversalPart(r.Context(), a, variant); err != nil {
		fail(w, 400, err.Error())
		return
	}
	reply(w, a)
}

func (s *Server) playerMediaCatalog(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ss, m := s.auth(r)
	if ss == nil || m == nil {
		s.mu.Unlock()
		fail(w, 403, "Нет доступа")
		return
	}
	registry, err := MergeDefinitionRegistries(sessionRegistries(ss))
	if err != nil {
		s.mu.Unlock()
		fail(w, 400, err.Error())
		return
	}
	out := campaignMediaView{Revision: ss.CharacterRevision, Files: []campaignMediaFile{}, Directories: []string{}}
	seen := map[string]bool{}
	usedNames := map[string]bool{}
	portraitsOnly := r.URL.Query().Get("portrait") == "1"
	add := func(aid string) {
		a := ss.Assets[aid]
		if a.ID == "" || seen[aid] || portraitsOnly && !universalMedia(a) {
			return
		}
		seen[aid] = true
		name := mediaUploadFilename(a.Filename, "image.png")
		base, ext := strings.TrimSuffix(name, path.Ext(name)), path.Ext(name)
		for n := 2; usedNames[libraryKey(name)]; n++ {
			name = fmt.Sprintf("%s (%d)%s", base, n, ext)
		}
		usedNames[libraryKey(name)] = true
		out.Files = append(out.Files, campaignMediaFile{LibraryFile: LibraryFile{Path: name, Type: "image", AssetID: aid, Width: a.Width, Height: a.Height, Size: a.Size, MimeType: a.MimeType}, AddedAt: a.CreatedAt})
	}
	for aid := range ss.MemberMedia[m.ID] {
		add(aid)
	}
	for cid, c := range ss.CharacterInstances {
		if memberCanAccessCharacter(ss, m, cid) {
			add(effectiveCharacterAvatar(ss, c, registry))
			add(characterPortraitAsset(ss, c))
		}
	}
	out.Revision = ss.MediaLibrary.Revision + ss.CharacterRevision
	s.mu.Unlock()
	reply(w, out)
}

// Normalize OS filenames only when necessary for the portable virtual namespace.
// A missing name is a generic image name; content IDs are not display names.
func mediaUploadFilename(raw, fallback string) string {
	normalize := func(value string) string {
		name := path.Base(strings.ReplaceAll(value, "\\", "/"))
		if name == "." || name == "/" || name == ".." {
			return ""
		}
		name = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || strings.ContainsRune("<>:\"|?*", r) {
				return '_'
			}
			return r
		}, name)
		name = strings.TrimRight(name, " .")
		if name == "" {
			return ""
		}
		if validateLibraryPath(name) == nil {
			return name
		}
		// Leave room for the extension, a collision suffix and a reserved-name prefix.
		ext := path.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		stem := strings.TrimSuffix(name, ext)
		for len(stem)+len(ext) > maxLibraryPathBytes-16 || utf8.RuneCountInString(stem)+utf8.RuneCountInString(ext) > maxLibrarySegmentRunes-12 {
			_, size := utf8.DecodeLastRuneInString(stem)
			if size == 0 {
				break
			}
			stem = stem[:len(stem)-size]
		}
		name = strings.TrimRight(stem, " .") + ext
		if validateLibraryPath(name) != nil {
			name = "_" + name
		}
		if validateLibraryPath(name) != nil {
			return ""
		}
		return name
	}
	if name := normalize(raw); name != "" {
		return name
	}
	if name := normalize(fallback); name != "" {
		return name
	}
	return "image.png"
}

// Caller is preparing a new upload under the existing image job limiter and
// holds the session lock. Catalog paths are independent of assignment paths.
func addUnsortedMedia(ss *Session, a Asset, filename string) error {
	for _, entry := range ss.MediaLibrary.Files {
		if entry.AssetID == a.ID {
			return nil
		}
	}
	next := cloneCampaignMedia(ss.MediaLibrary)
	ns, err := newMediaNamespace(next)
	if err != nil {
		return err
	}
	name := mediaUploadFilename(filename, a.Filename)
	if _, err = ns.addFile(&next, name, CampaignMediaEntry{a.ID, time.Now().Unix()}, true); err != nil {
		return err
	}
	next.Revision++
	ss.MediaLibrary = next
	return nil
}
