package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const artworkRuleset = `schema_version = 1
[presets.skeleton]
name = "Skeleton"
kind = "monster"
artwork = "media/skeleton.webp"
`

func TestRulesetArtworkTOMLOptionalAndMissingWarning(t *testing.T) {
	without, err := CompileRulesetFiles(map[string]string{"basic.toml": "schema_version = 1\n[presets.skeleton]\nname = 'Skeleton'\n"})
	if err != nil {
		t.Fatal(err)
	}
	if without.Registry.Presets["skeleton"].Artwork != "" {
		t.Fatal("absent artwork is not optional")
	}
	if got := resolvePresetArtwork(&CharacterPresetDefinition{ID: "skeleton"}, without, nil); got.Status != "placeholder" {
		t.Fatalf("no artwork: %#v", got)
	}
	snapshot, err := CompileRulesetFiles(map[string]string{"skeleton.toml": artworkRuleset})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Registry.Presets["skeleton"].Artwork != "media/skeleton.webp" {
		t.Fatal("artwork path not compiled")
	}
	if err = ValidateRulesetSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	warnings := rulesetArtworkWarnings(snapshot, map[string]Asset{})
	if len(warnings) != 1 || warnings[0].Path != "media/skeleton.webp" || warnings[0].PresetID != "skeleton" {
		t.Fatalf("missing artwork warning: %#v", warnings)
	}
	if got := resolvePresetArtwork(ptrPreset(snapshot.Registry.Presets["skeleton"]), snapshot, nil); got.Status != "missing" || got.Path != "media/skeleton.webp" {
		t.Fatalf("missing artwork: %#v", got)
	}
	for _, bad := range []string{"/var/a.png", "../media/a.png", "media/../a.png", "C:\\a.png", "https://example.org/a.png", "media/a.txt"} {
		if err := validateRulesetMediaPath(bad); err == nil {
			t.Fatalf("accepted unsafe artwork path %q", bad)
		}
	}
}
func ptrPreset(p CharacterPresetDefinition) *CharacterPresetDefinition { return &p }
func testArtworkAsset(id string) Asset {
	return Asset{ID: id, Kind: assetKindAvatar, Width: 64, Height: 64, RenderMode: renderModeBitmap, RetentionPolicy: assetReclaimable}
}

func TestRulesetArtworkResolutionMaterializationAndPresetRemoval(t *testing.T) {
	old, err := CompileRulesetFiles(map[string]string{"skeleton.toml": artworkRuleset})
	if err != nil {
		t.Fatal(err)
	}
	old.Media = map[string]string{"media/skeleton.webp": "A"}
	session := &Session{Ruleset: old, CampaignDefinitions: emptyCampaignRegistry(), Assets: map[string]Asset{"A": testArtworkAsset("A"), "B": testArtworkAsset("B"), "C": testArtworkAsset("C")}, CharacterInstances: map[string]CharacterInstance{}}
	preset := old.Registry.Presets["skeleton"]
	linked := NewCharacterInstance("linked", &preset)
	materialized := MaterializeCharacter(NewCharacterInstance("materialized", &preset), &preset, EffectiveRegistry{Stats: map[string]StatDefinition{}})
	session.CharacterInstances[linked.ID] = linked
	session.CharacterInstances[materialized.ID] = materialized
	for _, instance := range []CharacterInstance{linked, materialized} {
		if got := resolveCharacterArtwork(session, instance, &preset, old); got.AssetID != "A" || got.Status != "resolved" {
			t.Fatalf("artwork inheritance: %#v", got)
		}
	}
	next := cloneRulesetSnapshot(old)
	next.Media["media/skeleton.webp"] = "B"
	// Media candidate does not affect committed lookup until Save.
	if got := resolveCharacterArtwork(session, linked, &preset, session.Ruleset); got.AssetID != "A" {
		t.Fatalf("draft leaked: %#v", got)
	}
	session.Ruleset = next
	for _, instance := range []CharacterInstance{linked, materialized} {
		if got := resolveCharacterArtwork(session, instance, &preset, next); got.AssetID != "B" {
			t.Fatalf("new preset artwork not inherited: %#v", got)
		}
	}
	own := WithAvatarOverride(linked, "C")
	if own.Materialized != nil {
		t.Fatal("artwork override materialized mechanics")
	}
	if got := resolveCharacterArtwork(session, own, &preset, next); got.AssetID != "C" {
		t.Fatalf("character override not preferred: %#v", got)
	}
	session.CharacterInstances["own"] = own
	for _, id := range []string{"linked", "materialized"} {
		instance := session.CharacterInstances[id]
		if instance.Materialized != nil && id == "linked" {
			t.Fatal("linked character already materialized")
		}
	}
	removed, err := CompileRulesetFiles(map[string]string{"skeleton.toml": "schema_version = 1\n"})
	if err != nil {
		t.Fatal(err)
	}
	updates, err := prepareDefinitionCharacters(session, removed, emptyCampaignRegistry())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"linked", "materialized"} {
		instance := updates[id]
		if instance.AvatarAssetID == nil || *instance.AvatarAssetID != "B" || instance.Materialized == nil || instance.PresetID != "skeleton" {
			t.Fatalf("preset deletion lost resolved artwork/mechanics (%s): %#v", id, instance)
		}
	}
	if got := resolveCharacterArtwork(session, updates["linked"], nil, removed); got.AssetID != "B" {
		t.Fatalf("frozen artwork lost: %#v", got)
	}
	// Delete only logical mapping; TOML is still valid and preset becomes missing.
	unmapped := cloneRulesetSnapshot(next)
	unmapped.Media = map[string]string{}
	if err := ValidateRulesetSnapshot(unmapped); err != nil {
		t.Fatal(err)
	}
	if got := resolveCharacterArtwork(session, linked, &preset, unmapped); got.Status != "missing" || got.AssetID != "" {
		t.Fatalf("removed logical entry: %#v", got)
	}
	if got := resolveCharacterArtwork(session, own, &preset, unmapped); got.AssetID != "C" {
		t.Fatalf("override affected by removal: %#v", got)
	}
}

func rulesetMediaHTTP(t *testing.T, host, endpoint string, gm map[string]string, files map[string]string, media map[string]string, query url.Values) (int, []byte) {
	t.Helper()
	// Keep nil distinct from an explicit empty map: omitted/null preserves the
	// committed mapping, while {} intentionally removes every media entry.
	payload := struct {
		Files []rulesetUploadFile `json:"files"`
		Media map[string]string   `json:"media"`
	}{Media: media}
	for name, source := range files {
		payload.Files = append(payload.Files, rulesetUploadFile{Name: name, Content: source})
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if query == nil {
		query = url.Values{}
	}
	query.Set("session", gm["session"])
	request, err := http.NewRequest(http.MethodPost, host+endpoint+"?"+query.Encode(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+gm["key"])
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body bytes.Buffer
	if _, err = body.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body.Bytes()
}

func TestRulesetMediaCandidateApplyRollbackAndReuse(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Media candidate"})
	server.mu.Lock()
	session := server.sessions[gm["session"]]
	session.Assets["A"] = testArtworkAsset("A")
	session.Assets["B"] = testArtworkAsset("B")
	server.dirty = true
	if err := server.saveLocked(); err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	server.mu.Unlock()
	files := map[string]string{"skeleton.toml": artworkRuleset}
	media := map[string]string{"media/skeleton.webp": "A"}
	status, body := rulesetMediaHTTP(t, host.URL, "/api/definitions/ruleset/preview", gm, files, media, nil)
	if status != http.StatusOK {
		t.Fatalf("preview: %d %s", status, body)
	}
	var preview definitionPreviewResponse
	if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.ArtworkWarnings) != 0 || preview.Ruleset.Media["media/skeleton.webp"] != "A" {
		t.Fatalf("preview mapping: %#v", preview)
	}
	server.mu.Lock()
	if len(session.Ruleset.Media) != 0 || len(session.Ruleset.Files) != 0 || len(session.Assets) != 2 {
		server.mu.Unlock()
		t.Fatal("draft changed committed data")
	}
	server.mu.Unlock()
	apply := url.Values{"expectedRevision": {fmt.Sprint(preview.RegistryRevision)}, "digest": {preview.Digest}, "key": {"artwork-install"}}
	status, body = rulesetMediaHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, files, media, apply)
	if status != http.StatusOK {
		t.Fatalf("apply: %d %s", status, body)
	}
	server.mu.Lock()
	if session.Ruleset.Media["media/skeleton.webp"] != "A" || len(session.Assets) != 2 {
		server.mu.Unlock()
		t.Fatal("mapping commit duplicated an asset")
	}
	preset := session.Ruleset.Registry.Presets["skeleton"]
	linked := NewCharacterInstance("linked", &preset)
	linked.Persistent = true
	session.CharacterInstances["linked"] = linked
	server.dirty = true
	if err := server.saveLocked(); err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	before := cloneRulesetSnapshot(session.Ruleset)
	revision := session.RegistryRevision
	server.mu.Unlock()

	modified := map[string]string{"skeleton.toml": strings.Replace(artworkRuleset, "Skeleton", "Skeleton changed", 1)}
	mapping := map[string]string{"media/skeleton.webp": "B"}
	status, body = rulesetMediaHTTP(t, host.URL, "/api/definitions/ruleset/preview", gm, modified, mapping, nil)
	if status != http.StatusOK {
		t.Fatalf("changed preview: %d %s", status, body)
	}
	if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	if !reflect.DeepEqual(before, session.Ruleset) || session.CharacterInstances["linked"].Materialized != nil {
		server.mu.Unlock()
		t.Fatal("preview changed committed ruleset or character")
	}
	server.mu.Unlock()
	blocker := filepath.Join(root, "sessions.json.tmp")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	changedApply := url.Values{"expectedRevision": {fmt.Sprint(revision)}, "digest": {preview.Digest}, "key": {"artwork-fail"}}
	status, _ = rulesetMediaHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, modified, mapping, changedApply)
	if status != http.StatusInternalServerError {
		t.Fatalf("save should fail, got %d", status)
	}
	server.mu.Lock()
	if !reflect.DeepEqual(before, session.Ruleset) || session.CharacterInstances["linked"].Materialized != nil || session.RegistryRevision != revision {
		server.mu.Unlock()
		t.Fatal("save failed but state changed")
	}
	server.mu.Unlock()
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	status, body = rulesetMediaHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, modified, mapping, changedApply)
	if status != http.StatusOK {
		t.Fatalf("retry: %d %s", status, body)
	}
	server.mu.Lock()
	if session.Ruleset.Media["media/skeleton.webp"] != "B" || session.CharacterInstances["linked"].Materialized != nil || len(session.Assets) != 2 {
		server.mu.Unlock()
		t.Fatal("candidate save changed mechanics or duplicated blobs")
	}
	server.mu.Unlock()

	// Candidate deleting the mapping retains TOML; it is valid with a warning.
	status, body = rulesetMediaHTTP(t, host.URL, "/api/definitions/ruleset/preview", gm, modified, map[string]string{}, nil)
	if status != http.StatusOK {
		t.Fatalf("media deletion preview: %d %s", status, body)
	}
	if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.ArtworkWarnings) != 1 {
		t.Fatalf("missing artwork should warn: %#v", preview.ArtworkWarnings)
	}
	server.mu.Lock()
	if session.Ruleset.Media["media/skeleton.webp"] != "B" {
		server.mu.Unlock()
		t.Fatal("canceled draft changed committed mapping")
	}
	server.mu.Unlock()
}

func TestRulesetMediaPathAndAssetValidation(t *testing.T) {
	existing := map[string]string{"media/skeleton.webp": "A", "media/skeleton-2.webp": "B"}
	if got := suggestMediaPath("skeleton.webp", existing); got != "media/skeleton-3.webp" {
		t.Fatalf("unique path: %s", got)
	}
	session := &Session{Assets: map[string]Asset{"A": testArtworkAsset("A")}}
	pkg := rulesetPackage{Files: map[string]string{"ruleset.toml": artworkRuleset}, Media: map[string]string{"media/skeleton.webp": "A"}}
	snapshot, err := compileRulesetPackage(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRulesetMediaAssets(session, snapshot); err != nil {
		t.Fatal(err)
	}
	pkg.Media["media/skeleton.webp"] = "absent"
	snapshot, err = compileRulesetPackage(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRulesetMediaAssets(session, snapshot); err == nil {
		t.Fatal("nonexistent asset accepted")
	}
	if rulesetPackageDigest(rulesetPackage{Files: pkg.Files, Media: map[string]string{"media/a.png": "A"}}) == rulesetPackageDigest(rulesetPackage{Files: pkg.Files, Media: map[string]string{"media/a.png": "B"}}) {
		t.Fatal("digest excludes media mapping")
	}
}

func TestRulesetPresetDeleteAtomicSaveFreezesArtwork(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Remove preset"})
	session := server.sessions[gm["session"]]
	server.mu.Lock()
	session.Assets["A"] = testArtworkAsset("A")
	server.dirty = true
	if err := server.saveLocked(); err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	server.mu.Unlock()
	files := map[string]string{"source.toml": artworkRuleset}
	media := map[string]string{"media/skeleton.webp": "A"}
	status, body := rulesetMediaHTTP(t, host.URL, "/api/definitions/ruleset/preview", gm, files, media, nil)
	if status != http.StatusOK {
		t.Fatalf("preview install: %d %s", status, body)
	}
	var preview definitionPreviewResponse
	if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatal(err)
	}
	query := url.Values{"expectedRevision": {fmt.Sprint(preview.RegistryRevision)}, "digest": {preview.Digest}, "key": {"install-for-delete"}}
	status, body = rulesetMediaHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, files, media, query)
	if status != http.StatusOK {
		t.Fatalf("install: %d %s", status, body)
	}
	server.mu.Lock()
	preset := session.Ruleset.Registry.Presets["skeleton"]
	linked := NewCharacterInstance("linked", &preset)
	linked.Persistent = true
	materialized := MaterializeCharacter(NewCharacterInstance("frozen", &preset), &preset, EffectiveRegistry{})
	materialized.Persistent = true
	session.CharacterInstances[linked.ID] = linked
	session.CharacterInstances[materialized.ID] = materialized
	server.dirty = true
	if err := server.saveLocked(); err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	original := cloneRulesetSnapshot(session.Ruleset)
	revision := session.RegistryRevision
	server.mu.Unlock()

	empty := map[string]string{"source.toml": "schema_version = 1\n"}
	removedMedia := map[string]string{}
	status, body = rulesetMediaHTTP(t, host.URL, "/api/definitions/ruleset/preview", gm, empty, removedMedia, nil)
	if status != http.StatusOK {
		t.Fatalf("preview removal: %d %s", status, body)
	}
	if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatal(err)
	}
	remove := url.Values{"expectedRevision": {fmt.Sprint(revision)}, "digest": {preview.Digest}, "key": {"remove-preset"}}
	blocker := filepath.Join(root, "sessions.json.tmp")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	status, _ = rulesetMediaHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, empty, removedMedia, remove)
	if status != http.StatusInternalServerError {
		t.Fatalf("expected rollback, status=%d", status)
	}
	server.mu.Lock()
	if !reflect.DeepEqual(original, session.Ruleset) || session.CharacterInstances["linked"].AvatarAssetID != nil || session.CharacterInstances["linked"].Materialized != nil || session.CharacterInstances["frozen"].AvatarAssetID != nil {
		server.mu.Unlock()
		t.Fatal("preset removal save failure changed committed data")
	}
	server.mu.Unlock()
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	status, body = rulesetMediaHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, empty, removedMedia, remove)
	if status != http.StatusOK {
		t.Fatalf("retry preset removal: %d %s", status, body)
	}
	server.mu.Lock()
	for _, id := range []string{"linked", "frozen"} {
		instance := session.CharacterInstances[id]
		if instance.AvatarAssetID == nil || *instance.AvatarAssetID != "A" || instance.Materialized == nil || instance.PresetID != "skeleton" {
			server.mu.Unlock()
			t.Fatalf("preset removal broke character %s: %#v", id, instance)
		}
	}
	if session.Ruleset.Registry.Presets["skeleton"].ID != "" || len(session.Ruleset.Media) != 0 || session.Assets["A"].OrphanSince != nil {
		server.mu.Unlock()
		t.Fatal("frozen artwork no longer retained")
	}
	server.mu.Unlock()
}
