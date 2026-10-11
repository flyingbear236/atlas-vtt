package main

import (
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
)

func createTokenForCharacterTest(t *testing.T, connection *websocket.Conn, command Command) Token {
	t.Helper()
	if err := connection.WriteJSON(command); err != nil {
		t.Fatal(err)
	}
	token := tokenFrom(t, read(t, connection, "upsert"))
	if issue := ackError(t, read(t, connection, "ack")); issue != "" {
		t.Fatal(issue)
	}
	return token
}

func TestTokenCreationAndReassignmentMaintainCharacterInvariant(t *testing.T) {
	server := testServer(t, t.TempDir())
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Token characters"})

	server.mu.Lock()
	session := server.sessions[gm["session"]]
	session.CampaignDefinitions.Presets["wolf"] = CharacterPresetDefinition{ID: "wolf", Name: "Wolf", Stats: map[string]StatValue{}}
	persistent := NewCharacterInstance("roster-hero", nil)
	persistent.Name = "Roster hero"
	persistent.Persistent = true
	persistent.StatOverrides["legacy"] = StringStatValue("untouched")
	session.CampaignDefinitions.Stats["legacy"] = StatDefinition{ID: "legacy", Name: "Legacy", Type: StatTypeString}
	session.CharacterInstances[persistent.ID] = persistent
	session.RegistryRevision++
	server.mu.Unlock()

	connection := dial(t, host.URL, gm)
	read(t, connection, "snapshot")
	empty := createTokenForCharacterTest(t, connection, Command{Type: "create", Client: "empty", Seq: 1, Token: Token{Size: 80, Opacity: 1}})
	firstWolf := createTokenForCharacterTest(t, connection, Command{Type: "create", Client: "wolf-a", Seq: 1, PresetID: "wolf", Token: Token{Size: 80, Opacity: 1}})
	secondWolf := createTokenForCharacterTest(t, connection, Command{Type: "create", Client: "wolf-b", Seq: 1, PresetID: "wolf", Token: Token{Size: 80, Opacity: 1}})
	existing := createTokenForCharacterTest(t, connection, Command{Type: "create", Client: "existing", Seq: 1, CharacterID: persistent.ID, Token: Token{Size: 80, Opacity: 1}})

	server.mu.Lock()
	if empty.CharacterInstanceID == "" || session.CharacterInstances[empty.CharacterInstanceID].Name != defaultCharacterName || session.CharacterInstances[empty.CharacterInstanceID].Persistent {
		server.mu.Unlock()
		t.Fatal("empty token did not create one transient default character")
	}
	if firstWolf.CharacterInstanceID == secondWolf.CharacterInstanceID || session.CharacterInstances[firstWolf.CharacterInstanceID].Name != "Wolf" || session.CharacterInstances[secondWolf.CharacterInstanceID].Name != "Wolf" {
		server.mu.Unlock()
		t.Fatal("preset tokens did not create independent characters with the preset name")
	}
	if existing.CharacterInstanceID != persistent.ID || len(session.CharacterInstances) != 4 {
		server.mu.Unlock()
		t.Fatal("existing-character creation created an extra character")
	}
	oldEmptyID := empty.CharacterInstanceID
	server.mu.Unlock()

	writeCharacterCommand(t, connection, Command{Type: "characterLink", Client: "reassign", Seq: 1, SceneID: firstScene(session).ID, TokenID: empty.ID, CharacterID: persistent.ID})
	if issue := ackError(t, read(t, connection, "ack")); issue != "" {
		t.Fatal(issue)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if _, exists := session.CharacterInstances[oldEmptyID]; exists {
		t.Fatal("last reference did not collect the old transient character")
	}
	if got := session.CharacterInstances[persistent.ID]; got.Name != persistent.Name || got.StatOverrides["legacy"].String() != "untouched" {
		t.Fatal("reassignment copied or merged character data")
	}
	if len(session.characterReferences[persistent.ID]) != 2 {
		t.Fatalf("one character should be referenced by two tokens, got %d", len(session.characterReferences[persistent.ID]))
	}
}

func TestLegacyTokenMigrationMovesNameAndArtworkToCharacter(t *testing.T) {
	server := testServer(t, t.TempDir())
	session := &Session{
		Scenes: map[string]*Scene{"scene": newScene("scene", "Legacy")}, Assets: map[string]Asset{},
		Ruleset: cloneRulesetSnapshot(server.defaultRuleset), CampaignDefinitions: emptyCampaignRegistry(),
		CharacterInstances: map[string]CharacterInstance{}, CharacterRevision: 1,
	}
	scene := session.Scenes["scene"]
	asset := Asset{ID: "legacy-art", Kind: assetKindToken}
	session.Assets[asset.ID] = asset
	token := Token{ID: "legacy-token", Name: "Legacy hero", Asset: asset.ID, FloorID: firstFloorID(scene), LayerID: layerIDByKind(scene, firstFloorID(scene), layerKindTokens), Size: 80, Opacity: 1}
	scene.Tokens[token.ID] = token

	if !migrateLegacyTokenCharacters(session) {
		t.Fatal("legacy token was not migrated")
	}
	migrated := scene.Tokens[token.ID]
	instance := session.CharacterInstances[migrated.CharacterInstanceID]
	if migrated.CharacterInstanceID == "" || migrated.Name != "" || migrated.Asset != "" || instance.Name != token.Name || instance.AvatarAssetID == nil || *instance.AvatarAssetID != asset.ID {
		t.Fatalf("legacy data was not moved to the character: token=%+v character=%+v", migrated, instance)
	}
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		t.Fatal(err)
	}
	presented := presentToken(session, registry, migrated)
	if presented.Name != token.Name || presented.Asset != asset.ID {
		t.Fatalf("renderer projection did not use effective character data: %+v", presented)
	}
}
