package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMoveRenameReconnectRestart(t *testing.T) {
	root := t.TempDir()
	s := testServer(t, root)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Concurrent table"})
	player := post(t, ts.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})
	gc := dial(t, ts.URL, gm)
	read(t, gc, "snapshot")
	pc := dial(t, ts.URL, player)
	snap := read(t, pc, "snapshot")
	var who Member
	json.Unmarshal(snap["you"], &who)
	gc.WriteJSON(Command{Type: "create", Client: "gm", Seq: 1, Token: Token{Name: "Old", X: 10, Y: 20, Size: 80, Color: "#ffffff", Owner: who.ID}})
	token := tokenFrom(t, read(t, gc, "upsert"))
	read(t, gc, "ack")
	read(t, pc, "upsert")
	pc.WriteJSON(Command{Type: "move", Token: Token{ID: token.ID, X: 100, Y: 200}})
	read(t, pc, "move")
	read(t, gc, "move")
	name := "New name"
	gc.WriteJSON(Command{Type: "properties", Client: "gm", Seq: 2, Token: Token{ID: token.ID, X: -999, Y: -999}, Properties: Properties{Name: &name}})
	renamed := tokenFrom(t, read(t, gc, "upsert"))
	read(t, gc, "ack")
	read(t, pc, "upsert")
	if renamed.X != 100 || renamed.Y != 200 {
		t.Fatal("properties overwrote movement")
	}
	final := Command{Type: "final", Client: "player", Seq: 1, Token: Token{ID: token.ID, X: 345, Y: 678}}
	s.mu.Lock()
	writesBeforeFinal := s.writes
	s.mu.Unlock()
	pc.WriteJSON(final)
	read(t, pc, "upsert")
	read(t, pc, "ack")
	read(t, gc, "upsert")
	// ACK and realtime do not wait for persistence.
	s.mu.Lock()
	if s.writes != writesBeforeFinal {
		t.Fatal("final position caused a synchronous disk write")
	}
	s.mu.Unlock()
	disk := testServer(t, root)
	if got := firstScene(disk.sessions[gm["session"]]).Tokens[token.ID]; got.Name != name || got.X != 100 || got.Y != 200 {
		t.Fatalf("unflushed disk state changed at ack: %+v", got)
	}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	disk = testServer(t, root)
	if got := firstScene(disk.sessions[gm["session"]]).Tokens[token.ID]; got.Name != name || got.X != 345 || got.Y != 678 {
		t.Fatalf("flush did not persist final state: %+v", got)
	}
	pc.Close()
	pc = dial(t, ts.URL, player)
	read(t, pc, "snapshot")
	// Lost ACK: retransmit exactly the same command after reconnect.
	pc.WriteJSON(final)
	read(t, pc, "ack")
	if err := s.stop(); err != nil {
		t.Fatal(err)
	}
	ts.Close()
	restarted := testServer(t, root)
	ts2 := httptest.NewServer(restarted.routes())
	defer ts2.Close()
	pc2 := dial(t, ts2.URL, player)
	snap = read(t, pc2, "snapshot")
	var tokens map[string]Token
	json.Unmarshal(snap["tokens"], &tokens)
	got := tokens[token.ID]
	if got.Name != name || got.X != 345 || got.Y != 678 {
		t.Fatalf("restart lost state: %+v", got)
	}
	rev := firstScene(restarted.sessions[gm["session"]]).Revision
	pc2.WriteJSON(final)
	read(t, pc2, "ack")
	pc2.WriteJSON(Command{Type: "move", Client: "player", After: 0, Token: Token{ID: token.ID, X: -1, Y: -1}})
	pc2.WriteJSON(Command{Type: "sync"})
	snap = read(t, pc2, "snapshot")
	json.Unmarshal(snap["tokens"], &tokens)
	if tokens[token.ID].X != 345 {
		t.Fatal("late preview rolled back final position")
	}
	restarted.mu.Lock()
	defer restarted.mu.Unlock()
	if firstScene(restarted.sessions[gm["session"]]).Revision != rev {
		t.Fatal("duplicate final was applied again after restart")
	}
}

func TestGracefulStopSavesPreviewAndRejectsNewWrites(t *testing.T) {
	root := t.TempDir()
	s := testServer(t, root)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.serve(ctx, listener) }()
	url := "http://" + listener.Addr().String()
	gm := post(t, url+"/api/sessions", map[string]string{"name": "Stop"})
	gc := dial(t, url, gm)
	read(t, gc, "snapshot")
	gc.WriteJSON(Command{Type: "create", Token: Token{Name: "Hero", Size: 80}})
	tok := tokenFrom(t, read(t, gc, "upsert"))
	gc.WriteJSON(Command{Type: "move", Token: Token{ID: tok.ID, X: 765, Y: 432}})
	read(t, gc, "move")
	cancel() // Same cancellation branch used by signal.NotifyContext(Ctrl+C).
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown stuck")
	}
	restored := testServer(t, root)
	if got := firstScene(restored.sessions[gm["session"]]).Tokens[tok.ID]; got.X != 765 || got.Y != 432 {
		t.Fatalf("shutdown lost last move: %+v", got)
	}
}

func TestSavedStateErrors(t *testing.T) {
	for _, body := range []string{"broken", "null", "[]", `{"x":null}`, `{"x":{"id":"x"}}`} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "sessions.json")
			os.WriteFile(path, []byte(body), 0600)
			if _, e := newServer(root); e == nil {
				t.Fatal("bad state accepted")
			}
			b, _ := os.ReadFile(path)
			if string(b) != body {
				t.Fatal("bad state overwritten")
			}
		})
	}
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "sessions.json"), 0755)
	if _, e := newServer(root); e == nil {
		t.Fatal("read error silently ignored")
	}
	root = t.TempDir()
	os.WriteFile(filepath.Join(root, "sessions.json.tmp"), []byte("{}"), 0600)
	if _, e := newServer(root); e == nil {
		t.Fatal("orphaned save silently discarded")
	}
}

func TestImmediateCommandFlushFailureKeepsPendingMovementForRetry(t *testing.T) {
	root := t.TempDir()
	s := testServer(t, root)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Failure"})
	gc := dial(t, ts.URL, gm)
	read(t, gc, "snapshot")
	gc.WriteJSON(Command{Type: "create", Token: Token{Name: "Hero", Size: 80}})
	tok := tokenFrom(t, read(t, gc, "upsert"))
	blocker := filepath.Join(root, "sessions.json.tmp")
	if e := os.Mkdir(blocker, 0755); e != nil {
		t.Fatal(e)
	}
	final := Command{Type: "final", Client: "test", Seq: 1, Token: Token{ID: tok.ID, X: 999, Y: 222}}
	gc.WriteJSON(final)
	read(t, gc, "upsert")
	read(t, gc, "ack")
	name := "Saved together"
	properties := Command{Type: "properties", Client: "business", Seq: 1, Token: Token{ID: tok.ID}, Properties: Properties{Name: &name}}
	gc.WriteJSON(properties)
	read(t, gc, "saveError")
	snap := read(t, gc, "snapshot")
	var tokens map[string]Token
	json.Unmarshal(snap["tokens"], &tokens)
	if tokens[tok.ID].X != 999 || tokens[tok.ID].Name == name {
		t.Fatal("failed immediate command lost pending movement or was not rolled back")
	}
	if e := os.Remove(blocker); e != nil {
		t.Fatal(e)
	}
	gc.WriteJSON(properties)
	read(t, gc, "upsert")
	read(t, gc, "ack")
	disk := testServer(t, root)
	if got := firstScene(disk.sessions[gm["session"]]).Tokens[tok.ID]; got.X != 999 || got.Name != name {
		t.Fatalf("retry did not persist pending movement and business change: %+v", got)
	}
}

func TestFatalAuthentication(t *testing.T) {
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Auth"})
	for _, c := range []map[string]string{{"session": "deleted", "key": "bad"}, {"session": gm["session"], "key": "bad"}} {
		ws := dialRaw(t, ts.URL, c)
		read(t, ws, "fatal")
	}
}

func TestReceiptCannotAcknowledgeDifferentCommand(t *testing.T) {
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Receipt"})
	gc := dial(t, ts.URL, gm)
	read(t, gc, "snapshot")
	c := Command{Type: "create", Client: "tab", Seq: 1, Token: Token{Name: "First", Size: 80}}
	gc.WriteJSON(c)
	read(t, gc, "upsert")
	read(t, gc, "ack")
	c.Token.Name = "Other"
	gc.WriteJSON(c)
	read(t, gc, "fatal")
}

func TestReceiptPruningBoundsSessionState(t *testing.T) {
	ss := &Session{Receipts: map[string]Receipt{}}
	keep := "member:current"
	for i := 0; i < maxReceiptsPerSession+25; i++ {
		key := fmt.Sprintf("member:tab-%04d", i)
		ss.Receipts[key] = Receipt{Seq: 1, Digest: key, Updated: int64(i + 1)}
	}
	ss.Receipts[keep] = Receipt{Seq: 7, Digest: "keep", Updated: 1}
	if !pruneReceipts(ss, keep) {
		t.Fatal("receipt pruning did not run")
	}
	if len(ss.Receipts) > maxReceiptsPerSession {
		t.Fatalf("receipt map still too large: %d", len(ss.Receipts))
	}
	if _, ok := ss.Receipts[keep]; !ok {
		t.Fatal("current receipt stream was pruned")
	}
}

func TestUnavailableSceneAcknowledgesDurableCommandAndUnblocksStream(t *testing.T) {
	for _, removeScene := range []bool{false, true} {
		name := "unpublish"
		if removeScene {
			name = "delete"
		}
		t.Run(name, func(t *testing.T) {
			s := testServer(t, t.TempDir())
			ts := httptest.NewServer(s.routes())
			defer ts.Close()
			gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Receipt race"})
			player := post(t, ts.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Player"})
			gc := dialRaw(t, ts.URL, gm)
			gmHome := read(t, gc, "campaignSnapshot")
			var summaries []SceneSummary
			json.Unmarshal(gmHome["scenes"], &summaries)
			firstID := summaries[0].ID

			gc.WriteJSON(Command{Type: "sceneCreate", Client: "setup", Seq: 1, SceneName: "Fallback"})
			gmHome = read(t, gc, "campaignSnapshot")
			read(t, gc, "ack")
			json.Unmarshal(gmHome["scenes"], &summaries)
			var secondID string
			for _, summary := range summaries {
				if summary.ID != firstID {
					secondID = summary.ID
				}
			}
			published := true
			gc.WriteJSON(Command{Type: "sceneUpdate", Client: "setup", Seq: 2, SceneID: secondID, Published: &published})
			read(t, gc, "campaignSnapshot")
			read(t, gc, "ack")

			pc := dialRaw(t, ts.URL, player)
			read(t, pc, "campaignSnapshot")
			pc.WriteJSON(Command{Type: "subscribe", SceneID: firstID})
			playerSnapshot := read(t, pc, "snapshot")
			var who Member
			json.Unmarshal(playerSnapshot["you"], &who)
			for _, sceneID := range []string{firstID, secondID} {
				subscribeSceneLoaded(t, gc, sceneID)
				gc.WriteJSON(Command{Type: "create", Token: Token{Name: sceneID, Size: 80, Owner: who.ID}})
				read(t, gc, "upsert")
			}

			if removeScene {
				gc.WriteJSON(Command{Type: "sceneDelete", Client: "admin", Seq: 1, SceneID: firstID})
			} else {
				published = false
				gc.WriteJSON(Command{Type: "sceneUpdate", Client: "admin", Seq: 1, SceneID: firstID, Published: &published})
			}
			read(t, pc, "campaignSnapshot")
			read(t, gc, "campaignSnapshot")
			read(t, gc, "ack")

			blocked := Command{Type: "final", Client: "player-stream", Seq: 1, SceneID: firstID, Token: Token{ID: "gone", X: 1, Y: 2}}
			pc.WriteJSON(blocked)
			blockedAck := read(t, pc, "ack")
			var blockedError string
			json.Unmarshal(blockedAck["error"], &blockedError)
			if blockedError == "" {
				t.Fatal("unavailable scene command was acknowledged as successful")
			}

			secondSnapshot := subscribeSceneLoaded(t, pc, secondID)
			var tokens map[string]Token
			json.Unmarshal(secondSnapshot["tokens"], &tokens)
			var secondToken Token
			for _, token := range tokens {
				secondToken = token
			}
			accepted := Command{Type: "final", Client: "player-stream", Seq: 2, SceneID: secondID, Token: Token{ID: secondToken.ID, X: 50, Y: 60}}
			pc.WriteJSON(accepted)
			read(t, pc, "upsert")
			acceptedAck := read(t, pc, "ack")
			var acceptedError string
			json.Unmarshal(acceptedAck["error"], &acceptedError)
			if acceptedError != "" {
				t.Fatalf("next command remained blocked: %s", acceptedError)
			}
			pc.Close()
			pc = dialRaw(t, ts.URL, player)
			read(t, pc, "campaignSnapshot")
			pc.WriteJSON(accepted)
			read(t, pc, "ack")
			s.mu.Lock()
			got := s.sessions[gm["session"]].Scenes[secondID].Tokens[secondToken.ID]
			s.mu.Unlock()
			if got.X != 50 || got.Y != 60 {
				t.Fatalf("retry changed durable result: %+v", got)
			}
		})
	}
}

func TestNoOpDurableCommandDoesNotAdvanceSceneRevision(t *testing.T) {
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "No-op"})
	gc := dial(t, ts.URL, gm)
	read(t, gc, "snapshot")
	gc.WriteJSON(Command{Type: "create", Token: Token{Name: "Hero", X: 10, Y: 20, Size: 80, Color: "#ffffff"}})
	tok := tokenFrom(t, read(t, gc, "upsert"))

	s.mu.Lock()
	scene := firstScene(s.sessions[gm["session"]])
	initialRevision := scene.Revision
	initialWrites := s.writes
	s.mu.Unlock()

	gc.WriteJSON(Command{Type: "final", Client: "noop", Seq: 1, SceneID: scene.ID, Token: Token{ID: tok.ID, X: tok.X, Y: tok.Y}})
	ack := read(t, gc, "ack")
	var ackError string
	json.Unmarshal(ack["error"], &ackError)
	if ackError != "" {
		t.Fatalf("no-op final failed: %s", ackError)
	}
	s.mu.Lock()
	if scene.Revision != initialRevision || s.writes != initialWrites || s.dirty {
		t.Fatalf("no-op movement changed persistence state: revision=%d writes=%d dirty=%v", scene.Revision, s.writes, s.dirty)
	}
	s.mu.Unlock()

	name := tok.Name
	gc.WriteJSON(Command{Type: "properties", Client: "noop", Seq: 2, SceneID: scene.ID, Token: Token{ID: tok.ID}, Properties: Properties{Name: &name}})
	ack = read(t, gc, "ack")
	json.Unmarshal(ack["error"], &ackError)
	if ackError != "" {
		t.Fatalf("no-op properties failed: %s", ackError)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sessions[gm["session"]]
	if got := ss.Scenes[scene.ID].Revision; got != initialRevision {
		t.Fatalf("no-op commands advanced revision: got %d want %d", got, initialRevision)
	}
	stream := ss.Members[ss.Keys[gm["key"]]].ID + ":noop"
	if receipt := ss.Receipts[stream]; receipt.Seq != 2 {
		t.Fatalf("no-op receipt was not persisted: %+v", receipt)
	}
}

func TestMovementsCoalesceAndBroadcastBeforeFlush(t *testing.T) {
	root := t.TempDir()
	s := testServer(t, root)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Coalesce"})
	gc := dial(t, ts.URL, gm)
	read(t, gc, "snapshot")
	observer := dial(t, ts.URL, gm)
	read(t, observer, "snapshot")
	gc.WriteJSON(Command{Type: "create", Client: "business", Seq: 1, Token: Token{Name: "Mover", Size: 80}})
	tok := tokenFrom(t, read(t, gc, "upsert"))
	read(t, gc, "ack")
	read(t, observer, "upsert")
	s.mu.Lock()
	writesBeforeMoves := s.writes
	s.mu.Unlock()

	for i := 1; i <= 100; i++ {
		position := float64(i)
		gc.WriteJSON(Command{Type: "move", Token: Token{ID: tok.ID, X: position, Y: position + 1}})
	}
	gc.WriteJSON(Command{Type: "final", Client: "positions", Seq: 1, Token: Token{ID: tok.ID, X: 101, Y: 102}})
	for i := 1; i <= 100; i++ {
		msg := read(t, observer, "move")
		var x float64
		json.Unmarshal(msg["x"], &x)
		if x != float64(i) {
			t.Fatalf("observer saw %v, want %v", x, i)
		}
	}
	finalEvent := tokenFrom(t, read(t, observer, "upsert"))
	if finalEvent.X != 101 || finalEvent.Y != 102 {
		t.Fatalf("observer missed final position: %+v", finalEvent)
	}
	read(t, gc, "ack")
	s.mu.Lock()
	if s.writes != writesBeforeMoves {
		t.Fatalf("movements wrote synchronously: %d -> %d", writesBeforeMoves, s.writes)
	}
	if got := firstScene(s.sessions[gm["session"]]).Tokens[tok.ID]; got.X != 101 || got.Y != 102 {
		t.Fatalf("RAM does not contain latest position: %+v", got)
	}
	s.mu.Unlock()
	beforeFlush := testServer(t, root)
	if got := firstScene(beforeFlush.sessions[gm["session"]]).Tokens[tok.ID]; got.X == 101 {
		t.Fatal("latest position reached disk before flush")
	}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	if s.writes != writesBeforeMoves+1 || s.dirty {
		t.Fatalf("flush count/state: writes=%d dirty=%v", s.writes, s.dirty)
	}
	s.mu.Unlock()
	afterFlush := testServer(t, root)
	if got := firstScene(afterFlush.sessions[gm["session"]]).Tokens[tok.ID]; got.X != 101 || got.Y != 102 {
		t.Fatalf("flush did not coalesce to latest position: %+v", got)
	}
}

func TestServerOwnedFlushRunsOnceForPendingMovement(t *testing.T) {
	root := t.TempDir()
	s := testServer(t, root)
	s.flushInterval = 250 * time.Millisecond
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.serve(ctx, listener) }()
	url := "http://" + listener.Addr().String()
	gm := post(t, url+"/api/sessions", map[string]string{"name": "Periodic flush"})
	gc := dial(t, url, gm)
	read(t, gc, "snapshot")
	gc.WriteJSON(Command{Type: "create", Token: Token{Name: "Mover", Size: 80}})
	tok := tokenFrom(t, read(t, gc, "upsert"))
	s.mu.Lock()
	baseline := s.writes
	s.mu.Unlock()
	for _, x := range []float64{10, 20, 30} {
		gc.WriteJSON(Command{Type: "move", Token: Token{ID: tok.ID, X: x, Y: x}})
	}
	for range 3 {
		read(t, gc, "move")
	}
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		writes := s.writes
		s.mu.Unlock()
		if writes == baseline+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("periodic flush did not persist pending movement")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(3 * s.flushInterval)
	s.mu.Lock()
	if s.writes != baseline+1 {
		t.Fatalf("clean ticks caused extra saves: %d -> %d", baseline, s.writes)
	}
	s.mu.Unlock()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	restored := testServer(t, root)
	if got := firstScene(restored.sessions[gm["session"]]).Tokens[tok.ID]; got.X != 30 || got.Y != 30 {
		t.Fatalf("periodic flush did not keep last position: %+v", got)
	}
}

func TestPositionReceiptSequenceRecoversAfterCrashAndRestart(t *testing.T) {
	root := t.TempDir()
	s := testServer(t, root)
	ts := httptest.NewServer(s.routes())
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Crash recovery"})
	gc := dial(t, ts.URL, gm)
	read(t, gc, "snapshot")
	gc.WriteJSON(Command{Type: "create", Client: "business", Seq: 1, Token: Token{Name: "Mover", Size: 80}})
	tok := tokenFrom(t, read(t, gc, "upsert"))
	read(t, gc, "ack")
	gc.WriteJSON(Command{Type: "final", Client: "positions", Seq: 1, Token: Token{ID: tok.ID, X: 10, Y: 20}})
	read(t, gc, "upsert")
	read(t, gc, "ack")
	gc.Close()
	ts.Close() // Simulate process loss: do not call stop/save on the old Server.

	restarted := testServer(t, root)
	ts2 := httptest.NewServer(restarted.routes())
	defer ts2.Close()
	gc2 := dial(t, ts2.URL, gm)
	read(t, gc2, "snapshot")
	second := Command{Type: "final", Client: "positions", Seq: 2, Token: Token{ID: tok.ID, X: 30, Y: 40}}
	gc2.WriteJSON(second)
	read(t, gc2, "upsert")
	read(t, gc2, "ack")
	if err := restarted.save(); err != nil {
		t.Fatal(err)
	}
	revision := firstScene(restarted.sessions[gm["session"]]).Revision
	restartedAgain := testServer(t, root)
	ts3 := httptest.NewServer(restartedAgain.routes())
	defer ts3.Close()
	gc3 := dial(t, ts3.URL, gm)
	read(t, gc3, "snapshot")
	gc3.WriteJSON(second)
	read(t, gc3, "ack")
	if got := firstScene(restartedAgain.sessions[gm["session"]]); got.Revision != revision || got.Tokens[tok.ID].X != 30 {
		t.Fatalf("duplicate after restart changed state: revision=%d token=%+v", got.Revision, got.Tokens[tok.ID])
	}
}

func TestStorageFailureStatePersistsUntilSuccessfulFlush(t *testing.T) {
	root := t.TempDir()
	s := testServer(t, root)
	s.flushInterval = 50 * time.Millisecond
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.serve(ctx, listener) }()
	url := "http://" + listener.Addr().String()
	gm := post(t, url+"/api/sessions", map[string]string{"name": "Storage status"})
	gc := dial(t, url, gm)
	read(t, gc, "snapshot")
	gc.WriteJSON(Command{Type: "create", Token: Token{Name: "Mover", Size: 80}})
	tok := tokenFrom(t, read(t, gc, "upsert"))

	blocker := filepath.Join(root, "sessions.json.tmp")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	gc.WriteJSON(Command{Type: "final", Client: "positions", Seq: 1, Token: Token{ID: tok.ID, X: 77, Y: 88}})
	read(t, gc, "upsert")
	read(t, gc, "ack")
	read(t, gc, "storageError")

	s.mu.Lock()
	if !s.storageDegraded {
		s.mu.Unlock()
		t.Fatal("server did not retain degraded storage state")
	}
	s.mu.Unlock()

	observer := dialRaw(t, url, gm)
	read(t, observer, "campaignSnapshot")
	read(t, observer, "storageError")

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	read(t, gc, "storageRecovered")

	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		degraded := s.storageDegraded
		s.mu.Unlock()
		if !degraded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("storage state did not recover")
		}
		time.Sleep(10 * time.Millisecond)
	}

	restored := testServer(t, root)
	if got := firstScene(restored.sessions[gm["session"]]).Tokens[tok.ID]; got.X != 77 || got.Y != 88 {
		t.Fatalf("successful recovery flush did not persist pending movement: %+v", got)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
