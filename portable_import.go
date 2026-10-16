package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Portable imports are prepared from a bounded archive, independently of the
// live session. Preview and apply use exactly the same candidate builder.
type portableImportOptions struct {
	Mode           string            `json:"mode"` // replace or selected
	SelectedFiles  []string          `json:"selectedFiles,omitempty"`
	ImportCampaign bool              `json:"importCampaign"`
	Characters     map[string]string `json:"characters,omitempty"` // existing ID: replace or skip
	Media          map[string]string `json:"media,omitempty"`      // conflicting path: replace or keep
}
type portableImportArchive struct {
	Manifest   portableExportManifest
	Ruleset    map[string]string
	Campaign   []byte
	Characters map[string][]byte
	Media      map[string]*zip.File
	Unknown    map[string]string
}
type portableImportPreview struct {
	AvailableFiles        []string               `json:"availableFiles"`
	Digest                string                 `json:"digest"`
	CampaignRevision      uint64                 `json:"campaignRevision"`
	RegistryRevision      uint64                 `json:"registryRevision"`
	CharacterRevision     uint64                 `json:"characterRevision"`
	AddedFiles            []string               `json:"addedFiles"`
	ReplacedFiles         []string               `json:"replacedFiles"`
	RemovedFiles          []string               `json:"removedFiles"`
	RulesetDiff           RulesetReplacementDiff `json:"rulesetDiff"`
	CampaignDiff          RulesetReplacementDiff `json:"campaignDiff"`
	AddedCharacters       []string               `json:"addedCharacters"`
	ConflictingCharacters []string               `json:"conflictingCharacters"`
	ConflictingMedia      []string               `json:"conflictingMedia"`
	MissingMedia          []string               `json:"missingMedia"`
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

func importSafeName(name string) bool { return validateLibraryPath(name) == nil }

func readPortableImportArchive(reader *zip.Reader) (portableImportArchive, error) {
	if len(reader.File) > maxPortableImportFiles {
		return portableImportArchive{}, fmt.Errorf("too many archive entries")
	}
	entries := make(map[string]*zip.File, len(reader.File))
	var total uint64
	for _, f := range reader.File {
		if !importSafeName(f.Name) || !f.Mode().IsRegular() {
			return portableImportArchive{}, fmt.Errorf("unsafe archive entry %q", f.Name)
		}
		low := libraryKey(f.Name)
		if _, ok := entries[low]; ok {
			return portableImportArchive{}, fmt.Errorf("duplicate archive entry %q", f.Name)
		}
		if f.UncompressedSize64 > maxPortableImportUncompressed-total {
			return portableImportArchive{}, errors.New("archive uncompressed size exceeds limit")
		}
		total += f.UncompressedSize64
		entries[low] = f
	}
	manifestKey := libraryKey("manifest.toml")
	mf := entries[manifestKey]
	if mf == nil || mf.UncompressedSize64 > MaxDefinitionDocumentBytes {
		return portableImportArchive{}, errors.New("missing or oversized manifest.toml")
	}
	mbytes, err := readImportEntry(mf, MaxDefinitionDocumentBytes)
	if err != nil {
		return portableImportArchive{}, err
	}
	var manifest portableExportManifest
	if err = toml.NewDecoder(bytes.NewReader(mbytes)).DisallowUnknownFields().Decode(&manifest); err != nil {
		return portableImportArchive{}, fmt.Errorf("manifest: %w", err)
	}
	if manifest.FormatVersion != portablePackageVersion {
		return portableImportArchive{}, fmt.Errorf("unsupported archive format_version %d", manifest.FormatVersion)
	}
	out := portableImportArchive{Manifest: manifest, Ruleset: map[string]string{}, Characters: map[string][]byte{}, Media: map[string]*zip.File{}}
	listed := map[string]bool{manifestKey: true}
	add := func(name, prefix string, max uint64) (*zip.File, error) {
		if !importSafeName(name) || !strings.HasPrefix(name, prefix) {
			return nil, fmt.Errorf("invalid manifest path %q", name)
		}
		f := entries[libraryKey(name)]
		if f == nil || f.Name != name || listed[libraryKey(name)] {
			return nil, fmt.Errorf("missing, duplicated or case-conflicting entry %q", name)
		}
		if f.UncompressedSize64 > max {
			return nil, fmt.Errorf("archive entry %q exceeds size limit", name)
		}
		listed[libraryKey(name)] = true
		return f, nil
	}
	var rulesetBytes uint64
	var totalTOML uint64
	for _, name := range manifest.RulesetFiles {
		f, e := add(name, "ruleset/", MaxDefinitionDocumentBytes)
		if e != nil {
			return out, e
		}
		short := strings.TrimPrefix(name, "ruleset/")
		if e = validateRulesetFileName(short); e != nil {
			return out, e
		}
		rulesetBytes += f.UncompressedSize64
		totalTOML += f.UncompressedSize64
		if rulesetBytes > MaxRulesetPackageBytes {
			return out, errors.New("ruleset package exceeds limit")
		}
		data, e := readImportEntry(f, MaxDefinitionDocumentBytes)
		if e != nil {
			return out, e
		}
		out.Ruleset[short] = string(data)
	}
	if len(manifest.RulesetFiles) > MaxRulesetFiles {
		return out, errors.New("too many ruleset files")
	}
	if len(manifest.CampaignFiles) > 1 {
		return out, errors.New("multiple campaign documents are not supported")
	}
	for _, name := range manifest.CampaignFiles {
		if name != "campaign/generated-definitions.toml" {
			return out, fmt.Errorf("unsupported campaign path %q", name)
		}
		f, e := add(name, "campaign/", MaxDefinitionDocumentBytes)
		if e != nil {
			return out, e
		}
		totalTOML += f.UncompressedSize64
		out.Campaign, e = readImportEntry(f, MaxDefinitionDocumentBytes)
		if e != nil {
			return out, e
		}
	}
	if len(manifest.CharacterFiles) > MaxCharacterInstances {
		return out, errors.New("too many characters")
	}
	for _, name := range manifest.CharacterFiles {
		if e := validateCharacterSourceFileName(name); e != nil {
			return out, e
		}
		f, e := add(name, "", MaxDefinitionDocumentBytes)
		if e != nil {
			return out, e
		}
		totalTOML += f.UncompressedSize64
		if totalTOML > uint64(maxPortableTOMLBytes) {
			return out, errors.New("import TOML exceeds total size limit")
		}
		out.Characters[name], e = readImportEntry(f, MaxDefinitionDocumentBytes)
		if e != nil {
			return out, e
		}
	}
	if totalTOML > uint64(maxPortableTOMLBytes) {
		return out, errors.New("import TOML exceeds total size limit")
	}
	if len(manifest.MediaFiles) > maxRulesetMediaEntries {
		return out, errors.New("too many media paths")
	}
	seenMediaPaths := map[string]bool{}
	for _, name := range manifest.MediaFiles {
		if seenMediaPaths[libraryKey(name)] {
			return out, fmt.Errorf("duplicate media path %q", name)
		}
		seenMediaPaths[libraryKey(name)] = true
		if e := validateRulesetMediaPath(name); e != nil {
			return out, e
		}
		if manifest.IncludesMedia {
			f, e := add(name, "", maxPortableImportImage)
			if e != nil {
				return out, e
			}
			out.Media[name] = f
		}
	}
	if len(manifest.UnknownFiles) > maxUnknownTOMLFiles || len(manifest.Directories) > maxLibraryDirectories {
		return out, errors.New("library limit exceeded")
	}
	for _, d := range manifest.Directories {
		if e := validateLibraryPath(d); e != nil {
			return out, e
		}
	}
	if out.Unknown == nil {
		out.Unknown = map[string]string{}
	}
	for _, name := range manifest.UnknownFiles {
		if !strings.HasPrefix(name, "unknown/") {
			return out, fmt.Errorf("invalid unknown TOML entry %q", name)
		}
		logical := strings.TrimPrefix(name, "unknown/")
		if e := validateCharacterSourceFileName(logical); e != nil {
			return out, e
		}
		f, e := add(name, "unknown/", MaxDefinitionDocumentBytes)
		if e != nil {
			return out, e
		}
		totalTOML += f.UncompressedSize64
		if totalTOML > uint64(maxPortableTOMLBytes) {
			return out, errors.New("import TOML exceeds total size limit")
		}
		data, e := readImportEntry(f, MaxDefinitionDocumentBytes)
		if e != nil {
			return out, e
		}
		if _, exists := out.Unknown[logical]; exists {
			return out, fmt.Errorf("duplicate unknown TOML %q", logical)
		}
		var opaque map[string]any
		if e = toml.Unmarshal(data, &opaque); e != nil {
			return out, fmt.Errorf("unknown TOML %q: %w", logical, e)
		}
		out.Unknown[logical] = string(data)
	}
	if len(listed) != len(entries) {
		return out, errors.New("archive includes unlisted entries")
	}
	return out, nil
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
	info := portableImportPreview{AvailableFiles: a.fileNames(), AddedFiles: []string{}, ReplacedFiles: []string{}, RemovedFiles: []string{}, AddedCharacters: []string{}, ConflictingCharacters: []string{}, ConflictingMedia: []string{}, MissingMedia: []string{}}
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
		if selected[name] || a.Ruleset[name] == "" {
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
			if a.Ruleset[name] != "" && (opts.Mode == "replace" || selected[name]) {
				info.ReplacedFiles = append(info.ReplacedFiles, name)
			}
		} else if a.Ruleset[name] != "" {
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
	if opts.Mode == "selected" {
		maps.Copy(nextMedia, base.Ruleset.Media)
	}
	for _, name := range a.Manifest.MediaFiles {
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
	if len(a.Campaign) > 0 {
		return candidate, errors.New("legacy ZIP campaign definitions are unsupported; import TOML as a ruleset file")
	}
	campaign := emptyCampaignRegistry()
	registry, e := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: ruleset.Registry, Campaign: campaign})
	if e != nil {
		return candidate, e
	}
	next := cloneForPortableImport(base)
	next.Ruleset = ruleset
	if opts.Mode == "replace" {
		next.UnknownTOML = maps.Clone(a.Unknown)
		next.LibraryDirectories = append([]string(nil), a.Manifest.Directories...)
	}
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
		if name != "" && ruleset.Media[name] == "" {
			missing[name] = true
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
	info.CampaignDiff = DiffRulesetRegistries(RulesetRegistry{Stats: base.CampaignDefinitions.Stats, Actions: base.CampaignDefinitions.Actions, Presets: base.CampaignDefinitions.Presets}, RulesetRegistry{Stats: campaign.Stats, Actions: campaign.Actions, Presets: campaign.Presets})
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
