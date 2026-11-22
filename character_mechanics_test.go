package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

const inventoryRules = `
[stat.basic.strength]
default = 14
[stat.hp]
resource = true
default = { formula = "80 + stat.basic.strength" }

[action.weapon.attack]
[action.weapon.attack.hit]
roll = "1d20"
[action.weapon.attack.damage]
roll = "dice(source.damage) + source.grip.bonus"

[effect.ring_strength]
target = "stat.basic.strength"
formula = "target + 2"
[effect.direct]
target = "stat.basic.strength"
formula = "target + actor.trained"
[effect.runic_accuracy]
target = "action.weapon.attack.hit.roll"
scope = "source"
formula = "target + 1"
[effect.broken]
target = "stat.basic.strength"
formula = "target / 0"

[object.ring]
effects = ["effect.ring_strength"]
[object.runic_sword]
effects = ["effect.runic_accuracy"]
actions = ["action.weapon.attack", "action.weapon.attack.hit"]
damage = "1d8"
grip.bonus = 2
[object.sword]
actions = ["action.weapon.attack"]
damage = "1d6"
grip.bonus = 0
[object.broken]
effects = ["effect.broken"]
`

const inventoryHero = `
# Hero declaration
[character.hero]
name = "Hero"
trained = 1
effects = ["effect.direct"]
actions = ["action.weapon.attack.hit"]

[character.hero.stat.basic.strength]
base = 15
[character.hero.stat.hp]
current = 85

[[character.hero.inventory]]
id = "ring-1"
object = "object.ring"
equipped = true

[[character.hero.inventory]]
id = "ring-2"
object = "object.ring"
equipped = false

[[character.hero.inventory]]
id = "runic-1"
object = "object.runic_sword"
equipped = true
grip.bonus = 3

[[character.hero.inventory]]
id = "runic-2"
object = "object.runic_sword"

[[character.hero.inventory]]
id = "steel"
object = "object.sword"
equipped = true

[[character.hero.inventory]]
id = "broken"
object = "object.broken"
`

func sessionEffectiveHero(t *testing.T, session *Session, expected int64) (EffectiveRegistry, EffectiveCharacter) {
	t.Helper()
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		t.Fatal(err)
	}
	instance := session.CharacterInstances["hero"]
	state := mustEffectiveCharacter(t, instance, nil, registry)
	if instance.ID != "hero" || !instance.Persistent || len(instance.Inventory) != 6 || state.StatValues["stat.basic.strength.base"] != int64(15) ||
		state.Stats["basic.strength"] != IntegerStatValue(expected) || state.Resources["hp"] != (EffectiveResource{Initial: IntegerStatValue(80 + expected), Current: IntegerStatValue(85)}) {
		t.Fatalf("unexpected hero: %+v, stats %+v, resources %+v", instance, state.StatValues, state.Resources)
	}
	return registry, state
}

func TestRulesetV04InventoryCommandsAndRestart(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Inventory"})
	player := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})
	files := map[string]string{"rules.toml": inventoryRules, "hero.toml": inventoryHero}
	status, body := rulesetPackageHTTP(t, host.URL, "/api/definitions/ruleset/preview", gm, files, nil)
	if status != http.StatusOK {
		t.Fatalf("preview %d: %s", status, body)
	}
	var preview definitionPreviewResponse
	if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatal(err)
	}
	values := url.Values{"expectedRevision": {fmt.Sprint(preview.RegistryRevision)}, "digest": {preview.Digest}, "key": {"inventory"}}
	status, body = rulesetPackageHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, files, values)
	if status != http.StatusOK {
		t.Fatalf("apply %d: %s", status, body)
	}
	sessionEffectiveHero(t, server.sessions[gm["session"]], 18)
	gmWS := dialRaw(t, host.URL, gm)
	defer gmWS.Close()
	read(t, gmWS, "campaignSnapshot")
	playerWS := dialRaw(t, host.URL, player)
	defer playerWS.Close()
	read(t, playerWS, "campaignSnapshot")
	cmd := Command{Type: "characterItemEquip", Client: "equip", Seq: 1, CharacterID: "hero", ItemID: "ring-2", Equipped: boolPointer(true)}
	writeCharacterCommand(t, playerWS, cmd)
	if err := ackError(t, read(t, playerWS, "ack")); !strings.Contains(err, "access denied") {
		t.Fatalf("unauthorized equipment change: %s", err)
	}
	writeCharacterCommand(t, gmWS, cmd)
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	writeCharacterCommand(t, gmWS, cmd)
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	server.mu.Lock()
	sessionEffectiveHero(t, server.sessions[gm["session"]], 20)
	server.mu.Unlock()
	cmd.Seq, cmd.ItemID = 2, "broken"
	writeCharacterCommand(t, gmWS, cmd)
	if issue := ackError(t, read(t, gmWS, "ack")); !strings.Contains(issue, "division by zero") {
		t.Fatalf("invalid equip: %s", issue)
	}
	for i, item := range []string{"ring-1", "ring-2", "runic-1"} {
		cmd.Seq, cmd.ItemID, cmd.Equipped = uint64(3+i), item, boolPointer(false)
		writeCharacterCommand(t, gmWS, cmd)
		if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
			t.Fatal(issue)
		}
	}
	reloaded, err := newServer(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.stop()
	session := reloaded.sessions[gm["session"]]
	_, state := sessionEffectiveHero(t, session, 16)
	expected := []ActionSource{{"weapon.attack.hit", ""}, {"weapon.attack.damage", "steel"}, {"weapon.attack.hit", "steel"}}
	if !slices.Equal(state.ActionSources, expected) || session.CharacterInstances["hero"].Inventory[5].Equipped {
		t.Fatalf("sources or rollback after reload: %+v", state.ActionSources)
	}
}

func TestRulesetV04InventorySourcesAndZIP(t *testing.T) {
	base := &Session{RegistryRevision: 1, CharacterRevision: 1, CharacterInstances: map[string]CharacterInstance{}, CharacterSources: map[string]CharacterTOMLSource{}}
	makeCandidate := func(entries map[string][]byte) portableImportCandidate {
		t.Helper()
		archive, err := plainArchive(t, entries)
		if err != nil {
			t.Fatal(err)
		}
		opts := portableImportOptions{Mode: "replace"}
		if issues, incomplete := diagnosePortableImport(base, archive, opts); len(issues) > 0 || incomplete {
			t.Fatalf("ZIP issues: %+v", issues)
		}
		next, err := buildPortableImportCandidate(t.Context(), base, archive, opts, t.TempDir(), map[string]Asset{}, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		return next
	}
	rules := strings.Replace(inventoryRules, `formula = "target + 2"`, `formula = "target + 2"
stack = "unique"`, 1)
	candidate := makeCandidate(map[string][]byte{"rules.toml": []byte(rules), "party/hero.toml": []byte(inventoryHero)})
	session := &Session{RegistryRevision: 1, CharacterRevision: 1, Ruleset: candidate.Ruleset, CharacterInstances: candidate.Characters, CharacterSources: candidate.Sources}
	for _, item := range []string{"ring-2", "runic-2"} {
		if err := runStatCommand(t, session, Command{Type: "characterItemEquip", ItemID: item, Equipped: boolPointer(true)}); err != nil {
			t.Fatal(err)
		}
	}
	registry, state := sessionEffectiveHero(t, session, 18)
	for _, tc := range []struct {
		action, source string
		want           int64
	}{
		{"hit", "", 5}, {"hit", "runic-1", 6}, {"hit", "runic-2", 6}, {"hit", "steel", 5},
		{"damage", "runic-1", 8}, {"damage", "runic-2", 7}, {"damage", "steel", 5},
	} {
		choice := ActionSource{"weapon.attack." + tc.action, tc.source}
		if !slices.Contains(state.ActionSources, choice) {
			t.Fatalf("missing %+v", choice)
		}
		rng := &deterministicDiceRNG{values: []int{4}, failAt: -1}
		frame, err := session.Ruleset.characterActionFrame(session.CharacterInstances["hero"], registry, choice, nil, rng)
		if err != nil {
			t.Fatal(err)
		}
		result, err := frame.Evaluate(frame.Locals["roll"])
		if err != nil || result.scalar() != tc.want {
			t.Fatalf("%+v: %v %v", choice, result, err)
		}
	}
	if err := runStatCommand(t, session, Command{Type: "characterItemEquip", ItemID: "runic-2", Equipped: boolPointer(false)}); err != nil {
		t.Fatal(err)
	}
	hero := session.CharacterInstances["hero"]
	if _, err := session.Ruleset.characterActionFrame(hero, registry, ActionSource{"weapon.attack.hit", "runic-2"}, nil, nil); err == nil {
		t.Fatal("unequipped source accepted")
	}
	var packed bytes.Buffer
	if err := ExportPortablePackage(t.Context(), snapshotPortableExport(session), t.TempDir(), false, &packed); err != nil {
		t.Fatal(err)
	}
	files := unpackPortable(t, packed.Bytes())
	saved := files[CharacterTOMLFileName("hero")]
	if !bytes.Contains(saved, []byte("[[character.hero.inventory]]")) || !bytes.Equal(files["party/hero.toml"], []byte(inventoryHero)) {
		t.Fatalf("missing saved or authored TOML: %s", saved)
	}
	restored := makeCandidate(files)
	registry, _ = sessionEffectiveHero(t, &Session{Ruleset: restored.Ruleset, CharacterInstances: restored.Characters}, 18)
	got := restored.Characters["hero"]
	if !got.Inventory[1].Equipped || got.Inventory[3].Equipped || got.Inventory[2].ID != "runic-1" || got.Inventory[2].ObjectID != "runic_sword" {
		t.Fatalf("ZIP lost inventory: %+v", got.Inventory)
	}
	// Changes to an imported nested save keep comments and array table order.
	savedSession := &Session{RegistryRevision: 1, CharacterRevision: 1, Ruleset: restored.Ruleset, CharacterInstances: restored.Characters, CharacterSources: restored.Sources}
	origin := savedSession.CharacterSources["hero"]
	origin.Source = nestedCharacterSaveHeader + inventoryHero
	savedSession.CharacterSources["hero"] = origin
	if err := runStatCommand(t, savedSession, Command{Type: "characterItemEquip", ItemID: "runic-2", Equipped: boolPointer(true)}); err != nil {
		t.Fatal(err)
	}
	edited, err := CurrentCharacterTOML(savedSession, "hero")
	if err != nil || !bytes.Contains(edited, []byte("# Hero declaration\n")) || !bytes.Contains(edited, []byte("equipped = true\nid = \"runic-2\"")) {
		t.Fatalf("nested source edit: %s %v", edited, err)
	}
	// Legacy ATLAS artwork overrides still have a portable representation.
	got = WithAvatarOverride(got, "")
	flat, err := EncodeCharacterTOML(got, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCharacterTOML(flat, nil, registry)
	if err != nil || decoded.AvatarAssetID == nil || !decoded.Inventory[1].Equipped || !equalRulesetFields(decoded.V04, hero.V04) {
		t.Fatalf("extended saved instance round-trip: %+v %v", decoded, err)
	}
}

func TestRulesetV04MaterializeExistingDeclaration(t *testing.T) {
	snapshot, err := ParseRulesetTOML([]byte(inventoryRules + inventoryHero))
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{Ruleset: snapshot, CharacterInstances: map[string]CharacterInstance{}}
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		t.Fatal(err)
	}
	cmd := Command{CharacterDefinitionID: "hero"}
	hero, err := createCharacterInstance(session, cmd, registry)
	if err != nil || hero.ID != "hero" || !hero.Persistent || len(hero.Inventory) != 6 {
		t.Fatalf("materialize installed declaration: %+v %v", hero, err)
	}
	session.CharacterInstances[hero.ID] = hero
	if _, err := createCharacterInstance(session, cmd, registry); err == nil {
		t.Fatal("same character ID was created twice")
	}
}
