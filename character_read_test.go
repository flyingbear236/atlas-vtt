package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func readCharacterCatalogPage(t *testing.T, fixture characterCommandFixture, credentials map[string]string, kind, cursor string, limit int) (*http.Response, characterCatalogResponse) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, fixture.host.URL+"/api/characters?session="+credentials["session"]+"&kind="+kind+"&cursor="+cursor+"&limit="+jsonFloatForQuery(limit), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+credentials["key"])
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var body characterCatalogResponse
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
	}
	response.Body.Close()
	return response, body
}

func jsonFloatForQuery(value int) string {
	return fmt.Sprint(value)
}

func TestCharacterCatalogIsGMOnlyPagedSummary(t *testing.T) {
	fixture := newCharacterCommandFixture(t)
	fixture.server.mu.Lock()
	for index := 0; index < 120; index++ {
		characterID := fmt.Sprintf("persistent-%03d", index)
		avatar := "avatar"
		fixture.session.CharacterInstances[characterID] = CharacterInstance{
			ID: characterID, Name: fmt.Sprintf("Character %03d", index), Persistent: true, PresetID: "wolf",
			AvatarAssetID: &avatar, StatOverrides: map[string]StatValue{"hp": IntegerStatValue(int64(index))},
		}
	}
	fixture.server.mu.Unlock()

	response, first := readCharacterCatalogPage(t, fixture, fixture.gm, "roster", "", 999)
	if response.StatusCode != http.StatusOK || len(first.Characters) != maxCharacterCatalogPage || first.NextCursor == "" {
		t.Fatalf("first roster page: status=%d count=%d cursor=%q", response.StatusCode, len(first.Characters), first.NextCursor)
	}
	_, second := readCharacterCatalogPage(t, fixture, fixture.gm, "roster", first.NextCursor, maxCharacterCatalogPage)
	if len(second.Characters) != 20 || second.NextCursor != "" {
		t.Fatalf("second roster page: count=%d cursor=%q", len(second.Characters), second.NextCursor)
	}
	encoded, _ := json.Marshal(first)
	if bytes.Contains(encoded, []byte("statOverrides")) || bytes.Contains(encoded, []byte("avatarAssetId")) {
		t.Fatalf("roster leaked sheet or asset data: %s", encoded)
	}
	_, presets := readCharacterCatalogPage(t, fixture, fixture.gm, "presets", "", 10)
	if len(presets.Presets) != 1 || presets.Presets[0].ID != "wolf" || len(presets.Characters) != 0 {
		t.Fatalf("unexpected preset summaries: %+v", presets)
	}
	_, actions := readCharacterCatalogPage(t, fixture, fixture.gm, "actions", "", 10)
	if len(actions.Actions) != 1 || actions.Actions[0].ID != "bite" || len(actions.Characters) != 0 {
		t.Fatalf("unexpected action summaries: %+v", actions)
	}
	denied, _ := readCharacterCatalogPage(t, fixture, fixture.alice, "roster", "", 10)
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("player catalog status = %d, want 403", denied.StatusCode)
	}
}
