package main

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

type deterministicDiceRNG struct {
	values []int
	next   int
	failAt int
}

func (rng *deterministicDiceRNG) Intn(upperBound int) (int, error) {
	if rng.failAt >= 0 && rng.next == rng.failAt {
		return 0, errors.New("injected RNG failure")
	}
	value := 0
	if len(rng.values) != 0 {
		value = rng.values[rng.next%len(rng.values)]
	}
	rng.next++
	return value % upperBound, nil
}

type diceFixture struct {
	session *Session
	scene   *Scene
	alice   *Member
	gm      *Member
	service *RollService
	rng     *deterministicDiceRNG
}

func rollInt(value int) *int { return &value }

func newDiceFixture(t *testing.T) diceFixture {
	t.Helper()
	alice := &Member{ID: "alice", Name: "Alice", Role: "player"}
	gm := &Member{ID: "gm", Name: "Game Master", Role: "gm", GM: true}
	scene := newScene("scene", "Scene")
	scene.Published = true
	character := NewCharacterInstance("hero-1", nil)
	character.Name = "Server Hero"
	token := Token{
		ID: "hero-token", Name: "Hero Token", FloorID: firstFloorID(scene), Size: 80, Opacity: 1,
		OwnerIDs: []string{alice.ID}, CharacterInstanceID: character.ID,
	}
	token.LayerID = layerIDByKind(scene, token.FloorID, layerKindTokens)
	scene.Tokens[token.ID] = token
	scene.rebuildRuntime()
	session := &Session{
		ID: "campaign", Members: map[string]*Member{alice.ID: alice, gm.ID: gm}, Scenes: map[string]*Scene{scene.ID: scene},
		CharacterInstances: map[string]CharacterInstance{character.ID: character},
	}
	rebuildCharacterReferences(session)
	rng := &deterministicDiceRNG{values: []int{0}, failAt: -1}
	service := NewRollService(rng)
	service.now = func() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, time.FixedZone("test", 3*60*60)) }
	service.newEventID = func() string { return "server-event" }
	return diceFixture{session: session, scene: scene, alice: alice, gm: gm, service: service, rng: rng}
}

func TestManualRollDeterministic10d10(t *testing.T) {
	fixture := newDiceFixture(t)
	fixture.rng.values = []int{0, 6, 2, 9, 3, 3, 7, 1, 8, 5}
	event, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, RollRequest{Count: rollInt(10), Sides: rollInt(10)})
	if err != nil {
		t.Fatal(err)
	}
	wantResults := []int{1, 7, 3, 10, 4, 4, 8, 2, 9, 6}
	if !reflect.DeepEqual(event.Results, wantResults) || event.Total != 54 || event.Modifier != 0 {
		t.Fatalf("unexpected deterministic roll: %#v", event)
	}
	if event.ID != "server-event" || event.SceneID != fixture.scene.ID || event.UserID != fixture.alice.ID || event.AuthorName != fixture.alice.Name {
		t.Fatalf("server identity fields were not captured: %#v", event)
	}
	if !event.Timestamp.Equal(time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("timestamp = %v", event.Timestamp)
	}
}

func TestManualCompoundRollPreservesGroupsAndTotal(t *testing.T) {
	fixture := newDiceFixture(t)
	fixture.rng.values = []int{9, 0, 19}
	event, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, RollRequest{Groups: []DiceGroup{{Count: 1, Sides: 10}, {Count: 2, Sides: 20}}})
	if err != nil {
		t.Fatal(err)
	}
	wantGroups := []DiceGroup{{Count: 1, Sides: 10, Results: []int{10}}, {Count: 2, Sides: 20, Results: []int{1, 20}}}
	if event.Count != 0 || event.Sides != 0 || !reflect.DeepEqual(event.Groups, wantGroups) || !reflect.DeepEqual(event.Results, []int{10, 1, 20}) || event.Total != 31 {
		t.Fatalf("unexpected compound roll: %#v", event)
	}
	if err := validateRollEvent(event); err != nil {
		t.Fatalf("compound event is not persistable: %v", err)
	}
	fixture.rng.next = 0
	single, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, RollRequest{Groups: []DiceGroup{{Count: 1, Sides: 10}}})
	if err != nil || single.Count != 1 || single.Sides != 10 || len(single.Groups) != 0 {
		t.Fatalf("single group did not retain legacy event shape: %#v, %v", single, err)
	}
}

func TestManualRollSupportedSidesAndCountBounds(t *testing.T) {
	for _, sides := range []int{4, 6, 8, 10, 12, 20} {
		t.Run(string(rune('a'+sides)), func(t *testing.T) {
			fixture := newDiceFixture(t)
			fixture.rng.values = []int{sides - 1}
			event, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, RollRequest{Count: rollInt(1), Sides: rollInt(sides)})
			if err != nil || len(event.Results) != 1 || event.Results[0] != sides {
				t.Fatalf("d%d: event=%#v err=%v", sides, event, err)
			}
		})
	}
	for _, test := range []struct {
		count int
		ok    bool
	}{{0, false}, {1, true}, {100, true}, {101, false}} {
		fixture := newDiceFixture(t)
		event, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, RollRequest{Count: rollInt(test.count), Sides: rollInt(6)})
		if (err == nil) != test.ok {
			t.Fatalf("count %d: event=%#v err=%v", test.count, event, err)
		}
	}
	fixture := newDiceFixture(t)
	if _, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, RollRequest{Count: rollInt(1), Sides: rollInt(100)}); err == nil {
		t.Fatal("unsupported sides accepted")
	}
	if _, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, RollRequest{Groups: []DiceGroup{{Count: 60, Sides: 10}, {Count: 41, Sides: 20}}}); err == nil {
		t.Fatal("compound roll above the total dice limit accepted")
	}
	var fractional RollRequest
	if err := json.Unmarshal([]byte(`{"count":1.5,"sides":6}`), &fractional); err == nil {
		t.Fatal("fractional dice count decoded as an integer request")
	}
}

func TestManualRollRejectsActionFieldsBeforeRNG(t *testing.T) {
	fixture := newDiceFixture(t)
	for _, request := range []RollRequest{
		{ActionID: "strike"},
		{RollSpecID: "damage"},
		{CharacterInstanceID: "hero-1", ActionID: "strike", RollSpecID: "damage", Count: rollInt(1), Sides: rollInt(6)},
	} {
		if _, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, request); err == nil || err.Error() != v04RollCommandIssue {
			t.Fatalf("expected actionExecute diagnostic, got %v", err)
		}
	}
	if fixture.rng.next != 0 {
		t.Fatal("obsolete action request reached RNG")
	}
}

func TestRollRejectsSpoofedAuthorityAndUsesCanonicalAuthor(t *testing.T) {
	fixture := newDiceFixture(t)
	request := RollRequest{CharacterInstanceID: "hero-1", Count: rollInt(2), Sides: rollInt(10)}
	request.Results = []int{10, 10}
	if _, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, request); err == nil {
		t.Fatal("client results accepted")
	}
	request.Results = nil
	request.Groups = []DiceGroup{{Count: 1, Sides: 20, Results: []int{20}}}
	if _, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, request); err == nil {
		t.Fatal("client grouped results accepted")
	}
	request.Groups = nil
	request.AuthorName = "Mallory"
	if _, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, request); err == nil {
		t.Fatal("client author accepted")
	}
	request.AuthorName = ""
	forgedMember := &Member{ID: fixture.alice.ID, Name: "Mallory", Role: "gm", GM: true}
	event, err := fixture.service.Execute(fixture.session, fixture.scene.ID, forgedMember, request)
	if err != nil {
		t.Fatal(err)
	}
	if event.AuthorName != fixture.alice.Name || event.UserID != fixture.alice.ID {
		t.Fatalf("forged member metadata reached event: %#v", event)
	}
}

func TestCharacterRollRequiresAccessibleLinkedTokenInScene(t *testing.T) {
	fixture := newDiceFixture(t)
	request := RollRequest{CharacterInstanceID: "hero-1", Count: rollInt(1), Sides: rollInt(10)}
	token := fixture.scene.Tokens["hero-token"]
	token.Hidden = true
	fixture.scene.Tokens[token.ID] = token
	if _, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, request); err == nil {
		t.Fatal("player rolled through hidden token")
	}
	if _, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.gm, request); err != nil {
		t.Fatalf("GM could not roll through linked hidden token: %v", err)
	}
	other := newScene("other", "Other")
	other.Published = true
	fixture.session.Scenes[other.ID] = other
	if _, err := fixture.service.Execute(fixture.session, other.ID, fixture.gm, request); err == nil {
		t.Fatal("character without a token in target scene was accepted")
	}
}

func TestRollRejectsUnsafeTotalBeforeRNG(t *testing.T) {
	fixture := newDiceFixture(t)
	modifier := float64(MaxSafeInteger)
	_, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, RollRequest{Count: rollInt(1), Sides: rollInt(6), ManualModifier: &modifier})
	if err == nil || fixture.rng.next != 0 {
		t.Fatalf("unsafe total should fail before RNG: calls=%d err=%v", fixture.rng.next, err)
	}
	if err := validateRollNumber("test", math.Inf(1)); err == nil {
		t.Fatal("non-finite total accepted")
	}
}

func TestRollRNGFailureReturnsNoPartialEvent(t *testing.T) {
	fixture := newDiceFixture(t)
	fixture.rng.values = []int{1, 2, 3}
	fixture.rng.failAt = 2
	event, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, RollRequest{Count: rollInt(10), Sides: rollInt(10)})
	if err == nil || !reflect.DeepEqual(event, RollEvent{}) {
		t.Fatalf("RNG failure returned partial event: event=%#v err=%v", event, err)
	}
}

func TestCryptoDiceRNGRange(t *testing.T) {
	rng := CryptoDiceRNG{}
	for _, sides := range []int{4, 6, 8, 10, 12, 20} {
		value, err := rng.Intn(sides)
		if err != nil || value < 0 || value >= sides {
			t.Fatalf("crypto RNG d%d returned %d, %v", sides, value, err)
		}
	}
}

func TestManualCompoundRollWithModifier(t *testing.T) {
	fixture := newDiceFixture(t)
	fixture.rng.values = []int{19, 4, 5}
	bonus := 3.0
	request := RollRequest{Groups: []DiceGroup{{Count: 2, Sides: 20}, {Count: 1, Sides: 6}}, ManualModifier: &bonus}
	event, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, request)
	if err != nil {
		t.Fatal(err)
	}
	if event.Total != 34 || event.Modifier != 3 || !reflect.DeepEqual(event.Results, []int{20, 5, 6}) || len(event.Groups) != 2 {
		t.Fatalf("compound modifier result = %#v", event)
	}
	if err := validateRollEvent(event); err != nil {
		t.Fatalf("compound modifier event invalid: %v", err)
	}
	minus := -2.0
	fixture.rng.values = []int{5}
	single, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, RollRequest{Count: rollInt(1), Sides: rollInt(6), ManualModifier: &minus})
	if err != nil || single.Modifier != -2 || single.Total != 4 {
		t.Fatalf("single modifier event=%#v err=%v", single, err)
	}
	tooLarge := float64(MaxSafeInteger) + 10
	if _, err := fixture.service.Execute(fixture.session, fixture.scene.ID, fixture.alice, RollRequest{Count: rollInt(1), Sides: rollInt(6), ManualModifier: &tooLarge}); err == nil {
		t.Fatal("unbounded modifier accepted")
	}
}
