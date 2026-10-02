package main

import (
	"encoding/json"
	"testing"
)

func decodeCharacterSnapshot(t *testing.T, message map[string]json.RawMessage) characterSnapshot {
	t.Helper()
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot characterSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestCharacterWatchCrossSceneRevisionRevocationAndReconnect(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	fixture.server.mu.Lock()
	bobID := fixture.session.Keys[fixture.bob["key"]]
	token := fixture.sceneB.Tokens[fixture.tokens["cross-b"]]
	token.OwnerIDs = []string{bobID}
	fixture.sceneB.Tokens[token.ID] = token
	fixture.session.CampaignDefinitions.Stats["modifier"] = StatDefinition{ID: "modifier", Name: "Modifier", Type: StatTypeInteger}
	fixture.session.CampaignDefinitions.Stats["unused"] = StatDefinition{ID: "unused", Name: "Unused", Type: StatTypeString}
	bite := fixture.session.CampaignDefinitions.Actions["bite"]
	bite.Rolls = []RollSpec{{ID: "attack", Name: "Attack", Count: 1, Sides: 20, ModifierStat: "modifier"}}
	fixture.session.CampaignDefinitions.Actions["bite"] = bite
	fixture.session.CampaignDefinitions.Actions["unused"] = ActionDefinition{ID: "unused", Name: "Unused"}
	fixture.server.dirty = true
	if err := fixture.server.saveLocked(); err != nil {
		fixture.server.mu.Unlock()
		t.Fatal(err)
	}
	fixture.server.mu.Unlock()

	gmWS := dialRaw(t, fixture.host.URL, fixture.gm)
	defer gmWS.Close()
	read(t, gmWS, "campaignSnapshot")
	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "watch-setup", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["cross-a"], PresetID: "wolf"})
	characterID := characterIDFromACK(t, read(t, gmWS, "ack"))
	writeCharacterCommand(t, gmWS, Command{Type: "characterLink", Client: "watch-setup", Seq: 2, SceneID: fixture.sceneB.ID, TokenID: fixture.tokens["cross-b"], CharacterID: characterID})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}

	aliceWS := dialRaw(t, fixture.host.URL, fixture.alice)
	defer aliceWS.Close()
	read(t, aliceWS, "campaignSnapshot")
	bobWS := dialRaw(t, fixture.host.URL, fixture.bob)
	defer bobWS.Close()
	read(t, bobWS, "campaignSnapshot")
	charlieWS := dialRaw(t, fixture.host.URL, fixture.charlie)
	defer charlieWS.Close()
	read(t, charlieWS, "campaignSnapshot")

	for index, connection := range []interface{ WriteJSON(any) error }{aliceWS, bobWS} {
		if err := connection.WriteJSON(Command{Type: "characterWatch", CharacterID: characterID, CharacterWatch: uint64(index + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	aliceSnapshot := decodeCharacterSnapshot(t, read(t, aliceWS, "characterSnapshot"))
	bobSnapshot := decodeCharacterSnapshot(t, read(t, bobWS, "characterSnapshot"))
	if aliceSnapshot.CharacterID != characterID || bobSnapshot.CharacterID != characterID || aliceSnapshot.Revision == 0 {
		t.Fatalf("wrong initial watches: alice=%#v bob=%#v", aliceSnapshot, bobSnapshot)
	}
	if len(aliceSnapshot.StatDefinitions) != 3 || len(aliceSnapshot.Actions) != 1 || aliceSnapshot.StatDefinitions["modifier"].ID == "" || aliceSnapshot.StatDefinitions["unused"].ID != "" || aliceSnapshot.Actions["unused"].ID != "" {
		t.Fatalf("watch leaked unrelated definitions: stats=%#v actions=%#v", aliceSnapshot.StatDefinitions, aliceSnapshot.Actions)
	}
	if aliceSnapshot.Instance.ID != characterID || aliceSnapshot.Preset == nil || aliceSnapshot.Preset.ID != "wolf" || aliceSnapshot.Effective.Stats["hp"].Integer() != 11 {
		t.Fatalf("incomplete effective snapshot: %#v", aliceSnapshot)
	}

	if err := charlieWS.WriteJSON(Command{Type: "characterWatch", CharacterID: characterID, CharacterWatch: 1}); err != nil {
		t.Fatal(err)
	}
	closed := read(t, charlieWS, "characterWatchClosed")
	if _, leaked := closed["instance"]; leaked {
		t.Fatal("unauthorized close leaked a character instance")
	}

	writeCharacterCommand(t, aliceWS, Command{Type: "characterStatSet", Client: "watch-alice", Seq: 1, CharacterID: characterID, StatID: "hp", StatValue: statValuePointer(IntegerStatValue(7))})
	aliceSnapshot = decodeCharacterSnapshot(t, read(t, aliceWS, "characterSnapshot"))
	if issue := ackError(t, read(t, aliceWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	bobSnapshot = decodeCharacterSnapshot(t, read(t, bobWS, "characterSnapshot"))
	if aliceSnapshot.Effective.Stats["hp"].Integer() != 7 || bobSnapshot.Effective.Stats["hp"].Integer() != 7 || aliceSnapshot.Revision != bobSnapshot.Revision {
		t.Fatalf("cross-scene update mismatch: alice=%#v bob=%#v", aliceSnapshot.Effective.Stats, bobSnapshot.Effective.Stats)
	}

	fixture.server.mu.Lock()
	registryRevision := fixture.session.RegistryRevision
	preset := clonePresetDefinition(fixture.session.CampaignDefinitions.Presets["wolf"])
	fixture.server.mu.Unlock()
	preset.Stats["dexterity"] = IntegerStatValue(21)
	writeCharacterCommand(t, gmWS, Command{Type: "definitionUpdate", Client: "watch-def", Seq: 1, DefinitionKind: definitionKindPreset, DefinitionID: "wolf", PresetDefinition: &preset, ExpectedRegistryRevision: &registryRevision})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	aliceSnapshot = decodeCharacterSnapshot(t, read(t, aliceWS, "characterSnapshot"))
	bobSnapshot = decodeCharacterSnapshot(t, read(t, bobWS, "characterSnapshot"))
	if aliceSnapshot.Effective.Stats["dexterity"].Integer() != 21 || bobSnapshot.Effective.Stats["dexterity"].Integer() != 21 {
		t.Fatal("preset edit did not refresh effective state")
	}

	emptyOwners := []string{}
	writeCharacterCommand(t, gmWS, Command{Type: "properties", Client: "watch-token", Seq: 1, SceneID: fixture.sceneA.ID, Token: Token{ID: fixture.tokens["cross-a"]}, Properties: Properties{OwnerIDs: &emptyOwners}})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	read(t, aliceWS, "characterWatchClosed")
	read(t, bobWS, "characterSnapshot")
	writeCharacterCommand(t, aliceWS, Command{Type: "characterStatSet", Client: "watch-alice", Seq: 2, CharacterID: characterID, StatID: "hp", StatValue: statValuePointer(IntegerStatValue(8))})
	if issue := ackError(t, read(t, aliceWS, "ack")); issue == "" {
		t.Fatal("revoked owner mutated the character")
	}

	if err := bobWS.WriteJSON(Command{Type: "characterWatch", CharacterID: characterID, CharacterWatch: 2}); err != nil {
		t.Fatal(err)
	}
	read(t, bobWS, "characterSnapshot")
	if err := bobWS.WriteJSON(Command{Type: "characterWatch", CharacterID: characterID, CharacterWatch: 3}); err != nil {
		t.Fatal(err)
	}
	read(t, bobWS, "characterSnapshot")
	fixture.server.mu.Lock()
	watchSlots := 0
	for peer := range fixture.server.peers {
		if peer.session == fixture.session.ID && peer.member.ID == bobID && peer.characterID != "" {
			watchSlots++
			if peer.characterID != characterID || peer.characterWatch != 3 {
				t.Fatalf("stale watch remained active: %#v", peer)
			}
		}
	}
	fixture.server.mu.Unlock()
	if watchSlots != 1 {
		t.Fatalf("repeated subscriptions accumulated: %d", watchSlots)
	}

	bobWS.Close()
	bobWS = dialRaw(t, fixture.host.URL, fixture.bob)
	defer bobWS.Close()
	read(t, bobWS, "campaignSnapshot")
	if err := bobWS.WriteJSON(Command{Type: "characterWatch", CharacterID: characterID, CharacterWatch: 1}); err != nil {
		t.Fatal(err)
	}
	reconnected := decodeCharacterSnapshot(t, read(t, bobWS, "characterSnapshot"))
	if reconnected.Effective.Stats["hp"].Integer() != 7 || reconnected.Effective.Stats["dexterity"].Integer() != 21 {
		t.Fatalf("reconnect did not restore current state: %#v", reconnected.Effective.Stats)
	}

	published := false
	writeCharacterCommand(t, gmWS, Command{Type: "sceneUpdate", Client: "watch-scene", Seq: 1, SceneID: fixture.sceneB.ID, Published: &published})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	read(t, bobWS, "characterWatchClosed")
}

func TestCharacterWatchClosesWhenTransientCharacterIsDeleted(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	gmWS := dialRaw(t, fixture.host.URL, fixture.gm)
	defer gmWS.Close()
	read(t, gmWS, "campaignSnapshot")
	writeCharacterCommand(t, gmWS, Command{Type: "characterCreate", Client: "watch-delete", Seq: 1, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["red"], CharacterName: "Transient"})
	characterID := characterIDFromACK(t, read(t, gmWS, "ack"))
	if err := gmWS.WriteJSON(Command{Type: "characterWatch", CharacterID: characterID, CharacterWatch: 1}); err != nil {
		t.Fatal(err)
	}
	read(t, gmWS, "characterSnapshot")
	writeCharacterCommand(t, gmWS, Command{Type: "characterUnlink", Client: "watch-delete", Seq: 2, SceneID: fixture.sceneA.ID, TokenID: fixture.tokens["red"]})
	read(t, gmWS, "characterWatchClosed")
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
}
