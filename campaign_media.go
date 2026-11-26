package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A separate namespace: ruleset imports/replacements must not own GM media.
// Entries refer to immutable scene representations, never to filesystem paths.
const maxCampaignMediaLinks = 10000
const maxMediaImportEntries = 4096
const maxMediaZipBytes int64 = 512 << 20
const maxMediaExpandedBytes int64 = 768 << 20
const maxMediaBatch = 10000

type CampaignMediaEntry struct {
	AssetID string `json:"assetId"`
	AddedAt int64  `json:"addedAt"`
}
type CampaignMedia struct {
	Revision    uint64                        `json:"revision"`
	Files       map[string]CampaignMediaEntry `json:"files,omitempty"`
	Directories []string                      `json:"directories,omitempty"`
}
type campaignMediaFile struct {
	LibraryFile
	AddedAt int64 `json:"addedAt"`
}
type campaignMediaView struct {
	Revision    uint64              `json:"revision"`
	Directories []string            `json:"directories"`
	Files       []campaignMediaFile `json:"files"`
}

func cloneCampaignMedia(c CampaignMedia) CampaignMedia {
	c.Files = maps.Clone(c.Files)
	if c.Files == nil {
		c.Files = map[string]CampaignMediaEntry{}
	}
	c.Directories = slices.Clone(c.Directories)
	return c
}

// All implicit parents participate in the namespace, including case-folded
// conflicts between files and directories. Build once per transaction.
type mediaNamespace struct{ files, dirs map[string]string }

func newMediaNamespace(c CampaignMedia) (*mediaNamespace, error) {
	if len(c.Files) > maxCampaignMediaLinks || len(c.Directories) > maxLibraryDirectories {
		return nil, errors.New("Превышен лимит медиатеки")
	}
	n := &mediaNamespace{map[string]string{}, map[string]string{}}
	for p := range c.Files {
		if err := validateLibraryPath(p); err != nil {
			return nil, err
		}
		k := libraryKey(p)
		if _, exists := n.files[k]; exists {
			return nil, fmt.Errorf("Конфликт имён: %s", p)
		}
		n.files[k] = p
	}
	for _, p := range c.Directories {
		if p == "" {
			return nil, errors.New("Некорректная папка")
		}
		if err := n.addDir(p); err != nil {
			return nil, err
		}
	}
	for p := range c.Files {
		if err := n.addDir(mediaParent(p)); err != nil {
			return nil, err
		}
	}
	return n, nil
}
func mediaParent(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}
func mediaJoin(d, n string) string {
	if d == "" {
		return n
	}
	return d + "/" + n
}
func mediaUnder(p, d string) bool { return d == "" || p == d || strings.HasPrefix(p, d+"/") }
func (n *mediaNamespace) addDir(p string) error {
	newDirs := 0
	for d := p; d != ""; d = mediaParent(d) {
		if err := validateLibraryPath(d); err != nil {
			return err
		}
		k := libraryKey(d)
		if _, ok := n.files[k]; ok {
			return fmt.Errorf("Путь занят изображением: %s", d)
		}
		if old, ok := n.dirs[k]; ok {
			if old != d {
				return fmt.Errorf("Конфликт регистра папок: %s", d)
			}
		} else {
			newDirs++
		}
	}
	if len(n.dirs)+newDirs > maxLibraryDirectories {
		return errors.New("Лимит папок медиатеки достигнут")
	}
	for d := p; d != ""; d = mediaParent(d) {
		n.dirs[libraryKey(d)] = d
	}
	return nil
}
func (n *mediaNamespace) hasDir(p string) bool { return p == "" || n.dirs[libraryKey(p)] == p }
func (n *mediaNamespace) addFile(c *CampaignMedia, p string, e CampaignMediaEntry, unique bool) (string, error) {
	if err := validateLibraryPath(p); err != nil {
		return "", err
	}
	if old, ok := c.Files[p]; ok && old.AssetID == e.AssetID && unique {
		return p, nil
	}
	base, ext := strings.TrimSuffix(p, path.Ext(p)), path.Ext(p)
	for i := 2; ; i++ {
		k := libraryKey(p)
		if existing := n.files[k]; unique && existing != "" && c.Files[existing].AssetID == e.AssetID {
			return existing, nil
		}
		if n.files[k] == "" && n.dirs[k] == "" {
			break
		}
		if !unique {
			return "", fmt.Errorf("Путь уже существует: %s", p)
		}
		p = fmt.Sprintf("%s (%d)%s", base, i, ext)
		if err := validateLibraryPath(p); err != nil {
			return "", err
		}
	}
	if len(c.Files) >= maxCampaignMediaLinks {
		return "", errors.New("Лимит изображений медиатеки достигнут")
	}
	if err := n.addDir(mediaParent(p)); err != nil {
		return "", err
	}
	n.files[libraryKey(p)] = p
	c.Files[p] = e
	return p, nil
}
func validateCampaignMedia(ss *Session) error {
	if _, err := newMediaNamespace(ss.MediaLibrary); err != nil {
		return err
	}
	for p, e := range ss.MediaLibrary.Files {
		a := ss.Assets[e.AssetID]
		if a.ID == "" || !isSceneRasterKind(a.Kind) {
			return fmt.Errorf("Некорректный ресурс медиатеки: %s", p)
		}
	}
	return nil
}
func campaignMediaSnapshot(ss *Session) campaignMediaView {
	c := ss.MediaLibrary
	out := campaignMediaView{Revision: c.Revision, Directories: []string{}, Files: make([]campaignMediaFile, 0, len(c.Files))}
	if n, err := newMediaNamespace(c); err == nil {
		for _, d := range n.dirs {
			out.Directories = append(out.Directories, d)
		}
	}
	for p, e := range c.Files {
		a := ss.Assets[e.AssetID]
		out.Files = append(out.Files, campaignMediaFile{LibraryFile: LibraryFile{Path: p, Type: "image", AssetID: a.ID, MimeType: a.MimeType, Width: a.Width, Height: a.Height, Size: a.Size}, AddedAt: e.AddedAt})
	}
	slices.Sort(out.Directories)
	slices.SortFunc(out.Files, func(a, b campaignMediaFile) int { return strings.Compare(a.Path, b.Path) })
	return out
}
func (s *Server) readCampaignMedia(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ss, m := s.auth(r)
	if !memberIsGM(m) {
		s.mu.Unlock()
		fail(w, 403, "Нужны права ведущего")
		return
	}
	v := campaignMediaSnapshot(ss)
	s.mu.Unlock()
	reply(w, v)
}
func (s *Server) publishMediaRevision(ss *Session) {
	for p := range s.peers {
		if p.session == ss.ID && memberIsGM(p.member) && !p.playerPreview {
			s.send(p, map[string]any{"type": "mediaLibraryChanged", "revision": ss.MediaLibrary.Revision})
		}
	}
}

// Caller holds the session lock. Only committed assets/catalog entries survive a save failure.
func (s *Server) commitCampaignMedia(ss *Session, next CampaignMedia, added map[string]Asset) error {
	old, assets, dirty := ss.MediaLibrary, ss.Assets, s.dirty
	next.Revision = old.Revision + 1
	ss.MediaLibrary = next
	ss.Assets = maps.Clone(assets)
	if ss.Assets == nil {
		ss.Assets = map[string]Asset{}
	}
	for id, a := range added {
		if prev, ok := ss.Assets[id]; ok {
			a = prev
		}
		ss.Assets[id] = a
	}
	if err := validateCampaignMedia(ss); err != nil {
		ss.MediaLibrary, ss.Assets = old, assets
		return err
	}
	refreshAssetOrphans(ss, time.Now())
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		ss.MediaLibrary, ss.Assets, s.dirty = old, assets, dirty
		return err
	}
	s.publishMediaRevision(ss)
	return nil
}

type mediaOperation struct {
	Revision uint64   `json:"revision"`
	Type     string   `json:"type"`
	Paths    []string `json:"paths"`
	Path     string   `json:"path"`
	Target   string   `json:"target"`
}

func applyMediaOperation(c CampaignMedia, req mediaOperation) (CampaignMedia, error) {
	c = cloneCampaignMedia(c)
	n, err := newMediaNamespace(c)
	if err != nil {
		return c, err
	}
	if len(req.Paths) > maxMediaBatch {
		return c, errors.New("Слишком много изображений")
	}
	switch req.Type {
	case "folderCreate":
		if req.Target == "" || n.hasDir(req.Target) {
			return c, errors.New("Папка уже существует")
		}
		if !n.hasDir(mediaParent(req.Target)) {
			return c, errors.New("Родительская папка не найдена")
		}
		if err := n.addDir(req.Target); err != nil {
			return c, err
		}
		c.Directories = append(c.Directories, req.Target)
	case "folderMove":
		if req.Path == "" || !n.hasDir(req.Path) || req.Target == "" || mediaUnder(req.Target, req.Path) || !n.hasDir(mediaParent(req.Target)) {
			return c, errors.New("Недопустимый перенос папки")
		}
		if n.hasDir(req.Target) {
			return c, errors.New("Папка назначения уже существует")
		}
		if err := validateLibraryPath(req.Target); err != nil {
			return c, err
		}
		files := make(map[string]CampaignMediaEntry, len(c.Files))
		for p, e := range c.Files {
			if mediaUnder(p, req.Path) {
				p = req.Target + strings.TrimPrefix(p, req.Path)
			}
			if _, exists := files[p]; exists {
				return c, errors.New("Конфликт имён")
			}
			files[p] = e
		}
		c.Files = files
		for i, d := range c.Directories {
			if mediaUnder(d, req.Path) {
				c.Directories[i] = req.Target + strings.TrimPrefix(d, req.Path)
			}
		}
		if !slices.Contains(c.Directories, req.Target) {
			c.Directories = append(c.Directories, req.Target)
		}
	case "folderDelete":
		if req.Path == "" || !n.hasDir(req.Path) {
			return c, errors.New("Папка не найдена")
		}
		// Match prototype: remove paths in the subtree, return otherwise unlisted
		// resources to root. Actual scene/character references are never touched.
		removed := map[string]struct {
			p string
			e CampaignMediaEntry
		}{}
		remaining := map[string]bool{}
		for p, e := range c.Files {
			if mediaUnder(p, req.Path) {
				v, ok := removed[e.AssetID]
				if !ok || p < v.p {
					removed[e.AssetID] = struct {
						p string
						e CampaignMediaEntry
					}{p, e}
				}
				delete(c.Files, p)
			} else {
				remaining[e.AssetID] = true
			}
		}
		c.Directories = slices.DeleteFunc(c.Directories, func(d string) bool { return mediaUnder(d, req.Path) })
		n, err = newMediaNamespace(c)
		if err != nil {
			return c, err
		}
		ids := make([]string, 0, len(removed))
		for id := range removed {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			v := removed[id]
			if !remaining[id] {
				if _, err = n.addFile(&c, path.Base(v.p), v.e, true); err != nil {
					return c, err
				}
			}
		}
	case "rename":
		e, ok := c.Files[req.Path]
		if !ok {
			return c, errors.New("Изображение не найдено")
		}
		if req.Target == req.Path {
			return c, nil
		}
		if mediaParent(req.Target) != mediaParent(req.Path) {
			return c, errors.New("Имя должно оставаться в текущей папке")
		}
		delete(c.Files, req.Path)
		delete(n.files, libraryKey(req.Path))
		if _, err = n.addFile(&c, req.Target, e, false); err != nil {
			return c, err
		}
	case "copy", "move", "delete":
		if req.Type != "delete" && !n.hasDir(req.Target) {
			return c, errors.New("Папка назначения не найдена")
		}
		seen := map[string]bool{}
		for _, p := range req.Paths {
			if seen[p] {
				continue
			}
			seen[p] = true
			e, ok := c.Files[p]
			if !ok {
				return c, fmt.Errorf("Изображение не найдено: %s", p)
			}
			if req.Type == "delete" {
				delete(c.Files, p)
				delete(n.files, libraryKey(p))
				continue
			}
			dest := mediaJoin(req.Target, path.Base(p))
			if dest == p {
				continue
			}
			if req.Type == "move" {
				delete(c.Files, p)
				delete(n.files, libraryKey(p))
			}
			if _, err = n.addFile(&c, dest, e, true); err != nil {
				return c, err
			}
		}
	default:
		return c, errors.New("Неизвестная операция медиатеки")
	}
	// Empty folders resulting from removing their last file remain available.
	// Persist explicit folders and original source parents for file moves/deletes.
	if req.Type == "move" || req.Type == "delete" {
		for _, p := range req.Paths {
			d := mediaParent(p)
			if d != "" && !slices.Contains(c.Directories, d) {
				c.Directories = append(c.Directories, d)
			}
		}
	}
	slices.Sort(c.Directories)
	c.Directories = slices.Compact(c.Directories)
	_, err = newMediaNamespace(c)
	return c, err
}
func (s *Server) operateCampaignMedia(w http.ResponseWriter, r *http.Request) {
	var req mediaOperation
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		fail(w, 400, "Некорректная операция")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, m := s.auth(r)
	if !memberIsGM(m) {
		fail(w, 403, "Нужны права ведущего")
		return
	}
	if s.stopping {
		fail(w, 503, "Сервер останавливается")
		return
	}
	if req.Revision != ss.MediaLibrary.Revision {
		fail(w, 409, "Медиатека изменилась. Повторите операцию")
		return
	}
	next, err := applyMediaOperation(ss.MediaLibrary, req)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err = s.commitCampaignMedia(ss, next, nil); err != nil {
		fail(w, 500, err.Error())
		return
	}
	reply(w, campaignMediaSnapshot(ss))
}

// Gallery representations are immutable, small PNGs. For tiled images use the
// complete coarsest tile (<=512px), never the source or the full pyramid.
func prepareMediaPreviews(root string, a Asset) error {
	dir := filepath.Join(root, "assets", a.ID)
	thumb, view := filepath.Join(dir, "media-thumb.png"), filepath.Join(dir, "media-view.png")
	if _, err := os.Stat(thumb); err == nil {
		if _, err = os.Stat(view); err == nil {
			return nil
		}
	}
	name := "image.png"
	if a.RenderMode == renderModeTiled {
		name = fmt.Sprintf("%d_0_0.png", a.Levels-1)
	}
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	im, err := png.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	for _, out := range []struct {
		p    string
		side int
	}{{view, 1024}, {thumb, 192}} {
		b := im.Bounds()
		w, h := b.Dx(), b.Dy()
		if longest := max(w, h); longest > out.side {
			w = max(1, w*out.side/longest)
			h = max(1, h*out.side/longest)
		}
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(x, y, im.At(b.Min.X+x*b.Dx()/w, b.Min.Y+y*b.Dy()/h))
			}
		}
		tmp, err := os.CreateTemp(dir, ".media-preview-")
		if err != nil {
			return err
		}
		name := tmp.Name()
		err = png.Encode(tmp, dst)
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			if _, e := os.Stat(out.p); e == nil {
				os.Remove(name)
			} else {
				err = os.Rename(name, out.p)
			}
		}
		if err != nil {
			os.Remove(name)
			return err
		}
	}
	return nil
}
func (s *Server) readCampaignMediaImage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ss, m := s.auth(r)
	if !memberIsGM(m) {
		s.mu.Unlock()
		fail(w, 403, "Нужны права ведущего")
		return
	}
	e := ss.MediaLibrary.Files[r.URL.Query().Get("path")]
	a := ss.Assets[e.AssetID]
	s.mu.Unlock()
	if a.ID == "" {
		fail(w, 404, "Изображение не найдено")
		return
	}
	name := "media-thumb.png"
	switch r.URL.Query().Get("variant") {
	case "", "thumb":
	case "preview":
		name = "media-view.png"
	case "original":
		name = "original"
		w.Header().Set("Content-Type", a.MimeType)
	default:
		fail(w, 400, "Неизвестный вид изображения")
		return
	}
	if name != "original" {
		if err := s.ensureUniversalPart(r.Context(), a, name); err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeFile(w, r, filepath.Join(s.root, "assets", a.ID, universalPartName(a, name)))
}

type mediaImportIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}
type mediaImportResult struct {
	Imported int                `json:"imported"`
	Skipped  int                `json:"skipped"`
	Issues   []mediaImportIssue `json:"issues"`
	Catalog  campaignMediaView  `json:"catalog"`
	Paths    []string           `json:"paths,omitempty"`
}
type mediaImportItem struct {
	path  string
	asset Asset
}
type mediaImportJob struct {
	server   *Server
	ctx      context.Context
	result   mediaImportResult
	items    []mediaImportItem
	dirs     []string
	prepared map[string]Asset
	count    int
	expanded int64
}

func (j *mediaImportJob) issue(p string, err error) {
	j.result.Skipped++
	if len(j.result.Issues) < 100 {
		j.result.Issues = append(j.result.Issues, mediaImportIssue{p, err.Error()})
	}
}
func (j *mediaImportJob) addImage(p string, r io.Reader) {
	if err := validateLibraryPath(p); err != nil {
		j.issue(p, err)
		return
	}
	if j.ctx.Err() != nil {
		return
	}
	a, err := prepareCampaignMediaReader(j.ctx, j.server.root, r)
	if err == nil {
		a.Filename = mediaUploadFilename(path.Base(p), a.Filename)
	}
	if a.ID != "" && err == nil {
		j.prepared[a.ID] = a
	}
	if err == nil && !universalMedia(a) {
		err = prepareMediaPreviews(j.server.root, a)
	}
	if err != nil {
		j.issue(p, err)
		return
	}
	j.items = append(j.items, mediaImportItem{p, a})
}
func (j *mediaImportJob) addZip(r io.Reader, dest string) error {
	f, err := os.CreateTemp(j.server.root, ".media-import-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(contextReader{j.ctx, r}, maxMediaZipBytes+1))
	if err != nil {
		return err
	}
	if n > maxMediaZipBytes {
		return errors.New("ZIP больше 512 МиБ")
	}
	z, err := zip.NewReader(f, n)
	if err != nil {
		return err
	}
	if len(z.File) > maxMediaImportEntries-j.count {
		return errors.New("Слишком много записей в ZIP")
	}
	var expanded uint64
	for _, entry := range z.File {
		if entry.UncompressedSize64 > uint64(maxMediaExpandedBytes)-expanded {
			return errors.New("ZIP превышает лимит распаковки")
		}
		expanded += entry.UncompressedSize64
	}
	if int64(expanded) > maxMediaExpandedBytes-j.expanded {
		return errors.New("Превышен общий лимит распаковки")
	}
	j.expanded += int64(expanded)
	j.count += len(z.File)
	for _, entry := range z.File {
		if j.ctx.Err() != nil {
			return j.ctx.Err()
		}
		raw := strings.TrimSuffix(entry.Name, "/")
		if err := validateLibraryPath(raw); err != nil {
			j.issue(entry.Name, err)
			continue
		}
		p := mediaJoin(dest, raw)
		if err := validateLibraryPath(p); err != nil {
			j.issue(p, err)
			continue
		}
		if entry.FileInfo().IsDir() {
			j.dirs = append(j.dirs, p)
			continue
		}
		if !entry.Mode().IsRegular() || entry.Flags&1 != 0 || entry.UncompressedSize64 > uint64(uploadLimit) {
			j.issue(p, errors.New("Недопустимая запись ZIP или превышен лимит файла"))
			continue
		}
		if !mediaImageExtension(p) {
			j.issue(p, errors.New("Поддерживаются PNG, JPEG, WebP, GIF, AVIF и BMP"))
			continue
		}
		in, e := entry.Open()
		if e != nil {
			j.issue(p, e)
			continue
		}
		j.addImage(p, in)
		in.Close()
	}
	return nil
}
func mediaImageExtension(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".avif", ".bmp":
		return true
	}
	return false
}
func (s *Server) importCampaignMedia(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ss, m := s.auth(r)
	if !memberIsGM(m) {
		s.mu.Unlock()
		fail(w, 403, "Нужны права ведущего")
		return
	}
	dest := r.URL.Query().Get("folder")
	ns, err := newMediaNamespace(ss.MediaLibrary)
	allowed := err == nil && ns.hasDir(dest) && !s.stopping
	s.mu.Unlock()
	if !allowed {
		fail(w, 400, "Папка назначения недоступна")
		return
	}
	select {
	case s.imageJobs <- struct{}{}:
		defer func() { <-s.imageJobs }()
	default:
		fail(w, 429, "Другая загрузка изображений уже выполняется")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMediaExpandedBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		fail(w, 400, "Ожидается multipart импорт")
		return
	}
	j := mediaImportJob{server: s, ctx: r.Context(), prepared: map[string]Asset{}}
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for id := range j.prepared {
			s.discardPreparedAssetLocked(id)
		}
	}()
	parts := 0
	for {
		part, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			fail(w, 400, e.Error())
			return
		}
		parts++
		if parts > maxMediaImportEntries || j.count >= maxMediaImportEntries {
			part.Close()
			fail(w, 400, "Превышен лимит записей импорта")
			return
		}
		kind, p, ok := strings.Cut(part.FormName(), ":")
		if !ok || validateLibraryPath(p) != nil {
			j.issue(p, errors.New("Некорректный путь"))
			part.Close()
			continue
		}
		if kind == "zip" {
			zipDest := dest
			if parent := mediaParent(p); parent != "" {
				zipDest = mediaJoin(dest, parent)
			}
			if e = j.addZip(part, zipDest); e != nil {
				j.issue(p, e)
			}
		} else if kind == "file" && mediaImageExtension(p) {
			j.count++
			remaining := maxMediaExpandedBytes - j.expanded
			limited := &io.LimitedReader{R: part, N: remaining + 1}
			before := len(j.items)
			j.addImage(mediaJoin(dest, p), limited)
			// Include direct uploads in the same expanded budget as ZIP entries.
			used := remaining + 1 - limited.N
			if used > remaining {
				j.items = j.items[:before]
				j.issue(p, errors.New("Превышен общий лимит распаковки"))
				fail(w, 413, "Превышен общий лимит импорта")
				return
			}
			j.expanded += used
		} else {
			j.issue(p, errors.New("Поддерживаются PNG, JPEG, WebP, GIF, AVIF, BMP и ZIP"))
		}
		part.Close()
		if r.Context().Err() != nil {
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, member := s.auth(r)
	if current != ss || !memberIsGM(member) || s.stopping || r.Context().Err() != nil {
		fail(w, 409, "Кампания изменилась во время импорта")
		return
	}
	next := cloneCampaignMedia(ss.MediaLibrary)
	ns, err = newMediaNamespace(next)
	if err != nil || !ns.hasDir(dest) {
		fail(w, 409, "Папка назначения изменилась")
		return
	}
	added := map[string]Asset{}
	for _, d := range j.dirs {
		if ns.hasDir(d) {
			continue
		}
		if len(next.Directories) >= maxLibraryDirectories {
			j.issue(d, errors.New("Лимит папок"))
			continue
		}
		if err = ns.addDir(d); err != nil {
			j.issue(d, err)
			continue
		}
		next.Directories = append(next.Directories, d)
	}
	for _, item := range j.items {
		addedPath, addErr := ns.addFile(&next, item.path, CampaignMediaEntry{item.asset.ID, time.Now().Unix()}, true)
		if err = addErr; err != nil {
			j.issue(item.path, err)
			continue
		}
		added[item.asset.ID] = item.asset
		j.result.Imported++
		j.result.Paths = append(j.result.Paths, addedPath)
	}
	if j.result.Imported > 0 || len(next.Directories) != len(ss.MediaLibrary.Directories) {
		if err = s.commitCampaignMedia(ss, next, added); err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	j.result.Catalog = campaignMediaSnapshot(ss)
	reply(w, j.result)
}

// Choose the nearest visual layer below a token layer, then above. The
// ordering is identical to the scene tree (higher order = higher layer).
func mediaPlacementLayer(scene *Scene, floorID, layerID string) (Layer, error) {
	if scene.Floors[floorID].ID == "" {
		return Layer{}, errors.New("Этаж не найден")
	}
	layers := orderedLayers(scene, floorID)
	at := -1
	for i, l := range layers {
		if l.ID == layerID {
			at = i
			break
		}
	}
	if layerID != "" && at < 0 {
		return Layer{}, errors.New("Выбранный слой изменился")
	}
	if at >= 0 {
		if layers[at].Kind == layerKindVisual {
			return layers[at], nil
		}
		for i := at - 1; i >= 0; i-- {
			if layers[i].Kind == layerKindVisual {
				return layers[i], nil
			}
		}
		for i := at + 1; i < len(layers); i++ {
			if layers[i].Kind == layerKindVisual {
				return layers[i], nil
			}
		}
	} else {
		for _, l := range layers {
			if l.Kind == layerKindVisual {
				return l, nil
			}
		}
	}
	return Layer{}, errors.New("На этом этаже нет слоя для изображений. Создайте визуальный слой")
}

type mediaPlacement struct {
	Paths   []string `json:"paths"`
	SceneID string   `json:"sceneId"`
	FloorID string   `json:"floorId"`
	LayerID string   `json:"layerId"`
	X       float64  `json:"x"`
	Y       float64  `json:"y"`
}

func (s *Server) placeCampaignMedia(w http.ResponseWriter, r *http.Request) {
	var req mediaPlacement
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil || len(req.Paths) == 0 || len(req.Paths) > 256 || !validNumber(req.X) || !validNumber(req.Y) {
		fail(w, 400, "Некорректное размещение (до 256 изображений за раз)")
		return
	}
	s.mu.Lock()
	initial, member := s.auth(r)
	if !memberIsGM(member) {
		s.mu.Unlock()
		fail(w, 403, "Нужны права ведущего")
		return
	}
	sceneBefore := initial.Scenes[req.SceneID]
	if sceneBefore == nil {
		s.mu.Unlock()
		fail(w, 404, "Сцена не найдена")
		return
	}
	layerBefore, layerErr := mediaPlacementLayer(sceneBefore, req.FloorID, req.LayerID)
	if layerErr != nil || layerBefore.Locked || len(sceneBefore.Elements)+len(req.Paths) > maxSceneElements {
		s.mu.Unlock()
		if layerErr != nil {
			fail(w, 400, layerErr.Error())
		} else {
			fail(w, 400, "Слой заблокирован или достигнут лимит элементов")
		}
		return
	}
	planned := make(map[string]Asset, len(req.Paths))
	for _, p := range req.Paths {
		a := initial.Assets[initial.MediaLibrary.Files[p].AssetID]
		if a.ID == "" {
			s.mu.Unlock()
			fail(w, 400, "Изображение больше недоступно: "+p)
			return
		}
		planned[p] = a
	}
	s.mu.Unlock()
	for _, a := range planned {
		if err := s.ensureUniversalPart(r.Context(), a, "scene"); err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, m := s.auth(r)
	if ss != initial || !memberIsGM(m) {
		fail(w, 403, "Нужны права ведущего")
		return
	}
	if s.stopping {
		fail(w, 503, "Сервер останавливается")
		return
	}
	scene := ss.Scenes[req.SceneID]
	if scene == nil {
		fail(w, 404, "Сцена не найдена")
		return
	}
	layer, err := mediaPlacementLayer(scene, req.FloorID, req.LayerID)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if layer.Locked {
		fail(w, 400, "Слой заблокирован")
		return
	}
	if len(scene.Elements)+len(req.Paths) > maxSceneElements {
		fail(w, 400, "Лимит элементов сцены достигнут")
		return
	}
	z := 0
	for _, e := range scene.Elements {
		if e.LayerID == layer.ID && e.ZOrder >= z {
			z = e.ZOrder + 1
		}
	}
	items := make([]SceneElement, 0, len(req.Paths))
	for i, p := range req.Paths {
		e := ss.MediaLibrary.Files[p]
		a := ss.Assets[e.AssetID]
		if a.ID == "" || a.ID != planned[p].ID || !isSceneRasterKind(a.Kind) {
			fail(w, 400, "Изображение больше недоступно: "+p)
			return
		}
		t := Transform{X: req.X - float64(a.Width)/2 + float64(i*24), Y: req.Y - float64(a.Height)/2 + float64(i*24), Width: float64(a.Width), Height: float64(a.Height)}
		if !validTransform(t) {
			fail(w, 400, "Некорректные координаты")
			return
		}
		items = append(items, SceneElement{ID: id(), FloorID: layer.FloorID, LayerID: layer.ID, AssetID: a.ID, Name: strings.TrimSuffix(path.Base(p), path.Ext(p)), Transform: t, ZOrder: z + i, Visible: true, Opacity: 1})
	}
	oldRev, oldDirty := scene.Revision, s.dirty
	runtime := scene.ensureRuntime()
	for _, e := range items {
		scene.Elements[e.ID] = e
	}
	scene.Revision++
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		for _, e := range items {
			delete(scene.Elements, e.ID)
		}
		scene.Revision, s.dirty = oldRev, oldDirty
		fail(w, 500, "Ошибка сохранения сцены")
		return
	}
	// One batched commit; existing element runtime and publication paths preserve
	// GM catalogs, viewport filtering and private object interaction properties.
	for _, e := range items {
		runtime.addElement(e)
	}
	runtime.revision = scene.Revision
	if len(items) > 32 {
		s.publishSceneSnapshot(ss, scene.ID)
	} else {
		for _, e := range items {
			s.publishElement(ss, scene.ID, SceneElement{}, false, e, true, false)
		}
	}
	reply(w, map[string]any{"count": len(items), "layerId": layer.ID, "layerName": layer.Name})
}
