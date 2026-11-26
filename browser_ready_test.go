package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A synchronous, read-only probe can safely be retried when Chrome destroys an
// execution context during navigation. Never replay a mutating UI scenario.
const browserReadyExpression = `JSON.stringify({href:location.href,ready:document.documentElement?.dataset.atlasReady==='true',probe:!!globalThis.__atlasProbe,origin:performance.timeOrigin,state:document.readyState})`

func browserNavigationError(message string) bool {
	for _, fragment := range []string{"navigated or closed", "Cannot find context", "Execution context was destroyed", "Cannot find default execution context", "Inspected target navigated"} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

type browserPageState struct {
	Href   string  `json:"href"`
	Ready  bool    `json:"ready"`
	Probe  bool    `json:"probe"`
	Origin float64 `json:"origin"`
	State  string  `json:"state"`
}

func browserPageStatus(raw json.RawMessage) browserPageState {
	var response struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	var state browserPageState
	if json.Unmarshal(raw, &response) == nil {
		_ = json.Unmarshal([]byte(response.Result.Value), &state)
	}
	return state
}

func browserPageStatusCall(call func(string, any) json.RawMessage) browserPageState {
	return browserPageStatus(call("Runtime.evaluate", map[string]any{"expression": browserReadyExpression, "returnByValue": true}))
}

func waitBrowserReady(t *testing.T, call func(string, any) json.RawMessage, target string, previousOrigin float64, profile bool, errors func() []string) {
	t.Helper()
	var state browserPageState
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		state = browserPageStatusCall(call)
		if state.Ready && (!profile || state.Probe) && (target == "" || strings.TrimRight(state.Href, "/") == strings.TrimRight(target, "/")) && (previousOrigin == 0 || state.Origin != previousOrigin) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("ATLAS browser did not initialize: %+v; browser exceptions: %v", state, errors())
}

func navigateBrowser(t *testing.T, call func(string, any) json.RawMessage, target string, profile bool, errors func() []string) {
	t.Helper()
	previous := browserPageStatusCall(call)
	var result struct {
		ErrorText string `json:"errorText"`
	}
	_ = json.Unmarshal(call("Page.navigate", map[string]any{"url": target}), &result)
	if result.ErrorText != "" {
		t.Fatalf("Chrome navigation %s: %s", target, result.ErrorText)
	}
	waitBrowserReady(t, call, target, previous.Origin, profile, errors)
}

func (d *profileCDP) navigate(target string) {
	navigateBrowser(d.t, d.call, target, true, func() []string { return d.Errors })
}
func (d *profileCDP) reload() {
	previous := browserPageStatusCall(d.call)
	d.call("Page.reload", map[string]any{})
	waitBrowserReady(d.t, d.call, previous.Href, previous.Origin, true, func() []string { return d.Errors })
}

func TestBrowserReadyWaitsForNewDocumentAndProbe(t *testing.T) {
	states := []browserPageState{
		{Href: "http://atlas/", Ready: true, Probe: true, Origin: 1},  // old page
		{Href: "http://atlas/", Ready: true, Probe: false, Origin: 2}, // probe not initialized
		{Href: "http://atlas/", Ready: true, Probe: true, Origin: 2},
	}
	calls := 0
	call := func(method string, params any) json.RawMessage {
		if method != "Runtime.evaluate" || params.(map[string]any)["expression"] != browserReadyExpression {
			t.Fatal("non-read-only readiness poll")
		}
		if calls >= len(states) {
			t.Fatal("readiness was not detected")
		}
		state, _ := json.Marshal(states[calls])
		calls++
		raw, _ := json.Marshal(map[string]any{"result": map[string]any{"value": string(state)}})
		return raw
	}
	waitBrowserReady(t, call, "http://atlas", 1, true, func() []string { return nil })
	if calls != 3 {
		t.Fatalf("premature readiness: %d calls", calls)
	}
}
