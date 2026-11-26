package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var profileChromeArgs = []string{
	"--disable-background-timer-throttling",
	"--disable-renderer-backgrounding",
	"--disable-backgrounding-occluded-windows",
}

type testChrome struct {
	t        *testing.T
	profile  string
	log      *os.File
	done     chan struct{}
	waitErr  error
	stopTree func() error
	wsURL    string
	httpURL  string
	closed   bool
}

func startTestChrome(t *testing.T, chrome, output, logName string, timeout time.Duration, options ...string) *testChrome {
	t.Helper()
	profile, err := os.MkdirTemp(output, ".atlas-chrome-")
	if err != nil {
		t.Fatal(err)
	}
	b := &testChrome{t: t, profile: profile}
	t.Cleanup(b.close)

	logPath := filepath.Join(profile, "chrome.log")
	if logName != "" {
		logPath = filepath.Join(output, logName)
	}
	b.log, err = os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--headless=new", "--no-first-run", "--no-default-browser-check", "--remote-debugging-port=0", "--user-data-dir=" + profile}
	args = append(args, options...)
	cmd := exec.Command(chrome, append(args, "about:blank")...)
	cmd.Stdout, cmd.Stderr = b.log, b.log
	b.stopTree, err = startChromeProcess(cmd)
	if err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	b.done = make(chan struct{})
	go func() {
		b.waitErr = cmd.Wait()
		close(b.done)
	}()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case <-b.done:
			t.Fatalf("Chrome exited before DevTools startup: %v\n%s", b.waitErr, b.logText())
		case <-ticker.C:
			if endpoint := b.devToolsURL(); endpoint != nil {
				b.wsURL = endpoint.String()
				b.httpURL = "http://" + endpoint.Host
				return b
			}
		case <-deadline.C:
			t.Fatalf("Chrome did not expose a DevTools endpoint within %s\n%s", timeout, b.logText())
		}
	}
}

func (b *testChrome) logText() string {
	data, _ := os.ReadFile(b.log.Name())
	return strings.TrimSpace(string(data))
}

func (b *testChrome) devToolsURL() *url.URL {
	parse := func(raw string) *url.URL {
		endpoint, err := url.Parse(raw)
		if err == nil && endpoint.Scheme == "ws" && endpoint.Hostname() == "127.0.0.1" &&
			endpoint.Port() != "" && strings.HasPrefix(endpoint.Path, "/devtools/browser/") {
			return endpoint
		}
		return nil
	}
	if data, err := os.ReadFile(filepath.Join(b.profile, "DevToolsActivePort")); err == nil {
		port, path, ok := strings.Cut(strings.TrimSpace(string(data)), "\n")
		if ok {
			if endpoint := parse("ws://127.0.0.1:" + strings.TrimSpace(port) + strings.TrimSpace(path)); endpoint != nil {
				return endpoint
			}
		}
	}
	// Chrome can publish DevTools before writing the port file.
	for _, line := range strings.Split(b.logText(), "\n") {
		if raw, ok := strings.CutPrefix(strings.TrimSpace(line), "DevTools listening on "); ok {
			if endpoint := parse(raw); endpoint != nil {
				return endpoint
			}
		}
	}
	return nil
}

func (b *testChrome) pageURL(targetID string) string {
	b.t.Helper()
	client := http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	var err error
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		var response *http.Response
		response, err = client.Get(b.httpURL + "/json/list")
		if err == nil {
			var tabs []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
				URL  string `json:"webSocketDebuggerUrl"`
			}
			err = json.NewDecoder(response.Body).Decode(&tabs)
			response.Body.Close()
			for _, tab := range tabs {
				if tab.Type == "page" && tab.URL != "" && (targetID == "" || tab.ID == targetID) {
					return tab.URL
				}
			}
		}
		select {
		case <-b.done:
			b.t.Fatalf("Chrome exited waiting for page %q: %v\n%s", targetID, b.waitErr, b.logText())
		case <-time.After(50 * time.Millisecond):
		}
	}
	b.t.Fatalf("Chrome page %q is unavailable (last HTTP error: %v)\n%s", targetID, err, b.logText())
	return ""
}

func (b *testChrome) newPage(control *profileCDP) *profileCDP {
	b.t.Helper()
	var target struct {
		ID string `json:"targetId"`
	}
	raw := control.call("Target.createTarget", map[string]any{"url": "about:blank"})
	if err := json.Unmarshal(raw, &target); err != nil || target.ID == "" {
		b.t.Fatalf("Chrome did not create a page: %s", raw)
	}
	return profileDial(b.t, b.pageURL(target.ID))
}

func (b *testChrome) close() {
	if b.closed {
		return
	}
	b.closed = true
	if b.done != nil {
		select {
		case <-b.done:
		default:
			// Use an independent connection even if a scenario broke its CDP socket.
			if b.wsURL != "" {
				dialer := websocket.Dialer{HandshakeTimeout: time.Second}
				if ws, _, err := dialer.Dial(b.wsURL, nil); err == nil {
					_ = ws.SetWriteDeadline(time.Now().Add(time.Second))
					_ = ws.WriteJSON(map[string]any{"id": 1, "method": "Browser.close"})
					_ = ws.Close()
					select {
					case <-b.done:
					case <-time.After(3 * time.Second):
					}
				}
			}
		}
		// The job/process group also contains children that outlived the browser.
		if err := b.stopTree(); err != nil {
			b.t.Errorf("stop Chrome process tree: %v", err)
		}
		select {
		case <-b.done:
		case <-time.After(3 * time.Second):
			b.t.Error("Chrome process did not exit after shutdown")
		}
	}
	if b.log != nil {
		_ = b.log.Close()
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := os.RemoveAll(b.profile)
		if err == nil {
			return
		}
		// Windows may still be releasing handles immediately after job termination.
		if !chromeProfileLocked(err) || time.Now().After(deadline) {
			b.t.Errorf("remove Chrome profile: %v", err)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
