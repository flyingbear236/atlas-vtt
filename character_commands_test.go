package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type characterCommandFixture struct {
	server                  *Server
	host                    *httptest.Server
	gm, alice, bob, charlie map[string]string
	session                 *Session
	sceneA, sceneB          *Scene
	tokens                  map[string]string
}

func boolPointer(value bool) *bool { return &value }

func characterIDFromACK(t *testing.T, message map[string]json.RawMessage) string {
	t.Helper()
	var characterID string
	if err := json.Unmarshal(message["characterId"], &characterID); err != nil {
		t.Fatal(err)
	}
	return characterID
}

func writeCharacterCommand(t *testing.T, connection interface{ WriteJSON(any) error }, command Command) {
	t.Helper()
	if err := connection.WriteJSON(command); err != nil {
		t.Fatal(err)
	}
}

func newCharacterCommandFixture(t *testing.T) characterCommandFixture {
	t.Helper()
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	t.Cleanup(host.Close)
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Characters"})
	alice := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Alice"})
	bob := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Bob"})
	charlie := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Charlie"})

	server.mu.Lock()
	session := server.sessions[gm["session"]]
	var sceneA *Scene
	for _, scene := range session.Scenes {
		sceneA = scene
		break
	}
	sceneA.Published = true
	sceneB := newScene("second-scene", "Second")
	sceneB.Published = true
	session.Scenes[sceneB.ID] = sceneB
	session.CampaignDefinitions.Stats["hp"] = StatDefinition{ID: "hp", Name: "HP", Type: StatTypeInteger}
	session.CampaignDefinitions.Stats["dexterity"] = StatDefinition{ID: "dexterity", Name: "Dexterity", Type: StatTypeInteger}
	session.CampaignDefinitions.Actions["bite"] = ActionDefinition{ID: "bite", Name: "Bite"}
	session.CampaignDefinitions.Presets["wolf"] = CharacterPresetDefinition{
		ID: "wolf", Name: "Wolf", Kind: "monster",
		Stats: map[string]StatValue{"hp": IntegerStatValue(11), "dexterity": IntegerStatValue(15)}, ActionIDs: []string{"bite"},
	}
	session.RegistryRevision++
	session.Assets["avatar"] = Asset{ID: "avatar", Kind: assetKindAvatar, RetentionPolicy: assetReclaimable}

	aliceID, bobID := session.Keys[alice["key"]], session.Keys[bob["key"]]
	tokens := map[string]string{}
	addToken := func(scene *Scene, name string, owners []string) {
		tokenID := "token-" + name
		token := Token{ID: tokenID, Name: name, FloorID: firstFloorID(scene), X: 100, Y: 100, Size: 80, Color: "#abcdef", OwnerIDs: owners}
		token.LayerID = layerIDByKind(scene, token.FloorID, layerKindTokens)
		scene.Tokens[tokenID] = token
		tokens[name] = tokenID
	}
	for index := 1; index <= 5; index++ {
		owners := []string{}
		if index == 1 {
			owners = []string{aliceID, bobID}
		}
		addToken(sceneA, "wolf"+string(rune('0'+index)), owners)
	}
	addToken(sceneA, "red", []string{aliceID})
	addToken(sceneA, "lancelot", []string{aliceID})
	addToken(sceneA, "cross-a", []string{aliceID})
	addToken(sceneB, "cross-b", []string{aliceID})
	addToken(sceneB, "return", []string{aliceID})
	sceneA.rebuildRuntime()
	sceneB.rebuildRuntime()
	rebuildCharacterReferences(session)
	server.dirty = true
	if err := server.saveLocked(); err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	server.mu.Unlock()
	return characterCommandFixture{server: server, host: host, gm: gm, alice: alice, bob: bob, charlie: charlie, session: session, sceneA: sceneA, sceneB: sceneB, tokens: tokens}
}

func TestCharacterCommandsScenariosAThroughF(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	gmWS := dialRaw(t, fixture.host.URL, fixture.gm)
	read(t, gmWS, "campaignSnapshot")
	aliceWS := dialRaw(t, fixture.host.URL, fixture.alice)
	read(t, aliceWS, "campaignSnapshot")
	bobWS := dialRaw(t, fixture.host.URL, fixture.bob)
	read(t, bobWS, "campaignSnapshot")
	charlieWS := dialRaw(t, fixture.host.URL, fixture.charlie)
	read(t, charlieWS, "campaignSnapshot")

	wolves := make([]string, 0, 5)
	for index := 1; index <= 5; index++ {
		command := Command{Type: "characterCreate", Client: "wolves", Seq: uint64(index), SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["wolf"+string(rune('0'+index))], PresetID: "wolf"}
		writeCharacterCommand(t, gmWS, command)
		ack := read(t, gmWS, "ack")
		if issue := ackError(t, ack); issue != "" {
			t.Fatal(issue)
		}
		wolves = append(wolves, characterIDFromACK(t, ack))
		if index == 1 {
			writeCharacterCommand(t, gmWS, command)
			replayed := read(t, gmWS, "ack")
			if replayedID := characterIDFromACK(t, replayed); replayedID != wolves[0] {
				t.Fatalf("lost ACK created another character: %q != %q", replayedID, wolves[0])
			}
		}
	}
	seen := map[string]bool{}
	for _, characterID := range wolves {
		if characterID == "" || seen[characterID] {
			t.Fatalf("preset assignments are not independent: %#v", wolves)
		}
		seen[characterID] = true
	}
	// Stage 08 exposes character changes only through an explicit, single-slot
	// watch instead of broadcasting campaign character IDs.
	if err := bobWS.WriteJSON(Command{Type: "characterWatch", CharacterID: wolves[0], CharacterWatch: 1}); err != nil {
		t.Fatal(err)
	}
	read(t, bobWS, "characterSnapshot")

	writeCharacterCommand(t, aliceWS, Command{Type: "characterStatSet", Client: "alice-stats", Seq: 1, CharacterID: wolves[0], StatID: "hp", StatValue: statValuePointer(IntegerStatValue(4))})
	if issue := ackError(t, read(t, aliceWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	changedForBob := read(t, bobWS, "characterSnapshot")
	var changedCharacterID string
	if err := json.Unmarshal(changedForBob["characterId"], &changedCharacterID); err != nil || changedCharacterID != wolves[0] {
		t.Fatalf("co-owner did not receive character change: %v %q", err, changedCharacterID)
	}
	writeCharacterCommand(t, bobWS, Command{Type: "characterStatSet", Client: "bob-stats", Seq: 1, CharacterID: wolves[0], StatID: "dexterity", StatValue: statValuePointer(IntegerStatValue(99))})
	if issue := ackError(t, read(t, bobWS, "ack")); issue != "" {
		t.Fatal(issue)
	}

	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "wolves", Seq: 6, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["red"], PresetID: "wolf"})
	redACK := read(t, gmWS, "ack")
	redID := characterIDFromACK(t, redACK)
	writeCharacterCommand(t, aliceWS, Command{Type: "characterStatSet", Client: "alice-stats", Seq: 2, CharacterID: redID, StatID: "fur_color", StatValue: statValuePointer(StringStatValue("red"))})
	if issue := ackError(t, read(t, aliceWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	writeCharacterCommand(t, aliceWS, Command{Type: "characterStatSet", Client: "alice-stats", Seq: 3, CharacterID: redID, StatID: "hp", StatValue: statValuePointer(IntegerStatValue(20))})
	if issue := ackError(t, read(t, aliceWS, "ack")); issue != "" {
		t.Fatal(issue)
	}

	fixture.server.mu.Lock()
	revision := fixture.session.RegistryRevision
	fixture.server.mu.Unlock()
	updatedPreset := clonePresetDefinition(fixture.session.CampaignDefinitions.Presets["wolf"])
	updatedPreset.Stats["dexterity"] = IntegerStatValue(16)
	writeCharacterCommand(t, gmWS, Command{Type: "definitionUpdate", Client: "preset-update", Seq: 1, DefinitionKind: definitionKindPreset, DefinitionID: "wolf", PresetDefinition: &updatedPreset, ExpectedRegistryRevision: registryRevisionPointer(revision)})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}

	writeCharacterCommand(t, charlieWS, Command{Type: "characterStatSet", Client: "charlie", Seq: 1, CharacterID: wolves[0], StatID: "hp", StatValue: statValuePointer(IntegerStatValue(1))})
	if issue := ackError(t, read(t, charlieWS, "ack")); !strings.Contains(issue, "access denied") {
		t.Fatalf("Charlie changed a character: %q", issue)
	}
	writeCharacterCommand(t, aliceWS, Command{Type: "characterSetPersistent", Client: "alice-admin", Seq: 1, CharacterID: wolves[0], Persistent: boolPointer(true)})
	if issue := ackError(t, read(t, aliceWS, "ack")); !strings.Contains(issue, "GM") {
		t.Fatalf("owner changed persistent: %q", issue)
	}
	writeCharacterCommand(t, aliceWS, Command{Type: "characterActionRemove", Client: "alice-admin", Seq: 2, CharacterID: wolves[0], ActionID: "bite"})
	if issue := ackError(t, read(t, aliceWS, "ack")); !strings.Contains(issue, "GM") {
		t.Fatalf("owner changed actions: %q", issue)
	}
	writeCharacterCommand(t, aliceWS, Command{Type: "characterLink", Client: "alice-admin", Seq: 3, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["red"], CharacterID: wolves[0]})
	if issue := ackError(t, read(t, aliceWS, "ack")); !strings.Contains(issue, "GM") {
		t.Fatalf("owner relinked a token: %q", issue)
	}
	writeCharacterCommand(t, aliceWS, Command{Type: "characterCreate", Client: "alice-admin", Seq: 4, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["red"], PresetID: "wolf"})
	if issue := ackError(t, read(t, aliceWS, "ack")); !strings.Contains(issue, "GM") {
		t.Fatalf("owner changed preset through character creation: %q", issue)
	}

	writeCharacterCommand(t, aliceWS, Command{Type: "characterAvatarSet", Client: "alice-stats", Seq: 4, CharacterID: wolves[0], AvatarAssetID: stringPointer("avatar")})
	if issue := ackError(t, read(t, aliceWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	writeCharacterCommand(t, aliceWS, Command{Type: "characterAvatarReset", Client: "alice-stats", Seq: 5, CharacterID: wolves[0]})
	if issue := ackError(t, read(t, aliceWS, "ack")); issue != "" {
		t.Fatal(issue)
	}

	fixture.server.mu.Lock()
	registry, err := MergeDefinitionRegistries(sessionRegistries(fixture.session))
	if err != nil {
		fixture.server.mu.Unlock()
		t.Fatal(err)
	}
	first := fixture.session.CharacterInstances[wolves[0]]
	second := fixture.session.CharacterInstances[wolves[1]]
	red := fixture.session.CharacterInstances[redID]
	firstEffective := effectiveCharacterForTest(first, registry)
	secondEffective := effectiveCharacterForTest(second, registry)
	if first.StatOverrides["hp"].Integer() != 4 || second.StatOverrides["hp"].Type() != "" || firstEffective.Stats["dexterity"].Integer() != 99 || secondEffective.Stats["dexterity"].Integer() != 16 {
		fixture.server.mu.Unlock()
		t.Fatal("wolf instances do not preserve independent overrides and preset inheritance")
	}
	if red.PresetID != "wolf" || red.StatOverrides["fur_color"].String() != "red" || fixture.session.CampaignDefinitions.Stats["fur_color"].Type != StatTypeString {
		fixture.server.mu.Unlock()
		t.Fatal("unknown stat and red wolf override were not saved atomically")
	}
	if fixture.session.Assets["avatar"].OrphanSince == nil {
		fixture.server.mu.Unlock()
		t.Fatal("avatar reset did not refresh campaign asset references")
	}
	fixture.server.mu.Unlock()

	writeCharacterCommand(t, gmWS, Command{Type: "characterActionRemove", Client: "wolf-actions", Seq: 1, CharacterID: wolves[0], ActionID: "bite"})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	writeCharacterCommand(t, gmWS, Command{Type: "characterActionAdd", Client: "wolf-actions", Seq: 2, CharacterID: wolves[0], ActionID: "bite"})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	fixture.server.mu.Lock()
	first = fixture.session.CharacterInstances[wolves[0]]
	if len(first.AddedActionIDs) != 1 || first.AddedActionIDs[0] != "bite" || len(first.RemovedActionIDs) != 0 {
		fixture.server.mu.Unlock()
		t.Fatalf("action add/remove normalization failed: %#v", first)
	}
	fixture.server.mu.Unlock()

	writeCharacterCommand(t, aliceWS, Command{Type: "characterStatReset", Client: "alice-stats", Seq: 6, CharacterID: redID, StatID: "fur_color"})
	if issue := ackError(t, read(t, aliceWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	fixture.server.mu.Lock()
	_, overrideRemains := fixture.session.CharacterInstances[redID].StatOverrides["fur_color"]
	_, definitionRemains := fixture.session.CampaignDefinitions.Stats["fur_color"]
	fixture.server.mu.Unlock()
	if overrideRemains || !definitionRemains {
		t.Fatal("stat reset did not remove only the instance override")
	}

	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "lancelot", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["lancelot"], CharacterName: "Sir Lancelot", Persistent: boolPointer(true)})
	lancelotID := characterIDFromACK(t, read(t, gmWS, "ack"))
	writeCharacterCommand(t, gmWS, Command{Type: "characterStatSet", Client: "lancelot", Seq: 2, CharacterID: lancelotID, StatID: "hp", StatValue: statValuePointer(IntegerStatValue(7))})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	writeCharacterCommand(t, gmWS, Command{Type: "delete", Client: "delete-lancelot", Seq: 1, SceneID: fixture.sceneA.ID, Token: Token{ID: fixture.tokens["lancelot"]}})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	writeCharacterCommand(t, gmWS, Command{Type: "characterLink", Client: "lancelot", Seq: 3, SceneID: fixture.sceneB.ID, TokenID: fixture.tokens["return"], CharacterID: lancelotID})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	fixture.server.mu.Lock()
	if fixture.session.CharacterInstances[lancelotID].StatOverrides["hp"].Integer() != 7 || fixture.sceneB.Tokens[fixture.tokens["return"]].CharacterInstanceID != lancelotID {
		fixture.server.mu.Unlock()
		t.Fatal("persistent character did not survive token removal and relink")
	}
	fixture.server.mu.Unlock()

	for index, characterID := range append(wolves, redID) {
		tokenName := "red"
		if index < len(wolves) {
			tokenName = "wolf" + string(rune('1'+index))
		}
		writeCharacterCommand(t, gmWS, Command{Type: "delete", Client: "delete-wolves", Seq: uint64(index + 1), SceneID: fixture.sceneA.ID, Token: Token{ID: fixture.tokens[tokenName]}})
		if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
			t.Fatal(issue)
		}
		fixture.server.mu.Lock()
		_, remains := fixture.session.CharacterInstances[characterID]
		fixture.server.mu.Unlock()
		if remains {
			t.Fatalf("transient character %q survived its last token", characterID)
		}
	}
}

func TestCharacterGCIndexRelinkSceneDeleteAndSaveRollback(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	gmWS := dialRaw(t, fixture.host.URL, fixture.gm)
	read(t, gmWS, "campaignSnapshot")
	aliceWS := dialRaw(t, fixture.host.URL, fixture.alice)
	read(t, aliceWS, "campaignSnapshot")

	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "cross", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["cross-a"], CharacterName: "Transient"})
	transientID := characterIDFromACK(t, read(t, gmWS, "ack"))
	writeCharacterCommand(t, gmWS, Command{Type: "characterLink", Client: "cross", Seq: 2, SceneID: fixture.sceneB.ID, TokenID: fixture.tokens["cross-b"], CharacterID: transientID})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	writeCharacterCommand(t, gmWS, Command{Type: "delete", Client: "cross-delete", Seq: 1, SceneID: fixture.sceneA.ID, Token: Token{ID: fixture.tokens["cross-a"]}})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	fixture.server.mu.Lock()
	if _, exists := fixture.session.CharacterInstances[transientID]; !exists || len(fixture.session.characterReferences[transientID]) != 1 {
		fixture.server.mu.Unlock()
		t.Fatal("cross-scene reference did not protect transient character")
	}
	fixture.server.mu.Unlock()

	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "persistent", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["lancelot"], CharacterName: "Persistent", Persistent: boolPointer(true)})
	persistentID := characterIDFromACK(t, read(t, gmWS, "ack"))

	root := fixture.server.root
	blocker := filepath.Join(root, "sessions.json.tmp")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	relink := Command{Type: "characterLink", Client: "rollback", Seq: 1, SceneID: fixture.sceneB.ID, TokenID: fixture.tokens["cross-b"], CharacterID: persistentID}
	writeCharacterCommand(t, gmWS, relink)
	read(t, gmWS, "saveError")
	fixture.server.mu.Lock()
	if fixture.sceneB.Tokens[fixture.tokens["cross-b"]].CharacterInstanceID != transientID || len(fixture.session.characterReferences[transientID]) != 1 || len(fixture.session.characterReferences[persistentID]) != 1 {
		fixture.server.mu.Unlock()
		t.Fatal("save failure did not restore token linkage and reference index")
	}
	if _, exists := fixture.session.CharacterInstances[transientID]; !exists {
		fixture.server.mu.Unlock()
		t.Fatal("save failure leaked transient GC")
	}
	fixture.server.mu.Unlock()
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	writeCharacterCommand(t, gmWS, relink)
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	fixture.server.mu.Lock()
	if fixture.sceneB.Tokens[fixture.tokens["cross-b"]].CharacterInstanceID != persistentID {
		fixture.server.mu.Unlock()
		t.Fatal("relink retry did not install the new character")
	}
	if _, exists := fixture.session.CharacterInstances[transientID]; exists {
		fixture.server.mu.Unlock()
		t.Fatal("relink did not collect the replaced transient character")
	}
	fixture.server.mu.Unlock()

	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	atomicStat := Command{Type: "characterStatSet", Client: "atomic-stat", Seq: 1, CharacterID: persistentID, StatID: "new_stat", StatValue: statValuePointer(BooleanStatValue(true))}
	writeCharacterCommand(t, aliceWS, atomicStat)
	read(t, aliceWS, "saveError")
	fixture.server.mu.Lock()
	_, definitionLeaked := fixture.session.CampaignDefinitions.Stats["new_stat"]
	_, valueLeaked := fixture.session.CharacterInstances[persistentID].StatOverrides["new_stat"]
	aliceID := fixture.session.Keys[fixture.alice["key"]]
	receiptLeaked := fixture.session.Receipts[aliceID+":atomic-stat"].Seq != 0
	fixture.server.mu.Unlock()
	if definitionLeaked || valueLeaked || receiptLeaked {
		t.Fatal("failed unknown-stat save leaked definition, value, or receipt")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	writeCharacterCommand(t, aliceWS, atomicStat)
	if issue := ackError(t, read(t, aliceWS, "ack")); issue != "" {
		t.Fatal(issue)
	}

	writeCharacterCommand(t, gmWS, Command{Type: "characterUnlink", Client: "persistent", Seq: 2, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["lancelot"]})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	writeCharacterCommand(t, gmWS, Command{Type: "characterUnlink", Client: "persistent", Seq: 3, SceneID: fixture.sceneB.ID, TokenID: fixture.tokens["cross-b"]})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	fixture.server.mu.Lock()
	_, kept := fixture.session.CharacterInstances[persistentID]
	fixture.server.mu.Unlock()
	if !kept {
		t.Fatal("persistent character was collected without references")
	}
	writeCharacterCommand(t, gmWS, Command{Type: "characterSetPersistent", Client: "persistent", Seq: 4, CharacterID: persistentID, Persistent: boolPointer(false)})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	fixture.server.mu.Lock()
	_, kept = fixture.session.CharacterInstances[persistentID]
	fixture.server.mu.Unlock()
	if kept {
		t.Fatal("persistent=false without references did not trigger GC")
	}

	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "scene-gc", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["red"], CharacterName: "Scene only"})
	sceneOnlyID := characterIDFromACK(t, read(t, gmWS, "ack"))
	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "scene-gc", Seq: 2, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["wolf2"], CharacterName: "Shared"})
	sharedID := characterIDFromACK(t, read(t, gmWS, "ack"))
	writeCharacterCommand(t, gmWS, Command{Type: "characterLink", Client: "scene-gc", Seq: 3, SceneID: fixture.sceneB.ID, TokenID: fixture.tokens["return"], CharacterID: sharedID})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	writeCharacterCommand(t, gmWS, Command{Type: "sceneDelete", Client: "scene-delete", Seq: 1, SceneID: fixture.sceneA.ID})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	fixture.server.mu.Lock()
	_, sceneOnlyExists := fixture.session.CharacterInstances[sceneOnlyID]
	_, sharedExists := fixture.session.CharacterInstances[sharedID]
	sharedReferences := len(fixture.session.characterReferences[sharedID])
	fixture.server.mu.Unlock()
	if sceneOnlyExists || !sharedExists || sharedReferences != 1 {
		t.Fatalf("scene GC mismatch: sceneOnly=%v shared=%v refs=%d", sceneOnlyExists, sharedExists, sharedReferences)
	}
}

func statValuePointer(value StatValue) *StatValue { return &value }
func stringPointer(value string) *string          { return &value }

func effectiveCharacterForTest(instance CharacterInstance, registry EffectiveRegistry) EffectiveCharacter {
	if instance.PresetID == "" {
		return EffectiveCharacterState(instance, nil)
	}
	preset := registry.Presets[instance.PresetID]
	return EffectiveCharacterState(instance, &preset)
}
