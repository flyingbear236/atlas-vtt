package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCharacterMechanicalMaterializationLifecycle(t *testing.T) {
	preset := CharacterPresetDefinition{
		ID: "wolf", Name: "Wolf",
		Stats: map[string]StatValue{"hp": IntegerStatValue(11), "dex": IntegerStatValue(14)},
		ActionIDs: []string{"bite", "dash"},
	}
	linked := NewCharacterInstance("linked", &preset)
	if linked.Materialized != nil || linked.Name != "Wolf" {
		t.Fatalf("new preset character was unexpectedly materialized: %#v", linked)
	}
	linked.Name = "Personal name"
	linked.Persistent = true
	avatar := "own-avatar"
	linked.AvatarAssetID = &avatar
	if linked.Materialized != nil {
		t.Fatal("independent character fields materialized mechanics")
	}
	preset.Name = "New preset name"
	preset.Stats["hp"] = IntegerStatValue(12)
	preset.ActionIDs = append(preset.ActionIDs, "claw")
	if linked.Name != "Personal name" || EffectiveCharacterState(linked, &preset, EffectiveRegistry{Stats: map[string]StatDefinition{}}).Stats["hp"].Integer() != 12 {
		t.Fatal("name or linked mechanics incorrectly followed the preset")
	}
	if len(EffectiveCharacterState(linked, &preset, EffectiveRegistry{Stats: map[string]StatDefinition{}}).ActionIDs) != 3 {
		t.Fatal("linked character did not receive preset action")
	}

	edited := MaterializeCharacter(linked, &preset, EffectiveRegistry{Stats: map[string]StatDefinition{}})
	edited = WithStatOverride(edited, "hp", IntegerStatValue(3))
	if edited.Materialized.Stats["dex"].Integer() != 14 ||
		!reflect.DeepEqual(edited.Materialized.ActionIDs, []string{"bite", "dash", "claw"}) {
		t.Fatalf("incomplete mechanical snapshot: %#v", edited.Materialized)
	}
	if edited.Materialized.ResourceInitial != nil && len(edited.Materialized.ResourceInitial) != 0 {
		t.Fatal("unexpected resource initial values")
	}
	preset.Stats["dex"] = IntegerStatValue(99)
	preset.ActionIDs = []string{"bite"}
	effective := EffectiveCharacterState(edited, &preset, EffectiveRegistry{Stats: map[string]StatDefinition{}})
	if effective.Stats["dex"].Integer() != 14 || len(effective.ActionIDs) != 3 ||
		effective.AvatarAssetID != "own-avatar" || effective.Name != "Personal name" {
		t.Fatalf("materialized character changed with preset: %#v", effective)
	}
	edited = WithActionRemoved(edited, "bite")
	edited = WithActionAdded(edited, "bite")
	if !reflect.DeepEqual(edited.Materialized.ActionIDs, []string{"dash", "claw", "bite"}) {
		t.Fatal("action mutations did not change the independent action set")
	}
	bare := NewCharacterInstance("bare", nil)
	if bare.Materialized == nil || len(bare.Materialized.Stats) != 0 || bare.PresetID != "" {
		t.Fatalf("standalone character mechanics were not initialized: %#v", bare)
	}
}

func TestLegacyPartialOverridesBecomesWholeSnapshot(t *testing.T) {
	preset := CharacterPresetDefinition{
		ID: "wolf", Name: "Wolf",
		Stats: map[string]StatValue{"hp": IntegerStatValue(11), "dex": IntegerStatValue(14)},
		ActionIDs: []string{"bite", "dash"},
	}
	session := &Session{
		Ruleset: RulesetSnapshot{Registry: RulesetRegistry{
			Stats: map[string]StatDefinition{
				"hp": {ID: "hp", Type: StatTypeInteger},
				"dex": {ID: "dex", Type: StatTypeInteger},
			},
			Actions: map[string]ActionDefinition{
				"bite": {ID: "bite", Name: "Bite"},
				"dash": {ID: "dash", Name: "Dash"},
				"claw": {ID: "claw", Name: "Claw"},
			},
			Presets: map[string]CharacterPresetDefinition{"wolf": preset},
		}},
		CampaignDefinitions: emptyCampaignRegistry(),
		CharacterInstances: map[string]CharacterInstance{
			"custom": {
				ID: "custom", Name: "Retained", PresetID: "wolf", Persistent: true,
				StatOverrides: map[string]StatValue{"hp": IntegerStatValue(4)},
				AddedActionIDs: []string{"claw"}, RemovedActionIDs: []string{"bite"},
			},
			"linked": {ID: "linked", Name: "Independently named", PresetID: "wolf"},
			"empty": {ID: "empty", Name: "Empty"},
		},
	}
	changed, err := migrateLegacyCharacterMechanics(session)
	if err != nil || !changed {
		t.Fatalf("migration failed: changed=%v err=%v", changed, err)
	}
	custom := session.CharacterInstances["custom"]
	if custom.Materialized == nil || custom.Materialized.Stats["hp"].Integer() != 4 ||
		custom.Materialized.Stats["dex"].Integer() != 14 ||
		!reflect.DeepEqual(custom.Materialized.ActionIDs, []string{"dash", "claw"}) ||
		custom.PresetID != "wolf" || custom.Name != "Retained" || !custom.Persistent {
		t.Fatalf("legacy mechanics/independent fields lost: %#v", custom)
	}
	if len(custom.StatOverrides) != 0 || len(custom.AddedActionIDs) != 0 || len(custom.RemovedActionIDs) != 0 {
		t.Fatal("legacy overrides were left after migration")
	}
	if session.CharacterInstances["linked"].Materialized != nil || session.CharacterInstances["empty"].Materialized == nil {
		t.Fatal("linked or standalone legacy semantics were lost")
	}
}

func TestRulesetPresetRemovalProjectionGuardsGlobals(t *testing.T) {
	preset := CharacterPresetDefinition{ID: "wolf", Name: "Wolf", Stats: map[string]StatValue{"hp": IntegerStatValue(11)}, ActionIDs: []string{"bite"}}
	old := RulesetSnapshot{Registry: RulesetRegistry{
		Stats: map[string]StatDefinition{"hp": {ID: "hp", Type: StatTypeInteger}},
		Actions: map[string]ActionDefinition{"bite": {ID: "bite", Name: "Bite"}},
		Presets: map[string]CharacterPresetDefinition{"wolf": preset},
	}}
	materialized := MaterializeCharacter(NewCharacterInstance("custom", &preset), &preset, EffectiveRegistry{Stats: map[string]StatDefinition{}})
	materialized.Materialized.Stats["hp"] = IntegerStatValue(3)
	session := &Session{
		Ruleset: old, CampaignDefinitions: emptyCampaignRegistry(),
		CharacterInstances: map[string]CharacterInstance{
			"linked": NewCharacterInstance("linked", &preset),
			"custom": materialized,
		},
	}
	next := cloneRulesetSnapshot(old)
	delete(next.Registry.Presets, "wolf")
	updates, err := prepareDefinitionCharacters(session, next, session.CampaignDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	frozen := updates["linked"]
	if frozen.PresetID != "wolf" || frozen.Materialized == nil ||
		frozen.Materialized.Stats["hp"].Integer() != 11 || len(frozen.Materialized.ActionIDs) != 1 ||
		session.CharacterInstances["linked"].Materialized != nil ||
		session.CharacterInstances["custom"].Materialized.Stats["hp"].Integer() != 3 {
		t.Fatalf("projection changed committed characters or lost provenance: %#v", updates)
	}
	removedStat := cloneRulesetSnapshot(next)
	delete(removedStat.Registry.Stats, "hp")
	if _, err := prepareDefinitionCharacters(session, removedStat, session.CampaignDefinitions); err == nil || !strings.Contains(err.Error(), "stat") {
		t.Fatalf("used global stat removal was allowed: %v", err)
	}
	removedAction := cloneRulesetSnapshot(next)
	delete(removedAction.Registry.Actions, "bite")
	if _, err := prepareDefinitionCharacters(session, removedAction, session.CampaignDefinitions); err == nil || !strings.Contains(err.Error(), "action") {
		t.Fatalf("used global action removal was allowed: %v", err)
	}
	for id, updated := range updates {
		session.CharacterInstances[id] = updated
	}
	if err := ValidateCharacterInstance(session.CharacterInstances["linked"], func() EffectiveRegistry {
		r, _ := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: next.Registry})
		return r
	}()); err != nil {
		t.Fatalf("dangling preset provenance rejected: %v", err)
	}
}

func TestRulesetPresetRemovalSaveRollbackAndCommit(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Preset removal"})
	document := string(testRulesetTOML("delete-test", "1"))
	cut := strings.Index(document, "[presets.wolf]")
	if cut < 0 {
		t.Fatal("fixture missing preset")
	}
	installed := map[string]string{"ruleset.toml": document}
	reduced := map[string]string{"ruleset.toml": document[:cut]}
	base, err := CompileRulesetFiles(installed)
	if err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	session := server.sessions[gm["session"]]
	session.Ruleset = base
	preset := base.Registry.Presets["wolf"]
	linked := NewCharacterInstance("linked", &preset)
	linked.Name = "Unique name"
	linked.Persistent = true
	custom := MaterializeCharacter(NewCharacterInstance("custom", &preset), &preset, EffectiveRegistry{Stats: map[string]StatDefinition{}})
	custom.Materialized.Stats["hp"] = IntegerStatValue(7)
	session.CharacterInstances[linked.ID] = linked
	session.CharacterInstances[custom.ID] = custom
	session.RegistryRevision++
	server.dirty = true
	if err := server.saveLocked(); err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	beforeRevision := session.RegistryRevision
	server.mu.Unlock()

	status, body := rulesetPackageHTTP(t, host.URL, "/api/definitions/ruleset/preview", gm, reduced, nil)
	if status != http.StatusOK {
		t.Fatalf("preset removal preview rejected: %d %s", status, body)
	}
	var preview definitionPreviewResponse
	if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatal(err)
	}
	query := map[string][]string{
		"expectedRevision": {fmt.Sprint(preview.RegistryRevision)},
		"digest": {preview.Digest},
		"key": {"remove-preset"},
	}
	blocker := filepath.Join(root, "sessions.json.tmp")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	status, _ = rulesetPackageHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, reduced, query)
	if status != http.StatusInternalServerError {
		t.Fatalf("expected failed commit: %d", status)
	}
	server.mu.Lock()
	if session.RegistryRevision != beforeRevision || session.CharacterInstances["linked"].Materialized != nil ||
		session.Ruleset.Registry.Presets["wolf"].ID != "wolf" || session.DefinitionOperations["remove-preset"].Kind != "" {
		server.mu.Unlock()
		t.Fatal("failed ruleset save left a partial commit")
	}
	server.mu.Unlock()
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	status, body = rulesetPackageHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, reduced, query)
	if status != http.StatusOK {
		t.Fatalf("retry after rollback: %d %s", status, body)
	}
	server.mu.Lock()
	linked = session.CharacterInstances["linked"]
	custom = session.CharacterInstances["custom"]
	if linked.Materialized == nil || linked.Materialized.Stats["hp"].Integer() != 11 ||
		!reflect.DeepEqual(linked.Materialized.ActionIDs, []string{"bite"}) ||
		linked.PresetID != "wolf" || linked.Name != "Unique name" || !linked.Persistent ||
		custom.Materialized.Stats["hp"].Integer() != 7 || custom.PresetID != "wolf" {
		server.mu.Unlock()
		t.Fatalf("preset removal corrupted character mechanics: linked=%#v custom=%#v", linked, custom)
	}
	server.mu.Unlock()
}

func TestCharacterCommandsMaterializeOnlyForMechanics(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	fixture.server.mu.Lock()
	defer fixture.server.mu.Unlock()
	session := fixture.session
	gm := session.Members[session.Keys[fixture.gm["key"]]]
	create := func(tokenName string) string {
		mutation := newCharacterMutation(session)
		err := applyCharacterCommand(session, gm, fixture.sceneA.ID, Command{
			Type: "characterCreate", TokenID: fixture.tokens[tokenName], PresetID: "wolf",
		}, mutation)
		if err != nil {
			t.Fatal(err)
		}
		return mutation.resultCharacterID
	}
	edit := func(command Command) {
		mutation := newCharacterMutation(session)
		if err := applyCharacterCommand(session, gm, fixture.sceneA.ID, command, mutation); err != nil {
			mutation.rollback()
			t.Fatal(err)
		}
	}
	statID := create("wolf1")
	actionAddID := create("wolf2")
	actionRemoveID := create("wolf3")
	independentID := create("wolf4")
	edit(Command{Type: "characterStatSet", CharacterID: statID, StatID: "hp", StatValue: statValuePointer(IntegerStatValue(3))})
	edited := session.CharacterInstances[statID]
	if edited.Materialized == nil || edited.Materialized.Stats["dexterity"].Integer() != 15 ||
		!reflect.DeepEqual(edited.Materialized.ActionIDs, []string{"bite"}) {
		t.Fatalf("stat edit did not fully materialize: %#v", edited)
	}
	edit(Command{Type: "characterActionAdd", CharacterID: actionAddID, ActionID: "bite"})
	if session.CharacterInstances[actionAddID].Materialized == nil {
		t.Fatal("action add did not materialize")
	}
	edit(Command{Type: "characterActionRemove", CharacterID: actionRemoveID, ActionID: "bite"})
	removed := session.CharacterInstances[actionRemoveID]
	if removed.Materialized == nil || len(removed.Materialized.ActionIDs) != 0 ||
		removed.Materialized.Stats["hp"].Integer() != 11 {
		t.Fatalf("action removal did not materialize: %#v", removed)
	}
	edit(Command{Type: "characterNameSet", CharacterID: independentID, CharacterName: "Individual wolf"})
	edit(Command{Type: "characterSetPersistent", CharacterID: independentID, Persistent: boolPointer(true)})
	edit(Command{Type: "characterAvatarSet", CharacterID: independentID, AvatarAssetID: stringPointer("avatar")})
	token := fixture.sceneA.Tokens[fixture.tokens["wolf4"]]
	token.OwnerIDs = []string{session.Keys[fixture.alice["key"]]}
	fixture.sceneA.Tokens[token.ID] = token
	unchanged := session.CharacterInstances[independentID]
	if unchanged.Materialized != nil || unchanged.Name != "Individual wolf" ||
		!unchanged.Persistent || unchanged.AvatarAssetID == nil || *unchanged.AvatarAssetID != "avatar" {
		t.Fatalf("independent edits materialized mechanics: %#v", unchanged)
	}
}
