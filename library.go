package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

// The library is a namespace over existing storage, not an additional file store.
// Ruleset.Files owns source TOML, CharacterSources owns character TOML, and
// Ruleset.Media names immutable assets. UnknownTOML owns uninterpreted documents.
const maxLibraryDirectories = 4096
const maxUnknownTOMLFiles = 256
const maxLibraryPathBytes = 255
const maxLibrarySegmentRunes = 120

func validateLibraryPath(p string) error {
	if p == "" || len(p) > maxLibraryPathBytes || !utf8.ValidString(p) || p != path.Clean(p) || strings.HasPrefix(p, "/") || strings.ContainsRune(p, '\\') {
		return fmt.Errorf("unsafe library path %q", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || len([]rune(part)) > maxLibrarySegmentRunes || strings.HasSuffix(part, " ") || strings.HasSuffix(part, ".") {
			return fmt.Errorf("unsafe library path %q", p)
		}
		if strings.ContainsAny(part, "<>:\"|?*") {
			return fmt.Errorf("unsafe library path %q", p)
		}
		for _, ch := range part {
			if unicode.IsControl(ch) {
				return fmt.Errorf("unsafe library path %q", p)
			}
		}
		base := strings.SplitN(part, ".", 2)[0]
		switch strings.ToUpper(base) {
		case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "COM¹", "COM²", "COM³", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9", "LPT¹", "LPT²", "LPT³":
			return fmt.Errorf("reserved Windows path %q", p)
		}
	}
	return nil
}
func validateLibraryFilePath(p string) error { return validateLibraryPath(p) }
func libraryKey(p string) string {
	var key strings.Builder
	for _, r := range p {
		low := r
		for folded := unicode.SimpleFold(r); folded != r; folded = unicode.SimpleFold(folded) {
			if folded < low {
				low = folded
			}
		}
		key.WriteRune(low)
	}
	return key.String()
}

// LibraryFile is derived; definition IDs are not duplicated in persisted data.
type LibraryFile struct {
	Path            string   `json:"path"`
	Type            string   `json:"type"` // definitions, character, image, unknown-toml
	CharacterID     string   `json:"characterId,omitempty"`
	AssetID         string   `json:"assetId,omitempty"`
	StatIDs         []string `json:"statIds,omitempty"`
	ActionIDs       []string `json:"actionIds,omitempty"`
	PresetIDs       []string `json:"presetIds,omitempty"`
	SourcePreserved bool     `json:"sourcePreserved,omitempty"`
}
type LibraryTree struct {
	Directories         []string      `json:"directories"`
	ExplicitDirectories []string      `json:"explicitDirectories"`
	Files               []LibraryFile `json:"files"`
	RegistryRevision    uint64        `json:"registryRevision"`
	LibraryRevision     uint64        `json:"libraryRevision"`
	CharacterRevision   uint64        `json:"characterRevision"`
}

func libraryFiles(ss *Session) []LibraryFile {
	files := make([]LibraryFile, 0, len(ss.Ruleset.Files)+len(ss.CharacterSources)+len(ss.Ruleset.Media)+len(ss.UnknownTOML))
	for p, m := range ss.Ruleset.Files {
		files = append(files, LibraryFile{Path: p, Type: "definitions", StatIDs: append([]string(nil), m.Stats...), ActionIDs: append([]string(nil), m.Actions...), PresetIDs: append([]string(nil), m.Presets...), SourcePreserved: m.Source != ""})
	}
	for id, instance := range ss.CharacterInstances {
		if instance.Persistent {
			source := ss.CharacterSources[id]
			name := source.FileName
			if name == "" {
				name = characterSuggestedPath(instance, 8)
			}
			files = append(files, LibraryFile{Path: name, Type: "character", CharacterID: id, SourcePreserved: source.Source != ""})
		}
	}
	for p, asset := range ss.Ruleset.Media {
		files = append(files, LibraryFile{Path: p, Type: "image", AssetID: asset})
	}
	for p := range ss.UnknownTOML {
		files = append(files, LibraryFile{Path: p, Type: "unknown-toml", SourcePreserved: true})
	}
	slices.SortFunc(files, func(a, b LibraryFile) int { return strings.Compare(a.Path, b.Path) })
	return files
}
func libraryTree(ss *Session) LibraryTree {
	files := libraryFiles(ss)
	dirs := map[string]bool{}
	for _, p := range ss.LibraryDirectories {
		dirs[p] = true
	}
	for _, f := range files {
		for parent := path.Dir(f.Path); parent != "."; parent = path.Dir(parent) {
			dirs[parent] = true
		}
	}
	for _, p := range ss.LibraryDirectories {
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			dirs[parent] = true
		}
	}
	all := make([]string, 0, len(dirs))
	for p := range dirs {
		all = append(all, p)
	}
	sort.Strings(all)
	explicit := append([]string(nil), ss.LibraryDirectories...)
	sort.Strings(explicit)
	return LibraryTree{Directories: all, ExplicitDirectories: explicit, Files: files, RegistryRevision: ss.RegistryRevision, CharacterRevision: ss.CharacterRevision, LibraryRevision: ss.LibraryRevision}
}
func validateLibrary(ss *Session) error {
	if len(ss.LibraryDirectories) > maxLibraryDirectories || len(ss.UnknownTOML) > maxUnknownTOMLFiles {
		return errors.New("library size limit exceeded")
	}
	fileNames := map[string]string{}
	directories := map[string]string{}
	paths := make([]string, 0, len(ss.LibraryDirectories)+len(ss.Ruleset.Files)+len(ss.CharacterInstances)+len(ss.Ruleset.Media)+len(ss.UnknownTOML))
	for _, f := range libraryFiles(ss) {
		if err := validateLibraryFilePath(f.Path); err != nil {
			return err
		}
		key := libraryKey(f.Path)
		if original, exists := fileNames[key]; exists {
			return fmt.Errorf("library file collision: %q and %q", original, f.Path)
		}
		fileNames[key] = f.Path
		paths = append(paths, f.Path)
	}
	for _, p := range ss.LibraryDirectories {
		if err := validateLibraryPath(p); err != nil {
			return err
		}
		key := libraryKey(p)
		if original, exists := directories[key]; exists {
			return fmt.Errorf("library directory collision: %q and %q", original, p)
		}
		directories[key] = p
		paths = append(paths, p)
	}
	// Include all implicit ancestors in the same namespace, without adding
	// them to persistent state or imposing an O(files^2) scan on saves.
	for _, p := range paths {
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			if err := validateLibraryPath(parent); err != nil {
				return err
			}
			key := libraryKey(parent)
			if previous, exists := directories[key]; exists && previous != parent {
				return fmt.Errorf("directory case collision: %q and %q", previous, parent)
			}
			directories[key] = parent
		}
	}
	for key, p := range fileNames {
		if dir, exists := directories[key]; exists {
			return fmt.Errorf("file %q overlaps directory %q", p, dir)
		}
	}
	for p := range ss.UnknownTOML {
		if !strings.EqualFold(path.Ext(p), ".toml") {
			return fmt.Errorf("unknown TOML %q has invalid extension", p)
		}
	}
	return nil
}

func safeCharacterStem(name string) string {
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == ' ' {
			b.WriteRune(r)
		} else if r == '.' || r == '/' || r == '\\' {
			b.WriteRune('-')
		}
	}
	stem := strings.Trim(b.String(), " ._-")
	if stem == "" {
		stem = "Персонаж"
	}
	if len([]rune(stem)) > 45 {
		stem = string([]rune(stem)[:45])
	}
	if validateLibraryPath(stem) != nil {
		stem = "Персонаж"
	}
	return stem
}
func characterSuggestedPath(instance CharacterInstance, suffix int) string {
	digest := sha256.Sum256([]byte(instance.ID))
	token := hex.EncodeToString(digest[:])
	// Keep a visibly stable part of a hex UUID where possible.
	cleanID := strings.ReplaceAll(instance.ID, "-", "")
	if _, err := hex.DecodeString(cleanID); err == nil && len(cleanID) >= 8 {
		token = strings.ToLower(cleanID) + token
	}
	if suffix > len(token) {
		suffix = len(token)
	}
	return "characters/" + safeCharacterStem(instance.Name) + "-" + token[:suffix] + ".toml"
}
func syncLibraryCharacterPaths(ss *Session) error {
	if ss.CharacterSources == nil {
		ss.CharacterSources = map[string]CharacterTOMLSource{}
	}
	// Reserve explicit names first. Deterministic ordering avoids random map order.
	occupied := map[string]bool{}
	for p := range ss.Ruleset.Files {
		occupied[libraryKey(p)] = true
	}
	for p := range ss.Ruleset.Media {
		occupied[libraryKey(p)] = true
	}
	for p := range ss.UnknownTOML {
		occupied[libraryKey(p)] = true
	}
	for _, p := range ss.LibraryDirectories {
		occupied[libraryKey(p)] = true
	}
	for _, f := range libraryFiles(ss) {
		if f.Type == "character" {
			continue
		}
		for parent := path.Dir(f.Path); parent != "."; parent = path.Dir(parent) {
			occupied[libraryKey(parent)] = true
		}
	}
	for _, p := range ss.LibraryDirectories {
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			occupied[libraryKey(parent)] = true
		}
	}
	ids := PortableCharacterIDs(ss.CharacterInstances)
	for _, id := range ids {
		source := ss.CharacterSources[id]
		if !source.AutoName && source.FileName != "" {
			if occupied[libraryKey(source.FileName)] {
				return fmt.Errorf("character filename collision %q", source.FileName)
			}
			occupied[libraryKey(source.FileName)] = true
		}
	}
	for _, id := range ids {
		source := ss.CharacterSources[id]
		if source.FileName != "" && !source.AutoName {
			continue
		}
		for width := 8; width <= 64; width += 2 {
			p := characterSuggestedPath(ss.CharacterInstances[id], width)
			if !occupied[libraryKey(p)] {
				source.FileName = p
				source.AutoName = true
				ss.CharacterSources[id] = source
				occupied[libraryKey(p)] = true
				break
			}
		}
		if source.FileName == "" {
			return fmt.Errorf("unable to assign character filename %q", id)
		}
	}
	return nil
}

func (s *Server) readLibrary(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, member := s.auth(r)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	reply(w, libraryTree(ss))
}

// A single request rewrites the complete namespace in a candidate. The live
// session remains untouched until all cross-reference checks succeed.
type libraryRequest struct {
	Operation                 string  `json:"operation"`
	Path                      string  `json:"path"`
	Target                    string  `json:"target"`
	ExpectedRegistryRevision  *uint64 `json:"expectedRegistryRevision,omitempty"`
	ExpectedCharacterRevision *uint64 `json:"expectedCharacterRevision,omitempty"`
	ExpectedLibraryRevision   *uint64 `json:"expectedLibraryRevision,omitempty"`
}

func libraryHasPrefix(p, base string) bool { return p == base || strings.HasPrefix(p, base+"/") }
func renameLibraryPath(p, old, new string) string {
	if p == old {
		return new
	}
	if strings.HasPrefix(p, old+"/") {
		return new + p[len(old):]
	}
	return p
}
func cloneLibrarySession(ss *Session) *Session {
	candidate := *ss
	candidate.Ruleset = cloneRulesetSnapshot(ss.Ruleset)
	candidate.CharacterInstances = maps.Clone(ss.CharacterInstances)
	candidate.CharacterSources = maps.Clone(ss.CharacterSources)
	candidate.UnknownTOML = maps.Clone(ss.UnknownTOML)
	candidate.LibraryDirectories = append([]string(nil), ss.LibraryDirectories...)
	return &candidate
}
func libraryDependents(ss *Session, kind, id string) []string {
	registry, err := MergeDefinitionRegistries(sessionRegistries(ss))
	if err != nil {
		return []string{err.Error()}
	}
	return definitionReferences(ss, registry, kind, id)
}
func deleteLibraryFile(ss *Session, p string) error {
	if manifest, ok := ss.Ruleset.Files[p]; ok {
		if manifest.HasMetadata && ss.Ruleset.Metadata != (RulesetMetadata{}) {
			return fmt.Errorf("TOML %q owns active ruleset metadata", p)
		}
		for _, id := range manifest.Stats {
			if deps := libraryDependents(ss, definitionKindStat, id); len(deps) > 0 {
				return fmt.Errorf("stat %s is used by %v", id, deps)
			}
		}
		for _, id := range manifest.Actions {
			if deps := libraryDependents(ss, definitionKindAction, id); len(deps) > 0 {
				return fmt.Errorf("action %s is used by %v", id, deps)
			}
		}
		for _, id := range manifest.Presets {
			if deps := libraryDependents(ss, definitionKindPreset, id); len(deps) > 0 {
				return fmt.Errorf("preset %s is used by %v", id, deps)
			}
		}
		delete(ss.Ruleset.Files, p)
		for _, id := range manifest.Stats {
			delete(ss.Ruleset.Registry.Stats, id)
		}
		for _, id := range manifest.Actions {
			delete(ss.Ruleset.Registry.Actions, id)
		}
		for _, id := range manifest.Presets {
			delete(ss.Ruleset.Registry.Presets, id)
		}
		return nil
	}
	for id, src := range ss.CharacterSources {
		if src.FileName == p {
			for _, scene := range ss.Scenes {
				for _, token := range scene.Tokens {
					if token.CharacterInstanceID == id {
						return fmt.Errorf("character %q used by token %q", id, token.ID)
					}
				}
			}
			delete(ss.CharacterSources, id)
			delete(ss.CharacterInstances, id)
			return nil
		}
	}
	if assetID, ok := ss.Ruleset.Media[p]; ok {
		var deps []string
		for id, preset := range ss.Ruleset.Registry.Presets {
			if preset.Artwork == p {
				deps = append(deps, "preset:"+id)
			}
		}
		for id, inst := range ss.CharacterInstances {
			if inst.ArtworkPath == p {
				deps = append(deps, "character:"+id)
			}
		}
		if len(deps) > 0 {
			sort.Strings(deps)
			return fmt.Errorf("image %q (%s) is used by %v", p, assetID, deps)
		}
		delete(ss.Ruleset.Media, p)
		return nil
	}
	if _, ok := ss.UnknownTOML[p]; ok {
		delete(ss.UnknownTOML, p)
		return nil
	}
	return fmt.Errorf("unknown library file %q", p)
}
func applyLibraryOperation(ss *Session, req libraryRequest) error {
	if err := validateLibraryPath(req.Path); err != nil {
		return err
	}
	if err := syncLibraryCharacterPaths(ss); err != nil {
		return err
	}
	if err := validateLibrary(ss); err != nil {
		return err
	}
	files := libraryFiles(ss)
	fileType := ""
	for _, f := range files {
		if f.Path == req.Path {
			fileType = f.Type
			break
		}
	}
	dirs := libraryTree(ss).Directories
	isDir := slices.Contains(dirs, req.Path)
	switch req.Operation {
	case "createDirectory":
		for _, d := range dirs {
			if libraryKey(d) == libraryKey(req.Path) {
				return fmt.Errorf("directory %q already exists", d)
			}
		}
		ss.LibraryDirectories = append(ss.LibraryDirectories, req.Path)
	case "deleteDirectory":
		if !isDir {
			return fmt.Errorf("unknown directory %q", req.Path)
		}
		for _, f := range files {
			if strings.HasPrefix(f.Path, req.Path+"/") {
				return fmt.Errorf("directory %q contains files", req.Path)
			}
		}
		for _, d := range dirs {
			if d != req.Path && strings.HasPrefix(d, req.Path+"/") {
				return fmt.Errorf("directory %q contains subdirectories", req.Path)
			}
		}
		ss.LibraryDirectories = slices.DeleteFunc(ss.LibraryDirectories, func(p string) bool { return p == req.Path })
	case "renameFile", "moveFile", "renameDirectory", "moveDirectory":
		folder := strings.HasSuffix(req.Operation, "Directory")
		if folder && !isDir {
			return fmt.Errorf("unknown directory %q", req.Path)
		}
		if !folder && fileType == "" {
			return fmt.Errorf("unknown file %q", req.Path)
		}
		if err := validateLibraryPath(req.Target); err != nil {
			return err
		}
		if strings.HasPrefix(req.Operation, "rename") && path.Dir(req.Target) != path.Dir(req.Path) {
			return errors.New("rename may not change parent directory")
		}
		if folder && libraryHasPrefix(req.Target, req.Path) {
			return errors.New("cannot move directory into itself or its descendant")
		}
		if !folder && !strings.EqualFold(path.Ext(req.Target), path.Ext(req.Path)) {
			return errors.New("file extension cannot change")
		}
		if folder {
			moved := false
			for i, p := range ss.LibraryDirectories {
				if libraryHasPrefix(p, req.Path) {
					ss.LibraryDirectories[i] = renameLibraryPath(p, req.Path, req.Target)
					moved = true
				}
			}
			if !moved {
				ss.LibraryDirectories = append(ss.LibraryDirectories, req.Target)
			} // implicit folder becomes explicit
		}
		move := func(p string) string {
			if folder {
				return renameLibraryPath(p, req.Path, req.Target)
			}
			if p == req.Path {
				return req.Target
			}
			return p
		}
		// Validate every destination before inserting into maps: map insertion
		// would otherwise silently overwrite a colliding file.
		seen := map[string]string{}
		for _, f := range files {
			next := move(f.Path)
			if err := validateLibraryFilePath(next); err != nil {
				return err
			}
			key := libraryKey(next)
			if old, exists := seen[key]; exists {
				return fmt.Errorf("move would overwrite %q with %q", old, next)
			}
			seen[key] = next
		}
		nextFiles := map[string]RulesetFileManifest{}
		for p, v := range ss.Ruleset.Files {
			nextFiles[move(p)] = v
		}
		ss.Ruleset.Files = nextFiles
		nextMedia := map[string]string{}
		for p, v := range ss.Ruleset.Media {
			nextMedia[move(p)] = v
		}
		ss.Ruleset.Media = nextMedia
		nextUnknown := map[string]string{}
		for p, v := range ss.UnknownTOML {
			nextUnknown[move(p)] = v
		}
		ss.UnknownTOML = nextUnknown
		for id, src := range ss.CharacterSources {
			if src.FileName != "" {
				name := move(src.FileName)
				if name != src.FileName {
					src.FileName = name
					src.AutoName = false
					ss.CharacterSources[id] = src
				}
			}
		}
		// Only a moved image changes artwork references. Unknown TOML is opaque.
		for _, f := range files {
			if f.Type != "image" {
				continue
			}
			next := move(f.Path)
			if next == f.Path {
				continue
			}
			for id, preset := range ss.Ruleset.Registry.Presets {
				if preset.Artwork == f.Path {
					preset.Artwork = next
					ss.Ruleset.Registry.Presets[id] = preset
				}
			}
			for id, inst := range ss.CharacterInstances {
				if inst.ArtworkPath == f.Path {
					inst.ArtworkPath = next
					ss.CharacterInstances[id] = inst
				}
			}
		}
	case "deleteFile":
		if fileType == "" {
			return fmt.Errorf("unknown file %q", req.Path)
		}
		if err := deleteLibraryFile(ss, req.Path); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown library operation %q", req.Operation)
	}
	if err := validateLibrary(ss); err != nil {
		return err
	}
	if err := ValidateRulesetSnapshot(ss.Ruleset); err != nil {
		return err
	}
	if err := validateSessionCharacterState(ss); err != nil {
		return err
	}
	return nil
}
func (s *Server) libraryOperation(w http.ResponseWriter, r *http.Request) {
	var req libraryRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, member := s.auth(r)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	if s.stopping {
		fail(w, http.StatusServiceUnavailable, "Сервер останавливается")
		return
	}
	if req.ExpectedRegistryRevision != nil && *req.ExpectedRegistryRevision != ss.RegistryRevision || req.ExpectedCharacterRevision != nil && *req.ExpectedCharacterRevision != ss.CharacterRevision || req.ExpectedLibraryRevision != nil && *req.ExpectedLibraryRevision != ss.LibraryRevision {
		fail(w, http.StatusConflict, "Ревизия библиотеки изменилась")
		return
	}
	candidate := cloneLibrarySession(ss)
	if err := applyLibraryOperation(candidate, req); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	oldRuleset, oldCharacters, oldSources, oldAssets := ss.Ruleset, ss.CharacterInstances, ss.CharacterSources, ss.Assets
	oldDirs, oldUnknown, oldDirty, oldRegRev, oldCharRev, oldLibraryRev := ss.LibraryDirectories, ss.UnknownTOML, s.dirty, ss.RegistryRevision, ss.CharacterRevision, ss.LibraryRevision
	ss.Ruleset, ss.CharacterInstances, ss.CharacterSources = candidate.Ruleset, candidate.CharacterInstances, candidate.CharacterSources
	ss.LibraryDirectories, ss.UnknownTOML = candidate.LibraryDirectories, candidate.UnknownTOML
	if req.Operation == "deleteFile" {
		ss.Assets = cloneAssetRegistry(ss.Assets)
		refreshAssetOrphans(ss, time.Now())
	}
	registryChanged := !reflect.DeepEqual(oldRuleset, ss.Ruleset)
	characterChanged := !reflect.DeepEqual(oldCharacters, ss.CharacterInstances) || !reflect.DeepEqual(oldSources, ss.CharacterSources)
	if registryChanged {
		ss.RegistryRevision++
	}
	if characterChanged {
		ss.CharacterRevision++
	}
	ss.LibraryRevision++
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		ss.Ruleset, ss.CharacterInstances, ss.CharacterSources, ss.Assets = oldRuleset, oldCharacters, oldSources, oldAssets
		ss.LibraryDirectories, ss.UnknownTOML = oldDirs, oldUnknown
		ss.RegistryRevision, ss.CharacterRevision, ss.LibraryRevision = oldRegRev, oldCharRev, oldLibraryRev
		s.dirty = oldDirty
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if registryChanged {
		s.publishRegistryChanged(ss)
	}
	if registryChanged || characterChanged {
		s.refreshCharacterWatches(ss, nil, true)
	}
	reply(w, libraryTree(ss))
}
func (s *Server) importUnknownTOML(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxDefinitionDocumentBytes)
	var req struct {
		Path   string `json:"path"`
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid TOML request")
		return
	}
	if err := validateLibraryFilePath(req.Path); err != nil || !strings.EqualFold(path.Ext(req.Path), ".toml") {
		fail(w, http.StatusBadRequest, "Invalid TOML path")
		return
	}
	var parsed map[string]any
	if err := toml.Unmarshal([]byte(req.Source), &parsed); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if fmt.Sprint(parsed["schema_version"]) == "1" {
		for _, key := range []string{"stats", "actions", "presets", "ruleset", "character"} {
			if _, known := parsed[key]; known {
				fail(w, http.StatusBadRequest, "Known TOML schema requires the definitions or character import endpoint")
				return
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, member := s.auth(r)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	candidate := cloneLibrarySession(ss)
	if candidate.UnknownTOML == nil {
		candidate.UnknownTOML = map[string]string{}
	}
	if _, exists := candidate.UnknownTOML[req.Path]; exists {
		fail(w, http.StatusConflict, "TOML file already exists")
		return
	}
	candidate.UnknownTOML[req.Path] = req.Source
	if err := validateLibrary(candidate); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	old := ss.UnknownTOML
	oldRevision := ss.LibraryRevision
	dirty := s.dirty
	ss.UnknownTOML = candidate.UnknownTOML
	ss.LibraryRevision++
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		ss.UnknownTOML = old
		ss.LibraryRevision = oldRevision
		s.dirty = dirty
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	reply(w, libraryTree(ss))
}
