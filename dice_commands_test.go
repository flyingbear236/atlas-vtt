package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type diceCommandFixture struct {
	root           string
	server         *Server
	host           *httptest.Server
	session        *Session
	sceneA, sceneB *Scene
	gm, alice, bob map[string]string
	rng            *deterministicDiceRNG
	actionRoll     RollRequest
	characterID    string
}

func newDiceCommandFixture(t *testing.T) diceCommandFixture {
	t.Helper()
	root := t.TempDir()
	server := testServer(t, root)
	gmMember := &Member{ID: "gm", Name: "Game Master", Role: "gm", GM: true, Secret: "gm-key"}
	aliceMember := &Member{ID: "alice", Name: "Alice", Role: "player", Secret: "alice-key"}
	bobMember := &Member{ID: "bob", Name: "Bob", Role: "player", Secret: "bob-key"}
	sceneA := newScene("scene-a", "Scene A")
	sceneA.Published = true
	sceneB := newScene("scene-b", "Scene B")
	sceneB.Published = true
	registry := emptyCampaignRegistry()
	registry.Stats["agility"] = StatDefinition{ID: "agility", Name: "Agility", Type: StatTypeInteger}
	registry.Actions["strike"] = ActionDefinition{ID: "strike", Name: "Strike", Rolls: []RollSpec{{ID: "attack", Name: "Attack", Count: 2, Sides: 10, ModifierStat: "agility", ModifierFixed: 0.5}}}
	character := NewCharacterInstance("hero", nil)
	character.Name = "Hero"
	character.StatOverrides["agility"] = IntegerStatValue(2)
	character.AddedActionIDs = []string{"strike"}
	character.Persistent = true
	addToken := func(scene *Scene, tokenID string) {
		token := Token{ID: tokenID, Name: "Hero", FloorID: firstFloorID(scene), X: 50, Y: 60, Size: 80, Opacity: 1, Color: "#ffffff", OwnerIDs: []string{aliceMember.ID}, CharacterInstanceID: character.ID}
		token.LayerID = layerIDByKind(scene, token.FloorID, layerKindTokens)
		scene.Tokens[token.ID] = token
		scene.rebuildRuntime()
	}
	addToken(sceneA, "token-a")
	addToken(sceneB, "token-b")
	session := &Session{
		ID: "dice-campaign", Name: "Dice", Invite: "invite", Members: map[string]*Member{
			gmMember.ID: gmMember, aliceMember.ID: aliceMember, bobMember.ID: bobMember,
		}, Assets: map[string]Asset{}, Keys: map[string]string{
			gmMember.Secret: gmMember.ID, aliceMember.Secret: aliceMember.ID, bobMember.Secret: bobMember.ID,
		}, Receipts: map[string]Receipt{}, CampaignRevision: 1, RegistryRevision: 1, CharacterRevision: 1,
		DefinitionOperations: map[string]DefinitionOperationReceipt{}, Scenes: map[string]*Scene{sceneA.ID: sceneA, sceneB.ID: sceneB},
		Ruleset: cloneRulesetSnapshot(server.defaultRuleset), CampaignDefinitions: registry,
		CharacterInstances: map[string]CharacterInstance{character.ID: character}, RollHistory: []RollEvent{},
	}
	rebuildCharacterReferences(session)
	server.sessions[session.ID] = session
	rng := &deterministicDiceRNG{values: []int{0, 9, 4, 3}, failAt: -1}
	server.rollService = NewRollService(rng)
	server.rollService.now = func() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) }
	server.rollService.newEventID = func() string { return fmt.Sprintf("roll-%d", rng.next) }
	server.dirty = true
	if err := server.saveLocked(); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server.routes())
	t.Cleanup(host.Close)
	credentials := func(member *Member) map[string]string {
		return map[string]string{"session": session.ID, "key": member.Secret}
	}
	return diceCommandFixture{
		root: root, server: server, host: host, session: session, sceneA: sceneA, sceneB: sceneB,
		gm: credentials(gmMember), alice: credentials(aliceMember), bob: credentials(bobMember), rng: rng,
		actionRoll: RollRequest{CharacterInstanceID: character.ID, ActionID: "strike", RollSpecID: "attack"}, characterID: character.ID,
	}
}

func subscribeDiceScene(t *testing.T, connection *websocket.Conn, sceneID string) map[string]json.RawMessage {
	t.Helper()
	if err := connection.WriteJSON(Command{Type: "subscribe", SceneID: sceneID}); err != nil {
		t.Fatal(err)
	}
	read(t, connection, "snapshot")
	return read(t, connection, "rollHistory")
}

func rollEventFromMessage(t *testing.T, message map[string]json.RawMessage, key string) RollEvent {
	t.Helper()
	var event RollEvent
	if err := json.Unmarshal(message[key], &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func rollEventsFromHistory(t *testing.T, message map[string]json.RawMessage) []RollEvent {
	t.Helper()
	var events []RollEvent
	if err := json.Unmarshal(message["events"], &events); err != nil {
		t.Fatal(err)
	}
	return events
}

func rawBool(t *testing.T, value json.RawMessage) bool {
	t.Helper()
	var result bool
	if err := json.Unmarshal(value, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestReliableRollLostACKReconnectRestartAndDigest(t *testing.T) {
	fixture := newDiceCommandFixture(t)
	alice := dialRaw(t, fixture.host.URL, fixture.alice)
	read(t, alice, "campaignSnapshot")
	history := subscribeDiceScene(t, alice, fixture.sceneA.ID)
	if !rawBool(t, history["replayed"]) || len(rollEventsFromHistory(t, history)) != 0 {
		t.Fatalf("initial replay envelope = %#v", history)
	}
	observer := dialRaw(t, fixture.host.URL, fixture.gm)
	read(t, observer, "campaignSnapshot")
	subscribeDiceScene(t, observer, fixture.sceneA.ID)

	command := Command{Type: "roll", Client: "dice-tab", Seq: 1, SceneID: fixture.sceneA.ID, Roll: &fixture.actionRoll}
	if err := alice.WriteJSON(command); err != nil {
		t.Fatal(err)
	}
	live := read(t, alice, "rollEvent")
	if rawBool(t, live["replayed"]) {
		t.Fatal("live roll marked as replay")
	}
	event := rollEventFromMessage(t, live, "event")
	observed := rollEventFromMessage(t, read(t, observer, "rollEvent"), "event")
	if !reflect.DeepEqual(event, observed) || event.SceneID != fixture.sceneA.ID || event.Total != 13.5 {
		t.Fatalf("subscribers did not receive the same authoritative event: %#v / %#v", event, observed)
	}
	if fixture.rng.next != 2 {
		t.Fatalf("action RNG calls = %d", fixture.rng.next)
	}

	// Simulate a lost ACK: discard this connection after receiving only the live event.
	alice.Close()
	alice = dialRaw(t, fixture.host.URL, fixture.alice)
	read(t, alice, "campaignSnapshot")
	history = subscribeDiceScene(t, alice, fixture.sceneA.ID)
	replayed := rollEventsFromHistory(t, history)
	if !rawBool(t, history["replayed"]) || len(replayed) != 1 || !reflect.DeepEqual(replayed[0], event) {
		t.Fatalf("reconnect history = %#v", history)
	}
	if err := alice.WriteJSON(command); err != nil {
		t.Fatal(err)
	}
	ack := read(t, alice, "ack")
	if !rawBool(t, ack["replayed"]) || !reflect.DeepEqual(rollEventFromMessage(t, ack, "rollEvent"), event) || fixture.rng.next != 2 {
		t.Fatalf("lost ACK replay changed result or reran RNG: %#v", ack)
	}

	conflict := command
	conflict.Roll = &RollRequest{Count: rollInt(1), Sides: rollInt(20)}
	if err := alice.WriteJSON(conflict); err != nil {
		t.Fatal(err)
	}
	read(t, alice, "fatal")
	if fixture.rng.next != 2 {
		t.Fatal("same seq/different payload reached RNG")
	}

	fixture.host.Close()
	restarted := testServer(t, fixture.root)
	restartRNG := &deterministicDiceRNG{values: []int{5}, failAt: -1}
	restarted.rollService = NewRollService(restartRNG)
	host := httptest.NewServer(restarted.routes())
	defer host.Close()
	afterRestart := dialRaw(t, host.URL, fixture.alice)
	read(t, afterRestart, "campaignSnapshot")
	history = subscribeDiceScene(t, afterRestart, fixture.sceneA.ID)
	if got := rollEventsFromHistory(t, history); len(got) != 1 || !reflect.DeepEqual(got[0], event) {
		t.Fatalf("restart history = %#v", got)
	}
	if err := afterRestart.WriteJSON(command); err != nil {
		t.Fatal(err)
	}
	ack = read(t, afterRestart, "ack")
	if !reflect.DeepEqual(rollEventFromMessage(t, ack, "rollEvent"), event) || restartRNG.next != 0 {
		t.Fatalf("restart receipt replay changed result: %#v calls=%d", ack, restartRNG.next)
	}
}

func TestRollHistoryBoundedAndReceiptSurvivesEviction(t *testing.T) {
	fixture := newDiceCommandFixture(t)
	alice := dialRaw(t, fixture.host.URL, fixture.alice)
	read(t, alice, "campaignSnapshot")
	subscribeDiceScene(t, alice, fixture.sceneA.ID)
	command := Command{Type: "roll", Client: "eviction", Seq: 1, SceneID: fixture.sceneA.ID, Roll: &fixture.actionRoll}
	if err := alice.WriteJSON(command); err != nil {
		t.Fatal(err)
	}
	original := rollEventFromMessage(t, read(t, alice, "rollEvent"), "event")
	read(t, alice, "ack")

	fixture.server.mu.Lock()
	instance := fixture.session.CharacterInstances[fixture.characterID]
	instance.Persistent = false
	fixture.session.CharacterInstances[fixture.characterID] = instance
	for _, scene := range []*Scene{fixture.sceneA, fixture.sceneB} {
		tokenID := "token-a"
		if scene == fixture.sceneB {
			tokenID = "token-b"
		}
		token := scene.Tokens[tokenID]
		token.CharacterInstanceID = ""
		scene.Tokens[tokenID] = token
	}
	rebuildCharacterReferences(fixture.session)
	if _, collected := gcCharacterIfUnreferenced(fixture.session, fixture.characterID); !collected {
		fixture.server.mu.Unlock()
		t.Fatal("roll history retained an otherwise unreferenced character")
	}
	action := fixture.session.CampaignDefinitions.Actions["strike"]
	action.Name = "Renamed after roll"
	fixture.session.CampaignDefinitions.Actions[action.ID] = action
	if fixture.session.RollHistory[0].CharacterName != "Hero" || fixture.session.RollHistory[0].ActionName != "Strike" {
		fixture.server.mu.Unlock()
		t.Fatal("history did not retain character/action name snapshots")
	}
	for index := 0; index < MaxRollHistory; index++ {
		event := RollEvent{
			ID: fmt.Sprintf("later-%03d", index), SceneID: fixture.sceneB.ID, UserID: "gm", AuthorName: "Game Master",
			Count: 1, Sides: 4, Results: []int{1}, Total: 1, Timestamp: time.Date(2026, 10, 4, 13, 0, index, 0, time.UTC),
		}
		fixture.session.RollRevision++
		appendRollHistory(fixture.session, event)
	}
	fixture.server.dirty = true
	if err := fixture.server.saveLocked(); err != nil {
		fixture.server.mu.Unlock()
		t.Fatal(err)
	}
	if len(fixture.session.RollHistory) != MaxRollHistory || fixture.session.RollHistory[0].ID == original.ID {
		fixture.server.mu.Unlock()
		t.Fatal("campaign roll history was not bounded or oldest event remained")
	}
	fixture.server.mu.Unlock()

	fixture.host.Close()
	restarted := testServer(t, fixture.root)
	rng := &deterministicDiceRNG{values: []int{7}, failAt: -1}
	restarted.rollService = NewRollService(rng)
	host := httptest.NewServer(restarted.routes())
	defer host.Close()
	connection := dialRaw(t, host.URL, fixture.alice)
	read(t, connection, "campaignSnapshot")
	history := subscribeDiceScene(t, connection, fixture.sceneA.ID)
	if got := rollEventsFromHistory(t, history); len(got) != 0 {
		t.Fatalf("evicted scene A history unexpectedly returned %d events", len(got))
	}
	if err := connection.WriteJSON(command); err != nil {
		t.Fatal(err)
	}
	ack := read(t, connection, "ack")
	if !reflect.DeepEqual(rollEventFromMessage(t, ack, "rollEvent"), original) || rng.next != 0 {
		t.Fatal("result-bearing receipt did not outlive history eviction")
	}
	connection.WriteJSON(Command{Type: "subscribe", SceneID: fixture.sceneB.ID})
	read(t, connection, "snapshot")
	history = read(t, connection, "rollHistory")
	if got := rollEventsFromHistory(t, history); len(got) != MaxRollHistory {
		t.Fatalf("scene B replay count = %d", len(got))
	}
}

func TestRollSaveFailureRollsBackAndDoesNotBroadcast(t *testing.T) {
	fixture := newDiceCommandFixture(t)
	alice := dialRaw(t, fixture.host.URL, fixture.alice)
	read(t, alice, "campaignSnapshot")
	subscribeDiceScene(t, alice, fixture.sceneA.ID)
	observer := dialRaw(t, fixture.host.URL, fixture.gm)
	read(t, observer, "campaignSnapshot")
	subscribeDiceScene(t, observer, fixture.sceneA.ID)
	blocker := filepath.Join(fixture.root, "sessions.json.tmp")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	command := Command{Type: "roll", Client: "failure", Seq: 1, SceneID: fixture.sceneA.ID, Roll: &fixture.actionRoll}
	if err := alice.WriteJSON(command); err != nil {
		t.Fatal(err)
	}
	read(t, alice, "saveError")
	fixture.server.mu.Lock()
	receipt, receiptExists := fixture.session.Receipts["alice:failure"]
	if fixture.session.RollRevision != 0 || len(fixture.session.RollHistory) != 0 || receiptExists || receipt.Seq != 0 {
		fixture.server.mu.Unlock()
		t.Fatalf("failed save leaked dice state: revision=%d history=%d receipt=%#v", fixture.session.RollRevision, len(fixture.session.RollHistory), receipt)
	}
	fixture.server.mu.Unlock()
	observer.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	var unexpected map[string]json.RawMessage
	if err := observer.ReadJSON(&unexpected); err == nil {
		t.Fatalf("failed save broadcast message: %#v", unexpected)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	observer = dialRaw(t, fixture.host.URL, fixture.gm)
	read(t, observer, "campaignSnapshot")
	subscribeDiceScene(t, observer, fixture.sceneA.ID)
	if err := alice.WriteJSON(command); err != nil {
		t.Fatal(err)
	}
	event := rollEventFromMessage(t, read(t, observer, "rollEvent"), "event")
	read(t, alice, "rollEvent")
	ack := read(t, alice, "ack")
	if !reflect.DeepEqual(event, rollEventFromMessage(t, ack, "rollEvent")) || fixture.rng.next != 4 {
		t.Fatalf("retry did not create exactly one persisted event: %#v calls=%d", ack, fixture.rng.next)
	}
}

func TestRollSceneAccessTwoScenesUnpublishAndDelete(t *testing.T) {
	fixture := newDiceCommandFixture(t)
	alice := dialRaw(t, fixture.host.URL, fixture.alice)
	read(t, alice, "campaignSnapshot")
	subscribeDiceScene(t, alice, fixture.sceneA.ID)
	first := Command{Type: "roll", Client: "scenes", Seq: 1, SceneID: fixture.sceneA.ID, Roll: &fixture.actionRoll}
	alice.WriteJSON(first)
	eventA := rollEventFromMessage(t, read(t, alice, "rollEvent"), "event")
	read(t, alice, "ack")
	subscribeDiceScene(t, alice, fixture.sceneB.ID)
	second := Command{Type: "roll", Client: "scenes", Seq: 2, SceneID: fixture.sceneB.ID, Roll: &fixture.actionRoll}
	alice.WriteJSON(second)
	eventB := rollEventFromMessage(t, read(t, alice, "rollEvent"), "event")
	read(t, alice, "ack")
	if eventA.SceneID == eventB.SceneID {
		t.Fatal("rolls from two scenes were not scoped")
	}
	subscribeDiceScene(t, alice, fixture.sceneA.ID)
	// The history envelope immediately preceding this subscription was consumed by the helper;
	// request a sync to inspect the same scene-filtered replay contract.
	alice.WriteJSON(Command{Type: "sync"})
	read(t, alice, "snapshot")
	history := read(t, alice, "rollHistory")
	if events := rollEventsFromHistory(t, history); len(events) != 1 || events[0].ID != eventA.ID {
		t.Fatalf("scene A history leaked another scene: %#v", events)
	}

	bob := dialRaw(t, fixture.host.URL, fixture.bob)
	read(t, bob, "campaignSnapshot")
	subscribeDiceScene(t, bob, fixture.sceneA.ID)
	denied := Command{Type: "roll", Client: "denied", Seq: 1, SceneID: fixture.sceneA.ID, Roll: &fixture.actionRoll}
	bob.WriteJSON(denied)
	ack := read(t, bob, "ack")
	var issue string
	json.Unmarshal(ack["error"], &issue)
	if issue == "" || ack["rollEvent"] != nil {
		t.Fatalf("unowned character roll was accepted: %#v", ack)
	}

	fixture.server.mu.Lock()
	fixture.sceneA.Published = false
	fixture.server.dirty = true
	if err := fixture.server.saveLocked(); err != nil {
		fixture.server.mu.Unlock()
		t.Fatal(err)
	}
	fixture.server.mu.Unlock()
	denied.Seq = 2
	bob.WriteJSON(denied)
	ack = read(t, bob, "ack")
	json.Unmarshal(ack["error"], &issue)
	if issue == "" {
		t.Fatal("roll in unpublished scene was accepted for player")
	}
	outsider := dialRaw(t, fixture.host.URL, fixture.bob)
	read(t, outsider, "campaignSnapshot")
	outsider.WriteJSON(Command{Type: "subscribe", SceneID: fixture.sceneA.ID})
	read(t, outsider, "error")

	fixture.server.mu.Lock()
	delete(fixture.session.Scenes, fixture.sceneA.ID)
	rebuildCharacterReferences(fixture.session)
	fixture.server.dirty = true
	if err := fixture.server.saveLocked(); err != nil {
		fixture.server.mu.Unlock()
		t.Fatal(err)
	}
	fixture.server.mu.Unlock()
	gm := dialRaw(t, fixture.host.URL, fixture.gm)
	read(t, gm, "campaignSnapshot")
	gm.WriteJSON(Command{Type: "subscribe", SceneID: fixture.sceneA.ID})
	read(t, gm, "error")
}
