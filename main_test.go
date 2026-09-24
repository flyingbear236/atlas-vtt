package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func testServer(t *testing.T, root string) *Server {
	t.Helper()
	s, e := newServer(root)
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func post(t *testing.T, url string, body any) map[string]string {
	t.Helper()
	b, _ := json.Marshal(body)
	r, e := http.Post(url, "application/json", bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("%d: %s", r.StatusCode, b)
	}
	var out map[string]string
	if e = json.NewDecoder(r.Body).Decode(&out); e != nil {
		t.Fatal(e)
	}
	return out
}
func dial(t *testing.T, url string, c map[string]string) *websocket.Conn {
	ws := dialRaw(t, url, c)
	enterFirstScene(t, ws)
	return ws
}
func dialRaw(t *testing.T, url string, c map[string]string) *websocket.Conn {
	t.Helper()
	ws, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(url, "http")+"/ws", nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ws.Close() })
	if e = ws.WriteJSON(c); e != nil {
		t.Fatal(e)
	}
	return ws
}
func requestSceneRegion(t *testing.T, ws *websocket.Conn, sceneID string) {
	t.Helper()
	initial := read(t, ws, "snapshot")
	var scene SceneMetadata
	if e := json.Unmarshal(initial["scene"], &scene); e != nil {
		t.Fatal(e)
	}
	if scene.ID != sceneID {
		t.Fatalf("subscribed scene %s, got %s", sceneID, scene.ID)
	}
	half := maxSceneRegionSpan / 2
	region := SceneRegion{Left: -half, Top: -half, Right: half, Bottom: half}
	if e := ws.WriteJSON(Command{Type: "view", SceneID: scene.ID, Region: &region}); e != nil {
		t.Fatal(e)
	}
}

func subscribeScene(t *testing.T, ws *websocket.Conn, sceneID string) {
	t.Helper()
	if e := ws.WriteJSON(Command{Type: "subscribe", SceneID: sceneID}); e != nil {
		t.Fatal(e)
	}
	requestSceneRegion(t, ws, sceneID)
}

func subscribeSceneLoaded(t *testing.T, ws *websocket.Conn, sceneID string) map[string]json.RawMessage {
	t.Helper()
	subscribeScene(t, ws, sceneID)
	return read(t, ws, "snapshot")
}

func enterFirstScene(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	home := read(t, ws, "campaignSnapshot")
	var scenes []SceneSummary
	if e := json.Unmarshal(home["scenes"], &scenes); e != nil || len(scenes) == 0 {
		t.Fatalf("campaign has no accessible scene: %v", e)
	}
	subscribeScene(t, ws, scenes[0].ID)
}

func read(t *testing.T, c *websocket.Conn, kind string) map[string]json.RawMessage {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var msg map[string]json.RawMessage
		if e := c.ReadJSON(&msg); e != nil {
			t.Fatal(e)
		}
		var typ string
		json.Unmarshal(msg["type"], &typ)
		if typ == kind {
			return msg
		}
	}
}
func tokenFrom(t *testing.T, msg map[string]json.RawMessage) Token {
	t.Helper()
	var out Token
	if e := json.Unmarshal(msg["token"], &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestSessionPermissionsSyncAndPersistence(t *testing.T) {
	root := t.TempDir()
	s := testServer(t, root)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Test"})
	player := post(t, ts.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Alice"})
	gc := dial(t, ts.URL, gm)
	read(t, gc, "snapshot")
	pc := dial(t, ts.URL, player)
	snap := read(t, pc, "snapshot")
	var you Member
	json.Unmarshal(snap["you"], &you)
	gc.WriteJSON(map[string]any{"type": "create", "token": Token{Name: "Hero", X: 10, Y: 20, Size: 80, Color: "#abcdef"}})
	tok := tokenFrom(t, read(t, gc, "upsert"))
	read(t, pc, "upsert")
	pc.WriteJSON(map[string]any{"type": "move", "token": Token{ID: tok.ID, X: 100, Y: 200}})
	read(t, pc, "error")
	snap = read(t, pc, "snapshot")
	var tokens map[string]Token
	json.Unmarshal(snap["tokens"], &tokens)
	if tokens[tok.ID].X != 10 {
		t.Fatal("unauthorized movement applied")
	}
	owner := you.ID
	gc.WriteJSON(Command{Type: "properties", Token: Token{ID: tok.ID}, Properties: Properties{Owner: &owner}})
	read(t, gc, "upsert")
	read(t, pc, "upsert")
	pc.WriteJSON(map[string]any{"type": "move", "token": Token{ID: tok.ID, X: 300, Y: 400}})
	movedEvent := read(t, gc, "move")
	read(t, pc, "move")
	var movedX, movedY float64
	json.Unmarshal(movedEvent["x"], &movedX)
	json.Unmarshal(movedEvent["y"], &movedY)
	if movedX != 300 || movedY != 400 {
		t.Fatal("movement not synchronized")
	}
	hide := true
	gc.WriteJSON(Command{Type: "properties", Token: Token{ID: tok.ID}, Properties: Properties{Hidden: &hide}})
	read(t, gc, "upsert")
	hidden := read(t, pc, "delete")
	if _, ok := hidden["token"]; ok {
		t.Fatal("hidden token leaked")
	}
	pc.WriteJSON(map[string]string{"type": "sync"})
	snap = read(t, pc, "snapshot")
	tokens = map[string]Token{}
	json.Unmarshal(snap["tokens"], &tokens)
	if len(tokens) != 0 {
		t.Fatal("snapshot leaked hidden token")
	}
	hide = false
	gc.WriteJSON(Command{Type: "properties", Token: Token{ID: tok.ID}, Properties: Properties{Hidden: &hide}})
	read(t, gc, "upsert")
	read(t, pc, "upsert")
	pc.Close()
	pc2 := dial(t, ts.URL, player)
	snap = read(t, pc2, "snapshot")
	tokens = map[string]Token{}
	json.Unmarshal(snap["tokens"], &tokens)
	if tokens[tok.ID].X != 300 {
		t.Fatal("reconnect lost position")
	}
	if e := s.save(); e != nil {
		t.Fatal(e)
	}
	restored := testServer(t, root)
	if firstScene(restored.sessions[gm["session"]]).Tokens[tok.ID].Y != 400 {
		t.Fatal("persistence lost state")
	}
	if restored.sessions[gm["session"]].Keys[gm["key"]] == "" {
		t.Fatal("credentials lost")
	}
	gc.WriteJSON(Command{Type: "delete", Token: Token{ID: tok.ID}})
	read(t, gc, "delete")
	read(t, pc2, "delete")
}
func TestTilePyramidAndAssetAccess(t *testing.T) {
	root := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 1025, 513))
	for y := 0; y < 513; y++ {
		for x := 0; x < 1025; x++ {
			img.SetRGBA(x, y, color.RGBA{123, 80, 31, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	a, e := prepare(root, b.Bytes(), "map")
	if e != nil {
		t.Fatal(e)
	}
	if a.RenderMode != "tiled" || a.Levels != 3 {
		t.Fatalf("large image mode=%q levels=%d", a.RenderMode, a.Levels)
	}
	f, e := os.Open(filepath.Join(root, "assets", a.ID, "0_2_1.png"))
	if e != nil {
		t.Fatal(e)
	}
	edge, _, e := image.Decode(f)
	f.Close()
	if e != nil || edge.Bounds().Dx() != 1 || edge.Bounds().Dy() != 1 {
		t.Fatal("incorrect edge tile")
	}
	again, e := prepare(root, b.Bytes(), "map")
	if e != nil || again.ID != a.ID {
		t.Fatal("unstable asset hash")
	}
	if _, e = prepare(root, []byte("invalid"), "map"); e == nil {
		t.Fatal("invalid image accepted")
	}
	s := testServer(t, root)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Test"})
	s.mu.Lock()
	ss := s.sessions[gm["session"]]
	scene := firstScene(ss)
	ss.Assets[a.ID] = a
	floorID := firstFloorID(scene)
	layerID := firstLayerID(scene, floorID)
	scene.Elements["map"] = SceneElement{ID: "map", FloorID: floorID, LayerID: layerID, AssetID: a.ID, Transform: Transform{Width: float64(a.Width), Height: float64(a.Height)}, Visible: true, Opacity: 1}
	scene.Revision++
	scene.rebuildRuntime()
	s.mu.Unlock()
	url := ts.URL + "/api/asset/" + a.ID + "/0_0_0.png?session=" + gm["session"] + "&scene=" + scene.ID
	res, e := http.Get(url)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("anonymous asset access")
	}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+gm["key"])
	res, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
		t.Fatal("asset delivery/cache headers")
	}
}

func TestCurrentProtocolUsesCompactMoveAndPresence(t *testing.T) {
	root := t.TempDir()
	s := testServer(t, root)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Modern"})
	player := post(t, ts.URL+"/api/join", map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Alice"})

	gc := dial(t, ts.URL, gm)
	read(t, gc, "snapshot")
	pc := dial(t, ts.URL, player)
	snap := read(t, pc, "snapshot")

	presence := read(t, gc, "presence")
	var online bool
	if e := json.Unmarshal(presence["online"], &online); e != nil || !online {
		t.Fatal("presence did not report player online")
	}
	var member Member
	if e := json.Unmarshal(presence["member"], &member); e != nil || member.ID == "" {
		t.Fatal("presence did not include member")
	}

	var who Member
	if e := json.Unmarshal(snap["you"], &who); e != nil {
		t.Fatal(e)
	}
	gc.WriteJSON(Command{Type: "create", Token: Token{Name: "Hero", X: 10, Y: 20, Size: 80, Color: "#abcdef", Owner: who.ID}})
	tok := tokenFrom(t, read(t, gc, "upsert"))
	read(t, pc, "upsert")

	pc.WriteJSON(Command{Type: "move", Token: Token{ID: tok.ID, X: 300, Y: 400}})
	for _, c := range []*websocket.Conn{gc, pc} {
		msg := read(t, c, "move")
		if _, ok := msg["token"]; ok {
			t.Fatal("compact move unexpectedly included full token")
		}
		var x, y float64
		json.Unmarshal(msg["x"], &x)
		json.Unmarshal(msg["y"], &y)
		if x != 300 || y != 400 {
			t.Fatalf("compact move coordinates: %v, %v", x, y)
		}
	}

	pc.Close()
	presence = read(t, gc, "presence")
	if e := json.Unmarshal(presence["online"], &online); e != nil || online {
		t.Fatal("presence did not report player offline")
	}
}

func TestCreateAndJoinRollbackWhenPersistenceFails(t *testing.T) {
	root := t.TempDir()
	s := testServer(t, root)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	blocker := filepath.Join(root, "sessions.json.tmp")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"name": "Must roll back"})
	res, err := http.Post(ts.URL+"/api/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("create status = %d", res.StatusCode)
	}
	s.mu.Lock()
	if len(s.sessions) != 0 {
		t.Fatal("failed create remained in memory")
	}
	s.mu.Unlock()
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Stable"})
	s.mu.Lock()
	ss := s.sessions[gm["session"]]
	members, keys, revision := len(ss.Members), len(ss.Keys), ss.CampaignRevision
	s.mu.Unlock()
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(map[string]string{"session": gm["session"], "invite": gm["invite"], "name": "Ghost"})
	res, err = http.Post(ts.URL+"/api/join", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("join status = %d", res.StatusCode)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(ss.Members) != members || len(ss.Keys) != keys || ss.CampaignRevision != revision {
		t.Fatal("failed join remained in memory")
	}
}

func TestTokenAssetUploadDoesNotReviseOrBroadcastScene(t *testing.T) {
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Assets"})
	s.mu.Lock()
	ss := s.sessions[gm["session"]]
	scene := firstScene(ss)
	sceneID, revision := scene.ID, scene.Revision
	s.mu.Unlock()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	var pngBody bytes.Buffer
	if err := png.Encode(&pngBody, img); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/upload?session="+gm["session"]+"&scene="+sceneID+"&kind=token", bytes.NewReader(pngBody.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+gm["key"])
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(res.Body)
		t.Fatalf("upload status %d: %s", res.StatusCode, message)
	}
	var asset Asset
	if err := json.NewDecoder(res.Body).Decode(&asset); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if scene.Revision != revision {
		t.Fatalf("token asset changed scene revision: %d -> %d", revision, scene.Revision)
	}
	if ss.Assets[asset.ID].Kind != "token" {
		t.Fatal("token asset was not stored at campaign level")
	}
}
