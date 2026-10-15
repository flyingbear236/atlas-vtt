package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Only contiguous TOML table blocks are editable. Anything with ambiguous
// ownership is deliberately left for manual editing, preserving other sources.
var errPresetSourceRange = errors.New("Невозможно безопасно изменить блок шаблона через редактор: отредактируйте или замените TOML вручную")

type presetTableHeader struct {
	start    int
	segments []string
	array    bool
}

func parsePresetTableHeader(line string) ([]string, bool, bool) {
	raw := strings.TrimSpace(line)
	if !strings.HasPrefix(raw, "[") {
		return nil, false, false
	}
	// Parse the header with the actual TOML parser, including quoted/dotted keys.
	// Comments after the header are allowed. Non-header text is rejected.
	var parsed map[string]any
	if err := toml.Unmarshal([]byte(raw+"\n__atlas_header_marker__ = true\n"), &parsed); err != nil {
		return nil, false, false
	}
	var result []string
	cursor := parsed
	for len(cursor) == 1 {
		for key, value := range cursor {
			result = append(result, key)
			switch child := value.(type) {
			case map[string]any:
				cursor = child
			case []any:
				if len(child) != 1 {
					return nil, false, false
				}
				next, ok := child[0].(map[string]any)
				if !ok {
					return nil, false, false
				}
				cursor = next
			default:
				if key == "__atlas_header_marker__" {
					return result[:len(result)-1], strings.HasPrefix(raw, "[["), true
				}
				return nil, false, false
			}
		}
	}
	return nil, false, false
}

func presetSourceBounds(source, id string) (int, int, error) {
	// Multiline strings can contain lines resembling table headers. Reject
	// these rare cases rather than silently selecting the wrong byte range.
	if strings.Contains(source, "\"\"\"") || strings.Contains(source, "'''") {
		return 0, 0, errPresetSourceRange
	}
	var headers []presetTableHeader
	for at := 0; at < len(source); {
		next := strings.IndexByte(source[at:], '\n')
		end := len(source)
		if next >= 0 {
			end = at + next + 1
		}
		line := source[at:end]
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") && !strings.HasPrefix(trim, "#") {
			parts, array, ok := parsePresetTableHeader(line)
			if !ok {
				return 0, 0, errPresetSourceRange
			}
			headers = append(headers, presetTableHeader{start: at, segments: parts, array: array})
		}
		at = end
	}
	first, last := -1, -1
	baseCount := 0
	for i, header := range headers {
		if len(header.segments) >= 2 && header.segments[0] == "presets" && header.segments[1] == id {
			if header.array {
				return 0, 0, errPresetSourceRange
			}
			if len(header.segments) == 2 {
				baseCount++
				if baseCount > 1 {
					return 0, 0, errPresetSourceRange
				}
			}
			if first == -1 {
				if len(header.segments) != 2 {
					return 0, 0, errPresetSourceRange
				}
				first = i
			} else if last != i-1 {
				return 0, 0, errPresetSourceRange
			}
			last = i
		}
	}
	if first < 0 || last < first || baseCount != 1 {
		return 0, 0, errPresetSourceRange
	}
	start := headers[first].start
	end := len(source)
	if last+1 < len(headers) {
		end = headers[last+1].start
	}
	// Preserve contiguous comments/empty lines introducing the next definition.
	if end < len(source) {
		prefix := source[start:end]
		lines := strings.SplitAfter(prefix, "\n")
		offset := len(prefix)
		for i := len(lines) - 1; i >= 0; i-- {
			line := lines[i]
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				break
			}
			offset -= len(line)
		}
		if offset > 0 {
			end = start + offset
		}
	}
	if end <= start {
		return 0, 0, errPresetSourceRange
	}
	return start, end, nil
}

func canonicalPresetTOML(preset CharacterPresetDefinition) (string, error) {
	stats := make(map[string]any, len(preset.Stats))
	for id, value := range preset.Stats {
		stats[id] = value.Interface()
	}
	raw := struct {
		Presets map[string]presetTOMLDefinition `toml:"presets"`
	}{
		Presets: map[string]presetTOMLDefinition{preset.ID: {Name: preset.Name, Kind: preset.Kind, Artwork: preset.Artwork, Stats: stats, Actions: append([]string(nil), preset.ActionIDs...)}},
	}
	data, err := toml.Marshal(raw)
	if err != nil {
		return "", err
	}
	if !bytes.Contains(data, []byte("[presets.")) {
		return "", errPresetSourceRange
	}
	return strings.TrimRight(string(data), "\n") + "\n", nil
}

func rewritePresetSource(source string, preset CharacterPresetDefinition) (string, error) {
	start, end, err := presetSourceBounds(source, preset.ID)
	if err != nil {
		return "", err
	}
	canonical, err := canonicalPresetTOML(preset)
	if err != nil {
		return "", err
	}
	suffix := source[end:]
	if suffix != "" && !strings.HasPrefix(suffix, "\n") && !strings.HasSuffix(canonical, "\n\n") {
		canonical += "\n"
	}
	return source[:start] + canonical + suffix, nil
}

func rulesetPresetOwner(ruleset RulesetSnapshot, id string) (string, error) {
	owner := ""
	for name, manifest := range ruleset.Files {
		for _, presetID := range manifest.Presets {
			if presetID == id {
				if owner != "" {
					return "", errPresetSourceRange
				}
				owner = name
			}
		}
	}
	if owner == "" {
		return "", fmt.Errorf("Шаблон %s отсутствует в текущем ruleset", id)
	}
	return owner, nil
}

type presetEditorData struct {
	Preset          CharacterPresetDefinition   `json:"preset"`
	Owner           string                      `json:"owner"`
	Revision        uint64                      `json:"registryRevision"`
	Linked          int                         `json:"linked"`
	Materialized    int                         `json:"materialized"`
	StatDefinitions map[string]StatDefinition   `json:"statDefinitions"`
	Actions         map[string]ActionDefinition `json:"actions"`
	Artwork         ArtworkResolution           `json:"artwork"`
}

func (s *Server) readPresetEditor(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, member := s.auth(r)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	id := r.URL.Query().Get("id")
	if err := validateID(id); err != nil {
		fail(w, http.StatusBadRequest, "Некорректный preset ID")
		return
	}
	preset, ok := session.Ruleset.Registry.Presets[id]
	if !ok {
		fail(w, http.StatusNotFound, fmt.Sprintf("Шаблон %s отсутствует в текущем ruleset", id))
		return
	}
	owner, err := rulesetPresetOwner(session.Ruleset, id)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Registry повреждён")
		return
	}
	result := presetEditorData{Preset: clonePresetDefinition(preset), Owner: owner, Revision: session.RegistryRevision, StatDefinitions: registry.Stats, Actions: registry.Actions, Artwork: resolvePresetArtwork(&preset, session.Ruleset, session.Assets)}
	for _, character := range session.CharacterInstances {
		if character.PresetID == id {
			if character.Materialized == nil {
				result.Linked++
			} else {
				result.Materialized++
			}
		}
	}
	reply(w, result)
}

type presetEditorSaveRequest struct {
	Preset           CharacterPresetDefinition `json:"preset"`
	ExpectedRevision uint64                    `json:"expectedRevision"`
	Media            map[string]string         `json:"media,omitempty"`
}

func (s *Server) savePresetEditor(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var req presetEditorSaveRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil {
		fail(w, http.StatusBadRequest, "Некорректный preset draft")
		return
	}
	if err := validateID(req.Preset.ID); err != nil {
		fail(w, http.StatusBadRequest, "Некорректный preset ID")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, member := s.auth(r)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	if req.ExpectedRevision != session.RegistryRevision {
		fail(w, http.StatusConflict, "Ruleset изменился: обновите редактор и повторите правки")
		return
	}
	if _, ok := session.Ruleset.Registry.Presets[req.Preset.ID]; !ok {
		fail(w, http.StatusNotFound, fmt.Sprintf("Шаблон %s отсутствует в текущем ruleset", req.Preset.ID))
		return
	}
	_, err := rulesetPresetOwner(session.Ruleset, req.Preset.ID)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	pkg := rulesetPackage{Files: map[string]string{}, Media: map[string]string{}}
	for name, manifest := range session.Ruleset.Files {
		pkg.Files[name] = manifest.Source
	}
	for name, id := range session.Ruleset.Media {
		pkg.Media[name] = id
	}
	for name, id := range req.Media {
		if err := validateRulesetMediaPath(name); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		if existing, ok := pkg.Media[name]; ok && existing != id {
			fail(w, http.StatusConflict, "Ruleset media path уже занят")
			return
		}
		if !isCharacterArtworkAsset(session.Assets[id]) {
			fail(w, http.StatusBadRequest, "Media asset отсутствует")
			return
		}
		pkg.Media[name] = id
	}
	// Keep the user-authored source immutable. Validate the prospective
	// registry against a source-preserving view, but persist only the registry.
	candidate := cloneRulesetSnapshot(session.Ruleset)
	candidate.Registry.Presets[req.Preset.ID] = clonePresetDefinition(req.Preset)
	for path, assetID := range req.Media {
		if candidate.Media == nil {
			candidate.Media = map[string]string{}
		}
		candidate.Media[path] = assetID
	}
	for filename := range candidate.Files {
		pkg.Files[filename], err = RenderRulesetFileTOML(candidate, filename)
		if err != nil {
			break
		}
	}
	if err == nil {
		var rendered RulesetSnapshot
		rendered, err = compileRulesetPackage(pkg)
		if err == nil && !equalRulesetRegistries(rendered.Registry, candidate.Registry) {
			err = errors.New("rendered TOML changed unrelated ruleset definitions")
		}
	}
	var updates map[string]CharacterInstance
	if err == nil {
		updates, err = prepareDefinitionCharacters(session, candidate, session.CampaignDefinitions)
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	// Unknown stat/action values or invalid artwork are rejected by the normal
	// ruleset compilation + full candidate validation above.
	if s.stopping {
		fail(w, http.StatusServiceUnavailable, "Сервер останавливается")
		return
	}
	oldRuleset := cloneRulesetSnapshot(session.Ruleset)
	oldAssets := cloneAssetRegistry(session.Assets)
	oldCharacters := map[string]CharacterInstance{}
	oldRevision, oldCharRevision, oldDirty := session.RegistryRevision, session.CharacterRevision, s.dirty
	session.Ruleset = cloneRulesetSnapshot(candidate)
	for id, updated := range updates {
		oldCharacters[id] = cloneCharacterInstance(session.CharacterInstances[id])
		session.CharacterInstances[id] = updated
	}
	refreshAssetOrphans(session, time.Now())
	session.RegistryRevision++
	session.CharacterRevision++
	s.dirty = true
	if err = s.saveLocked(); err != nil {
		session.Ruleset = oldRuleset
		session.Assets = oldAssets
		for id, original := range oldCharacters {
			session.CharacterInstances[id] = original
		}
		session.RegistryRevision, session.CharacterRevision, s.dirty = oldRevision, oldCharRevision, oldDirty
		fail(w, http.StatusInternalServerError, "Ошибка сохранения шаблона")
		return
	}
	s.publishRegistryChanged(session)
	s.refreshCharacterWatches(session, nil, true)
	reply(w, map[string]any{"registryRevision": session.RegistryRevision, "artworkWarnings": rulesetArtworkWarnings(candidate, session.Assets)})
}
