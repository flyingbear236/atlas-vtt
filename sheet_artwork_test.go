package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A projection with unchanged inventory and stats must still refresh artwork.
// Exercise the real save handler and delta generation, not a mutated client fixture.
func TestSheetArtworkRefreshesCachedProjection(t *testing.T) {
	s, ss, gm := diagnosticFixture(t, diagnosticScale{"sheet-art", 1, 1, 2, 2, 1, 0})
	ss.Keys = map[string]string{"gm-key": gm.ID, "player-key": "player"}
	asset := Asset{ID: "gear-art", Kind: assetKindScene, RepresentationVersion: universalRepresentationVersion, Width: 256, Height: 256, RetentionPolicy: assetReclaimable}
	ss.Assets[asset.ID] = asset
	cid := "character-0000"
	project := func(previous *characterSnapshot) characterSnapshot {
		t.Helper()
		snapshot, err := buildCharacterSnapshot(ss, gm, cid, 1, false, previous)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	set := func(key, kind, id, artwork string, status int) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"kind": kind, "id": id, "assetId": artwork})
		req := httptest.NewRequest("POST", "/api/media-library/sheet-artwork?session="+ss.ID, strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+key)
		out := httptest.NewRecorder()
		s.setSheetArtwork(out, req)
		if out.Code != status {
			t.Fatalf("artwork %s: %d != %d: %s", id, out.Code, status, out.Body.String())
		}
	}
	before := project(nil)
	before.CharacterSequence = 1
	set("player-key", "item", "gear", asset.ID, 403)
	set("gm-key", "item", "gear", asset.ID, 200)
	after := project(&before)
	after.CharacterSequence = 2
	for _, item := range after.Instance.Inventory {
		if before.ItemDetails[item.ID].ArtworkAssetID != "" || after.ItemDetails[item.ID].ArtworkAssetID != asset.ID {
			t.Fatalf("stale item artwork for %s", item.ID)
		}
	}
	assertDeltaProjection(t, before, characterDeltaMessage{Revision: after.Revision, CharacterSequence: after.CharacterSequence, Changes: characterSnapshotChanges(before, after)}, after)
	// The effect UI uses an effect.-prefixed ID, while the definitions and grants use bare IDs.
	set("gm-key", "effect", "effect.item_bonus", asset.ID, 200)
	withEffect := project(&after)
	effects := withEffect.ItemDetails["item-0000"].Effects
	if len(effects) == 0 || effects[0].ArtworkAssetID != asset.ID {
		t.Fatal("effect artwork was not refreshed")
	}
	if !sheetArtworkVisibleTo(ss, ss.Members["player"], asset.ID) {
		t.Fatal("owner cannot access assigned artwork")
	}
	// Verify actual persisted data rather than only the in-memory state.
	raw, err := os.ReadFile(filepath.Join(s.root, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	var art struct{ ItemArtwork, EffectArtwork map[string]string }
	if err := json.Unmarshal(saved[ss.ID], &art); err != nil {
		t.Fatal(err)
	}
	if art.ItemArtwork["gear"] != asset.ID || art.EffectArtwork["item_bonus"] != asset.ID {
		t.Fatal("artwork was not persisted")
	}
	set("gm-key", "item", "gear", "", 200)
	cleared := project(&withEffect)
	if cleared.ItemDetails["item-0000"].ArtworkAssetID != "" {
		t.Fatal("removed artwork remains in projection")
	}
}
