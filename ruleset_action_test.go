package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const executorRules = `
[stat.strength]
default = 16
bonus = { formula = "floor((value - 10) / 2)" }
[stat.proficiency]
default = { formula = "2 + floor((6 - 1) / 4)" }
[stat.ac]
default = 15
[stat.hp]
default = 30
resource = true
[stat.quotient]
default = { formula = "floor(16 / stat.strength.base)" }

[effect.trained]
target = "action.athletics.roll"
formula = "target + stat.proficiency"
[effect.accurate]
target = "action.weapon.hit.roll"
formula = "target + 1"
scope = "source"

[action.simple]
roll = "1d20"
message = "roll {roll} / {roll}"

[action.athletics]
roll = "1d20 + stat.strength.bonus"

[action.weapon]
[action.weapon.hit]
target = "select(character)"
roll = "1d20 + stat.strength.bonus"
var.dc = "8 + stat.proficiency"
var.total = "roll + var.dc"
var.unused = "1d20"
success = "roll >= target.stat.ac"
message = "{actor.name} attacks"
apply = { path = "actor.stat.hp.current", formula = "value - 1" }
[action.weapon.hit.on.success]
message = "Hit {target.name}: DC {var.dc}, total {var.total}"
apply = { path = "target.stat.hp.current", formula = "value - 5" }
[action.weapon.hit.on.fail]
message = "Miss: {roll}"
apply = { path = "target.stat.hp.current", formula = "value - 2" }

[action.weapon.damage]
target = "select(character)"
roll = "dice(source.damage)"
message = "Damage {roll}"
apply = { path = "target.stat.hp.current", formula = "value - roll" }

[action.weapon.drain]
target = "source"
apply = { path = "target.store.charges", formula = "value - 1" }
message = "{target.name} {source.id}"

[action.inspect]
target = "select(object)"
message = "Inspect {target.name}: {target.store.charges}"

[action.recover]
target = "self"
apply = { path = "actor.stat.hp.current", formula = "value + 3" }
success = "target.stat.hp.current == 20"
[action.recover.on.success]
apply = { path = "target.stat.hp.current", formula = "value + 4" }

[action.choose]
target = "select(character)"
roll = "1d20 + target.stat.ac"
var.dc = "target.stat.ac"
var.independent = "2 + 2"
success = "true"
message = "{var.dc}"
[action.choose.on.success]
message = "Independent {var.independent}"
apply = { path = "actor.notes.count", formula = "value + 1" }

[action.unknown]
message = "{actor.missing}"

[action.write_id]
apply = { path = "actor.id", formula = "'new-id'" }

[action.write_base]
apply = { path = "actor.stat.strength.base", formula = "value * 2" }

[action.bad_draft]
apply = { path = "actor.stat.strength.base", formula = "0" }

[action.bad_branch]
message = "A message"
apply = { path = "actor.stat.hp.current", formula = "10" }
success = "true"
[action.bad_branch.on.success]
apply = { path = "actor.notes.missing", formula = "1" }

[object.sword]
name = "Sword"
damage = "1d8"
actions = ["action.weapon"]
effects = ["effect.accurate"]

[character.hero]
name = "Hero"
effects = ["effect.trained"]
actions = [
  "action.simple", "action.athletics", "action.inspect", "action.recover",
  "action.choose", "action.unknown", "action.write_id", "action.write_base",
  "action.bad_draft", "action.bad_branch", "action.weapon.damage", "action.weapon.drain"
]
notes.count = 5
notes.label = "Notes"
[character.hero.stat.hp]
current = 20
[[character.hero.inventory]]
id = "blade"
object = "object.sword"
equipped = true
store.charges = 3
[[character.hero.inventory]]
id = "spare"
object = "object.sword"
store.charges = 8

[character.goblin]
name = "Goblin"
[character.goblin.stat.hp]
current = 27
`

func executorSession(t *testing.T) (*Session, *Member) {
	t.Helper()
	snapshot, err := ParseRulesetTOML([]byte(executorRules))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: snapshot.Registry})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := seedRulesetCharacters(nil, nil, snapshot, registry)
	if err != nil {
		t.Fatal(err)
	}
	member := &Member{ID: "player"}
	scene := &Scene{ID: "arena", Published: true, Revision: 7, Tokens: map[string]Token{
		"a": {ID: "a", CharacterInstanceID: "hero", OwnerIDs: []string{member.ID}},
		"t": {ID: "t", CharacterInstanceID: "goblin"},
	}}
	session := &Session{ID: "session", Members: map[string]*Member{member.ID: member}, RegistryRevision: 3, CharacterRevision: 5, CampaignRevision: 2,
		CharacterInstances: characters, Ruleset: snapshot, Scenes: map[string]*Scene{scene.ID: scene}}
	rebuildCharacterReferences(session)
	return session, member
}

func TestRulesetActionRollEffectsAndConsequences(t *testing.T) {
	session, member := executorSession(t)
	before, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	goblin := &ActionInstanceRef{CharacterID: "goblin"}
	for _, tc := range []struct {
		action, source string
		target         *ActionInstanceRef
		draw, side     int
		base, want     int64
		msg            string
		success        *bool
		hpAfter        int64
	}{
		{"simple", "", nil, 8, 20, 9, 9, "roll 9 / 9", nil, 0},
		{"athletics", "", nil, 9, 20, 13, 16, "", nil, 0},
		{"weapon.hit", "blade", goblin, 10, 20, 14, 15, "Hit Goblin: DC 11, total 26", boolPointer(true), 22},
		{"weapon.hit", "blade", goblin, 4, 20, 8, 9, "Miss: 9", boolPointer(false), 25},
		{"weapon.damage", "blade", goblin, 6, 8, 7, 7, "Damage 7", nil, 20},
	} {
		t.Run(tc.action+tc.msg, func(t *testing.T) {
			rng := &deterministicDiceRNG{values: []int{tc.draw}, failAt: 1}
			out, err := ExecuteRulesetAction(session, "arena", member, ActionExecutionRequest{"hero", tc.action, tc.source, tc.target}, rng)
			if err != nil {
				t.Fatal(err)
			}
			if out.Roll == nil || out.Roll.Base != tc.base || out.Roll.Total != tc.want || len(out.Roll.Dice) != 1 ||
				out.Roll.Dice[0].Sides != tc.side || !slices.Equal(out.Roll.Dice[0].Results, []int{tc.draw + 1}) || rng.next != 1 {
				t.Fatalf("roll: %+v (%d draws)", out.Roll, rng.next)
			}
			if (tc.msg != "" && !slices.Contains(out.Messages, tc.msg)) || (tc.success == nil) != (out.Success == nil) ||
				tc.success != nil && *out.Success != *tc.success {
				t.Fatalf("unexpected consequences: %+v", out)
			}
			if tc.hpAfter == 0 && out.Plan != nil {
				t.Fatalf("unexpected plan: %+v", out.Plan)
			}
			if tc.hpAfter != 0 {
				if out.Plan == nil || out.Plan.SceneRevision != 7 || out.Plan.RegistryRevision != 3 || out.Plan.CharacterRevision != 5 || out.Plan.CampaignRevision != 2 {
					t.Fatalf("missing validated plan: %+v", out.Plan)
				}
				draft := out.Plan.replacements["goblin"]
				write := out.Plan.Changes[len(out.Plan.Changes)-1]
				if draft.ResourceCurrent["hp"] != IntegerStatValue(tc.hpAfter) || write.Instance != *goblin || write.Before.Integer() != 27 || write.After.Integer() != tc.hpAfter {
					t.Fatalf("wrong damage plan: %+v", out.Plan)
				}
			}
			if tc.success != nil && *tc.success && out.Variables["var.dc"] != int64(11) {
				t.Fatalf("DC: %+v", out.Variables)
			}
			if _, ok := out.Variables["var.unused"]; ok {
				t.Fatal("unused variable executed")
			}
		})
	}
	after, err := json.Marshal(session)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("execution modified the session")
	}
}

func TestRulesetActionOptionalTargetsAndSavedFields(t *testing.T) {
	session, member := executorSession(t)
	run := func(action, source string, target *ActionInstanceRef) (ActionExecutionResult, *deterministicDiceRNG) {
		t.Helper()
		rng := &deterministicDiceRNG{values: []int{5}, failAt: 1}
		out, err := ExecuteRulesetAction(session, "arena", member, ActionExecutionRequest{"hero", action, source, target}, rng)
		if err != nil {
			t.Fatal(err)
		}
		return out, rng
	}
	out, _ := run("weapon.hit", "blade", nil)
	if out.Roll == nil || out.Roll.Total != 10 || out.Success != nil || !slices.Equal(out.Messages, []string{"Hero attacks"}) ||
		out.Plan.replacements["hero"].ResourceCurrent["hp"].Integer() != 19 ||
		!slices.ContainsFunc(out.Skipped, func(issue ActionExecutionIssue) bool { return issue.Operation == "on.success" }) {
		t.Fatalf("partial action: %+v", out)
	}
	out, _ = run("weapon.damage", "blade", nil)
	if out.Roll == nil || out.Roll.Total != 6 || out.Plan != nil || !slices.Equal(out.Messages, []string{"Damage 6"}) ||
		len(out.Skipped) != 1 || out.Skipped[0].Operation != "apply" {
		t.Fatalf("damage with missing target: %+v", out)
	}
	out, rng := run("choose", "", nil)
	if out.Roll != nil || rng.next != 0 || out.Success == nil || !*out.Success ||
		!slices.Equal(out.Messages, []string{"Independent 4"}) || out.Variables["var.independent"] != int64(4) ||
		out.Plan.replacements["hero"].V04.values["notes"].(map[string]any)["count"] != int64(6) {
		t.Fatalf("skipped dependent operations: %+v", out)
	}
	if session.CharacterInstances["hero"].V04.values["notes"].(map[string]any)["count"] != int64(5) {
		t.Fatal("custom field leaked into session")
	}
	out, rng = run("inspect", "", &ActionInstanceRef{CharacterID: "hero", ItemID: "spare"})
	if out.Roll != nil || rng.next != 0 || !slices.Equal(out.Messages, []string{"Inspect Sword: 8"}) {
		t.Fatalf("inspect: %+v", out)
	}
	out, _ = run("inspect", "", nil)
	if len(out.Messages) != 0 || len(out.Skipped) != 1 || out.Skipped[0].Operation != "message" {
		t.Fatalf("inspect without object: %+v", out)
	}
	out, _ = run("weapon.drain", "blade", nil)
	if out.Target == nil || out.Target.ItemID != "blade" || !slices.Equal(out.Messages, []string{"Sword blade"}) ||
		out.Plan.Changes[0].After.Integer() != 2 || out.Plan.replacements["hero"].Inventory[0].Fields.values["store"].(map[string]any)["charges"] != int64(2) {
		t.Fatalf("source selector and write: %+v", out)
	}
	out, _ = run("recover", "", nil)
	if out.Target == nil || out.Target.CharacterID != "hero" || len(out.Plan.Changes) != 2 ||
		out.Plan.Changes[1].Before.Integer() != 23 || out.Plan.Changes[1].After.Integer() != 27 {
		t.Fatalf("staged value across two writes: %+v", out)
	}
	out, _ = run("write_base", "", nil)
	if out.Plan.replacements["hero"].Materialized.Stats["strength"] != IntegerStatValue(32) {
		t.Fatalf("base plan: %+v", out.Plan)
	}
}

func TestRulesetActionRejectsInvalidExecution(t *testing.T) {
	session, member := executorSession(t)
	for _, tc := range []struct {
		action, source, message string
		target                  *ActionInstanceRef
	}{
		{"weapon", "", "group", nil},
		{"weapon.hit", "spare", "cannot use", nil},
		{"weapon.damage", "", "source.damage", nil},
		{"weapon.drain", "", "requires an item", nil},
		{"unknown", "", "actor.missing", nil},
		{"write_id", "", "read-only", nil},
		{"bad_draft", "", "division by zero", nil},
		{"bad_branch", "", "saved scalar", nil},
		{"weapon.hit", "blade", "unavailable", &ActionInstanceRef{CharacterID: "missing"}},
		{"inspect", "", "wrong instance type", &ActionInstanceRef{CharacterID: "goblin"}},
		{"inspect", "", "unavailable", &ActionInstanceRef{CharacterID: "goblin", ItemID: "hidden_item"}},
		{"simple", "", "no target selector", &ActionInstanceRef{CharacterID: "hero"}},
	} {
		out, err := ExecuteRulesetAction(session, "arena", member, ActionExecutionRequest{"hero", tc.action, tc.source, tc.target}, nil)
		if err == nil || !strings.Contains(err.Error(), tc.message) || len(out.Errors) != 1 || out.Plan != nil {
			t.Errorf("%s: %+v %v", tc.action, out, err)
		}
	}
	scene := session.Scenes["arena"]
	hidden := scene.Tokens["t"]
	hidden.Hidden = true
	scene.Tokens["t"] = hidden
	for _, req := range []ActionExecutionRequest{
		{"goblin", "simple", "", nil},
		{"hero", "weapon.hit", "blade", &ActionInstanceRef{CharacterID: "goblin"}},
	} {
		forged := &Member{ID: member.ID, GM: true}
		rng := &deterministicDiceRNG{failAt: 0}
		_, err := ExecuteRulesetAction(session, "arena", forged, req, rng)
		if err == nil || !strings.Contains(err.Error(), "unavailable") || rng.next != 0 {
			t.Fatalf("visibility or impersonation: %v", err)
		}
	}
}

func TestRulesetActionValidation(t *testing.T) {
	for _, tc := range []struct{ source, fragment string }{
		{"[action.group.first]\nroll='1'\nvar.dc='2'\n[action.group.second]\nmessage='{var.dc}'", "unknown or unavailable reference"},
		{"[action.group.first]\nroll='1'\nvar.dc='2'\n[action.group.second]\nroll='var.dc'", "unknown or unavailable reference"},
		{"[action.test]\nroll='var.foo'\nvar.foo='roll'", "cyclic"},
		{"[action.test]\nmessage='{1 + 2}'", "invalid message reference"},
		{"[stat.x]\ndefault={formula='1'}\n[action.test]\napply={path='actor.stat.x.base',formula='1'}", "editable stat base"},
		{"[stat.x]\ndefault=1\nbonus={formula='value'}\n[action.test]\napply={path='actor.stat.x.bonus',formula='1'}", "editable stat base"},
	} {
		if _, err := ParseRulesetTOML([]byte(tc.source)); err == nil || !strings.Contains(err.Error(), tc.fragment) {
			t.Errorf("expected %q: %v", tc.fragment, err)
		}
	}
}
