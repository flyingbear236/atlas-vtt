package main

import (
	"net/http"
	"sort"
	"strconv"
)

const (
	defaultCharacterCatalogPage = 50
	maxCharacterCatalogPage     = 100
)

type characterSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	PresetID string `json:"presetId,omitempty"`
}

type presetSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

type actionSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type characterCatalogResponse struct {
	Kind              string             `json:"kind"`
	CharacterRevision uint64             `json:"characterRevision"`
	RegistryRevision  uint64             `json:"registryRevision"`
	Characters        []characterSummary `json:"characters,omitempty"`
	Presets           []presetSummary    `json:"presets,omitempty"`
	Actions           []actionSummary    `json:"actions,omitempty"`
	NextCursor        string             `json:"nextCursor,omitempty"`
}

func catalogPage(ids []string, cursor string, limit int) ([]string, string) {
	sort.Strings(ids)
	start := sort.SearchStrings(ids, cursor)
	for start < len(ids) && ids[start] <= cursor {
		start++
	}
	end := min(len(ids), start+limit)
	next := ""
	if end < len(ids) && end > start {
		next = ids[end-1]
	}
	return ids[start:end], next
}

func characterCatalogLimit(request *http.Request) int {
	limit, err := strconv.Atoi(request.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		return defaultCharacterCatalogPage
	}
	return min(limit, maxCharacterCatalogPage)
}

func (s *Server) readCharacterCatalog(w http.ResponseWriter, request *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, member := s.auth(request)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	kind := request.URL.Query().Get("kind")
	response := characterCatalogResponse{Kind: kind, CharacterRevision: session.CharacterRevision, RegistryRevision: session.RegistryRevision}
	limit, cursor := characterCatalogLimit(request), request.URL.Query().Get("cursor")
	switch kind {
	case "roster":
		ids := make([]string, 0, len(session.CharacterInstances))
		for characterID, instance := range session.CharacterInstances {
			if instance.Persistent {
				ids = append(ids, characterID)
			}
		}
		page, next := catalogPage(ids, cursor, limit)
		response.Characters = make([]characterSummary, 0, len(page))
		for _, characterID := range page {
			instance := session.CharacterInstances[characterID]
			response.Characters = append(response.Characters, characterSummary{ID: instance.ID, Name: instance.Name, PresetID: instance.PresetID})
		}
		response.NextCursor = next
	case "presets":
		registry, err := MergeDefinitionRegistries(sessionRegistries(session))
		if err != nil {
			fail(w, http.StatusInternalServerError, "Registry повреждён")
			return
		}
		ids := make([]string, 0, len(registry.Presets))
		for presetID := range registry.Presets {
			ids = append(ids, presetID)
		}
		page, next := catalogPage(ids, cursor, limit)
		response.Presets = make([]presetSummary, 0, len(page))
		for _, presetID := range page {
			preset := registry.Presets[presetID]
			response.Presets = append(response.Presets, presetSummary{ID: preset.ID, Name: preset.Name, Kind: preset.Kind})
		}
		response.NextCursor = next
	case "actions":
		registry, err := MergeDefinitionRegistries(sessionRegistries(session))
		if err != nil {
			fail(w, http.StatusInternalServerError, "Registry повреждён")
			return
		}
		ids := make([]string, 0, len(registry.Actions))
		for actionID := range registry.Actions {
			ids = append(ids, actionID)
		}
		page, next := catalogPage(ids, cursor, limit)
		response.Actions = make([]actionSummary, 0, len(page))
		for _, actionID := range page {
			action := registry.Actions[actionID]
			response.Actions = append(response.Actions, actionSummary{ID: action.ID, Name: action.Name})
		}
		response.NextCursor = next
	default:
		fail(w, http.StatusBadRequest, "Неизвестный каталог персонажей")
		return
	}
	reply(w, response)
}
