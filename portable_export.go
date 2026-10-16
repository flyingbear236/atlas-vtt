package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"image/png"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

const maxPortableMediaBytes int64 = 512 << 20
const maxPortableTOMLBytes int64 = 128 << 20

type portableExportPlan struct {
	MediaFiles  []string
	Directories []string
}

type portableExportSnapshot struct {
	Session Session
	Assets  map[string]Asset
}

// snapshotPortableExport is called under s.mu. It captures only the portable
// mechanical state, never scenes, tokens, roll history or raw image bytes.
func snapshotPortableExport(session *Session) portableExportSnapshot {
	copy := Session{
		Ruleset:            cloneRulesetSnapshot(session.Ruleset),
		CharacterInstances: make(map[string]CharacterInstance),
		CharacterSources:   make(map[string]CharacterTOMLSource),
		LibraryDirectories: append([]string(nil), session.LibraryDirectories...),
		UnknownTOML:        maps.Clone(session.UnknownTOML),
	}
	for id, instance := range session.CharacterInstances {
		if instance.Persistent {
			copy.CharacterInstances[id] = cloneCharacterInstance(instance)
			if origin, ok := session.CharacterSources[id]; ok {
				copy.CharacterSources[id] = origin
			}
		}
	}
	assets := maps.Clone(session.Assets)
	return portableExportSnapshot{Session: copy, Assets: assets}
}

// resolvePortableMediaPaths operates on the export-only snapshot. It never
// changes the campaign. An unmapped legacy asset gets a stable .png path.
func resolvePortableMediaPaths(s *portableExportSnapshot) error {
	if s.Session.Ruleset.Media == nil {
		s.Session.Ruleset.Media = map[string]string{}
	}
	media := s.Session.Ruleset.Media
	mapped := make(map[string]bool, len(media))
	for name, id := range media {
		if err := validateRulesetMediaPath(name); err != nil {
			return err
		}
		mapped[id] = true
	}
	ensure := func(id string) error {
		if id == "" || mapped[id] {
			return nil
		}
		if err := validateID(id); err != nil {
			return fmt.Errorf("artwork asset ID: %w", err)
		}
		name := "character-artwork-" + id + ".png"
		if err := validateRulesetMediaPath(name); err != nil {
			return err
		}
		if existing, ok := media[name]; ok && existing != id {
			for index := 2; ; index++ {
				candidate := fmt.Sprintf("character-artwork-%s-%d.png", id, index)
				if _, occupied := media[candidate]; !occupied {
					name = candidate
					break
				}
			}
		}
		media[name] = id
		mapped[id] = true
		return nil
	}
	for _, instance := range s.Session.CharacterInstances {
		if instance.AvatarAssetID != nil && *instance.AvatarAssetID != "" {
			if err := ensure(*instance.AvatarAssetID); err != nil {
				return err
			}
		}
	}
	if len(media) > maxRulesetMediaEntries {
		return fmt.Errorf("portable media mappings exceed %d entries", maxRulesetMediaEntries)
	}
	return nil
}

// portableExportFiles produces only small TOML bytes. No image bytes are
// buffered, and no session mutex is held while building them.
func portableExportFiles(snapshot *portableExportSnapshot, includeMedia bool) (map[string][]byte, portableExportPlan, error) {
	ss := &snapshot.Session
	if err := resolvePortableMediaPaths(snapshot); err != nil {
		return nil, portableExportPlan{}, err
	}
	if _, err := MergeDefinitionRegistries(sessionRegistries(ss)); err != nil {
		return nil, portableExportPlan{}, err
	}
	if err := validateLibrary(ss); err != nil {
		return nil, portableExportPlan{}, err
	}
	files := map[string][]byte{}
	plan := portableExportPlan{MediaFiles: []string{}, Directories: append([]string(nil), ss.LibraryDirectories...)}
	original, err := RulesetSnapshotWithSources(ss.Ruleset)
	if err != nil {
		return nil, plan, err
	}
	names := make([]string, 0, len(original.Files))
	for name := range original.Files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		content, err := RenderRulesetFileTOML(original, name)
		if err != nil {
			return nil, plan, fmt.Errorf("ruleset/%s: %w", name, err)
		}
		p := name
		files[p] = []byte(content)
	}
	used := make(map[string]string, len(ss.CharacterInstances))
	// Reserve imported paths first: generated filenames must not claim them,
	// regardless of the lexical order of character IDs.
	for _, id := range PortableCharacterIDs(ss.CharacterInstances) {
		if origin, ok := ss.CharacterSources[id]; ok && origin.FileName != "" {
			if err := validateCharacterSourceFileName(origin.FileName); err != nil {
				return nil, plan, err
			}
			key := libraryKey(origin.FileName)
			if first := used[key]; first != "" {
				return nil, plan, fmt.Errorf("characters %q and %q share %q", first, id, origin.FileName)
			}
			used[key] = id
		}
	}
	var totalTOML int64
	for _, data := range files {
		totalTOML += int64(len(data))
	}
	if totalTOML > maxPortableTOMLBytes {
		return nil, plan, fmt.Errorf("export TOML exceeds %d bytes", maxPortableTOMLBytes)
	}
	for _, id := range PortableCharacterIDs(ss.CharacterInstances) {
		name := CharacterSourceFileName(ss, id)
		if err := validateCharacterSourceFileName(name); err != nil {
			return nil, plan, err
		}
		// Imported paths were reserved. Generated paths are stable unless
		// explicitly occupied by a user-provided filename.
		if used[libraryKey(name)] != id {
			originalName := name
			for i := 2; used[libraryKey(name)] != ""; i++ {
				name = strings.TrimSuffix(originalName, ".toml") + fmt.Sprintf("-%d.toml", i)
			}
			used[libraryKey(name)] = id
		}
		data, err := CurrentCharacterTOML(ss, id)
		if err != nil {
			return nil, plan, fmt.Errorf("%s: %w", name, err)
		}
		totalTOML += int64(len(data))
		if totalTOML > maxPortableTOMLBytes {
			return nil, plan, fmt.Errorf("export TOML exceeds %d bytes", maxPortableTOMLBytes)
		}
		if _, exists := files[name]; exists {
			return nil, plan, fmt.Errorf("archive path collision %q", name)
		}
		files[name] = data
	}
	for name := range ss.Ruleset.Media {
		plan.MediaFiles = append(plan.MediaFiles, name)
	}
	slices.Sort(plan.MediaFiles)
	slices.Sort(plan.Directories)
	for name, source := range ss.UnknownTOML {
		if err := validateLibraryFilePath(name); err != nil {
			return nil, plan, err
		}
		if !strings.EqualFold(path.Ext(name), ".toml") {
			return nil, plan, fmt.Errorf("unknown file %q must be TOML", name)
		}
		p := name
		if _, exists := files[p]; exists {
			return nil, plan, fmt.Errorf("archive path collision %q", p)
		}
		files[p] = []byte(source)
		totalTOML += int64(len(source))
		if totalTOML > maxPortableTOMLBytes {
			return nil, plan, fmt.Errorf("export TOML exceeds %d bytes", maxPortableTOMLBytes)
		}
	}
	return files, plan, nil
}

// mediaSourcePath is the immutable processed PNG, not the originally uploaded
// filename. Extension conversion is performed before writing a ZIP entry.
func mediaSourcePath(root string, asset Asset) (string, error) {
	if !isCharacterArtworkAsset(asset) {
		return "", fmt.Errorf("asset %q is unavailable or is not artwork", asset.ID)
	}
	if err := validateID(asset.ID); err != nil {
		return "", err
	}
	if asset.ID == "." || asset.ID == ".." || strings.ContainsAny(asset.ID, "/\\:") {
		return "", fmt.Errorf("unsafe asset ID %q", asset.ID)
	}
	file := "avatar.png"
	if asset.Kind == assetKindToken {
		file = "token.png"
	}
	return filepath.Join(root, "assets", asset.ID, file), nil
}

// writePortableMedia validates source bytes and encodes the requested image
// format; it never labels PNG bytes with a different extension.
func writePortableMedia(ctx context.Context, dst io.Writer, root, logicalPath string, asset Asset) error {
	source, err := mediaSourcePath(root, asset)
	if err != nil {
		return err
	}
	file, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("%s: %w", logicalPath, err)
	}
	defer file.Close()
	var sig [8]byte
	if _, err := io.ReadFull(file, sig[:]); err != nil || !bytes.Equal(sig[:], []byte("\x89PNG\r\n\x1a\n")) {
		return fmt.Errorf("%s: stored asset is not PNG", logicalPath)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	switch strings.ToLower(path.Ext(logicalPath)) {
	case ".png":
		_, err = io.Copy(dst, file)
	case ".jpg", ".jpeg":
		img, decodeErr := png.Decode(file)
		if decodeErr != nil {
			return fmt.Errorf("%s: %w", logicalPath, decodeErr)
		}
		err = jpeg.Encode(dst, img, &jpeg.Options{Quality: 92})
	case ".webp":
		// WebP encoding is not in the Go standard library. Use the same
		// bounded libvips subprocess already required by image uploads.
		tool, lookupErr := findVips()
		if lookupErr != nil {
			return lookupErr
		}
		tmp, tmpErr := os.MkdirTemp("", "atlas-export-webp-")
		if tmpErr != nil {
			return tmpErr
		}
		defer os.RemoveAll(tmp)
		out := filepath.Join(tmp, "image.webp")
		if err = runVips(ctx, tool, tmp, "copy", source, out); err != nil {
			return err
		}
		converted, openErr := os.Open(out)
		if openErr != nil {
			return openErr
		}
		defer converted.Close()
		var header [12]byte
		if _, err = io.ReadFull(converted, header[:]); err != nil || string(header[:4]) != "RIFF" || string(header[8:]) != "WEBP" {
			return fmt.Errorf("%s: WebP conversion failed", logicalPath)
		}
		if _, err = converted.Seek(0, io.SeekStart); err != nil {
			return err
		}
		_, err = io.Copy(dst, converted)
	default:
		err = fmt.Errorf("unsupported media path %q", logicalPath)
	}
	return err
}

// ExportPortablePackage is reusable by an HTTP handler or a future scheduled
// job. The session snapshot is captured elsewhere, and the zip is streamed to w.
func ExportPortablePackage(ctx context.Context, snapshot portableExportSnapshot, root string, includeMedia bool, w io.Writer) (err error) {
	files, plan, err := portableExportFiles(&snapshot, includeMedia)
	if err != nil {
		return err
	}
	entries := len(files) + len(plan.Directories)
	if includeMedia {
		entries += len(plan.MediaFiles)
	}
	if entries > maxPortableImportFiles {
		return fmt.Errorf("archive exceeds %d entries", maxPortableImportFiles)
	}
	if includeMedia {
		var total int64
		for _, name := range plan.MediaFiles {
			assetID := snapshot.Session.Ruleset.Media[name]
			// portableExportFiles resolves generated media only on its local
			// copy; it must also be visible to the ZIP writer.
			if assetID == "" {
				return fmt.Errorf("media mapping for %q is missing", name)
			}
			asset := snapshot.Assets[assetID]
			source, err := mediaSourcePath(root, asset)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			info, err := os.Stat(source)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s: not a regular file", name)
			}
			total += info.Size()
			if total > maxPortableMediaBytes {
				return fmt.Errorf("media exceeds %d bytes", maxPortableMediaBytes)
			}
		}
	}
	// ZIP extractors on Windows can alias names that differ only in case.
	seenPaths := map[string]string{}
	for p := range files {
		seenPaths[libraryKey(p)] = p
	}
	for _, p := range plan.MediaFiles {
		key := libraryKey(p)
		if first, ok := seenPaths[key]; ok {
			return fmt.Errorf("archive paths %q and %q conflict", first, p)
		}
		seenPaths[key] = p
	}
	if len(seenPaths) != len(files)+len(plan.MediaFiles) {
		return fmt.Errorf("archive contains case-insensitive duplicate paths")
	}
	for _, d := range plan.Directories {
		if e := validateLibraryPath(d); e != nil {
			return e
		}
		if first, exists := seenPaths[libraryKey(d)]; exists {
			return fmt.Errorf("archive file %q conflicts with directory %q", first, d)
		}
		for parent := path.Dir(d); parent != "."; parent = path.Dir(parent) {
			if first, exists := seenPaths[libraryKey(parent)]; exists {
				return fmt.Errorf("archive file %q conflicts with directory %q", first, parent)
			}
		}
	}
	for _, p := range seenPaths {
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			if first, exists := seenPaths[libraryKey(parent)]; exists {
				return fmt.Errorf("archive file %q conflicts with directory %q", first, parent)
			}
		}
	}
	zw := zip.NewWriter(w)
	defer func() {
		closeErr := zw.Close()
		if err == nil {
			err = closeErr
		}
	}()
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, d := range plan.Directories {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := zw.Create(d + "/"); err != nil {
			return err
		}
	}
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := zw.Create(p)
		if err != nil {
			return err
		}
		if _, err := entry.Write(files[p]); err != nil {
			return err
		}
	}
	if !includeMedia {
		return nil
	}
	for _, p := range plan.MediaFiles {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := zw.Create(p)
		if err != nil {
			return err
		}
		if err := writePortableMedia(ctx, entry, root, p, snapshot.Assets[snapshot.Session.Ruleset.Media[p]]); err != nil {
			return fmt.Errorf("export media %q: %w", p, err)
		}
	}
	return nil
}
