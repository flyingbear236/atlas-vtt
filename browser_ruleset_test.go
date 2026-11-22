package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBrowserRulesetV04(t *testing.T) {
	chrome := os.Getenv("ATLAS_CHROME")
	if chrome == "" {
		t.Skip("set ATLAS_CHROME for real browser checks")
	}
	f := newDiceCommandFixture(t)
	credentials := post(t, f.host.URL+"/api/sessions", map[string]string{"name": "Ruleset browser"})
	archive, _ := specZIP(t)
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
	ws.Close()
	f.server.mu.Lock()
	f.rng.values = []int{9, 3, 0}
	f.server.mu.Unlock()
	script, err := os.ReadFile("testdata/ruleset-v04/browser.js")
	if err != nil {
		t.Fatal(err)
	}
	runRulesetBrowser(t, chrome, f.host.URL, "("+string(script)+")("+string(msgIntoJSON(t, credentials))+")")
}

func runRulesetBrowser(t *testing.T, chrome, url, script string) {
	t.Helper()
	profile := t.TempDir()
	cmd := exec.Command(chrome, "--no-sandbox", "--headless=new", "--no-first-run", "--disable-background-networking", "--remote-debugging-port=0", "--user-data-dir="+profile, url)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	var tabURL string
	client := http.Client{Timeout: time.Second}
	for deadline := time.Now().Add(10 * time.Second); tabURL == "" && time.Now().Before(deadline); {
		port, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		response, err := client.Get("http://127.0.0.1:" + strings.Split(string(port), "\n")[0] + "/json/list")
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		var tabs []struct {
			Type                 string
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		err = json.NewDecoder(response.Body).Decode(&tabs)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, tab := range tabs {
			if tab.Type == "page" {
				tabURL = tab.WebSocketDebuggerURL
				break
			}
		}
	}
	if tabURL == "" {
		t.Fatal("Chrome tab is unavailable")
	}
	conn, _, err := websocket.DefaultDialer.Dial(tabURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	evaluate := func(expression string, attempts int) {
		for id := 1; id <= attempts; id++ {
			if err := conn.WriteJSON(map[string]any{"id": id, "method": "Runtime.evaluate", "params": map[string]any{"expression": expression, "awaitPromise": true, "returnByValue": true}}); err != nil {
				t.Fatal(err)
			}
			var result struct {
				ID     int
				Error  json.RawMessage
				Result struct {
					Exception json.RawMessage `json:"exceptionDetails"`
					Result    struct{ Value json.RawMessage }
				}
			}
			if err := conn.ReadJSON(&result); err != nil {
				t.Fatal(err)
			}
			if id < attempts && (strings.Contains(string(result.Error), "context") || len(result.Result.Exception) > 0) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			if result.ID != id || len(result.Error) > 0 || len(result.Result.Exception) > 0 || string(result.Result.Result.Value) != "true" {
				t.Fatalf("Chrome: %s %s", result.Error, result.Result.Exception)
			}
			return
		}
	}
	evaluate(`(async()=>{for(let i=0;i<100;i++){if(document.querySelector('script[src="/app.js"]'))return true;await new Promise(r=>setTimeout(r,50));}throw Error('ATLAS page did not load')})()`, 5)
	evaluate(script, 1)
}
