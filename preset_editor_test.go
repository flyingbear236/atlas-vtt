package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const presetEditorSource = `schema_version = 1
# keep this comment intact
[stats.hp]
name = "Hit points"
type = "integer"
kind = "resource"

[actions.bite]
name = "Bite"

[presets.wolf]
name = "Wolf"
kind = "monster"
actions = ["bite"]
artwork = "media/wolf.webp"
[presets.wolf.stats]
hp = 55

# unrelated definition comment is preserved
[presets.bear]
name = "Bear"
kind = "monster"
[presets.bear.stats]
hp = 100
`

func TestPresetSourceRewritePreservesOtherDefinitions(t *testing.T) {
	before := presetEditorSource
	start, end, err := presetSourceBounds(before, "wolf")
	if err != nil {
		t.Fatal(err)
	}
	prefix, suffix := before[:start], before[end:]
	preset := CharacterPresetDefinition{ID: "wolf", Name: "Wolf renamed", Kind: "monster", Stats: map[string]StatValue{"hp": IntegerStatValue(99)}, ActionIDs: []string{"bite"}, Artwork: "media/wolf-2.webp"}
	after, err := rewritePresetSource(before, preset)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(after, prefix) || !strings.HasSuffix(after, suffix) {
		t.Fatalf("changed foreign source bytes:\n%s", after)
	}
	next, err := CompileRulesetFiles(map[string]string{"owner.toml": after, "extra.toml": "schema_version = 1\n[stats.note]\ntype='string'\n"})
	if err != nil {
		t.Fatal(err)
	}
	if next.Registry.Presets["wolf"].Stats["hp"].Integer() != 99 || next.Registry.Presets["bear"].Stats["hp"].Integer() != 100 || next.Registry.Presets["wolf"].Artwork != "media/wolf-2.webp" {
		t.Fatal("rewrite changed wrong preset")
	}
	if next.Files["extra.toml"].Source != "schema_version = 1\n[stats.note]\ntype='string'\n" {
		t.Fatal("changed independent TOML")
	}
}

func TestPresetSourceAmbiguityIsRejected(t *testing.T) {
	for _, source := range []string{
		"schema_version=1\n[presets.wolf.stats]\nhp=1\n",
		"schema_version=1\n[presets.wolf]\nname='Wolf'\n[presets.bear]\nname='Bear'\n[presets.wolf.stats]\nhp=2\n",
		"schema_version=1\n[presets.wolf]\nname='Wolf'\n[presets.wolf]\nname='Repeated'\n",
		"schema_version=1\n[presets.wolf]\nname='Wolf'\ntext='''\n[presets.bear]\n'''\n",
	} {
		updated, err := rewritePresetSource(source, CharacterPresetDefinition{ID: "wolf", Name: "New"})
		if err == nil || updated != "" {
			t.Fatalf("unsafe range rewritten: %q, %v", updated, err)
		}
	}
}

func presetEditorHTTP(t *testing.T, host, path string, creds map[string]string, body any) (int, []byte) {
	t.Helper()
	data := []byte(nil)
	method := "GET"
	if body != nil {
		method = "POST"
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	q := url.Values{"session": {creds["session"]}}
	request, err := http.NewRequest(method, host+path+func() string {
		if strings.Contains(path, "?") {
			return "&"
		}
		return "?"
	}()+q.Encode(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+creds["key"])
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result bytes.Buffer
	if _, err = result.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, result.Bytes()
}

func TestPresetEditorGMAndAtomicSave(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Preset editor"})
	player := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})
	server.mu.Lock()
	session := server.sessions[gm["session"]]
	session.Assets["A"] = testArtworkAsset("A")
	server.dirty = true
	if err := server.saveLocked(); err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	server.mu.Unlock()
	files := map[string]string{"owner.toml": presetEditorSource, "extra.toml": "schema_version = 1\n[stats.note]\ntype='string'\n"}
	media := map[string]string{"media/wolf.webp": "A"}
	code, body := rulesetMediaHTTP(t, host.URL, "/api/definitions/ruleset/preview", gm, files, media, nil)
	if code != 200 {
		t.Fatalf("preview: %d %s", code, body)
	}
	var preview definitionPreviewResponse
	if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatal(err)
	}
	params := url.Values{"expectedRevision": {fmtInt(preview.RegistryRevision)}, "digest": {preview.Digest}, "key": {"preset-editor-setup"}}
	code, body = rulesetMediaHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, files, media, params)
	if code != 200 {
		t.Fatalf("install: %d %s", code, body)
	}
	code, _ = presetEditorHTTP(t, host.URL, "/api/definitions/preset?id=wolf", player, nil)
	if code != http.StatusForbidden {
		t.Fatalf("player read %d", code)
	}
	code, body = presetEditorHTTP(t, host.URL, "/api/definitions/preset?id=wolf", gm, nil)
	if code != 200 {
		t.Fatalf("get: %d %s", code, body)
	}
	var data presetEditorData
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Owner != "owner.toml" || data.Preset.Name != "Wolf" {
		t.Fatalf("bad data %#v", data)
	}
	server.mu.Lock()
	oldPreset := session.Ruleset.Registry.Presets["wolf"]
	linked := NewCharacterInstance("linked", &oldPreset)
	linked.Persistent = true
	linked.ResourceCurrent = map[string]StatValue{"hp": IntegerStatValue(20)}
	frozen := MaterializeCharacter(NewCharacterInstance("frozen", &oldPreset), &oldPreset, EffectiveRegistry{Stats: session.Ruleset.Registry.Stats})
	frozen.Persistent = true
	session.CharacterInstances["linked"] = linked
	session.CharacterInstances["frozen"] = frozen
	server.dirty = true
	if err := server.saveLocked(); err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	before := cloneRulesetSnapshot(session.Ruleset)
	revision := session.RegistryRevision
	server.mu.Unlock()
	data.Preset.Name = "Wolf changed"
	data.Preset.Stats["hp"] = IntegerStatValue(100)
	data.Preset.Artwork = "media/missing.webp"
	draft := presetEditorSaveRequest{Preset: data.Preset, ExpectedRevision: data.Revision}
	code, _ = presetEditorHTTP(t, host.URL, "/api/definitions/preset", player, draft)
	if code != 403 {
		t.Fatalf("player save %d", code)
	}
	wrong := draft
	wrong.ExpectedRevision = 0
	code, _ = presetEditorHTTP(t, host.URL, "/api/definitions/preset", gm, wrong)
	if code != 409 {
		t.Fatalf("stale draft accepted: %d", code)
	}
	server.mu.Lock()
	if !reflect.DeepEqual(before, session.Ruleset) {
		server.mu.Unlock()
		t.Fatal("draft changed committed ruleset")
	}
	server.mu.Unlock()
	blocker := filepath.Join(root, "sessions.json.tmp")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	code, _ = presetEditorHTTP(t, host.URL, "/api/definitions/preset", gm, draft)
	if code != 500 {
		t.Fatalf("expected rollback: %d", code)
	}
	server.mu.Lock()
	if !reflect.DeepEqual(before, session.Ruleset) || session.RegistryRevision != revision || session.CharacterInstances["linked"].Materialized != nil {
		server.mu.Unlock()
		t.Fatal("failed save leaked candidate state")
	}
	server.mu.Unlock()
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	code, body = presetEditorHTTP(t, host.URL, "/api/definitions/preset", gm, draft)
	if code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	server.mu.Lock()
	newRuleset := cloneRulesetSnapshot(session.Ruleset)
	newLinked := session.CharacterInstances["linked"]
	newFrozen := session.CharacterInstances["frozen"]
	server.mu.Unlock()
	if newRuleset.Registry.Presets["wolf"].Stats["hp"].Integer() != 100 || newLinked.Materialized != nil || newLinked.ResourceCurrent["hp"].Integer() != 20 || newFrozen.Materialized.ResourceInitial["hp"].Integer() != 55 {
		t.Fatal("save broke mechanical independence")
	}
	if newRuleset.Files["extra.toml"].Source != before.Files["extra.toml"].Source || newRuleset.Media["media/wolf.webp"] != "A" {
		t.Fatal("save modified unrelated sources or mapping")
	}
	if newRuleset.Files["owner.toml"].Source != presetEditorSource {
		t.Fatal("editor must not replace the user's original TOML source")
	}
	current, err := RenderRulesetFileTOML(newRuleset, "owner.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(current, "# keep this comment intact") || !strings.Contains(current, "hp = 100") || !strings.Contains(current, `name = "Wolf changed"`) {
		t.Fatalf("current view lost changes or comments:\n%s", current)
	}
	if warnings := rulesetArtworkWarnings(newRuleset, session.Assets); len(warnings) != 1 {
		t.Fatalf("missing artwork should warn: %#v", warnings)
	}
	code, _ = presetEditorHTTP(t, host.URL, "/api/definitions/preset", gm, draft)
	if code != 409 {
		t.Fatal("same stale revision overwrote newer ruleset")
	}
	reloaded := testServer(t, root)
	saved := reloaded.sessions[gm["session"]]
	if saved.Ruleset.Registry.Presets["wolf"].Name != "Wolf changed" || saved.CharacterInstances["linked"].ResourceCurrent["hp"].Integer() != 20 {
		t.Fatal("preset editor changes did not persist")
	}
}

func fmtInt(value uint64) string { return strconv.FormatUint(value, 10) }
