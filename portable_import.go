package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Portable imports are prepared from a bounded archive, independently of the
// live session. Preview and apply use exactly the same candidate builder.
type portableImportOptions struct {
	Mode          string            `json:"mode"` // replace or selected
	SelectedFiles []string          `json:"selectedFiles,omitempty"`
	Characters    map[string]string `json:"characters,omitempty"` // existing ID: replace or skip
	Media         map[string]string `json:"media,omitempty"`      // conflicting path: replace or keep
	Unknown       map[string]string `json:"unknown,omitempty"`    // unknown TOML collision: keep or replace
}
type portableImportArchive struct {
	Directories []string
	Skipped     []string
	Ruleset     map[string]string
	Characters  map[string][]byte
	Media       map[string]*zip.File
	Unknown     map[string]string
	Diagnostics []portableImportIssue
}
type portableImportIssue struct {
	File    string `json:"file,omitempty"`
	Message string `json:"message"`
}
type portableImportPreview struct {
	AvailableFiles        []string               `json:"availableFiles"`
	Digest                string                 `json:"digest"`
	CampaignRevision      uint64                 `json:"campaignRevision"`
	LibraryRevision       uint64                 `json:"libraryRevision"`
	OptionsDigest         string                 `json:"optionsDigest"`
	UnknownFiles          []string               `json:"unknownFiles"`
	SkippedFiles          []string               `json:"skippedFiles"`
	RegistryRevision      uint64                 `json:"registryRevision"`
	CharacterRevision     uint64                 `json:"characterRevision"`
	AddedFiles            []string               `json:"addedFiles"`
	ReplacedFiles         []string               `json:"replacedFiles"`
	RemovedFiles          []string               `json:"removedFiles"`
	RulesetDiff           RulesetReplacementDiff `json:"rulesetDiff"`
	AddedCharacters       []string               `json:"addedCharacters"`
	ConflictingCharacters []string               `json:"conflictingCharacters"`
	AddedMedia            []string               `json:"addedMedia"`
	ConflictingMedia      []string               `json:"conflictingMedia"`
	ConflictingUnknown    []string               `json:"conflictingUnknown"`
	MissingMedia          []string               `json:"missingMedia"`
	Errors                []portableImportIssue  `json:"errors,omitempty"`
	Warnings              []portableImportIssue  `json:"warnings,omitempty"`
	ChecksIncomplete      bool                   `json:"checksIncomplete,omitempty"`
}
type portableImportCandidate struct {
	Ruleset     RulesetSnapshot
	Campaign    CampaignRegistry
	Characters  map[string]CharacterInstance
	Sources     map[string]CharacterTOMLSource
	Assets      map[string]Asset
	Unknown     map[string]string
	Directories []string
	Preview     portableImportPreview
}

const maxPortableImportFiles = 2048
const maxPortableImportCompressed int64 = 512 << 20
const maxPortableImportUncompressed uint64 = 768 << 20
const maxPortableImportImage uint64 = 32 << 20

func portableOptionsDigest(opts portableImportOptions) string {
	// Bind the preview to normalized user decisions, not map iteration order.
	if opts.Mode == "replace" {
		opts.SelectedFiles = nil
	} else {
		opts.SelectedFiles = append([]string(nil), opts.SelectedFiles...)
		slices.Sort(opts.SelectedFiles)
	}
	if opts.Characters == nil {
		opts.Characters = map[string]string{}
	}
	if opts.Media == nil {
		opts.Media = map[string]string{}
	}
	if opts.Unknown == nil {
		opts.Unknown = map[string]string{}
	}
	data, _ := json.Marshal(opts)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func readPortableImportArchive(reader *zip.Reader) (portableImportArchive, error) {
	return readPortableImportArchiveWithDiagnostics(reader, false)
}

func readPortableImportArchiveWithDiagnostics(reader *zip.Reader, collectDiagnostics bool) (portableImportArchive, error) {
	if len(reader.File) > maxPortableImportFiles {
		return portableImportArchive{}, errors.New("too many ZIP entries")
	}
	a := portableImportArchive{Ruleset: map[string]string{}, Characters: map[string][]byte{}, Media: map[string]*zip.File{}, Unknown: map[string]string{}, Directories: []string{}, Skipped: []string{}}
	keys := make(map[string]string, len(reader.File))
	files, dirs := map[string]string{}, map[string]string{}
	var total, totalTOML, totalRules uint64
	for _, f := range reader.File {
		isDir := f.FileInfo().IsDir()
		name := f.Name
		if isDir {
			name = strings.TrimSuffix(name, "/")
		}
		if err := validateLibraryPath(name); err != nil {
			return a, fmt.Errorf("unsafe ZIP entry %q: %w", f.Name, err)
		}
		if !isDir && !f.Mode().IsRegular() {
			return a, fmt.Errorf("unsupported ZIP entry %q", f.Name)
		}
		if isDir && (f.Name != name+"/" || f.UncompressedSize64 != 0) {
			return a, fmt.Errorf("unsafe directory entry %q", f.Name)
		}
		key := libraryKey(name)
		if old, ok := keys[key]; ok {
			return a, fmt.Errorf("duplicate ZIP path %q and %q", old, f.Name)
		}
		keys[key] = f.Name
		if isDir {
			dirs[key] = name
			a.Directories = append(a.Directories, name)
			continue
		}
		files[key] = name
		if f.UncompressedSize64 > maxPortableImportUncompressed-total {
			return a, errors.New("ZIP uncompressed size exceeds limit")
		}
		total += f.UncompressedSize64
		if f.UncompressedSize64 > 8<<20 && (f.CompressedSize64 == 0 || f.UncompressedSize64/f.CompressedSize64 > 200) {
			return a, fmt.Errorf("suspicious compression ratio in %q", name)
		}
		switch strings.ToLower(path.Ext(name)) {
		case ".toml":
			if f.UncompressedSize64 > MaxDefinitionDocumentBytes {
				return a, fmt.Errorf("TOML %q exceeds size limit", name)
			}
			totalTOML += f.UncompressedSize64
			if totalTOML > uint64(maxPortableTOMLBytes) {
				return a, errors.New("ZIP TOML exceeds total limit")
			}
			data, err := readImportEntry(f, MaxDefinitionDocumentBytes)
			if err != nil {
				return a, err
			}
			var root map[string]any
			if err := toml.Unmarshal(data, &root); err != nil {
				if collectDiagnostics {
					a.Diagnostics = append(a.Diagnostics, portableImportIssue{File: name, Message: err.Error()})
					continue
				}
				return a, fmt.Errorf("TOML %q: %w", name, err)
			}
			if libraryKey(name) == libraryKey("manifest.toml") {
				if _, old := root["format_version"]; old {
					if _, ok := root["ruleset_files"]; ok {
						return a, errors.New("старый ZIP ATLAS с manifest.toml не поддерживается")
					}
				}
			}
			_, character := root["character"]
			known := false
			for _, field := range []string{"stats", "actions", "presets", "ruleset"} {
				if _, ok := root[field]; ok {
					known = true
				}
			}
			if character && known {
				if collectDiagnostics {
					a.Diagnostics = append(a.Diagnostics, portableImportIssue{File: name, Message: "Смешаны секции персонажа и определений"})
					continue
				}
				return a, fmt.Errorf("TOML %q mixes character and definitions", name)
			}
			if character {
				if len(a.Characters) >= MaxCharacterInstances {
					return a, errors.New("too many character TOMLs")
				}
				a.Characters[name] = data
			} else if known {
				totalRules += f.UncompressedSize64
				if totalRules > MaxRulesetPackageBytes || len(a.Ruleset) >= MaxRulesetFiles {
					return a, errors.New("ruleset package exceeds limits")
				}
				if _, err = decodeRulesetSourceFile(name, string(data)); err != nil {
					if collectDiagnostics {
						a.Diagnostics = append(a.Diagnostics, portableImportIssue{File: name, Message: err.Error()})
						continue
					}
					return a, err
				}
				a.Ruleset[name] = string(data)
			} else {
				if len(a.Unknown) >= maxUnknownTOMLFiles {
					return a, errors.New("too many unknown TOMLs")
				}
				a.Unknown[name] = string(data)
			}
		case ".png", ".jpg", ".jpeg", ".webp":
			if f.UncompressedSize64 > maxPortableImportImage || len(a.Media) >= maxRulesetMediaEntries {
				return a, fmt.Errorf("image %q exceeds limits", name)
			}
			a.Media[name] = f
		default:
			a.Skipped = append(a.Skipped, name)
		}
	}
	if len(a.Directories) > maxLibraryDirectories {
		return a, errors.New("too many directories")
	}
	// ZIP paths are a single virtual namespace: ancestors cannot be files.
	for _, name := range files {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if conflicting, ok := files[libraryKey(parent)]; ok {
				return a, fmt.Errorf("file %q conflicts with directory %q", conflicting, parent)
			}
		}
	}
	for key, name := range dirs {
		if other, ok := files[key]; ok {
			return a, fmt.Errorf("file %q conflicts with directory %q", other, name)
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if conflicting, ok := files[libraryKey(parent)]; ok {
				return a, fmt.Errorf("file %q conflicts with directory %q", conflicting, parent)
			}
		}
	}
	// Preserve a unique directory spelling even when only implicit parents exist.
	allDirectories := map[string]string{}
	for _, name := range keys {
		logical := strings.TrimSuffix(name, "/")
		if strings.HasSuffix(name, "/") {
			key := libraryKey(logical)
			if old, ok := allDirectories[key]; ok && old != logical {
				return a, fmt.Errorf("directory case collision: %q and %q", old, logical)
			}
			allDirectories[key] = logical
		}
		for parent := path.Dir(logical); parent != "."; parent = path.Dir(parent) {
			key := libraryKey(parent)
			if old, ok := allDirectories[key]; ok && old != parent {
				return a, fmt.Errorf("directory case collision: %q and %q", old, parent)
			}
			allDirectories[key] = parent
		}
	}
	slices.Sort(a.Directories)
	slices.Sort(a.Skipped)
	return a, nil
}
func readImportEntry(f *zip.File, limit int) ([]byte, error) {
	r, e := f.Open()
	if e != nil {
		return nil, e
	}
	defer r.Close()
	data, e := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if e != nil {
		return nil, e
	}
	if len(data) > limit {
		return nil, fmt.Errorf("archive entry %q exceeds limit", f.Name)
	}
	return data, nil
}
func (a portableImportArchive) fileNames() []string {
	out := make([]string, 0, len(a.Ruleset))
	for p := range a.Ruleset {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}
func cloneForPortableImport(ss *Session) *Session {
	copy := *ss
	copy.Ruleset = cloneRulesetSnapshot(ss.Ruleset)
	copy.CampaignDefinitions = cloneCampaignRegistry(ss.CampaignDefinitions)
	copy.CharacterInstances = make(map[string]CharacterInstance, len(ss.CharacterInstances))
	for id, inst := range ss.CharacterInstances {
		copy.CharacterInstances[id] = cloneCharacterInstance(inst)
	}
	copy.CharacterSources = maps.Clone(ss.CharacterSources)
	copy.ResourceOverlays = cloneResourceOverlayConfig(ss.ResourceOverlays)
	copy.Assets = cloneAssetRegistry(ss.Assets)
	copy.UnknownTOML = maps.Clone(ss.UnknownTOML)
	copy.LibraryDirectories = append([]string(nil), ss.LibraryDirectories...)
	// Candidate validation never reads or changes scene data before the final locked apply.
	copy.Scenes = nil
	return &copy
}
func equalImportedMedia(ctx context.Context, root string, f *zip.File, asset Asset) bool {
	if !isCharacterArtworkAsset(asset) {
		return false
	}
	r, e := f.Open()
	if e != nil {
		return false
	}
	defer r.Close()
	incoming := sha256.New()
	if _, e = io.Copy(incoming, io.LimitReader(r, int64(maxPortableImportImage)+1)); e != nil {
		return false
	}
	existing := sha256.New()
	if e = writePortableMedia(ctx, existing, root, f.Name, asset); e != nil {
		return false
	}
	return bytes.Equal(incoming.Sum(nil), existing.Sum(nil))
}

// previewMedia uses temporary metadata for images that will be prepared at apply.
// Conflict choices are required only at apply, never silently inferred.
func buildPortableImportCandidate(ctx context.Context, base *Session, a portableImportArchive, opts portableImportOptions, root string, assets map[string]Asset, mediaIDs map[string]string, strict bool) (portableImportCandidate, error) {
	candidate := portableImportCandidate{Characters: map[string]CharacterInstance{}, Sources: map[string]CharacterTOMLSource{}}
	info := portableImportPreview{AddedMedia: []string{}, ConflictingUnknown: []string{}, UnknownFiles: []string{}, SkippedFiles: append([]string(nil), a.Skipped...), AvailableFiles: a.fileNames(), AddedFiles: []string{}, ReplacedFiles: []string{}, RemovedFiles: []string{}, AddedCharacters: []string{}, ConflictingCharacters: []string{}, ConflictingMedia: []string{}, MissingMedia: []string{}}
	if opts.Mode != "replace" && opts.Mode != "selected" {
		return candidate, errors.New("import mode must be replace or selected")
	}
	files := map[string]string{}
	if opts.Mode == "selected" {
		withSources, e := RulesetSnapshotWithSources(base.Ruleset)
		if e != nil {
			return candidate, e
		}
		for name := range withSources.Files {
			rendered, e := RenderRulesetFileTOML(withSources, name)
			if e != nil {
				return candidate, e
			}
			files[name] = rendered
		}
	}
	selected := map[string]bool{}
	for _, name := range opts.SelectedFiles {
		if selected[name] {
			return candidate, fmt.Errorf("duplicate selected ruleset file %q", name)
		}
		if _, ok := a.Ruleset[name]; !ok {
			return candidate, fmt.Errorf("unknown or duplicate selected ruleset file %q", name)
		}
		selected[name] = true
	}
	for name, source := range a.Ruleset {
		if opts.Mode == "replace" || selected[name] {
			files[name] = source
		}
	}
	for name := range files {
		if _, ok := base.Ruleset.Files[name]; ok {
			if opts.Mode == "replace" || selected[name] {
				info.ReplacedFiles = append(info.ReplacedFiles, name)
			}
		} else if _, incoming := a.Ruleset[name]; incoming {
			info.AddedFiles = append(info.AddedFiles, name)
		}
	}
	if opts.Mode == "replace" {
		for name := range base.Ruleset.Files {
			if _, ok := files[name]; !ok {
				info.RemovedFiles = append(info.RemovedFiles, name)
			}
		}
	}
	for name := range a.Unknown {
		info.UnknownFiles = append(info.UnknownFiles, name)
	}
	slices.Sort(info.UnknownFiles)
	slices.Sort(info.AddedFiles)
	slices.Sort(info.ReplacedFiles)
	slices.Sort(info.RemovedFiles)
	ruleset, e := CompileRulesetFiles(files)
	if e != nil {
		return candidate, e
	}
	// Recompilation is needed to validate the selected set, but must not
	// replace original manifests of untouched files with rendered snapshots.
	if opts.Mode == "selected" {
		for name, manifest := range base.Ruleset.Files {
			if !selected[name] {
				ruleset.Files[name] = manifest
			}
		}
	}
	// Only listed paths are part of a fully replaced media library. For a
	// selective import, preserve untouched existing mappings as well.
	nextMedia := map[string]string{}
	maps.Copy(nextMedia, base.Ruleset.Media)
	for name := range a.Media {
		existingID := base.Ruleset.Media[name]
		incoming := a.Media[name]
		if incoming == nil {
			if existingID != "" {
				nextMedia[name] = existingID
			} else {
				info.MissingMedia = append(info.MissingMedia, name)
			}
			continue
		}
		same := existingID != "" && equalImportedMedia(ctx, root, incoming, base.Assets[existingID])
		if same {
			nextMedia[name] = existingID
			continue
		}
		if existingID == "" {
			info.AddedMedia = append(info.AddedMedia, name)
		}
		if existingID != "" && !same {
			info.ConflictingMedia = append(info.ConflictingMedia, name)
			action := opts.Media[name]
			if strict && action != "keep" && action != "replace" {
				return candidate, fmt.Errorf("media %q requires keep or replace", name)
			}
			if action == "keep" {
				nextMedia[name] = existingID
				continue
			}
		}
		newID := mediaIDs[name]
		if newID == "" {
			h := sha256.Sum256([]byte(name))
			newID = "portable_" + hex.EncodeToString(h[:12])
		}
		nextMedia[name] = newID
		if _, ok := assets[newID]; !ok {
			assets[newID] = Asset{ID: newID, Kind: assetKindAvatar}
		}
	}
	slices.Sort(info.AddedMedia)
	slices.Sort(info.ConflictingMedia)
	for name := range nextMedia {
		if err := validateRulesetMediaPath(name); err != nil {
			return candidate, err
		}
	}
	pathOwner := map[string]string{}
	for name := range nextMedia {
		lower := libraryKey(name)
		if first, ok := pathOwner[lower]; ok && first != name {
			return candidate, fmt.Errorf("media paths %q and %q conflict on case-insensitive filesystems", first, name)
		}
		pathOwner[lower] = name
	}
	ruleset.Media = nextMedia
	if e = ValidateRulesetSnapshot(ruleset); e != nil {
		return candidate, e
	}
	campaign := emptyCampaignRegistry()
	registry, e := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: ruleset.Registry, Campaign: campaign})
	if e != nil {
		return candidate, e
	}
	next := cloneForPortableImport(base)
	next.Ruleset = ruleset
	if next.UnknownTOML == nil {
		next.UnknownTOML = map[string]string{}
	}
	for name, source := range a.Unknown {
		if existing, ok := next.UnknownTOML[name]; ok && existing != source {
			info.ConflictingUnknown = append(info.ConflictingUnknown, name)
			choice := opts.Unknown[name]
			if strict && choice != "keep" && choice != "replace" {
				return candidate, fmt.Errorf("unknown TOML %q requires keep or replace", name)
			}
			if choice != "replace" {
				continue
			}
		}
		next.UnknownTOML[name] = source
	}
	slices.Sort(info.ConflictingUnknown)
	next.LibraryDirectories = append(next.LibraryDirectories, a.Directories...)
	slices.Sort(next.LibraryDirectories)
	next.LibraryDirectories = slices.Compact(next.LibraryDirectories)
	next.CampaignDefinitions = campaign
	next.Assets = assets
	seenCharacters := map[string]bool{}
	for name, data := range a.Characters {
		instance, e := DecodeCharacterTOML(data, ruleset.Media, registry)
		if e != nil {
			return candidate, fmt.Errorf("%s: %w", name, e)
		}
		if seenCharacters[instance.ID] {
			return candidate, fmt.Errorf("duplicate character ID %q", instance.ID)
		}
		seenCharacters[instance.ID] = true
		candidate.Sources[instance.ID] = CharacterTOMLSource{FileName: name, Source: string(data)}
		if _, exists := next.CharacterInstances[instance.ID]; exists {
			info.ConflictingCharacters = append(info.ConflictingCharacters, instance.ID)
			action := opts.Characters[instance.ID]
			if strict && action != "skip" && action != "replace" {
				return candidate, fmt.Errorf("character %q requires skip or replace", instance.ID)
			}
			if action != "replace" {
				delete(candidate.Sources, instance.ID)
				continue
			}
		} else {
			info.AddedCharacters = append(info.AddedCharacters, instance.ID)
		}
		next.CharacterInstances[instance.ID] = instance
		next.CharacterSources[instance.ID] = candidate.Sources[instance.ID]
	}
	slices.Sort(info.AddedCharacters)
	slices.Sort(info.ConflictingCharacters)
	// prepareDefinitionCharacters enforces references for ALL existing characters,
	// including ones not supplied by the ZIP. Never silently materialize an old
	// linked character just because its preset vanished.
	for id, inst := range base.CharacterInstances {
		if _, imported := candidate.Sources[id]; imported {
			continue
		}
		if inst.PresetID != "" && inst.Materialized == nil {
			if _, ok := registry.Presets[inst.PresetID]; !ok {
				return candidate, fmt.Errorf("existing linked character %q would lose preset %q", id, inst.PresetID)
			}
		}
	}
	updates, e := prepareDefinitionCharacters(next, ruleset, campaign)
	if e != nil {
		return candidate, e
	}
	for id, inst := range updates {
		next.CharacterInstances[id] = inst
	}
	if e = validateLibrary(next); e != nil {
		return candidate, e
	}
	if e = validateSessionCharacterState(next); e != nil {
		return candidate, e
	}
	// Refuse imports which cannot be exported again (including duplicate
	// character source names and unsafe source-preserving edits).
	exportSnapshot := snapshotPortableExport(next)
	if _, _, e = portableExportFiles(&exportSnapshot, false); e != nil {
		return candidate, fmt.Errorf("non-exportable imported state: %w", e)
	}
	missing := map[string]bool{}
	for _, name := range info.MissingMedia {
		missing[name] = true
	}
	reportArtwork := func(name string) {
		if name != "" {
			id := ruleset.Media[name]
			if id == "" || !isCharacterArtworkAsset(next.Assets[id]) {
				missing[name] = true
			}
		}
	}
	for _, preset := range ruleset.Registry.Presets {
		reportArtwork(preset.Artwork)
	}
	for _, preset := range campaign.Presets {
		reportArtwork(preset.Artwork)
	}
	for _, inst := range next.CharacterInstances {
		reportArtwork(inst.ArtworkPath)
	}
	info.MissingMedia = info.MissingMedia[:0]
	for name := range missing {
		info.MissingMedia = append(info.MissingMedia, name)
	}
	slices.Sort(info.MissingMedia)
	info.RulesetDiff = DiffRulesetRegistries(base.Ruleset.Registry, ruleset.Registry)
	candidate.Ruleset = ruleset
	candidate.Campaign = campaign
	candidate.Characters = next.CharacterInstances
	candidate.Sources = next.CharacterSources
	candidate.Assets = next.Assets
	candidate.Unknown = next.UnknownTOML
	candidate.Directories = next.LibraryDirectories
	candidate.Preview = info
	return candidate, nil
}
