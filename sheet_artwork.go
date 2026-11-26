package main

import (
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"time"
)

// Artwork is presentation metadata of the ruleset item/effect definition, not
// part of an inventory instance or its executable expressions. No binary copies.
func sheetEffectDefinitionID(id string) string { return strings.TrimPrefix(id, "effect.") }

func (s *Server) setSheetArtwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind    string `json:"kind"`
		ID      string `json:"id"`
		AssetID string `json:"assetId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		fail(w, 400, "Некорректный артворк")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, m := s.auth(r)
	if !memberIsGM(m) {
		fail(w, 403, "Нужны права ведущего")
		return
	}
	if req.AssetID != "" && !isCharacterArtworkAsset(ss.Assets[req.AssetID]) {
		fail(w, 400, "Изображение недоступно")
		return
	}
	var dest *map[string]string
	switch req.Kind {
	case "item":
		if ss.Ruleset.Registry.Objects[req.ID] == nil {
			fail(w, 404, "Предмет не найден")
			return
		}
		dest = &ss.ItemArtwork
	case "effect":
		req.ID = sheetEffectDefinitionID(req.ID)
		if ss.Ruleset.Registry.Effects[req.ID] == nil {
			fail(w, 404, "Эффект не найден")
			return
		}
		dest = &ss.EffectArtwork
	default:
		fail(w, 400, "Неизвестный вид артворка")
		return
	}
	if (*dest)[req.ID] == req.AssetID {
		reply(w, map[string]any{"assetId": req.AssetID})
		return
	}
	old, assets, rev, dirty := *dest, cloneAssetRegistry(ss.Assets), ss.CharacterRevision, s.dirty
	next := maps.Clone(old)
	if next == nil {
		next = map[string]string{}
	}
	if req.AssetID == "" {
		delete(next, req.ID)
	} else {
		next[req.ID] = req.AssetID
	}
	*dest = next
	ss.CharacterRevision++
	refreshAssetOrphans(ss, time.Now())
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		*dest, ss.Assets, ss.CharacterRevision, s.dirty = old, assets, rev, dirty
		fail(w, 500, "Ошибка сохранения артворка")
		return
	}
	ss.sheetArtworkRevision++
	s.refreshCharacterWatches(ss, nil, true)
	reply(w, map[string]any{"assetId": req.AssetID})
}

// Resolve only media referenced by an accessible character. No catalog paths or
// unassigned GM resources are included in player responses.
func sheetArtworkVisibleTo(ss *Session, m *Member, assetID string) bool {
	if memberIsGM(m) {
		return true
	}
	if ss == nil || m == nil {
		return false
	}
	if len(ss.ItemArtwork) == 0 && len(ss.EffectArtwork) == 0 {
		return false
	}
	registry, err := MergeDefinitionRegistries(sessionRegistries(ss))
	if err != nil {
		return false
	}
	for cid, c := range ss.CharacterInstances {
		if !memberCanAccessCharacter(ss, m, cid) {
			continue
		}
		for _, item := range c.Inventory {
			if ss.ItemArtwork[item.ObjectID] == assetID {
				return true
			}
			if registry.mechanics != nil {
				for _, eid := range registry.mechanics.objects[item.ObjectID].effects {
					if ss.EffectArtwork[eid] == assetID {
						return true
					}
				}
			}
		}
		for _, eid := range c.EffectIDs {
			if ss.EffectArtwork[eid] == assetID {
				return true
			}
		}
	}
	return false
}
