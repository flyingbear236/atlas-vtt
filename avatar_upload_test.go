package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAvatarAssetSurvivesStartupValidation(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Avatar restart"})
	host.Close()
	asset, err := prepare(root, pipelinePNG(t, 32, 32), assetKindAvatar)
	if err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	session := server.sessions[gm["session"]]
	session.Assets[asset.ID] = asset
	instance := NewCharacterInstance("persistent-avatar", nil)
	instance.Name = "Persistent"
	instance.Persistent = true
	instance = WithAvatarOverride(instance, asset.ID)
	session.CharacterInstances[instance.ID] = instance
	refreshAssetOrphans(session, time.Now())
	server.dirty = true
	if err := server.saveLocked(); err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	server.mu.Unlock()
	restored, err := newServer(root)
	if err != nil {
		t.Fatal(err)
	}
	if restored.sessions[session.ID].Assets[asset.ID].Kind != assetKindAvatar {
		t.Fatal("avatar asset was not restored")
	}
}

func uploadAvatarRequest(t *testing.T, fixture characterCommandFixture, actor map[string]string, targetKey, targetID string, body []byte) (int, Asset, string) {
	t.Helper()
	query := url.Values{"session": {actor["session"]}, "kind": {assetKindAvatar}, targetKey: {targetID}}
	request, err := http.NewRequest(http.MethodPost, fixture.host.URL+"/api/upload?"+query.Encode(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+actor["key"])
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var asset Asset
	if response.StatusCode == http.StatusOK {
		if err := json.Unmarshal(encoded, &asset); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode, asset, string(encoded)
}

func getAvatar(t *testing.T, fixture characterCommandFixture, actor map[string]string, assetID string) int {
	t.Helper()
	query := url.Values{"session": {actor["session"]}}
	request, err := http.NewRequest(http.MethodGet, fixture.host.URL+"/api/asset/"+assetID+"/avatar.png?"+query.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+actor["key"])
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response.StatusCode
}

func TestAvatarUploadAssignmentSharingAndAuthorization(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	gmWS := dialRaw(t, fixture.host.URL, fixture.gm)
	read(t, gmWS, "campaignSnapshot")
	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "avatar-create", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["wolf1"], PresetID: "wolf"})
	firstID := characterIDFromACK(t, read(t, gmWS, "ack"))
	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "avatar-create", Seq: 2, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["red"], CharacterName: "Red"})
	secondID := characterIDFromACK(t, read(t, gmWS, "ack"))
	firstTokenImage := fixture.sceneA.Tokens[fixture.tokens["wolf1"]].Asset

	body := pipelinePNG(t, 96, 64)
	status, firstAsset, message := uploadAvatarRequest(t, fixture, fixture.alice, "characterId", firstID, body)
	if status != http.StatusOK {
		t.Fatalf("first avatar upload: %d %s", status, message)
	}
	status, secondAsset, message := uploadAvatarRequest(t, fixture, fixture.alice, "characterId", secondID, body)
	if status != http.StatusOK {
		t.Fatalf("shared avatar upload: %d %s", status, message)
	}
	if firstAsset.ID == "" || firstAsset.ID != secondAsset.ID || firstAsset.Kind != assetKindAvatar {
		t.Fatalf("avatar representation was not shared: %#v %#v", firstAsset, secondAsset)
	}
	fixture.server.mu.Lock()
	first := fixture.session.CharacterInstances[firstID]
	second := fixture.session.CharacterInstances[secondID]
	tokenImage := fixture.sceneA.Tokens[fixture.tokens["wolf1"]].Asset
	fixture.server.mu.Unlock()
	if first.AvatarAssetID == nil || second.AvatarAssetID == nil || *first.AvatarAssetID != firstAsset.ID || *second.AvatarAssetID != firstAsset.ID {
		t.Fatal("shared avatar was not assigned to both characters")
	}
	if tokenImage != firstTokenImage {
		t.Fatal("avatar assignment changed token artwork")
	}
	if status := getAvatar(t, fixture, fixture.bob, firstAsset.ID); status != http.StatusOK {
		t.Fatalf("co-owner cannot read avatar without scene context: %d", status)
	}
	if status := getAvatar(t, fixture, fixture.charlie, firstAsset.ID); status != http.StatusForbidden {
		t.Fatalf("unrelated player read avatar: %d", status)
	}
	entries, err := os.ReadDir(filepath.Join(fixture.server.root, "assets", firstAsset.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("avatar-only source was retained: %v", entries)
	}
}

func TestAvatarPresetOverlayAndPersistentGMAccess(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	fixture.server.mu.Lock()
	fixture.session.Ruleset.Metadata = RulesetMetadata{ID: "test", Name: "Test", Version: "1"}
	fixture.session.Ruleset.Registry.Presets["ruleset_hero"] = CharacterPresetDefinition{ID: "ruleset_hero", Name: "Ruleset hero", Stats: map[string]StatValue{}}
	fixture.server.dirty = true
	if err := fixture.server.saveLocked(); err != nil {
		fixture.server.mu.Unlock()
		t.Fatal(err)
	}
	fixture.server.mu.Unlock()

	status, presetAsset, message := uploadAvatarRequest(t, fixture, fixture.gm, "presetId", "ruleset_hero", pipelinePNG(t, 40, 50))
	if status != http.StatusOK {
		t.Fatalf("preset avatar upload: %d %s", status, message)
	}
	fixture.server.mu.Lock()
	overlay := fixture.session.CampaignDefinitions.Presets["ruleset_hero"]
	fixture.server.mu.Unlock()
	if overlay.ID != "ruleset_hero" || overlay.AvatarAssetID != presetAsset.ID {
		t.Fatalf("ruleset preset did not receive campaign overlay: %#v", overlay)
	}

	gmWS := dialRaw(t, fixture.host.URL, fixture.gm)
	read(t, gmWS, "campaignSnapshot")
	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "inherited-avatar", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["wolf1"], PresetID: "ruleset_hero"})
	if inheritedID := characterIDFromACK(t, read(t, gmWS, "ack")); inheritedID == "" {
		t.Fatal("inherited avatar character was not created")
	}
	if status := getAvatar(t, fixture, fixture.bob, presetAsset.ID); status != http.StatusOK {
		t.Fatalf("owner cannot read inherited avatar without scene context: %d", status)
	}
	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "persistent-avatar", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["lancelot"], CharacterName: "Persistent", Persistent: boolPointer(true)})
	characterID := characterIDFromACK(t, read(t, gmWS, "ack"))
	writeCharacterCommand(t, gmWS, Command{Type: "characterUnlink", Client: "persistent-avatar", Seq: 2, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["lancelot"]})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	jpegImage := image.NewGray(image.Rect(0, 0, 80, 100))
	var jpegBody bytes.Buffer
	if err := jpeg.Encode(&jpegBody, jpegImage, nil); err != nil {
		t.Fatal(err)
	}
	status, persistentAsset, message := uploadAvatarRequest(t, fixture, fixture.gm, "characterId", characterID, jpegBody.Bytes())
	if status != http.StatusOK {
		t.Fatalf("persistent avatar upload: %d %s", status, message)
	}
	if status := getAvatar(t, fixture, fixture.gm, persistentAsset.ID); status != http.StatusOK {
		t.Fatalf("GM cannot read persistent avatar without scene: %d", status)
	}
	if status, _, _ := uploadAvatarRequest(t, fixture, fixture.alice, "characterId", characterID, jpegBody.Bytes()); status != http.StatusForbidden {
		t.Fatalf("player uploaded unreferenced persistent avatar: %d", status)
	}
}

func TestAvatarUploadRechecksPermissionAfterWorker(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	gmWS := dialRaw(t, fixture.host.URL, fixture.gm)
	read(t, gmWS, "campaignSnapshot")
	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "avatar-revoke", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["red"], CharacterName: "Revoked"})
	characterID := characterIDFromACK(t, read(t, gmWS, "ack"))

	started, release := make(chan struct{}), make(chan struct{})
	imageWorkerObserver = func(*exec.Cmd) func() {
		close(started)
		<-release
		return nil
	}
	defer func() { imageWorkerObserver = nil }()
	type result struct {
		status  int
		asset   Asset
		message string
	}
	resultChannel := make(chan result, 1)
	go func() {
		status, asset, message := uploadAvatarRequest(t, fixture, fixture.alice, "characterId", characterID, pipelinePNG(t, 128, 128))
		resultChannel <- result{status: status, asset: asset, message: message}
	}()
	<-started
	fixture.server.mu.Lock()
	token := fixture.sceneA.Tokens[fixture.tokens["red"]]
	token.OwnerIDs = nil
	fixture.sceneA.Tokens[token.ID] = token
	fixture.server.mu.Unlock()
	close(release)
	resultValue := <-resultChannel
	if resultValue.status != http.StatusConflict {
		t.Fatalf("revoked upload status: %d %s", resultValue.status, resultValue.message)
	}
	fixture.server.mu.Lock()
	instance := fixture.session.CharacterInstances[characterID]
	assetCount := len(fixture.session.Assets)
	fixture.server.mu.Unlock()
	if instance.AvatarAssetID != nil || assetCount != 1 {
		t.Fatal("revoked upload assigned or registered avatar")
	}
	assertNoPartialAssets(t, fixture.server.root)
}

func TestAvatarUploadSaveFailureRollsBackAndCleansFiles(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	gmWS := dialRaw(t, fixture.host.URL, fixture.gm)
	read(t, gmWS, "campaignSnapshot")
	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "avatar-save", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["red"], CharacterName: "Rollback"})
	characterID := characterIDFromACK(t, read(t, gmWS, "ack"))
	blocker := filepath.Join(fixture.server.root, "sessions.json.tmp")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	status, _, message := uploadAvatarRequest(t, fixture, fixture.alice, "characterId", characterID, pipelinePNG(t, 72, 72))
	if status != http.StatusInternalServerError {
		t.Fatalf("save failure status: %d %s", status, message)
	}
	fixture.server.mu.Lock()
	instance := fixture.session.CharacterInstances[characterID]
	assetCount := len(fixture.session.Assets)
	fixture.server.mu.Unlock()
	if instance.AvatarAssetID != nil || assetCount != 1 {
		t.Fatal("failed save leaked avatar assignment or registry record")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	assertNoPartialAssets(t, fixture.server.root)
}

func TestAvatarHTTPSizeLimitCleansTemporaryInput(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	body := bytes.Repeat([]byte{1}, int(avatarUploadLimit)+1)
	status, _, message := uploadAvatarRequest(t, fixture, fixture.gm, "presetId", "wolf", body)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("avatar size status: %d %s", status, message)
	}
	assertNoPartialAssets(t, fixture.server.root)
}
