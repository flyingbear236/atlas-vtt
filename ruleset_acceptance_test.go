package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

const specExamplesPath = "testdata/ruleset-v04/spec-examples.toml"

// Give the examples which the original hero doesn't have to another character.
const specSupplement = `
[character.trained]
name = "Ученица"
actions = ["action.check.athletics", "action.check.investigation", "action.save.strength", "action.inspect"]
effects = ["effect.strength_bonus", "effect.make_redder", "effect.reduce_red", "effect.athletics_training", "effect.proficiency.investigation", "effect.shared_bonus"]
notes.profile.title = "Разведчица"

[[character.trained.inventory]]
id = "training-dagger"
object = "object.dagger"
marks.rank = 2

[[character.trained.inventory]]
id = "second-sword"
object = "object.runic_sword"
equipped = true

[[character.trained.inventory]]
id = "steel"
object = "object.sword"
equipped = true

[[character.trained.inventory]]
id = "axe"
object = "object.greataxe"
equipped = true
`

func specZIP(t *testing.T) ([]byte, []byte) {
	t.Helper()
	source, err := os.ReadFile(specExamplesPath)
	if err != nil {
		t.Fatal(err)
	}
	return plainZIP(t, map[string][]byte{"rules/examples.toml": source, "party/trained.toml": []byte(specSupplement)}), source
}

func importSpecZIP(t *testing.T, url string, gm map[string]string, zip []byte) {
	t.Helper()
	exchange := func(op string, preview map[string]json.RawMessage) map[string]json.RawMessage {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, err := form.CreateFormFile("file", "examples.zip")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(zip); err != nil {
			t.Fatal(err)
		}
		fields := map[string]string{"options": `{"mode":"replace"}`}
		for _, key := range []string{"registryRevision", "characterRevision", "campaignRevision", "libraryRevision", "digest", "optionsDigest"} {
			if raw := preview[key]; raw != nil {
				fields[key] = strings.Trim(string(raw), "\"")
			}
		}
		for key, value := range fields {
			if err := form.WriteField(key, value); err != nil {
				t.Fatal(err)
			}
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest("POST", url+"/api/definitions/package/"+op+"?session="+gm["session"], &body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+gm["key"])
		req.Header.Set("Content-Type", form.FormDataContentType())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s %v", op, resp.StatusCode, data, err)
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if raw := string(result["errors"]); raw != "" && raw != "[]" && raw != "null" {
			t.Fatalf("%s: %s", op, data)
		}
		return result
	}
	exchange("apply", exchange("preview", nil))
}

// Link existing instances with the normal websocket create command.
func linkSpecCharacter(t *testing.T, ws *websocket.Conn, sceneID, characterID string, seq uint64) {
	t.Helper()
	sendCommand(t, ws, Command{Type: "create", Client: "tokens", Seq: seq, SceneID: sceneID, CharacterID: characterID,
		Token: Token{X: float64(200 + seq*100), Y: 200, Size: 70, Opacity: 1, Color: "#ffffff"},
	})
	if msg := read(t, ws, "ack"); string(msg["error"]) != "\"\"" {
		t.Fatalf("link %s: %s", characterID, msg)
	}
}

func TestRulesetSpecExamplesThroughCommandsAndZIP(t *testing.T) {
	fixture := newDiceCommandFixture(t)
	// Use a separate empty campaign so the authored section can seed hero.
	gm := post(t, fixture.host.URL+"/api/sessions", map[string]string{"name": "Ruleset acceptance"})
	zip, source := specZIP(t)
	importSpecZIP(t, fixture.host.URL, gm, zip)
	ws := dialRaw(t, fixture.host.URL, gm)
	home := read(t, ws, "campaignSnapshot")
	var scenes []SceneSummary
	if err := json.Unmarshal(home["scenes"], &scenes); err != nil || len(scenes) != 1 {
		t.Fatalf("campaign: %s", home)
	}
	scene := scenes[0].ID
	subscribeDiceScene(t, ws, scene)
	linkSpecCharacter(t, ws, scene, "hero", 1)
	linkSpecCharacter(t, ws, scene, "trained", 2)
	sendCommand(t, ws, Command{Type: "characterWatch", CharacterID: "hero", CharacterWatch: 1})
	msg := read(t, ws, "characterSnapshot")
	var sheet characterSnapshot
	if err := json.Unmarshal(msgIntoJSON(t, msg), &sheet); err != nil {
		t.Fatal(err)
	}
	if sheet.Effective.Stats["basic.strength"].Integer() != 18 || sheet.Effective.Stats["basic.proficiency"].Integer() != 3 || sheet.Effective.Resources["hp"].Current.Integer() != 85 ||
		sheet.StatFields["basic.strength"].Base != float64(16) {
		t.Fatalf("hero projection: %s", msg)
	}

	rows := []struct {
		actor, id, source, target string
		values                    []int
		total                     int64
		success                   *bool
		message                   string
	}{
		{"hero", "roll.d20", "", "", []int{9}, 10, nil, ""},
		{"hero", "check.athletics", "", "", []int{9}, 17, nil, ""},
		{"hero", "save.strength", "", "", []int{9}, 17, nil, ""},
		{"hero", "weapon.attack.hit", "item-002", "trained", []int{9}, 18, boolPointer(true), "Попадание!"},
		{"hero", "weapon.attack.hit", "item-002", "trained", []int{0}, 9, boolPointer(false), "Промах!"},
		{"hero", "weapon.attack.hit", "item-002", "", []int{9}, 18, nil, ""},
		{"hero", "weapon.attack.damage", "item-002", "trained", []int{3}, 8, nil, "Бросок урона: 8"},
		{"hero", "spell.shock.hit", "", "trained", []int{13}, 16, boolPointer(true), "Попадание! Цели нужен спасбросок Силы, DC 13"},
		{"hero", "spell.shock.hit", "", "trained", []int{0}, 3, boolPointer(false), "Заклинание не попало"},
		{"hero", "spell.shock.damage", "", "trained", []int{0, 1}, 3, nil, ""},
		{"trained", "check.investigation", "", "", []int{9}, 15, nil, ""},
		{"trained", "check.athletics", "", "", []int{9}, 18, nil, ""},
		{"trained", "save.strength", "", "", []int{9}, 16, nil, ""},
		{"trained", "weapon.attack.hit", "second-sword", "", []int{9}, 15, nil, ""},
		{"trained", "weapon.attack.hit", "steel", "", []int{9}, 14, nil, ""},
		{"trained", "weapon.attack.damage", "axe", "", []int{10}, 15, nil, "Бросок урона: 15"},
	}
	var last ActionEvent
	var command actionCommandWire
	for i, row := range rows {
		t.Run(row.id+"/"+row.source+"/"+strconv.Itoa(i), func(t *testing.T) {
			fixture.server.mu.Lock()
			fixture.rng.values, fixture.rng.next = row.values, 0
			fixture.server.mu.Unlock()
			request := &ActionExecutionRequest{CharacterID: row.actor, ActionID: row.id, SourceID: row.source}
			if row.target != "" {
				request.Target = &ActionInstanceRef{CharacterID: row.target}
			}
			command = actionCommandWire{Type: "actionExecute", Client: "examples", Seq: uint64(i + 1), SceneID: scene, Action: request}
			sendCommand(t, ws, command)
			last = actionACK(t, ws, false)
			if last.Roll == nil || last.Roll.Total != row.total || !reflect.DeepEqual(last.Success, row.success) ||
				strings.Join(last.Messages, "") != row.message {
				t.Fatalf("result %+v", last)
			}
		})
	}
	sendCommand(t, ws, command)
	if replay := actionACK(t, ws, true); !reflect.DeepEqual(replay, last) {
		t.Fatalf("replay %+v", replay)
	}

	stone := addUploadedTestObject(t, fixture, gm, scene, "Камень", 125, 125)
	sendCommand(t, ws, Command{Type: "view", SceneID: scene, Region: &SceneRegion{Left: 0, Top: 0, Right: 1000, Bottom: 1000}})
	read(t, ws, "snapshot")
	request := &ActionExecutionRequest{CharacterID: "trained", ActionID: "inspect"}
	sendCommand(t, ws, Command{Type: "actionTargets", SceneID: scene, Seq: 1, Action: request})
	targets := read(t, ws, "actionTargets")
	var choices []actionTargetOption
	if err := json.Unmarshal(targets["targets"], &choices); err != nil || len(choices) != 1 {
		t.Fatalf("targets %s", targets)
	}
	request.Target = &ActionInstanceRef{ElementID: stone}
	sendCommand(t, ws, actionCommandWire{Type: "actionExecute", Client: "examples", Seq: uint64(len(rows) + 1), SceneID: scene, Action: request})
	if event := actionACK(t, ws, false); event.Roll != nil || strings.Join(event.Messages, "") != "Вы осматриваете КаменьКамень молчит." {
		t.Fatalf("inspect %+v", event)
	}

	sendCommand(t, ws, Command{Type: "characterItemEquip", Client: "examples", Seq: uint64(len(rows) + 2), SceneID: scene, CharacterID: "hero", ItemID: "item-001", Equipped: boolPointer(false)})
	if msg := read(t, ws, "ack"); string(msg["error"]) != "\"\"" {
		t.Fatalf("equip: %s", msg)
	}
	export, err := http.NewRequest("GET", fixture.host.URL+"/api/definitions/package/export?session="+gm["session"], nil)
	if err != nil {
		t.Fatal(err)
	}
	export.Header.Set("Authorization", "Bearer "+gm["key"])
	resp, err := http.DefaultClient.Do(export)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	archive, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("export: %d %s %v", resp.StatusCode, archive, err)
	}
	files := unpackPortable(t, archive)
	if !bytes.Equal(files["rules/examples.toml"], source) || string(files["party/trained.toml"]) != specSupplement {
		t.Fatal("source changed")
	}
	fresh := post(t, fixture.host.URL+"/api/sessions", map[string]string{"name": "Restored"})
	importSpecZIP(t, fixture.host.URL, fresh, archive)
	fixture.server.mu.Lock()
	defer fixture.server.mu.Unlock()
	ss := fixture.server.sessions[fresh["session"]]
	hero, trained := ss.CharacterInstances["hero"], ss.CharacterInstances["trained"]
	if hero.Inventory[0].Equipped || !hero.Inventory[1].Equipped || hero.ResourceCurrent["hp"].Integer() != 85 ||
		trained.ResourceCurrent["hp"].Integer() != 89 || len(trained.Inventory) != 4 || !strings.Contains(trained.V04.toml, "Разведчица") ||
		!strings.Contains(trained.Inventory[0].Fields.toml, "rank = 2") {
		t.Fatalf("lost saved data: %+v %+v", hero, trained)
	}
	effective, err := characterStatsForRegistry(hero, mustSpecRegistry(t, ss))
	if err != nil || effective.Stats["basic.strength"].Integer() != 16 || effective.StatValues["stat.basic.strength.bonus"] != int64(3) {
		t.Fatalf("restored stats %+v %v", effective, err)
	}
	state, err := characterStatsForRegistry(trained, mustSpecRegistry(t, ss))
	if err != nil || state.StatValues["stat.color.r"] != int64(7) || state.StatValues["stat.flags.door_open"] != false || state.StatValues["stat.motto"] != "Мы все умрём" {
		t.Fatalf("computed fields %+v %v", state.StatValues, err)
	}
}

func msgIntoJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRulesetSpecTargetChoicesVisibility(t *testing.T) {
	f := newActionFixture(t)
	stone := addUploadedTestObject(t, f, f.gm, f.sceneA.ID, "Stone", 125, 125)
	f.server.mu.Lock()
	target := f.sceneA.Tokens["token-goblin"]
	target.Hidden = true
	f.sceneA.Tokens[target.ID] = target
	f.server.mu.Unlock()
	alice := actionConnection(t, f, f.alice)
	sendCommand(t, alice, Command{Type: "actionTargets", SceneID: f.sceneA.ID, Seq: 1, Action: damageCommand(f, "hero", 1).Action})
	// A picker request describes the actor and grant. The selected target
	// belongs to the separate execution command.
	if event := read(t, alice, "actionTargets"); event["error"] == nil {
		t.Fatal("picker accepted a supplied target")
	}
	damage := damageCommand(f, "hero", 1)
	damage.Action.Target = nil
	sendCommand(t, alice, Command{Type: "actionTargets", SceneID: f.sceneA.ID, Seq: 2, Action: damage.Action})
	if choices := string(read(t, alice, "actionTargets")["targets"]); !strings.Contains(choices, "bob-hero") || strings.Contains(choices, "goblin") {
		t.Fatalf("character choices: %s", choices)
	}
	sendCommand(t, alice, Command{Type: "view", SceneID: f.sceneA.ID, Region: &SceneRegion{Left: 0, Top: 0, Right: 1000, Bottom: 1000}})
	read(t, alice, "snapshot")
	sendCommand(t, alice, Command{Type: "actionTargets", SceneID: f.sceneA.ID, Seq: 3, Action: &ActionExecutionRequest{CharacterID: "hero", ActionID: "inspect"}})
	if choices := string(read(t, alice, "actionTargets")["targets"]); !strings.Contains(choices, stone) || strings.Contains(choices, "bob-hero") {
		t.Fatalf("object choices: %s", choices)
	}
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	if f.rng.next != 0 || f.session.ActionRevision != 0 {
		t.Fatal("listing targets executed an action")
	}
}

func mustSpecRegistry(t *testing.T, ss *Session) EffectiveRegistry {
	t.Helper()
	registry, err := rollRegistry(ss)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestRulesetSpecIndependentExamples(t *testing.T) {
	file, err := os.ReadFile("testdata/ruleset-v04/independent.toml")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := ParseRulesetTOML(file)
	if err != nil {
		t.Fatal(err)
	}
	registry := mustSpecRegistry(t, &Session{Ruleset: snapshot})
	characters, err := seedRulesetCharacters(nil, nil, snapshot, registry)
	if err != nil {
		t.Fatal(err)
	}
	member := &Member{ID: "gm", GM: true}
	scene := newScene("room", "Room")
	addTestSceneObject(scene, "stone", "verbatim user data")
	for id := range characters {
		scene.Tokens[id] = Token{ID: id, CharacterInstanceID: id}
	}
	session := &Session{ID: "campaign", Ruleset: snapshot, CharacterInstances: characters, Scenes: map[string]*Scene{scene.ID: scene}, Members: map[string]*Member{member.ID: member}}
	rebuildCharacterReferences(session)
	execute := func(action, source string, target *ActionInstanceRef, draw int) ActionExecutionResult {
		t.Helper()
		result, err := ExecuteRulesetAction(session, scene.ID, member, ActionExecutionRequest{CharacterID: "hero", ActionID: action, SourceID: source, Target: target}, &deterministicDiceRNG{values: []int{draw}, failAt: -1})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if r := execute("example", "", nil, 9); r.Plan != nil || r.Success == nil || !*r.Success || strings.Join(r.Messages, "|") != "Сложность: 8|Цель получает 5 урона" {
		t.Fatalf("no target: %+v", r)
	}
	if r := execute("example", "", &ActionInstanceRef{CharacterID: "other"}, 9); r.Plan == nil || r.Plan.Changes[0].Before.Integer() != 30 || r.Plan.Changes[0].After.Integer() != 25 {
		t.Fatalf("conditional write: %+v", r)
	}
	if r := execute("rest", "", nil, 0); r.Roll != nil || r.Plan == nil || r.Plan.Changes[0].After.Integer() != 32 {
		t.Fatalf("self: %+v", r)
	}
	if r := execute("check_object", "", &ActionInstanceRef{ElementID: "stone"}, 0); r.Success == nil || *r.Success || strings.Join(r.Messages, "") != "verbatim user data" {
		t.Fatalf("object DC: %+v", r)
	}
	if r := execute("use_axe", "axe", nil, 3); r.Roll == nil || r.Roll.Total != 9 || r.Plan == nil || r.Plan.Changes[0].After.Integer() != 2 {
		t.Fatalf("source: %+v", r)
	}
}

func TestRulesetSpecRejectsUnusedMechanics(t *testing.T) {
	for name, example := range map[string]string{
		"stat source scope": `[stat.hp]
default=30
[effect.test]
target="stat.hp"
scope="source"
formula="target + 1"`,
		"branch roll": `[action.hit]
roll="1d20"
success="true"
on.success={message="ok", roll="1d4"}`,
		"unknown outcome": `[action.hit]
roll="1d20"
on.miss={message="ok"}`,
		"extra apply field": `[stat.hp]
default=30
resource=true
[action.hit]
apply={path="actor.stat.hp.current", formula="value - 1", when="false"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRulesetTOML([]byte(example)); err == nil || !strings.Contains(err.Error(), "unknown") && !strings.Contains(err.Error(), "scope source") {
				t.Fatalf("silently ignored mechanics: %v", err)
			}
		})
	}
}
