package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestBrowserRulesetV04(t *testing.T) {
	chrome := os.Getenv("ATLAS_CHROME")
	if chrome == "" {
		t.Skip("set ATLAS_CHROME for real browser checks")
	}
	f := newDiceCommandFixture(t)
	credentials := post(t, f.host.URL+"/api/sessions", map[string]string{"name": "Ruleset browser"})
	_, source := specZIP(t)
	// select(object) currently selects an unambiguous item via its owner's token.
	// Keep the three-equipped-weapon fixture and add a dedicated single-item target.
	archive := plainZIP(t, map[string][]byte{
		"rules/examples.toml": source,
		"party/trained.toml":  []byte(specSupplement),
		"party/target.toml": []byte(`[character.browser_target]
name = "Хранитель кинжала"
[[character.browser_target.inventory]]
id = "browser-dagger"
object = "object.dagger"
`),
	})
	importSpecZIP(t, f.host.URL, credentials, archive)
	ws := dialRaw(t, f.host.URL, credentials)
	home := read(t, ws, "campaignSnapshot")
	var scenes []SceneSummary
	if err := json.Unmarshal(home["scenes"], &scenes); err != nil || len(scenes) != 1 {
		t.Fatalf("campaign: %s", home)
	}
	subscribeDiceScene(t, ws, scenes[0].ID)
	linkSpecCharacter(t, ws, scenes[0].ID, "hero", 1)
	linkSpecCharacter(t, ws, scenes[0].ID, "trained", 2)
	linkSpecCharacter(t, ws, scenes[0].ID, "browser_target", 3)
	ws.Close()
	f.server.mu.Lock()
	f.rng.values = []int{9, 3, 0}
	f.server.mu.Unlock()
	script, err := os.ReadFile("testdata/ruleset-v04/browser.js")
	if err != nil {
		t.Fatal(err)
	}
	runRulesetBrowser(t, chrome, f.host.URL, credentials, string(script))
}

func runRulesetBrowser(t *testing.T, chrome, url string, credentials map[string]string, script string) {
	t.Helper()
	browser := startTestChrome(t, chrome, "", "", 10*time.Second, "--no-sandbox", "--disable-background-networking")
	defer browser.close()
	page := profileDial(t, browser.pageURL(""))
	page.call("Runtime.enable", map[string]any{})
	page.call("Page.enable", map[string]any{})
	page.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1440, "height": 1000, "deviceScaleFactor": 1, "mobile": false})
	seed, err := json.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	page.call("Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": "localStorage.setItem('atlas-sessions',JSON.stringify([" + string(seed) + "]));localStorage.setItem('atlas-dice-animation','off');"})
	navigateBrowser(t, page.call, url, false, func() []string { return page.Errors })
	if result := page.eval("(" + script + ")(" + string(seed) + ")"); string(result) != "true" {
		t.Fatalf("Ruleset browser scenario: %s; exceptions: %v", result, page.Errors)
	}
	if len(page.Errors) > 0 {
		t.Fatalf("Browser exceptions: %v", page.Errors)
	}
}
