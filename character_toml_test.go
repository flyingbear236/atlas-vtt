package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func portableTestRegistry() EffectiveRegistry {
	return EffectiveRegistry{
		Stats: map[string]StatDefinition{
			"strength": {ID: "strength", Name: "Strength", Type: StatTypeInteger},
			"hp":       {ID: "hp", Name: "HP", Type: StatTypeInteger, Kind: StatKindResource},
		},
		Actions: map[string]ActionDefinition{"attack": {ID: "attack", Name: "Attack"}},
		Presets: map[string]CharacterPresetDefinition{
			"goblin": {ID: "goblin", Name: "Goblin", Stats: map[string]StatValue{"strength": IntegerStatValue(5), "hp": IntegerStatValue(7)}, ActionIDs: []string{"attack"}},
		},
	}
}

func TestPortableCharacterTOMLRoundTrip(t *testing.T) {
	registry := portableTestRegistry()
	instance := CharacterInstance{ID: "goblin-a12", Name: "Green goblin", PresetID: "goblin", Persistent: true, ArtworkPath: "media/goblin.png",
		Materialized:    &CharacterMechanicalState{Stats: map[string]StatValue{"strength": IntegerStatValue(6)}, ResourceInitial: map[string]StatValue{"hp": IntegerStatValue(7)}, ActionIDs: []string{"attack"}},
		ResourceCurrent: map[string]StatValue{"hp": IntegerStatValue(4)}}
	data, err := EncodeCharacterTOML(instance, map[string]string{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"[character.resource_current]", "hp = 4", "materialized = true", "media/goblin.png"} {
		if !strings.Contains(string(data), needle) {
			t.Fatalf("missing %q in %s", needle, data)
		}
	}
	got, err := DecodeCharacterTOML(data, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != instance.ID || got.ArtworkPath != instance.ArtworkPath || got.Materialized.Stats["strength"].Integer() != 6 || got.ResourceCurrent["hp"].Integer() != 4 || got.Materialized.ResourceInitial["hp"].Integer() != 7 || !reflect.DeepEqual(got.Materialized.ActionIDs, instance.Materialized.ActionIDs) {
		t.Fatalf("round-trip mismatch: %#v", got)
	}
}

func TestPortableCharacterTOMLInheritanceAndPlaceholder(t *testing.T) {
	registry := portableTestRegistry()
	instance := CharacterInstance{ID: "goblin", Name: "Goblin", PresetID: "goblin", Persistent: true, ResourceCurrent: map[string]StatValue{"hp": IntegerStatValue(4)}}
	data, err := EncodeCharacterTOML(instance, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCharacterTOML(data, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	if got.Materialized != nil || got.AvatarAssetID != nil {
		t.Fatalf("inheritance lost: %#v", got)
	}
	preset := registry.Presets["goblin"]
	preset.Stats["strength"] = IntegerStatValue(10)
	effective := EffectiveCharacterState(got, &preset, registry)
	if effective.Stats["strength"].Integer() != 10 || effective.Resources["hp"].Current.Integer() != 4 {
		t.Fatal("inherited mechanics not preserved")
	}
	empty := ""
	instance.AvatarAssetID = &empty
	data, err = EncodeCharacterTOML(instance, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	got, err = DecodeCharacterTOML(data, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	if got.AvatarAssetID == nil || *got.AvatarAssetID != "" {
		t.Fatal("explicit placeholder lost")
	}
	if _, err = DecodeCharacterTOML(append(data, []byte("\n[unexpected]\nvalue = 1\n")...), nil, registry); err == nil {
		t.Fatal("unknown TOML table accepted")
	}
}

func TestPortableCharacterTOMLSourcePreservation(t *testing.T) {
	registry := portableTestRegistry()
	instance := CharacterInstance{ID: "goblin", Name: "Goblin", PresetID: "goblin", Persistent: true, ResourceCurrent: map[string]StatValue{"hp": IntegerStatValue(4)}}
	session := &Session{CharacterInstances: map[string]CharacterInstance{"goblin": instance},
		CampaignDefinitions: CampaignRegistry{Stats: registry.Stats, Actions: registry.Actions, Presets: registry.Presets},
		CharacterSources:    map[string]CharacterTOMLSource{"goblin": {FileName: "characters/goblin.toml", Source: "schema_version = 1\n# authored note\n\n[character]\nid = 'goblin'\nname = 'Goblin'\npreset_id = 'goblin'\npersistent = true\nmaterialized = false\n\n[character.resource_current]\nhp = 7 # damage\n"}}}
	data, err := CurrentCharacterTOML(session, "goblin")
	if err != nil {
		t.Fatal(err)
	}
	expected := "schema_version = 1\n# authored note\n\n[character]\nid = 'goblin'\nname = 'Goblin'\npreset_id = 'goblin'\npersistent = true\nmaterialized = false\n\n[character.resource_current]\nhp = 4 # damage\n"
	if string(data) != expected {
		t.Fatalf("unexpected diff:\n%s", data)
	}
	session.CharacterInstances["goblin"] = CharacterInstance{ID: "goblin", Name: "Goblin", PresetID: "goblin", Persistent: true, ResourceCurrent: map[string]StatValue{"hp": IntegerStatValue(7)}}
	data, err = CurrentCharacterTOML(session, "goblin")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != session.CharacterSources["goblin"].Source {
		t.Fatalf("unchanged original rewritten: %q", data)
	}
}

func TestPortableCharacterIDsAndArtwork(t *testing.T) {
	values := map[string]CharacterInstance{"keep": {ID: "keep", Persistent: true}, "temp": {ID: "temp"}}
	if got := PortableCharacterIDs(values); !reflect.DeepEqual(got, []string{"keep"}) {
		t.Fatal(got)
	}
	if CharacterTOMLFileName("/abc") == CharacterTOMLFileName("abc") || !strings.HasPrefix(CharacterTOMLFileName("/abc"), "characters/") {
		t.Fatal("unsafe generated path")
	}
	session := &Session{Ruleset: RulesetSnapshot{Media: map[string]string{"media/green.png": "A"}}, Assets: map[string]Asset{"A": {ID: "A", Kind: assetKindAvatar}}}
	if r := resolveCharacterArtwork(session, CharacterInstance{ArtworkPath: "media/green.png"}, nil, session.Ruleset); r.AssetID != "A" {
		t.Fatal(r)
	}
	if r := resolveCharacterArtwork(session, CharacterInstance{ArtworkPath: "media/missing.png"}, nil, session.Ruleset); r.Status != "missing" {
		t.Fatal(r)
	}
}

func TestAttachRulesetMediaPath(t *testing.T) {
	server := testServer(t, t.TempDir())
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Media registry"})
	server.mu.Lock()
	session := server.sessions[gm["session"]]
	session.Assets["imageA"] = testArtworkAsset("imageA")
	if err := server.saveLocked(); err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	server.mu.Unlock()
	payload, _ := json.Marshal(map[string]string{"assetId": "imageA", "filename": "goblin.webp"})
	req, err := http.NewRequest(http.MethodPost, host.URL+"/api/definitions/ruleset/media/attach?session="+gm["session"], bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gm["key"])
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("media attach status %d", response.StatusCode)
	}
	var result struct {
		Path             string `json:"path"`
		RegistryRevision uint64 `json:"registryRevision"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Path != "media/goblin.png" {
		t.Fatalf("expected decoded png path, got %s", result.Path)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.sessions[gm["session"]].Ruleset.Media[result.Path] != "imageA" {
		t.Fatal("attachment not saved in ruleset media")
	}
}
