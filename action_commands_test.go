package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func newActionFixture(t *testing.T) diceCommandFixture {
	t.Helper()
	f := newDiceCommandFixture(t)
	template, _ := executorSession(t)
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	f.session.Ruleset = template.Ruleset
	f.session.CharacterInstances = template.CharacterInstances
	f.session.ResourceOverlays = []ResourceOverlayConfig{{StatID: "hp", Enabled: true, Position: "top", Slot: 1, Color: "#00ff00", Mode: "bar"}}
	a := f.sceneA.Tokens["token-a"]
	b := cloneCharacterInstance(template.CharacterInstances["hero"])
	b.ID = "bob-hero"
	f.session.CharacterInstances[b.ID] = b
	for _, v := range []struct {
		id     string
		owners []string
	}{{"goblin", nil}, {b.ID, []string{"bob"}}} {
		token := a
		token.ID = "token-" + v.id
		token.CharacterInstanceID = v.id
		token.OwnerIDs = v.owners
		f.sceneA.Tokens[token.ID] = token
	}
	f.sceneA.rebuildRuntime()
	rebuildCharacterReferences(f.session)
	f.rng.values = []int{1, 3, 5}
	f.server.dirty = true
	if err := f.server.saveLocked(); err != nil {
		t.Fatal(err)
	}
	return f
}

func damageCommand(f diceCommandFixture, actor string, seq uint64) actionCommandWire {
	return actionCommandWire{Type: "actionExecute", Client: "tab", Seq: seq, SceneID: f.sceneA.ID, Action: &ActionExecutionRequest{
		CharacterID: actor, ActionID: "weapon.damage", SourceID: "blade", Target: &ActionInstanceRef{CharacterID: "goblin"},
	}}
}

func sendCommand(t *testing.T, conn *websocket.Conn, c any) {
	t.Helper()
	if err := conn.WriteJSON(c); err != nil {
		t.Fatal(err)
	}
}

func actionConnection(t *testing.T, f diceCommandFixture, credentials map[string]string) *websocket.Conn {
	t.Helper()
	conn := dialRaw(t, f.host.URL, credentials)
	read(t, conn, "campaignSnapshot")
	subscribeDiceScene(t, conn, f.sceneA.ID)
	return conn
}

func actionACK(t *testing.T, conn *websocket.Conn, replay bool) ActionEvent {
	t.Helper()
	ack := read(t, conn, "ack")
	var e ActionEvent
	if string(ack["error"]) != "\"\"" || rawBool(t, ack["replayed"]) != replay || json.Unmarshal(ack["action"], &e) != nil || e.ExecutionID == "" {
		t.Fatalf("unexpected ACK: %s", ack)
	}
	return e
}

func TestActionCommandConcurrentDamageReplayAndRestart(t *testing.T) {
	f := newActionFixture(t)
	alice := actionConnection(t, f, f.alice)
	bob := actionConnection(t, f, f.bob)
	gm := actionConnection(t, f, f.gm)
	sendCommand(t, gm, Command{Type: "view", SceneID: f.sceneA.ID, Region: &SceneRegion{Left: 0, Top: 0, Right: 1000, Bottom: 1000}})
	read(t, gm, "snapshot")
	sendCommand(t, gm, Command{Type: "characterWatch", CharacterID: "goblin", CharacterWatch: 1})
	read(t, gm, "characterSnapshot")
	one, two := damageCommand(f, "hero", 1), damageCommand(f, "bob-hero", 1)
	// Both commands are in flight before either client waits for its ACK.
	sendCommand(t, alice, one)
	sendCommand(t, bob, two)
	first := actionACK(t, alice, false)
	second := actionACK(t, bob, false)
	if first.ExecutionID == second.ExecutionID || first.Roll.Total+second.Roll.Total != 6 {
		t.Fatalf("rolls %+v / %+v", first, second)
	}
	for i := 0; i < 2; i++ {
		roll := read(t, gm, "actionRoll")
		msg := read(t, gm, "actionMessage")
		display := read(t, gm, "characterDisplayChanged")
		snap := read(t, gm, "characterSnapshot")
		if !bytes.Equal(display["executionId"], snap["executionId"]) || !bytes.Equal(roll["executionId"], msg["executionId"]) || !bytes.Equal(roll["executionId"], snap["executionId"]) {
			t.Fatal("unrelated execution IDs")
		}
		var inst CharacterInstance
		if err := json.Unmarshal(snap["instance"], &inst); err != nil {
			t.Fatal(err)
		}
		if i == 1 && inst.ResourceCurrent["hp"].Integer() != 21 {
			t.Fatalf("lost damage: %+v", inst)
		}
	}
	sendCommand(t, alice, one)
	if got := actionACK(t, alice, true); !reflect.DeepEqual(got, first) {
		t.Fatalf("duplicate changed result: %+v", got)
	}
	f.server.mu.Lock()
	if f.session.CharacterRevision != 3 || f.session.ActionRevision != 2 || f.session.RollRevision != 2 || f.rng.next != 2 {
		t.Error("incorrect revisions or reroll")
	}
	f.server.mu.Unlock()
	alice.Close()
	bob.Close()
	gm.Close()
	f.host.Close()
	restarted := testServer(t, f.root)
	rng := &deterministicDiceRNG{failAt: 0}
	restarted.rollService = NewRollService(rng)
	host := httptest.NewServer(restarted.routes())
	defer host.Close()
	client := dialRaw(t, host.URL, f.alice)
	read(t, client, "campaignSnapshot")
	history := subscribeDiceScene(t, client, f.sceneA.ID)
	var events []ActionEvent
	if err := json.Unmarshal(history["actions"], &events); err != nil || len(events) != 2 || events[0].Revision != 1 || events[1].Revision != 2 {
		t.Fatalf("reconnect history %s", history)
	}
	if hp := restarted.sessions[f.session.ID].CharacterInstances["goblin"].ResourceCurrent["hp"].Integer(); hp != 21 {
		t.Fatalf("saved HP = %d", hp)
	}
	sendCommand(t, client, one)
	if got := actionACK(t, client, true); !reflect.DeepEqual(got, first) || rng.next != 0 {
		t.Fatalf("restart replay: %+v", got)
	}
}

func TestActionCommandFailureIsAtomic(t *testing.T) {
	f := newActionFixture(t)
	alice := actionConnection(t, f, f.alice)
	gm := actionConnection(t, f, f.gm)
	sendCommand(t, gm, Command{Type: "characterWatch", CharacterID: "goblin", CharacterWatch: 1})
	read(t, gm, "characterSnapshot")
	blocker := filepath.Join(f.root, "sessions.json.tmp")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	// One hit modifies both the attacker and the target.
	c := damageCommand(f, "hero", 1)
	c.Action.ActionID = "weapon.hit"
	f.rng.values = []int{19}
	sendCommand(t, alice, c)
	read(t, alice, "saveError")
	sendCommand(t, gm, Command{Type: "sync"})
	// The sync response is a barrier on the observer's ordered WebSocket.
	var next map[string]json.RawMessage
	if err := gm.ReadJSON(&next); err != nil || string(next["type"]) != "\"snapshot\"" {
		t.Fatalf("publication before commit: %s %v", next, err)
	}
	history := read(t, gm, "rollHistory")
	if string(history["actions"]) != "[]" {
		t.Fatalf("uncommitted history: %s", history)
	}
	f.server.mu.Lock()
	ss := f.session
	if ss.CharacterInstances["goblin"].ResourceCurrent["hp"].Integer() != 27 || ss.CharacterInstances["hero"].ResourceCurrent["hp"].Integer() != 20 || ss.RollRevision != 0 || ss.ActionRevision != 0 || ss.CharacterRevision != 1 || len(ss.Receipts) != 0 || len(ss.ActionStreams) != 0 {
		t.Error("save error left partial state")
	}
	f.server.mu.Unlock()
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	sendCommand(t, alice, c)
	actionACK(t, alice, false)
	sendCommand(t, alice, c)
	actionACK(t, alice, true)
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	if ss.CharacterInstances["goblin"].ResourceCurrent["hp"].Integer() != 22 || ss.CharacterInstances["hero"].ResourceCurrent["hp"].Integer() != 19 || ss.CharacterRevision != 2 || ss.RollRevision != 1 || len(ss.ActionHistory) != 1 {
		t.Fatal("retry did not commit exactly once")
	}
}

func TestActionCommandAuthorizationAndPrivateMessages(t *testing.T) {
	f := newActionFixture(t)
	addUploadedTestObject(t, f, f.gm, f.sceneA.ID, "Stone", 125, 125)
	alice := actionConnection(t, f, f.alice)
	bob := actionConnection(t, f, f.bob)
	seq := uint64(0)
	reject := func(actor, source, target string) {
		seq++
		c := damageCommand(f, actor, seq)
		c.Action.SourceID = source
		c.Action.Target.CharacterID = target
		sendCommand(t, alice, c)
		if ack := read(t, alice, "ack"); string(ack["error"]) == "\"\"" || ack["action"] != nil {
			t.Fatalf("accepted invalid command %s", ack)
		}
	}
	reject("bob-hero", "blade", "goblin")
	reject("hero", "spare", "goblin")
	reject("hero", "blade", "absent")
	f.server.mu.Lock()
	tok := f.sceneA.Tokens["token-goblin"]
	tok.Hidden = true
	f.sceneA.Tokens[tok.ID] = tok
	f.server.mu.Unlock()
	reject("hero", "blade", "goblin")
	seq++
	cmd := damageCommand(f, "hero", seq)
	sendCommand(t, alice, map[string]any{"type": cmd.Type, "sceneId": cmd.SceneID, "client": cmd.Client, "seq": seq, "action": cmd.Action, "roll": 1})
	if ack := read(t, alice, "ack"); string(ack["error"]) == "\"\"" {
		t.Fatal("accepted client supplied roll")
	}
	seq++
	cmd.Seq = seq
	sendCommand(t, alice, Command{Type: "view", SceneID: f.sceneA.ID, Region: &SceneRegion{Left: 0, Top: 0, Right: 1000, Bottom: 1000}})
	read(t, alice, "snapshot")
	f.server.mu.Lock()
	var stone SceneElement
	for _, e := range f.sceneA.Elements {
		if e.Name == "Stone" {
			stone = e
			break
		}
	}
	stone.Interaction = &ObjectInteraction{Reactions: map[string]ObjectReaction{"inspect": {Any: ObjectMessage{Text: "Secret clue"}}}}
	f.sceneA.Elements[stone.ID] = stone
	f.server.mu.Unlock()
	cmd.Action = &ActionExecutionRequest{CharacterID: "hero", ActionID: "inspect", Target: &ActionInstanceRef{ElementID: stone.ID}}
	sendCommand(t, alice, cmd)
	msg := read(t, alice, "actionMessage")
	event := actionACK(t, alice, false)
	if event.Roll != nil || len(event.Messages) != 2 || string(msg["message"]) != "\"Inspect Stone\"" || event.Messages[1] != "Secret clue" {
		t.Fatalf("inspect: %s %+v", msg, event)
	}
	sendCommand(t, bob, Command{Type: "sync"})
	if msg := read(t, bob, "snapshot"); strings.Contains(string(msgIntoJSON(t, msg)), "Secret clue") {
		t.Fatal("private object leaked in snapshot")
	}
	if history := read(t, bob, "rollHistory"); strings.Contains(string(history["actions"]), "Secret clue") {
		t.Fatal("private reaction leaked in history")
	}
	sendCommand(t, alice, cmd)
	if got := actionACK(t, alice, true); !reflect.DeepEqual(got, event) {
		t.Fatal("inspect replay mismatch")
	}
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	if f.rng.next != 0 || f.session.RollRevision != 0 || len(f.session.RollHistory) != 0 || f.session.CharacterRevision != 1 || f.session.ActionRevision != 1 {
		t.Fatal("rejected or message-only action produced a roll or mutation")
	}
}

func TestActionCommandReplayAfterStreamEviction(t *testing.T) {
	f := newActionFixture(t)
	conn := actionConnection(t, f, f.alice)
	c := damageCommand(f, "hero", 1)
	sendCommand(t, conn, c)
	original := actionACK(t, conn, false)
	f.server.mu.Lock()
	ss := f.session
	for i := 0; i < maxReceiptsPerSession; i++ {
		ss.Receipts[fmt.Sprintf("other:%d", i)] = Receipt{Seq: 1, Updated: 1 << 60}
	}
	if !pruneReceipts(ss, "other:0") || ss.Receipts["alice:tab"].Seq != 0 || ss.ActionGeneration != 1 {
		t.Error("action stream not evicted")
	}
	f.server.dirty = true
	if err := f.server.saveLocked(); err != nil {
		t.Error(err)
	}
	f.server.mu.Unlock()
	sendCommand(t, conn, c)
	if got := actionACK(t, conn, true); !reflect.DeepEqual(got, original) {
		t.Fatal("history did not recover evicted ACK")
	}

	// Advance the bounded journal as if another stream completed more actions.
	f.server.mu.Lock()
	entry := ss.ActionHistory[0]
	for i := 0; i < MaxRollHistory; i++ {
		entry.ExecutionID = fmt.Sprintf("later-%d", i)
		entry.Stream = "alice:later"
		entry.Seq = uint64(i + 1)
		entry.Revision = ss.ActionRevision + 1
		m := actionCommit{entry: entry}
		m.apply(ss)
		ss.Receipts[entry.Stream] = Receipt{Seq: entry.Seq, Digest: entry.Digest, Action: &m.entry}
	}
	ss.ActionStreams[entry.Stream] = ss.ActionGeneration
	f.server.dirty = true
	if err := f.server.saveLocked(); err != nil {
		t.Error(err)
	}
	f.server.mu.Unlock()
	sendCommand(t, conn, c)
	read(t, conn, "fatal")
	f.server.mu.Lock()
	if f.rng.next != 1 || ss.CharacterInstances["goblin"].ResourceCurrent["hp"].Integer() != 25 {
		t.Error("evicted command executed again")
	}
	f.server.mu.Unlock()
	// A fresh tab using the generation advertised on subscribe can still play.
	c.Client = "new-tab"
	c.ActionGeneration = 1
	sendCommand(t, conn, c)
	actionACK(t, conn, false)
}

func TestActionCommandWritePermissions(t *testing.T) {
	f := newActionFixture(t)
	actor := &peer{sceneID: f.sceneA.ID, member: f.session.Members["alice"]}
	target := ActionInstanceRef{CharacterID: "goblin"}
	a := audienceForAction(f.session, actor)
	for _, tc := range []struct {
		instance ActionInstanceRef
		field    string
		allowed  bool
	}{
		{target, "stat.hp.current", true}, {target, "stat.strength.base", false}, {target, "name", false},
		{ActionInstanceRef{CharacterID: "hero"}, "stat.hp.base", false}, {ActionInstanceRef{CharacterID: "hero"}, "stat.strength.base", true},
		{ActionInstanceRef{CharacterID: "bob-hero", ItemID: "blade"}, "equipped", false},
	} {
		if canApplyActionChange(a, &target, ActionChange{Instance: tc.instance, Field: tc.field}) != tc.allowed {
			t.Errorf("wrong permission for %+v", tc)
		}
	}
}

func TestActionCommandDeniedBranchDiscardsEarlierWrites(t *testing.T) {
	f := newActionFixture(t)
	rules := strings.Replace(executorRules, `path = "target.stat.hp.current", formula = "value - 5"`, `path = "target.stat.strength.base", formula = "value - 5"`, 1)
	snapshot, err := ParseRulesetTOML([]byte(rules))
	if err != nil {
		t.Fatal(err)
	}
	f.server.mu.Lock()
	f.session.Ruleset = snapshot
	f.rng.values = []int{19}
	f.server.mu.Unlock()
	alice := actionConnection(t, f, f.alice)
	gm := actionConnection(t, f, f.gm)
	c := damageCommand(f, "hero", 1)
	c.Action.ActionID = "weapon.hit"
	sendCommand(t, alice, c)
	if ack := read(t, alice, "ack"); string(ack["error"]) == "\"\"" || ack["details"] != nil {
		t.Fatal("foreign base write allowed")
	}
	f.server.mu.Lock()
	if f.session.CharacterInstances["hero"].ResourceCurrent["hp"].Integer() != 20 || f.session.CharacterRevision != 1 || f.session.ActionRevision != 0 {
		t.Error("part of rejected plan committed")
	}
	f.server.mu.Unlock()
	sendCommand(t, gm, c)
	ack := read(t, gm, "ack")
	var details actionDetails
	if err := json.Unmarshal(ack["details"], &details); err != nil || len(details.Changes) != 2 || details.Source.ItemID != "blade" {
		t.Fatalf("GM result: %s", ack)
	}
	sendCommand(t, alice, c)
	if ack := read(t, alice, "ack"); string(ack["error"]) == "\"\"" || ack["details"] != nil {
		t.Fatal("denied receipt lost")
	}
}

func TestActionCommandRejectsForgedPayloadAtEveryLevel(t *testing.T) {
	f := newActionFixture(t)
	alice := actionConnection(t, f, f.alice)
	for i, extra := range []string{`,"effects":["effect.accurate"]`, `,"roll":{"total":50}`, `,"plan":{"changes":[]}`, `,"target":{"characterId":"goblin","finalHP":0}`, `,"actionId":"weapon.damage"`} {
		raw := fmt.Sprintf(`{"type":"actionExecute","client":"tab","seq":%d,"sceneId":"%s","action":{"characterId":"hero","actionId":"simple"%s}}`, i+1, f.sceneA.ID, extra)
		if err := alice.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		if ack := read(t, alice, "ack"); string(ack["error"]) == "\"\"" {
			t.Errorf("accepted %s", extra)
		}
	}
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	if f.rng.next != 0 || f.session.ActionRevision != 0 {
		t.Fatal("forged data reached executor")
	}
}

func TestActionHistoryPagesBoundedAndVisibilityFiltered(t *testing.T) {
	fixture := newActionFixture(t)
	fixture.session.ActionHistory = nil
	for i := 1; i <= 251; i++ {
		sceneID := fixture.sceneA.ID
		if i == 121 {
			sceneID = fixture.sceneB.ID
		}
		fixture.session.ActionHistory = append(fixture.session.ActionHistory, actionJournalEntry{ActionEvent: ActionEvent{
			ExecutionID: fmt.Sprintf("execution-%d", i), SceneID: sceneID, ActionID: "weapon.hit",
			CharacterID: "hero", Revision: uint64(i), Trace: &ActionTrace{RollFormula: "1d20 + 3"},
		}})
	}
	gm := &peer{member: &Member{GM: true}, sceneID: fixture.sceneA.ID}
	first, hasMore := actionHistoryPageForPeer(fixture.session, gm, 0)
	if len(first) != actionHistoryPageSize || !hasMore || first[0].Revision != 152 || first[len(first)-1].Revision != 251 {
		t.Fatalf("first page: len=%d more=%v first=%d last=%d", len(first), hasMore, first[0].Revision, first[len(first)-1].Revision)
	}
	second, hasMore := actionHistoryPageForPeer(fixture.session, gm, first[0].Revision)
	if len(second) != actionHistoryPageSize || !hasMore || second[0].Revision != 51 || second[len(second)-1].Revision != 151 {
		t.Fatalf("second page: len=%d more=%v first=%d last=%d", len(second), hasMore, second[0].Revision, second[len(second)-1].Revision)
	}
	third, hasMore := actionHistoryPageForPeer(fixture.session, gm, second[0].Revision)
	if len(third) != 50 || hasMore || third[0].Revision != 1 || third[len(third)-1].Revision != 50 {
		t.Fatalf("third page: len=%d more=%v", len(third), hasMore)
	}
	gm.playerPreview = true
	preview, _ := actionHistoryPageForPeer(fixture.session, gm, 0)
	if len(preview) == 0 {
		t.Fatal("player preview test has no visible history")
	}
	for _, event := range preview {
		if event.Trace != nil {
			t.Fatalf("player preview leaked GM trace: %+v", event)
		}
	}
}
