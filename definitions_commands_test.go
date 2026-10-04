package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func registryRevisionPointer(revision uint64) *uint64 { return &revision }

func definitionHTTP(t *testing.T, serverURL, path string, credentials map[string]string, body []byte, query url.Values) (int, []byte) {
	t.Helper()
	if query == nil {
		query = url.Values{}
	}
	query.Set("session", credentials["session"])
	request, err := http.NewRequest(http.MethodPost, serverURL+path+"?"+query.Encode(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+credentials["key"])
	request.Header.Set("Content-Type", "application/toml")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data := new(bytes.Buffer)
	if _, err := data.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data.Bytes()
}

func readDefinitionsHTTP(t *testing.T, serverURL string, credentials map[string]string) (int, definitionRegistryResponse) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, serverURL+"/api/definitions?session="+url.QueryEscape(credentials["session"]), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+credentials["key"])
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result definitionRegistryResponse
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode, result
}

func exportDefinitionsHTTP(t *testing.T, serverURL, path string, credentials map[string]string) (int, []byte, http.Header) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, serverURL+path+"?session="+url.QueryEscape(credentials["session"]), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+credentials["key"])
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data := new(bytes.Buffer)
	if _, err := data.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data.Bytes(), response.Header.Clone()
}

func ackError(t *testing.T, message map[string]json.RawMessage) string {
	t.Helper()
	var issue string
	if err := json.Unmarshal(message["error"], &issue); err != nil {
		t.Fatal(err)
	}
	return issue
}

func TestDefinitionCommandsAreReliableGMOnlyAndReferenceSafe(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Definitions"})
	player := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})

	server.mu.Lock()
	session := server.sessions[gm["session"]]
	session.Scenes = map[string]*Scene{}
	session.CampaignRevision++
	server.dirty = true
	if err := server.saveLocked(); err != nil {
		server.mu.Unlock()
		t.Fatal(err)
	}
	server.mu.Unlock()

	playerWS := dialRaw(t, host.URL, player)
	read(t, playerWS, "campaignSnapshot")
	denied := Command{
		Type: "definitionCreate", Client: "player-definitions", Seq: 1,
		DefinitionKind: definitionKindStat, DefinitionID: "forbidden", ExpectedRegistryRevision: registryRevisionPointer(1),
		StatDefinition: &StatDefinition{ID: "forbidden", Name: "Forbidden", Type: StatTypeInteger},
	}
	if err := playerWS.WriteJSON(denied); err != nil {
		t.Fatal(err)
	}
	deniedReply := read(t, playerWS, "ack")
	if issue := ackError(t, deniedReply); !strings.Contains(issue, "ведущему") {
		t.Fatalf("player definition command was not rejected: %q", issue)
	}
	if status, _ := readDefinitionsHTTP(t, host.URL, player); status != http.StatusForbidden {
		t.Fatalf("player read registry status = %d", status)
	}

	gmWS := dialRaw(t, host.URL, gm)
	read(t, gmWS, "campaignSnapshot")
	createStat := Command{
		Type: "definitionCreate", Client: "definitions", Seq: 1,
		DefinitionKind: definitionKindStat, DefinitionID: "power", ExpectedRegistryRevision: registryRevisionPointer(1),
		StatDefinition: &StatDefinition{ID: "power", Name: "Power", Type: StatTypeInteger},
	}
	if err := gmWS.WriteJSON(createStat); err != nil {
		t.Fatal(err)
	}
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	server.mu.Lock()
	if session.RegistryRevision != 2 || len(session.CampaignDefinitions.Stats) != 1 {
		server.mu.Unlock()
		t.Fatalf("create did not update registry once: revision=%d stats=%d", session.RegistryRevision, len(session.CampaignDefinitions.Stats))
	}
	server.mu.Unlock()
	reloaded := testServer(t, root)
	reloadedSession := reloaded.sessions[gm["session"]]
	if reloadedSession.RegistryRevision != 2 || len(reloadedSession.CampaignDefinitions.Stats) != 1 || reloadedSession.Receipts[session.Members[session.Keys[gm["key"]]].ID+":definitions"].Seq != 1 {
		t.Fatal("definition and lost-ACK receipt were not persisted together")
	}

	// Lost ACK retry uses the persisted command receipt and cannot create a
	// second definition or advance the registry revision again.
	gmWS.WriteJSON(createStat)
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	server.mu.Lock()
	if session.RegistryRevision != 2 || len(session.CampaignDefinitions.Stats) != 1 {
		server.mu.Unlock()
		t.Fatal("lost ACK retry duplicated a definition")
	}
	server.mu.Unlock()

	action := ActionDefinition{ID: "strike", Name: "Strike", Rolls: []RollSpec{{ID: "hit", Name: "Hit", Count: 1, Sides: 20, ModifierStat: "power"}}}
	gmWS.WriteJSON(Command{Type: "definitionCreate", Client: "definitions", Seq: 2, DefinitionKind: definitionKindAction, DefinitionID: action.ID, ActionDefinition: &action, ExpectedRegistryRevision: registryRevisionPointer(2)})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	preset := CharacterPresetDefinition{ID: "hero", Name: "Hero", Stats: map[string]StatValue{"luck": IntegerStatValue(7)}, ActionIDs: []string{"strike"}}
	gmWS.WriteJSON(Command{Type: "definitionCreate", Client: "definitions", Seq: 3, DefinitionKind: definitionKindPreset, DefinitionID: preset.ID, PresetDefinition: &preset, ExpectedRegistryRevision: registryRevisionPointer(3)})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	server.mu.Lock()
	if inferred := session.CampaignDefinitions.Stats["luck"]; inferred.Type != StatTypeInteger {
		server.mu.Unlock()
		t.Fatalf("preset save did not infer campaign stat: %#v", inferred)
	}
	server.mu.Unlock()

	changedType := StatDefinition{ID: "power", Name: "Power", Type: StatTypeBoolean}
	gmWS.WriteJSON(Command{Type: "definitionUpdate", Client: "definitions", Seq: 4, DefinitionKind: definitionKindStat, DefinitionID: "power", StatDefinition: &changedType, ExpectedRegistryRevision: registryRevisionPointer(4)})
	if issue := ackError(t, read(t, gmWS, "ack")); !strings.Contains(issue, "cannot change type") {
		t.Fatalf("referenced stat type change was not rejected: %q", issue)
	}

	gmWS.WriteJSON(Command{Type: "definitionDelete", Client: "definitions", Seq: 5, DefinitionKind: definitionKindAction, DefinitionID: "strike", ExpectedRegistryRevision: registryRevisionPointer(4)})
	if issue := ackError(t, read(t, gmWS, "ack")); !strings.Contains(issue, "referenc") {
		t.Fatalf("referenced action deletion was not rejected: %q", issue)
	}
	gmWS.WriteJSON(Command{Type: "definitionDuplicate", Client: "definitions", Seq: 6, DefinitionKind: definitionKindAction, DefinitionID: "strike", DuplicateDefinitionID: "strike_copy", ExpectedRegistryRevision: registryRevisionPointer(4)})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	copyDefinition := action
	copyDefinition.ID = "strike_copy"
	copyDefinition.Description = "Independent copy"
	gmWS.WriteJSON(Command{Type: "definitionUpdate", Client: "definitions", Seq: 7, DefinitionKind: definitionKindAction, DefinitionID: "strike_copy", ActionDefinition: &copyDefinition, ExpectedRegistryRevision: registryRevisionPointer(5)})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	server.mu.Lock()
	if session.CampaignDefinitions.Actions["strike"].Description != "" || session.CampaignDefinitions.Actions["strike_copy"].Description != "Independent copy" {
		server.mu.Unlock()
		t.Fatal("duplicate shares mutable state with its source")
	}
	server.mu.Unlock()

	status, registry := readDefinitionsHTTP(t, host.URL, gm)
	if status != http.StatusOK || registry.RegistryRevision != 6 || registry.Effective.Presets["hero"].ID == "" {
		t.Fatalf("GM registry read failed: status=%d registry=%#v", status, registry)
	}
}

func TestDefinitionHTTPInstallImportIdempotencyAndRollback(t *testing.T) {
	root := t.TempDir()
	server := testServer(t, root)
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Definition HTTP"})
	player := post(t, host.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})
	rulesetDocument := testRulesetTOML("core", "1")

	if status, _ := definitionHTTP(t, host.URL, "/api/definitions/ruleset/preview", player, rulesetDocument, nil); status != http.StatusForbidden {
		t.Fatalf("player ruleset preview status = %d", status)
	}
	status, body := definitionHTTP(t, host.URL, "/api/definitions/ruleset/preview", gm, rulesetDocument, nil)
	if status != http.StatusOK {
		t.Fatalf("ruleset preview: %d %s", status, body)
	}
	var rulesetPreview definitionPreviewResponse
	if err := json.Unmarshal(body, &rulesetPreview); err != nil {
		t.Fatal(err)
	}
	applyQuery := url.Values{"expectedRevision": {fmt.Sprint(rulesetPreview.RegistryRevision)}, "digest": {rulesetPreview.Digest}, "key": {"install-1"}}
	status, body = definitionHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, rulesetDocument, applyQuery)
	if status != http.StatusOK {
		t.Fatalf("ruleset apply: %d %s", status, body)
	}
	var installed DefinitionApplyResult
	if err := json.Unmarshal(body, &installed); err != nil || installed.RegistryRevision != 2 || installed.RulesetMetadata.ID != "core" {
		t.Fatalf("unexpected install result: %v %#v", err, installed)
	}
	if status, _, _ := exportDefinitionsHTTP(t, host.URL, "/api/definitions/ruleset/export", player); status != http.StatusForbidden {
		t.Fatalf("player ruleset export status = %d", status)
	}
	status, exportedRuleset, exportHeaders := exportDefinitionsHTTP(t, host.URL, "/api/definitions/ruleset/export", gm)
	if status != http.StatusOK || !strings.Contains(exportHeaders.Get("Content-Type"), "application/toml") {
		t.Fatalf("ruleset export: %d %s", status, exportedRuleset)
	}
	exportedSnapshot, err := ParseRulesetTOML(exportedRuleset)
	if err != nil || exportedSnapshot.Metadata.ID != "core" {
		t.Fatalf("ruleset export round trip: %v %#v", err, exportedSnapshot.Metadata)
	}
	status, repeatedBody := definitionHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, rulesetDocument, applyQuery)
	if status != http.StatusOK || !bytes.Equal(bytes.TrimSpace(body), bytes.TrimSpace(repeatedBody)) {
		t.Fatalf("idempotent install mismatch: %d %s", status, repeatedBody)
	}
	reloaded := testServer(t, root)
	if receipt, exists := reloaded.sessions[gm["session"]].DefinitionOperations["install-1"]; !exists || receipt.Result.RegistryRevision != installed.RegistryRevision {
		t.Fatal("ruleset apply result was not persisted for idempotent replay")
	}

	oversize := bytes.Repeat([]byte{'x'}, MaxDefinitionDocumentBytes+1)
	if status, _ := definitionHTTP(t, host.URL, "/api/definitions/campaign/preview", gm, oversize, nil); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize definitions document status = %d", status)
	}

	alternateRuleset := testRulesetTOML("alternate", "2")
	alternateDigest := definitionsDigest(alternateRuleset)
	conflictingKey := url.Values{"expectedRevision": {"2"}, "digest": {alternateDigest}, "key": {"install-1"}}
	if status, _ := definitionHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, alternateRuleset, conflictingKey); status != http.StatusConflict {
		t.Fatalf("same key/different digest status = %d", status)
	}
	replace := url.Values{"expectedRevision": {"2"}, "digest": {alternateDigest}, "key": {"install-2"}}
	if status, _ := definitionHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, alternateRuleset, replace); status != http.StatusConflict {
		t.Fatalf("ruleset replacement status = %d", status)
	}

	// A campaign overlay can be removed safely when it reveals the compatible
	// immutable ruleset definition underneath.
	gmWS := dialRaw(t, host.URL, gm)
	read(t, gmWS, "campaignSnapshot")
	override := StatDefinition{ID: "hp", Name: "Campaign HP", Type: StatTypeInteger}
	gmWS.WriteJSON(Command{Type: "definitionCreate", Client: "overlay", Seq: 1, DefinitionKind: definitionKindStat, DefinitionID: "hp", StatDefinition: &override, ExpectedRegistryRevision: registryRevisionPointer(2)})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	gmWS.WriteJSON(Command{Type: "definitionDelete", Client: "overlay", Seq: 2, DefinitionKind: definitionKindStat, DefinitionID: "hp", ExpectedRegistryRevision: registryRevisionPointer(3)})
	if issue := ackError(t, read(t, gmWS, "ack")); issue != "" {
		t.Fatal(issue)
	}
	status, registry := readDefinitionsHTTP(t, host.URL, gm)
	if status != http.StatusOK || registry.RegistryRevision != 4 || registry.Effective.Stats["hp"].Name != "HP" {
		t.Fatalf("overlay reset did not reveal ruleset: status=%d %#v", status, registry.Effective.Stats["hp"])
	}

	largeRegistry := emptyCampaignRegistry()
	for index := 0; index < 12; index++ {
		definitionID := fmt.Sprintf("large_action_%02d", index)
		largeRegistry.Actions[definitionID] = ActionDefinition{ID: definitionID, Name: definitionID, Description: strings.Repeat(string(rune('a'+index)), 1800)}
	}
	largeDocument, err := ExportDefinitionsTOML(largeRegistry)
	if err != nil {
		t.Fatal(err)
	}
	if len(largeDocument) <= maxWebSocketMessageBytes {
		t.Fatalf("HTTP acceptance document is only %d bytes", len(largeDocument))
	}
	status, body = definitionHTTP(t, host.URL, "/api/definitions/campaign/preview", gm, largeDocument, nil)
	if status != http.StatusOK {
		t.Fatalf("large campaign preview: %d %s", status, body)
	}
	var largePreview definitionPreviewResponse
	json.Unmarshal(body, &largePreview)
	largeApply := url.Values{"expectedRevision": {fmt.Sprint(largePreview.RegistryRevision)}, "digest": {largePreview.Digest}, "key": {"large-import"}}
	status, body = definitionHTTP(t, host.URL, "/api/definitions/campaign/apply", gm, largeDocument, largeApply)
	if status != http.StatusOK {
		t.Fatalf("large campaign apply: %d %s", status, body)
	}
	status, repeatedBody = definitionHTTP(t, host.URL, "/api/definitions/campaign/apply", gm, largeDocument, largeApply)
	if status != http.StatusOK || !bytes.Equal(bytes.TrimSpace(body), bytes.TrimSpace(repeatedBody)) {
		t.Fatalf("campaign apply was not idempotent: %d %s", status, repeatedBody)
	}

	staleRegistry := emptyCampaignRegistry()
	staleRegistry.Stats["stale"] = StatDefinition{ID: "stale", Name: "Stale", Type: StatTypeBoolean}
	staleDocument, _ := ExportDefinitionsTOML(staleRegistry)
	status, body = definitionHTTP(t, host.URL, "/api/definitions/campaign/preview", gm, staleDocument, nil)
	if status != http.StatusOK {
		t.Fatal(string(body))
	}
	var stalePreview definitionPreviewResponse
	json.Unmarshal(body, &stalePreview)

	interveningRegistry := emptyCampaignRegistry()
	interveningRegistry.Stats["newer"] = StatDefinition{ID: "newer", Name: "Newer", Type: StatTypeString}
	interveningDocument, _ := ExportDefinitionsTOML(interveningRegistry)
	status, body = definitionHTTP(t, host.URL, "/api/definitions/campaign/preview", gm, interveningDocument, nil)
	var interveningPreview definitionPreviewResponse
	json.Unmarshal(body, &interveningPreview)
	interveningApply := url.Values{"expectedRevision": {fmt.Sprint(interveningPreview.RegistryRevision)}, "digest": {interveningPreview.Digest}, "key": {"intervening"}}
	if status, body = definitionHTTP(t, host.URL, "/api/definitions/campaign/apply", gm, interveningDocument, interveningApply); status != http.StatusOK {
		t.Fatalf("intervening apply: %d %s", status, body)
	}
	staleApply := url.Values{"expectedRevision": {fmt.Sprint(stalePreview.RegistryRevision)}, "digest": {stalePreview.Digest}, "key": {"stale"}}
	if status, _ := definitionHTTP(t, host.URL, "/api/definitions/campaign/apply", gm, staleDocument, staleApply); status != http.StatusConflict {
		t.Fatalf("stale campaign apply status = %d", status)
	}

	failureRegistry := emptyCampaignRegistry()
	failureRegistry.Stats["must_not_persist"] = StatDefinition{ID: "must_not_persist", Name: "Rollback", Type: StatTypeInteger}
	failureDocument, _ := ExportDefinitionsTOML(failureRegistry)
	status, body = definitionHTTP(t, host.URL, "/api/definitions/campaign/preview", gm, failureDocument, nil)
	var failurePreview definitionPreviewResponse
	json.Unmarshal(body, &failurePreview)
	server.mu.Lock()
	session := server.sessions[gm["session"]]
	beforeRevision, beforeOperations := session.RegistryRevision, len(session.DefinitionOperations)
	server.mu.Unlock()
	blocker := filepath.Join(root, "sessions.json.tmp")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	failureApply := url.Values{"expectedRevision": {fmt.Sprint(failurePreview.RegistryRevision)}, "digest": {failurePreview.Digest}, "key": {"failed-import"}}
	status, _ = definitionHTTP(t, host.URL, "/api/definitions/campaign/apply", gm, failureDocument, failureApply)
	if status != http.StatusInternalServerError {
		t.Fatalf("save failure apply status = %d", status)
	}
	server.mu.Lock()
	_, leaked := session.CampaignDefinitions.Stats["must_not_persist"]
	if leaked || session.RegistryRevision != beforeRevision || len(session.DefinitionOperations) != beforeOperations {
		server.mu.Unlock()
		t.Fatal("failed apply left registry revision or operation receipt behind")
	}
	server.mu.Unlock()
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if status, body = definitionHTTP(t, host.URL, "/api/definitions/campaign/apply", gm, failureDocument, failureApply); status != http.StatusOK {
		t.Fatalf("retry after rolled-back save failure: %d %s", status, body)
	}
	status, exportedCampaign, exportHeaders := exportDefinitionsHTTP(t, host.URL, "/api/definitions/campaign/export", gm)
	if status != http.StatusOK || !strings.Contains(exportHeaders.Get("Content-Disposition"), "campaign-extensions.toml") {
		t.Fatalf("campaign export: %d %s", status, exportedCampaign)
	}
	if status, _, _ := exportDefinitionsHTTP(t, host.URL, "/api/definitions/campaign/export", player); status != http.StatusForbidden {
		t.Fatalf("player campaign export status = %d", status)
	}
	if bytes.Contains(exportedCampaign, []byte("characters")) || bytes.Contains(exportedCampaign, []byte("instances")) {
		t.Fatalf("campaign export leaked runtime state: %s", exportedCampaign)
	}
	preview, err := PreviewCampaignDefinitionsImport(DefinitionRegistries{Ruleset: exportedSnapshot.Registry}, exportedCampaign)
	if err != nil || len(preview.Registry.Actions) == 0 {
		t.Fatalf("campaign export round trip: %v %#v", err, preview.Registry)
	}
}
