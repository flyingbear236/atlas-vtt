package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Preset edits always rewrite one owned TOML block; no campaign override is
// created and no other source file is serialized.
type presetEditRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision"`
	PresetID string `json:"presetId"`
	Preset CharacterPresetDefinition `json:"preset"`
	ArtworkAssetID *string `json:"artworkAssetId,omitempty"`
}

func presetOwnerFile(snapshot RulesetSnapshot, presetID string) (string, error) {
	owner := ""
	for filename, manifest := range snapshot.Files {
		if slices.Contains(manifest.Presets, presetID) {
			if owner != "" { return "", fmt.Errorf("preset %q has ambiguous TOML ownership", presetID) }
			owner = filename
		}
	}
	if owner == "" { return "", fmt.Errorf("preset %q has no TOML owner", presetID) }
	return owner, nil
}

func canonicalPresetBlock(preset CharacterPresetDefinition) (string, error) {
	raw := presetTOMLDefinition{
		Name: preset.Name, Kind: preset.Kind, Artwork: preset.Artwork,
		Stats: make(map[string]any, len(preset.Stats)),
		Actions: append([]string(nil), preset.ActionIDs...),
	}
	for id, value := range preset.Stats { raw.Stats[id] = value.Interface() }
	doc := struct {
		Presets map[string]presetTOMLDefinition `toml:"presets"`
	}{Presets: map[string]presetTOMLDefinition{preset.ID: raw}}
	output, err := toml.Marshal(doc)
	if err != nil { return "", err }
	return string(output), nil
}

// replacePresetSourceBlock accepts only unambiguous, contiguous, simple
// [presets.id] table groups. Complex table syntax is left for manual editing.
func replacePresetSourceBlock(source, presetID, replacement string) (string, error) {
	mainHeader := "[presets." + presetID + "]"
	childPrefix := "[presets." + presetID + "."
	if strings.ContainsAny(presetID, `.[]"\\`) || strings.TrimSpace(presetID) != presetID {
		return "", errors.New("preset requires manual TOML editing: unsupported table key syntax")
	}
	type line struct { start, end int; value string }
	lines := make([]line, 0)
	for offset := 0; offset < len(source); {
		end := offset + strings.IndexByte(source[offset:], '\n') + 1
		if end <= offset { end = len(source) }
		lines = append(lines, line{offset, end, source[offset:end]})
		offset = end
	}
	start, end, found, closed := -1, len(source), false, false
	for _, item := range lines {
		trimmed := strings.TrimSpace(item.value)
		if !strings.HasPrefix(trimmed, "[") { continue }
		header := strings.TrimSpace(strings.SplitN(trimmed, "#", 2)[0])
		belongs := header == mainHeader || (strings.HasPrefix(header, childPrefix) && strings.HasSuffix(header, "]"))
		if belongs {
			if closed || (!found && header != mainHeader) {
				return "", errors.New("preset source range is ambiguous; edit TOML manually")
			}
			if !found { start, found = item.start, true }
		} else if found && !closed {
			end, closed = item.start, true
		}
	}
	if !found { return "", errors.New("preset source range was not found; edit TOML manually") }
	// Comments and whitespace directly before the next definition belong to
	// that definition and must survive byte-for-byte outside the splice.
	if closed {
		for i := len(lines)-1; i >= 0; i-- {
			if lines[i].end > end || lines[i].start >= end { continue }
			if lines[i].start < start { break }
			trimmed := strings.TrimSpace(lines[i].value)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") { end = lines[i].start; continue }
			break
		}
	}
	if start >= end { return "", errors.New("preset source range is empty or ambiguous") }
	if !strings.HasSuffix(replacement, "\n") { replacement += "\n" }
	return source[:start] + replacement + source[end:], nil
}

func (s *Server) applyPresetEdit(w http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(w, request.Body, MaxDefinitionDocumentBytes)
	var input presetEditRequest
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil { fail(w, http.StatusBadRequest, "Некорректные изменения preset"); return }
	s.mu.Lock()
	defer s.mu.Unlock()
	session, member := s.auth(request)
	if !memberIsGM(member) { fail(w, http.StatusForbidden, "Нужны права ведущего"); return }
	if input.ExpectedRevision != session.RegistryRevision {
		fail(w, http.StatusConflict, "Ruleset изменился, обновите редактор")
		return
	}
	if input.PresetID == "" || input.Preset.ID != input.PresetID {
		fail(w, http.StatusBadRequest, "Preset ID не совпадает")
		return
	}
	previous, ok := session.Ruleset.Registry.Presets[input.PresetID]
	if !ok { fail(w, http.StatusNotFound, "Шаблон "+input.PresetID+" отсутствует в текущем ruleset"); return }
	sources, err := RulesetSnapshotWithSources(session.Ruleset)
	if err != nil { fail(w, http.StatusConflict, err.Error()); return }
	owner, err := presetOwnerFile(sources, input.PresetID)
	if err != nil { fail(w, http.StatusConflict, err.Error()); return }
	preset := clonePresetDefinition(input.Preset)
	media := make(map[string]string, len(sources.Media)+1)
	for path, assetID := range sources.Media { media[path] = assetID }
	if input.ArtworkAssetID != nil {
		preset.Artwork = ""
		if *input.ArtworkAssetID != "" {
			asset, exists := session.Assets[*input.ArtworkAssetID]
			if !exists || !isCharacterArtworkAsset(asset) {
				fail(w, http.StatusBadRequest, "Изображение отсутствует в медиа кампании")
				return
			}
			for path, assetID := range media {
				if assetID == asset.ID { preset.Artwork = path; break }
			}
			if preset.Artwork == "" {
				preset.Artwork = "media/" + asset.ID + ".png"
				media[preset.Artwork] = asset.ID
			}
		}
	} else if preset.Artwork == "" && previous.Artwork != "" {
		// The caller sends the complete preset, including artwork.
	}
	if err := validateRulesetArtworkPath(preset.Artwork); err != nil { fail(w, http.StatusBadRequest, err.Error()); return }
	block, err := canonicalPresetBlock(preset)
	if err != nil { fail(w, http.StatusBadRequest, err.Error()); return }
	files := make(map[string]string, len(sources.Files))
	for name, manifest := range sources.Files { files[name] = manifest.Source }
	files[owner], err = replacePresetSourceBlock(files[owner], input.PresetID, block)
	if err != nil { fail(w, http.StatusConflict, err.Error()); return }
	candidate, err := CompileRulesetFiles(files)
	if err == nil {
		candidate.Media = media
		err = validateDefinitionState(session, candidate, session.CampaignDefinitions)
	}
	if err != nil { fail(w, http.StatusConflict, err.Error()); return }
	characters, err := prepareRulesetCharacters(session, candidate, session.CampaignDefinitions)
	if err != nil { fail(w, http.StatusConflict, err.Error()); return }
	oldRuleset, oldCharacters := session.Ruleset, session.CharacterInstances
	oldRegistryRevision, oldCharacterRevision, oldDirty := session.RegistryRevision, session.CharacterRevision, s.dirty
	session.Ruleset, session.CharacterInstances = candidate, characters
	session.RegistryRevision++
	session.CharacterRevision++
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		session.Ruleset, session.CharacterInstances = oldRuleset, oldCharacters
		session.RegistryRevision, session.CharacterRevision, s.dirty = oldRegistryRevision, oldCharacterRevision, oldDirty
		fail(w, http.StatusInternalServerError, "Ошибка сохранения preset")
		return
	}
	s.publishRegistryChanged(session)
	s.refreshCharacterWatches(session, nil, true)
	reply(w, map[string]any{"registryRevision": session.RegistryRevision, "owner": owner})
}
