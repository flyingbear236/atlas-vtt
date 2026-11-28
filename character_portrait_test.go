package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCharacterPortraitAssignmentAndProjection(t *testing.T) {
	s, ss, gm := diagnosticFixture(t, diagnosticScale{"portrait", 1, 1, 2, 0, 0, 0})
	id := "character-0000"
	player := ss.Members["player"]
	outsider := &Member{ID: "viewer", Role: "player"}
	ss.Members[outsider.ID] = outsider
	ss.Keys = map[string]string{"owner": player.ID, "viewer": outsider.ID, "gm-key": gm.ID}
	portrait := Asset{ID: "portrait-a", Kind: assetKindScene, RepresentationVersion: universalRepresentationVersion, Width: 600, Height: 1200, RetentionPolicy: assetReclaimable}
	private := portrait
	private.ID = "portrait-private"
	ss.Assets[portrait.ID], ss.Assets[private.ID] = portrait, private
	ss.MemberMedia = map[string]map[string]bool{player.ID: {portrait.ID: true}}
	set := func(key, asset, text string, keep bool) int {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"characterId": id, "assetId": asset, "text": text, "keepImage": keep})
		req := httptest.NewRequest("POST", "/api/media-library/character-artwork?session="+ss.ID, strings.NewReader(string(data)))
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		s.setCharacterPortrait(w, req)
		return w.Code
	}
	before, err := characterSnapshotForMember(ss, player, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if code := set("viewer", portrait.ID, "Not allowed", false); code != 403 {
		t.Fatalf("unowned character: %d", code)
	}
	if code := set("owner", private.ID, "Not allowed", false); code != 403 {
		t.Fatalf("private image: %d", code)
	}
	if code := set("owner", portrait.ID, "Public portrait", false); code != 200 {
		t.Fatalf("owner update: %d", code)
	}
	next, err := buildCharacterSnapshot(ss, player, id, 1, false, &before)
	if err != nil {
		t.Fatal(err)
	}
	if next.PortraitAssetID != portrait.ID || next.Instance.PortraitText != "Public portrait" || next.Effective.AvatarAssetID != before.Effective.AvatarAssetID {
		t.Fatal("portrait should update independently of avatar")
	}
	if avatarAssetVisibleTo(ss, outsider, portrait.ID) {
		t.Fatal("private portrait should not be directly visible to non-owner")
	}
	if code := set("owner", "", "Text only update", true); code != 200 || characterPortraitAsset(ss, ss.CharacterInstances[id]) != portrait.ID {
		t.Fatalf("keep image: %d", code)
	}
	if code := set("gm-key", "", "", false); code != 200 || characterPortraitAsset(ss, ss.CharacterInstances[id]) != "" {
		t.Fatalf("remove image: %d", code)
	}
}

func TestCharacterHoverVisibleTokenGrant(t *testing.T) {
	s, ss, gm := diagnosticFixture(t, diagnosticScale{"hover", 1, 1, 2, 0, 0, 0})
	id := "character-0000"
	scene := ss.Scenes["scene"]
	viewer := &Member{ID: "viewer", Role: "player"}
	ss.Members[viewer.ID] = viewer
	asset := Asset{ID: "portrait-a", Kind: assetKindScene, RepresentationVersion: universalRepresentationVersion, Width: 600, Height: 1200}
	ss.Assets[asset.ID] = asset
	instance := ss.CharacterInstances[id]
	instance.PortraitAssetID, instance.PortraitText = asset.ID, "Public"
	ss.CharacterInstances[id] = instance
	p := &peer{member: viewer, sceneID: scene.ID, floorID: firstFloorID(scene), region: &SceneRegion{Left: 0, Top: 0, Right: 500, Bottom: 500}, out: make(chan any, 1)}
	c := Command{SceneID: scene.ID, TokenID: id, Seq: 1}
	send := func() map[string]any { s.sendCharacterHover(ss, p, c); return (<-p.out).(map[string]any) }
	msg := send()
	if msg["text"] != "Public" {
		t.Fatal(msg)
	}
	media, ok := msg["media"].(map[string]any)
	if !ok {
		t.Fatal(msg)
	}
	grant, ok := media["grant"].(string)
	if !ok || !validObjectMediaGrant(ss, viewer, asset.ID, grant) || validObjectMediaGrant(ss, gm, asset.ID, grant) {
		t.Fatal("invalid member-scoped grant")
	}
	token := scene.Tokens[id]
	token.Hidden = true
	scene.Tokens[id] = token
	if msg = send(); msg["media"] != nil || msg["text"] != nil {
		t.Fatal("hidden token disclosed portrait")
	}
	token.Hidden = false
	scene.Tokens[id] = token
	p.region = &SceneRegion{Left: 1000, Top: 1000, Right: 1500, Bottom: 1500}
	if msg = send(); msg["media"] != nil {
		t.Fatal("unloaded token disclosed portrait")
	}
	p.region = &SceneRegion{Left: 0, Top: 0, Right: 500, Bottom: 500}
	instance.PortraitAssetID = ""
	avatar := "avatar-a"
	instance.AvatarAssetID = &avatar
	ss.CharacterInstances[id] = instance
	if msg = send(); msg["media"] != nil || msg["text"] != "Public" {
		t.Fatal("avatar must not be used as fallback")
	}
	scene.Published = false
	if msg = send(); msg["text"] != nil {
		t.Fatal("unpublished scene disclosed portrait")
	}
}
