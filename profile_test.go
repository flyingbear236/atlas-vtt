package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Test-only host in a separate process: fixture generation, CDP and the load
// driver are NOT included in server CPU/RAM measurements.
func TestProfileHost(t *testing.T) {
	root := os.Getenv("ATLAS_PROFILE_HOST")
	if root == "" {
		t.Skip("profiling helper")
	}
	s := testServer(t, filepath.Join(root, "data"))
	var workers profileWorkerTracker
	imageWorkerObserver = workers.observe
	defer func() { imageWorkerObserver = nil }()
	m := &Member{ID: "gm", Name: "Load GM", Role: "gm", Secret: "load-key"}
	scene := newScene("load-scene", "Load scene")
	scene.Published = true
	ss := &Session{ID: "load", Name: "Profile", Invite: "load-invite", Members: map[string]*Member{m.ID: m}, Keys: map[string]string{m.Secret: m.ID}, Assets: map[string]Asset{}, Receipts: map[string]Receipt{}, CampaignRevision: 1, Scenes: map[string]*Scene{scene.ID: scene}}
	s.sessions[ss.ID] = ss
	// Tiny, distinct assets expose accidental upsampling, and overflow at 512px.
	var small []Asset
	for i := 0; i < 128; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 32, 32))
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				img.SetRGBA(x, y, color.RGBA{uint8(i + 20), uint8(x * 7), uint8(y * 7), 255})
			}
		}
		var b bytes.Buffer
		png.Encode(&b, img)
		a, e := prepare(s.root, b.Bytes(), "token")
		if e != nil {
			t.Fatal(e)
		}
		ss.Assets[a.ID] = a
		small = append(small, a)
	}
	layerAssets := map[int][]Asset{}
	type bakedProxyFixture struct {
		asset       Asset
		sourceBytes int
	}
	bakedProxyAssets := map[string]bakedProxyFixture{}
	ensureBakedProxy := func(width, height int) (bakedProxyFixture, error) {
		key := fmt.Sprintf("%dx%d", width, height)
		if fixture, ok := bakedProxyAssets[key]; ok {
			return fixture, nil
		}
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				off := y*img.Stride + x*4
				// Detailed deterministic pixels keep the proxy from becoming an
				// unrealistically tiny/compressible raster. This models only the
				// runtime representation of a baked layer, not bake implementation.
				v := uint8((x*17 + y*11 + (x*y)%251) % 256)
				img.Pix[off] = v
				img.Pix[off+1] = uint8((x*7 + y*19) % 256)
				img.Pix[off+2] = uint8((x*3 + y*5 + int(v)) % 256)
				img.Pix[off+3] = 255
			}
		}
		var b bytes.Buffer
		if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 78}); err != nil {
			return bakedProxyFixture{}, err
		}
		img = nil
		runtime.GC()
		debug.FreeOSMemory()
		a, err := prepare(s.root, b.Bytes(), assetKindScene)
		if err != nil {
			return bakedProxyFixture{}, err
		}
		fixture := bakedProxyFixture{asset: a, sourceBytes: b.Len()}
		bakedProxyAssets[key] = fixture
		return fixture, nil
	}
	ensureLayerAssets := func(size, count int) ([]Asset, error) {
		assets := layerAssets[size]
		for len(assets) < count {
			i := len(assets)
			img := image.NewRGBA(image.Rect(0, 0, size, size))
			for y := 0; y < size; y++ {
				for x := 0; x < size; x++ {
					off := y*img.Stride + x*4
					img.Pix[off] = uint8((x*11 + y*3 + i*29) % 256)
					img.Pix[off+1] = uint8((x*5 + y*17 + i*13) % 256)
					img.Pix[off+2] = uint8((x*y + i*37) % 256)
					img.Pix[off+3] = 255
				}
			}
			var b bytes.Buffer
			if err := png.Encode(&b, img); err != nil {
				return nil, err
			}
			a, err := prepare(s.root, b.Bytes(), assetKindScene)
			if err != nil {
				return nil, err
			}
			assets = append(assets, a)
		}
		layerAssets[size] = assets
		return assets[:count], nil
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	base := s.routes()
	mux := http.NewServeMux()
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		b, _ := web.ReadFile("web/app.js")
		source := string(b)
		const immutableCanvasBinding = "const canvas = $('board'), ctx = canvas.getContext('2d');"
		const mutableCanvasBinding = "let canvas = $('board'), ctx = canvas.getContext('2d');"
		switch {
		case strings.Contains(source, mutableCanvasBinding):
			// Current app.js already exposes mutable bindings for the isolated
			// profiler's canvas replacement experiment.
		case strings.Contains(source, immutableCanvasBinding):
			// Keep compatibility with snapshots from before the production binding
			// was made mutable.
			source = strings.Replace(source, immutableCanvasBinding, mutableCanvasBinding, 1)
		default:
			http.Error(w, "profiling canvas binding not found", http.StatusInternalServerError)
			return
		}
		io.WriteString(w, source)
		io.WriteString(w, profileProbe)
	})
	mux.HandleFunc("POST /__bench/scene", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Count, Images, MapWidth, MapElements int
			DistinctMaps                         bool
		}
		json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		defer s.mu.Unlock()
		if req.MapWidth == 0 {
			req.MapWidth = 4096
		}
		floorID, layerID := firstFloorID(scene), firstLayerID(scene, firstFloorID(scene))
		scene.Elements = map[string]SceneElement{}
		var maps []Asset
		for _, a := range ss.Assets {
			if isSceneRasterKind(a.Kind) && a.Width == req.MapWidth {
				maps = append(maps, a)
			}
		}
		sort.Slice(maps, func(i, j int) bool { return maps[i].ID < maps[j].ID })
		mapElements := req.MapElements
		if mapElements == 0 && len(maps) > 0 {
			mapElements = 1
		}
		for i := 0; i < mapElements && len(maps) > 0; i++ {
			a := maps[0]
			if req.DistinctMaps {
				a = maps[i%len(maps)]
			}
			id := fmt.Sprintf("map-%d", i)
			scene.Elements[id] = SceneElement{ID: id, FloorID: floorID, LayerID: layerID, AssetID: a.ID, Name: fmt.Sprintf("Large map %d", i+1), Transform: Transform{X: float64(i * 160), Y: float64(i * 120), Width: float64(a.Width), Height: float64(a.Height)}, ZOrder: i, Visible: true, Opacity: 1}
			scene.Bounds.Width = math.Max(scene.Bounds.Width, float64(a.Width+i*160))
			scene.Bounds.Height = math.Max(scene.Bounds.Height, float64(a.Height+i*120))
		}
		scene.Tokens = map[string]Token{}
		for i := 0; i < req.Count; i++ {
			key := fmt.Sprintf("t%04d", i)
			if i == 0 {
				key = "moving"
			}
			token := Token{ID: key, Name: fmt.Sprintf("Token %d", i), FloorID: floorID, LayerID: layerIDByKind(scene, floorID, layerKindTokens), X: float64(200 + (i%50)*65), Y: float64(200 + (i/50)*65), Size: 48, Color: "#c2d89b"}
			if req.Images > 0 {
				token.Asset = small[i%req.Images].ID
			}
			scene.Tokens[key] = token
		}
		scene.Revision++
		scene.rebuildRuntime()
		s.dirty = true
		s.publishSceneSnapshot(ss, scene.ID)
		reply(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /__bench/layer", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Count, Images, SourceSize, ElementSize int
			Layout, Representation                 string
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			fail(w, 400, "bad layer benchmark")
			return
		}
		if req.Representation == "" {
			req.Representation = "elements"
		}
		if req.Count < 1 || req.Count > 1000 || req.Images < 1 || req.Images > 64 || req.SourceSize < 32 || req.SourceSize > 1024 || req.ElementSize < 16 || req.ElementSize > 1024 || (req.Layout != "dense" && req.Layout != "grid") || (req.Representation != "elements" && req.Representation != "baked-proxy") {
			fail(w, 400, "bad layer benchmark")
			return
		}
		assets, err := ensureLayerAssets(req.SourceSize, req.Images)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		columns := min(25, req.Count)
		step := float64(req.ElementSize)
		if req.Layout == "dense" {
			step = math.Max(28, float64(req.ElementSize)*0.28)
		} else {
			step *= 1.08
		}
		rows := (req.Count + columns - 1) / columns
		margin := float64(req.ElementSize)
		bounds := SceneBounds{Width: margin*2 + float64(columns-1)*step + float64(req.ElementSize), Height: margin*2 + float64(rows-1)*step + float64(req.ElementSize)}
		var bakedProxy bakedProxyFixture
		if req.Representation == "baked-proxy" {
			bakedProxy, err = ensureBakedProxy(max(1, int(math.Ceil(bounds.Width))), max(1, int(math.Ceil(bounds.Height))))
			if err != nil {
				fail(w, 500, err.Error())
				return
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		floorID, layerID := firstFloorID(scene), firstLayerID(scene, firstFloorID(scene))
		for _, asset := range assets {
			ss.Assets[asset.ID] = asset
		}
		scene.Tokens = map[string]Token{}
		scene.Bounds = bounds
		sourceElements := make(map[string]SceneElement, req.Count)
		for i := 0; i < req.Count; i++ {
			elementID := fmt.Sprintf("layer-%04d", i)
			sourceElements[elementID] = SceneElement{ID: elementID, FloorID: floorID, LayerID: layerID, AssetID: assets[i%len(assets)].ID, Name: fmt.Sprintf("Static %d", i), Transform: Transform{X: margin + float64(i%columns)*step, Y: margin + float64(i/columns)*step, Width: float64(req.ElementSize), Height: float64(req.ElementSize), Rotation: float64(i%9 - 4)}, ZOrder: i, Visible: true, Opacity: 0.65 + float64(i%4)*0.1}
		}
		sourceElementJSON, _ := json.Marshal(sourceElements)
		bakedProxySourceBytes := 0
		if req.Representation == "baked-proxy" {
			ss.Assets[bakedProxy.asset.ID] = bakedProxy.asset
			bakedProxySourceBytes = bakedProxy.sourceBytes
			scene.Elements = map[string]SceneElement{
				"baked-proxy": {ID: "baked-proxy", FloorID: floorID, LayerID: layerID, AssetID: bakedProxy.asset.ID, Name: "Test-only baked proxy", Transform: Transform{Width: scene.Bounds.Width, Height: scene.Bounds.Height}, Visible: true, Opacity: 1},
			}
		} else {
			scene.Elements = sourceElements
		}
		scene.Revision++
		scene.rebuildRuntime()
		s.dirty = true
		region := SceneRegion{Left: 0, Top: 0, Right: scene.Bounds.Width, Bottom: scene.Bounds.Height}
		for peer := range s.peers {
			if peer.session == ss.ID {
				peer.sceneID = scene.ID
				peer.floorID = floorID
				peer.region = &region
			}
		}
		s.publishSceneSnapshot(ss, scene.ID)
		var snapshot []byte
		var snapshotBuild []float64
		for i := 0; i < 30; i++ {
			at := time.Now()
			snapshot, _ = json.Marshal(s.snapshotSceneAtFloor(ss, m, scene.ID, &region, floorID))
			snapshotBuild = append(snapshotBuild, float64(time.Since(at).Microseconds())/1000)
		}
		renderedElements, _ := json.Marshal(scene.Elements)
		reply(w, map[string]any{"ok": true, "revision": scene.Revision, "snapshotBytes": len(snapshot), "snapshotBuildMs": stats(snapshotBuild), "elementJSONBytes": len(renderedElements), "sourceElementJSONBytes": len(sourceElementJSON), "bounds": scene.Bounds, "elements": req.Count, "renderedElements": len(scene.Elements), "uniqueAssets": req.Images, "sourceSize": req.SourceSize, "elementSize": req.ElementSize, "layout": req.Layout, "representation": req.Representation, "bakedProxySourceBytes": bakedProxySourceBytes})
	})
	mux.HandleFunc("GET /__bench/storage", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var marshal, save []float64
		for i := 0; i < 20; i++ {
			at := time.Now()
			json.Marshal(s.sessions)
			marshal = append(marshal, float64(time.Since(at).Microseconds())/1000)
			s.dirty = true
			at = time.Now()
			e := s.saveLocked()
			if e != nil {
				fail(w, 500, e.Error())
				return
			}
			save = append(save, float64(time.Since(at).Microseconds())/1000)
		}
		b, _ := json.Marshal(s.sessions)
		reply(w, map[string]any{"jsonBytes": len(b), "marshalMs": stats(marshal), "saveSyncMs": stats(save)})
	})
	mux.HandleFunc("GET /__bench/profile", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name != "mutex" && name != "heap" && name != "block" {
			fail(w, 400, "bad profile")
			return
		}
		pprof.Lookup(name).WriteTo(w, 0)
	})
	mux.HandleFunc("GET /__bench/metrics", func(w http.ResponseWriter, r *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		reply(w, map[string]any{"process": readProcessStats(os.Getpid()), "heapAlloc": m.HeapAlloc, "heapSys": m.HeapSys, "gc": m.NumGC})
	})
	mux.HandleFunc("GET /__bench/worker", func(w http.ResponseWriter, r *http.Request) { reply(w, workers.snapshot()) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mux.HandleFunc("POST /__bench/stop", func(w http.ResponseWriter, r *http.Request) { reply(w, map[string]bool{"ok": true}); cancel() })
	mux.Handle("/", base)
	runtime.SetMutexProfileFraction(1)
	runtime.SetBlockProfileRate(1000000)
	cpu, e := os.Create(filepath.Join(root, "server.cpu.pprof"))
	if e != nil {
		t.Fatal(e)
	}
	pprof.StartCPUProfile(cpu)
	defer cpu.Close()
	defer pprof.StopCPUProfile()
	meta, _ := json.Marshal(map[string]any{"url": "http://" + listener.Addr().String(), "pid": os.Getpid()})
	os.WriteFile(filepath.Join(root, "host.json"), meta, 0600)
	h := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go h.Serve(listener)
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.stop()
			h.Close()
			return
		case <-ticker.C:
			s.save()
		}
	}
}

func stats(values []float64) map[string]float64 {
	v := append([]float64{}, values...)
	sort.Float64s(v)
	if len(v) == 0 {
		return map[string]float64{}
	}
	sum := 0.
	for _, x := range v {
		sum += x
	}
	return map[string]float64{"mean": sum / float64(len(v)), "p50": v[len(v)/2], "p95": v[min(len(v)-1, int(float64(len(v))*.95))], "max": v[len(v)-1]}
}

type profileCDP struct {
	t                                *testing.T
	c                                *websocket.Conn
	seq                              int
	HTTPBytes, Requests, WSIn, WSOut int64
	Errors                           []string
}

func profileDial(t *testing.T, url string) *profileCDP {
	c, _, e := websocket.DefaultDialer.Dial(url, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	return &profileCDP{t: t, c: c}
}
func (d *profileCDP) call(method string, params any) json.RawMessage {
	d.seq++
	d.c.WriteJSON(map[string]any{"id": d.seq, "method": method, "params": params})
	d.c.SetReadDeadline(time.Now().Add(90 * time.Second))
	for {
		var msg struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if e := d.c.ReadJSON(&msg); e != nil {
			d.t.Fatal(e)
		}
		switch msg.Method {
		case "Network.loadingFinished":
			var p struct {
				EncodedDataLength float64 `json:"encodedDataLength"`
			}
			json.Unmarshal(msg.Params, &p)
			d.HTTPBytes += int64(p.EncodedDataLength)
			d.Requests++
		case "Network.webSocketFrameReceived", "Network.webSocketFrameSent":
			var p struct {
				Response struct {
					PayloadData string `json:"payloadData"`
				} `json:"response"`
			}
			json.Unmarshal(msg.Params, &p)
			if msg.Method == "Network.webSocketFrameReceived" {
				d.WSIn += int64(len(p.Response.PayloadData))
			} else {
				d.WSOut += int64(len(p.Response.PayloadData))
			}
		case "Runtime.exceptionThrown":
			d.Errors = append(d.Errors, string(msg.Params))
		}
		if msg.ID == d.seq {
			if len(msg.Error) > 0 {
				d.t.Fatalf("CDP %s: %s", method, msg.Error)
			}
			return msg.Result
		}
	}
}
func (d *profileCDP) eval(js string) json.RawMessage {
	r := d.call("Runtime.evaluate", map[string]any{"expression": js, "awaitPromise": true, "returnByValue": true})
	var v struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	json.Unmarshal(r, &v)
	if len(v.Exception) > 0 {
		d.t.Fatalf("JS: %s", r)
	}
	return v.Result.Value
}
func (d *profileCDP) resetNet() { d.HTTPBytes = 0; d.Requests = 0; d.WSIn = 0; d.WSOut = 0 }

func profileJSON(t *testing.T, url, method string, body any) json.RawMessage {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("%s: %d %s", url, res.StatusCode, b)
	}
	return b
}

type browserMemoryLifecycle struct {
	Camera struct {
		X, Y, Scale float64
	} `json:"camera"`
	SceneState struct {
		SceneID, FloorID, SelectedToken, SelectedElement, SelectedTransition, TransitionDraftPhase string
		SceneRevision, Tokens, Elements                                                            int64
	} `json:"sceneState"`
	Canvas struct {
		Width, Height                 int
		ViewportWidth, ViewportHeight float64
		DPR, DevicePixelRatio         float64
	} `json:"canvas"`
	Images struct {
		Entries, Pending, ActiveLoads, LiveBitmaps    int
		TrackedBytes, BitmapBytes, FallbackBytes      int64
		ArtworkBytes, CanvasBytes                     int64
		BitmapCreated, BitmapClosed, BitmapCloseCalls int64
		BitmapCreatedBytes, BitmapClosedBytes         int64
		DecodeCount, DiskReads                        int64
	} `json:"images"`
	ResetFrames struct {
		Count                    int
		Mean, P50, P95, P99, Max float64
	} `json:"resetFrames"`
}

type browserMemoryTotals struct {
	WorkingSet, PrivateBytes, RendererPrivateBytes, GPUPrivateBytes uint64
}

type browserMemoryOutcome struct {
	Name                                    string
	Baseline, BeforeAction, After10         browserMemoryTotals
	BeforeActionLifecycle, After10Lifecycle browserMemoryLifecycle
	HTTPRequests, HTTPBytes                 int64
	ResetFrameCount                         int
	ResetMaxFrameMS                         float64
	ResetFrameMetricAvailable               bool
	JSErrors                                int
}

func runBrowserMemoryCausalProfile(t *testing.T, output, chrome, hostURL string, report map[string]any, rows *[]map[string]any, persist func()) {
	t.Helper()
	report["profile"] = "canvas-gpu-lifetime-causal"
	report["largeAsset"] = "one 10000x10000 tiled scene image"
	report["comparison"] = "each variant runs in a fresh Chromium process and fresh browser profile"

	browserLog, err := os.OpenFile(filepath.Join(output, "browser.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer browserLog.Close()

	var csv strings.Builder
	csv.WriteString("variant,stage,seconds_after_reset,private_mib,working_set_mib,renderer_private_mib,gpu_private_mib,js_heap_mib,atlas_bitmap_mib,atlas_fallback_mib,atlas_artwork_mib,live_bitmaps,bitmap_created,bitmap_closed,bitmap_close_calls,bitmap_created_mib,bitmap_closed_mib,http_requests,http_mib,idb_reads,decode_count,reset_frames,reset_max_frame_ms,camera_x,camera_y,camera_scale,canvas_width,canvas_height,dpr,js_errors\n")

	type resetStats struct {
		ElapsedMS float64 `json:"elapsedMs"`
		Frame     struct {
			Count int     `json:"count"`
			Mean  float64 `json:"mean"`
			P50   float64 `json:"p50"`
			P95   float64 `json:"p95"`
			P99   float64 `json:"p99"`
			Max   float64 `json:"max"`
		} `json:"frame"`
	}
	type checkpointResult struct {
		Browser   browserMemoryTotals
		Lifecycle browserMemoryLifecycle
		JSHeap    float64
	}
	type variantSpec struct {
		Name   string
		Action string
	}
	variants := []variantSpec{{"A_control", "control"}, {"B_backing_store_reset", "backing"}, {"C_canvas_context_replacement", "replace"}, {"D_page_reload", "reload"}}
	outcomes := make([]browserMemoryOutcome, 0, len(variants))
	const mib = float64(1024 * 1024)

	for variantIndex, variant := range variants {
		t.Logf("browser memory causal variant %s", variant.Name)
		profileJSON(t, hostURL+"/__bench/scene", "POST", map[string]any{"Count": 0, "MapWidth": 10000, "MapElements": 1, "DistinctMaps": true})

		profileDir, err := os.MkdirTemp(output, ".chrome-"+variant.Name+"-")
		if err != nil {
			t.Fatal(err)
		}
		args := []string{"--no-sandbox", "--headless=new", "--no-first-run", "--no-default-browser-check", "--disable-background-timer-throttling", "--disable-renderer-backgrounding", "--disable-backgrounding-occluded-windows", "--remote-debugging-port=0", "--user-data-dir=" + profileDir, "about:blank"}
		browser := exec.Command(chrome, args...)
		browser.Stdout = browserLog
		browser.Stderr = browserLog
		if err := browser.Start(); err != nil {
			os.RemoveAll(profileDir)
			t.Fatal(err)
		}
		cleanupBrowser := func() {
			if browser.Process != nil {
				_ = browser.Process.Kill()
			}
			_ = browser.Wait()
			_ = os.RemoveAll(profileDir)
		}

		func() {
			defer cleanupBrowser()
			var port, endpoint string
			for i := 0; i < 100; i++ {
				if b, err := os.ReadFile(filepath.Join(profileDir, "DevToolsActivePort")); err == nil {
					lines := strings.Split(strings.TrimSpace(string(b)), "\n")
					if len(lines) >= 2 {
						port = strings.TrimSpace(lines[0])
						endpoint = strings.TrimSpace(lines[1])
						break
					}
				}
				time.Sleep(100 * time.Millisecond)
			}
			if port == "" {
				t.Fatal("browser startup")
			}

			control := profileDial(t, "ws://127.0.0.1:"+port+endpoint)
			if variantIndex == 0 {
				report["browser"] = json.RawMessage(control.call("Browser.getVersion", map[string]any{}))
				report["gpu"] = json.RawMessage(control.call("SystemInfo.getInfo", map[string]any{}))
				report["viewport"] = "1440x1000 DPR1, headless, GPU not disabled"
				persist()
			}

			var tabs []struct {
				Type string `json:"type"`
				URL  string `json:"webSocketDebuggerUrl"`
			}
			b := profileJSON(t, "http://127.0.0.1:"+port+"/json", "GET", nil)
			json.Unmarshal(b, &tabs)
			pageURL := ""
			for _, tab := range tabs {
				if tab.Type == "page" && tab.URL != "" {
					pageURL = tab.URL
					break
				}
			}
			if pageURL == "" {
				t.Fatal("browser page target not found")
			}
			page := profileDial(t, pageURL)
			page.call("Runtime.enable", map[string]any{})
			page.call("Network.enable", map[string]any{})
			page.call("Page.enable", map[string]any{})
			page.call("Performance.enable", map[string]any{})
			page.call("HeapProfiler.enable", map[string]any{})
			page.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1440, "height": 1000, "deviceScaleFactor": 1, "mobile": false})
			page.call("Page.navigate", map[string]any{"url": hostURL})
			// Page.navigate returns before the new document has necessarily finished
			// starting. Give Chromium a short chance to switch away from about:blank,
			// then allow the profile app/WebSocket substantially longer than the old
			// 5s window. Slow startup must not invalidate an otherwise useful memory run.
			time.Sleep(300 * time.Millisecond)
			ready := page.eval(`(async()=>{for(let i=0;i<400&&!window.__atlasProbe;i++)await new Promise(r=>setTimeout(r,50));if(!window.__atlasProbe)return false;__atlasProbe.enter({session:'load',key:'load-key'});for(let i=0;i<400&&!__atlasProbe.connected();i++)await new Promise(r=>setTimeout(r,50));return __atlasProbe.connected()})()`)
			if string(ready) != "true" {
				state := page.eval(`JSON.stringify({href:location.href,readyState:document.readyState,probe:!!window.__atlasProbe,connected:!!window.__atlasProbe&&__atlasProbe.connected()})`)
				t.Fatalf("profile campaign did not connect: %s", state)
			}

			browserMem := func() (map[string]any, browserMemoryTotals) {
				var info struct {
					ProcessInfo []struct {
						ID   int     `json:"id"`
						Type string  `json:"type"`
						CPU  float64 `json:"cpuTime"`
					} `json:"processInfo"`
				}
				json.Unmarshal(control.call("SystemInfo.getProcessInfo", map[string]any{}), &info)
				var totals browserMemoryTotals
				var processes []any
				groups := map[string]map[string]uint64{}
				for _, p := range info.ProcessInfo {
					v := readProcessStats(p.ID)
					totals.WorkingSet += v.WorkingSet
					totals.PrivateBytes += v.PrivateBytes
					group := groups[p.Type]
					if group == nil {
						group = map[string]uint64{}
						groups[p.Type] = group
					}
					group["workingSet"] += v.WorkingSet
					group["privateBytes"] += v.PrivateBytes
					group["count"]++
					processes = append(processes, map[string]any{"id": p.ID, "type": p.Type, "cpuTime": p.CPU, "workingSet": v.WorkingSet, "privateBytes": v.PrivateBytes})
				}
				totals.RendererPrivateBytes = groups["renderer"]["privateBytes"]
				totals.GPUPrivateBytes = groups["GPU"]["privateBytes"]
				return map[string]any{"workingSetSum": totals.WorkingSet, "privateBytesSum": totals.PrivateBytes, "groups": groups, "processes": processes}, totals
			}

			var reset resetStats
			checkpoint := func(stage string, secondsAfterReset float64, includeResetNetwork bool) checkpointResult {
				lifeRaw := page.eval(`__atlasProbe.lifecycle()`)
				var life browserMemoryLifecycle
				json.Unmarshal(lifeRaw, &life)
				heapRaw := page.call("Runtime.getHeapUsage", map[string]any{})
				var heap struct {
					UsedSize float64 `json:"usedSize"`
				}
				json.Unmarshal(heapRaw, &heap)
				dom := page.call("Memory.getDOMCounters", map[string]any{})
				performance := page.call("Performance.getMetrics", map[string]any{})
				browserRaw, browserTotals := browserMem()
				httpRequests, httpBytes := int64(0), int64(0)
				if includeResetNetwork {
					httpRequests, httpBytes = page.Requests, page.HTTPBytes
				}
				row := map[string]any{"name": variant.Name + "_" + stage, "variant": variant.Name, "stage": stage, "secondsAfterReset": secondsAfterReset, "lifecycle": json.RawMessage(lifeRaw), "heap": json.RawMessage(heapRaw), "dom": json.RawMessage(dom), "performance": json.RawMessage(performance), "browser": browserRaw, "httpRequestsAfterReset": httpRequests, "httpBytesAfterReset": httpBytes, "reset": reset, "errors": append([]string{}, page.Errors...)}
				*rows = append(*rows, row)
				persist()
				csv.WriteString(fmt.Sprintf("%s,%s,%.1f,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f,%d,%d,%d,%d,%.2f,%.2f,%d,%.3f,%d,%d,%d,%.3f,%.3f,%.3f,%.6f,%d,%d,%.3f,%d\n",
					variant.Name, stage, secondsAfterReset, float64(browserTotals.PrivateBytes)/mib, float64(browserTotals.WorkingSet)/mib, float64(browserTotals.RendererPrivateBytes)/mib, float64(browserTotals.GPUPrivateBytes)/mib, heap.UsedSize/mib, float64(life.Images.BitmapBytes)/mib, float64(life.Images.FallbackBytes)/mib, float64(life.Images.ArtworkBytes)/mib, life.Images.LiveBitmaps, life.Images.BitmapCreated, life.Images.BitmapClosed, life.Images.BitmapCloseCalls, float64(life.Images.BitmapCreatedBytes)/mib, float64(life.Images.BitmapClosedBytes)/mib, httpRequests, float64(httpBytes)/mib, life.Images.DiskReads, life.Images.DecodeCount, reset.Frame.Count, reset.Frame.Max, life.Camera.X, life.Camera.Y, life.Camera.Scale, life.Canvas.Width, life.Canvas.Height, life.Canvas.DPR, len(page.Errors)))
				if err := os.WriteFile(filepath.Join(output, "summary.csv"), []byte(csv.String()), 0600); err != nil {
					t.Fatal(err)
				}
				return checkpointResult{Browser: browserTotals, Lifecycle: life, JSHeap: heap.UsedSize}
			}

			page.call("HeapProfiler.collectGarbage", map[string]any{})
			time.Sleep(1500 * time.Millisecond)
			baseline := checkpoint("campaign_home_baseline", -1, false)

			opened := page.eval(`(async()=>{__atlasProbe.openScene('load-scene');for(let i=0;i<200&&!__atlasProbe.ready();i++)await new Promise(r=>setTimeout(r,50));if(!__atlasProbe.ready())return false;__atlasProbe.fit();for(let i=0;i<400;i++){if(__atlasProbe.layerSettled(1)){await new Promise(r=>setTimeout(r,750));if(__atlasProbe.layerSettled(1))return true}await new Promise(r=>setTimeout(r,100))}return false})()`)
			if string(opened) != "true" {
				t.Fatalf("memory fixture did not settle: %s", page.eval(`JSON.stringify(__atlasProbe.layerStatus())`))
			}
			checkpoint("scene_settled", -1, false)

			page.eval(`__atlasProbe.memorySweep();true`)
			var peakPrivate uint64
			until := time.Now().Add(20 * time.Second)
			for time.Now().Before(until) {
				_, current := browserMem()
				if current.PrivateBytes > peakPrivate {
					peakPrivate = current.PrivateBytes
				}
				time.Sleep(250 * time.Millisecond)
			}
			settled := page.eval(`(async()=>{__atlasProbe.stop();for(let i=0;i<200;i++){if(__atlasProbe.layerSettled(1)){await new Promise(r=>setTimeout(r,500));if(__atlasProbe.layerSettled(1))return true}await new Promise(r=>setTimeout(r,100))}return false})()`)
			if string(settled) != "true" {
				t.Fatalf("memory fixture did not settle after sweep: %s", page.eval(`JSON.stringify(__atlasProbe.layerStatus())`))
			}
			beforeRelease := checkpoint("before_release", -1, false)
			(*rows)[len(*rows)-1]["peakPrivateBytes"] = peakPrivate
			persist()

			page.eval(`__atlasProbe.beginReset();true`)
			resetWallStart := time.Now()
			if variant.Action == "backing" || variant.Action == "replace" {
				// Candidate lifecycle paths must retain the live scene and decoded
				// ImageBitmap cache. Only trim entries that the current cache policy
				// already considers disposable before touching the render surface.
				page.eval(`__atlasProbe.stop();__atlasProbe.trimImages();true`)
			} else {
				// Control and reload keep the existing strongest cleanup path.
				page.eval(`(async()=>{__atlasProbe.stop();__atlasProbe.home();await __atlasProbe.clearImages();return true})()`)
			}
			page.call("HeapProfiler.collectGarbage", map[string]any{})
			beforeAction := checkpoint("before_surface_action", 0, false)

			// Drain events from the workload and common cleanup before measuring
			// network traffic caused by the actual surface release operation.
			page.eval(`true`)
			page.resetNet()
			beforeActionLifecycle := beforeAction.Lifecycle
			resetFrameMetricAvailable := true
			switch variant.Action {
			case "control":
				// Common cleanup is the control; do not touch the canvas.
			case "backing":
				page.eval(`__atlasProbe.backingReset()`)
			case "replace":
				page.eval(`__atlasProbe.replaceCanvas()`)
			case "reload":
				page.eval(`__atlasProbe.prepareReloadReset();true`)
				page.call("Page.reload", map[string]any{})
				reloaded := page.eval(`(async()=>{for(let i=0;i<200&&!window.__atlasProbe;i++)await new Promise(r=>setTimeout(r,50));return !!window.__atlasProbe})()`)
				if string(reloaded) != "true" {
					t.Fatal("profile probe did not return after reload")
				}
			}
			page.call("HeapProfiler.collectGarbage", map[string]any{})
			if resetFrameMetricAvailable {
				resetRaw := page.eval(`__atlasProbe.endReset()`)
				json.Unmarshal(resetRaw, &reset)
			} else {
				reset.ElapsedMS = float64(time.Since(resetWallStart).Microseconds()) / 1000
			}
			resetWallMS := float64(time.Since(resetWallStart).Microseconds()) / 1000
			if reset.ElapsedMS < resetWallMS {
				reset.ElapsedMS = resetWallMS
			}

			releaseCompleted := time.Now()
			time.Sleep(1500 * time.Millisecond)
			after15 := checkpoint("after_1_5s", 1.5, true)
			_ = after15
			if wait := 10*time.Second - time.Since(releaseCompleted); wait > 0 {
				time.Sleep(wait)
			}
			after10 := checkpoint("after_10s", 10, true)

			outcome := browserMemoryOutcome{Name: variant.Name, Baseline: baseline.Browser, BeforeAction: beforeAction.Browser, After10: after10.Browser, BeforeActionLifecycle: beforeActionLifecycle, After10Lifecycle: after10.Lifecycle, HTTPRequests: page.Requests, HTTPBytes: page.HTTPBytes, ResetFrameCount: reset.Frame.Count, ResetMaxFrameMS: reset.Frame.Max, ResetFrameMetricAvailable: resetFrameMetricAvailable, JSErrors: len(page.Errors)}
			outcomes = append(outcomes, outcome)
			if len(page.Errors) > 0 {
				t.Fatalf("browser JavaScript errors in %s: %v", variant.Name, page.Errors)
			}
			_ = beforeRelease
		}()
	}

	residualReduction := func(before, after, baseline uint64) float64 {
		if before <= baseline {
			if after <= before {
				return 1
			}
			return 0
		}
		beforeResidual := before - baseline
		afterResidual := uint64(0)
		if after > baseline {
			afterResidual = after - baseline
		}
		if afterResidual >= beforeResidual {
			return 0
		}
		return float64(beforeResidual-afterResidual) / float64(beforeResidual)
	}
	stateEqual := func(a, b browserMemoryLifecycle) bool {
		return a.Camera == b.Camera && a.SceneState == b.SceneState && a.Canvas.Width == b.Canvas.Width && a.Canvas.Height == b.Canvas.Height && a.Canvas.DPR == b.Canvas.DPR
	}
	decision := map[string]any{}
	anyEligible := false
	t.Log("canvas lifetime comparison (10s after release)")
	t.Log("variant                         private MiB   GPU MiB   residual private/GPU   HTTP   decodeΔ   cache   state   eligible")
	for _, outcome := range outcomes {
		privateReduction := residualReduction(outcome.BeforeAction.PrivateBytes, outcome.After10.PrivateBytes, outcome.Baseline.PrivateBytes)
		gpuReduction := residualReduction(outcome.BeforeAction.GPUPrivateBytes, outcome.After10.GPUPrivateBytes, outcome.Baseline.GPUPrivateBytes)
		withinBaseline := outcome.After10.PrivateBytes <= outcome.Baseline.PrivateBytes+150*1024*1024 && outcome.After10.GPUPrivateBytes <= outcome.Baseline.GPUPrivateBytes+150*1024*1024
		decodeDelta := outcome.After10Lifecycle.Images.DecodeCount - outcome.BeforeActionLifecycle.Images.DecodeCount
		createdDelta := outcome.After10Lifecycle.Images.BitmapCreated - outcome.BeforeActionLifecycle.Images.BitmapCreated
		idbReadDelta := outcome.After10Lifecycle.Images.DiskReads - outcome.BeforeActionLifecycle.Images.DiskReads
		counterReset := decodeDelta < 0 || createdDelta < 0 || idbReadDelta < 0
		if counterReset {
			decodeDelta = outcome.After10Lifecycle.Images.DecodeCount
			createdDelta = outcome.After10Lifecycle.Images.BitmapCreated
			idbReadDelta = outcome.After10Lifecycle.Images.DiskReads
		}
		cachePreserved := outcome.BeforeActionLifecycle.Images.TrackedBytes == outcome.After10Lifecycle.Images.TrackedBytes && outcome.BeforeActionLifecycle.Images.BitmapBytes == outcome.After10Lifecycle.Images.BitmapBytes && outcome.BeforeActionLifecycle.Images.FallbackBytes == outcome.After10Lifecycle.Images.FallbackBytes && outcome.BeforeActionLifecycle.Images.LiveBitmaps == outcome.After10Lifecycle.Images.LiveBitmaps
		unchanged := stateEqual(outcome.BeforeActionLifecycle, outcome.After10Lifecycle)
		candidate := outcome.Name == "B_backing_store_reset" || outcome.Name == "C_canvas_context_replacement"
		eligible := candidate && (privateReduction >= .5 || gpuReduction >= .5 || withinBaseline) && outcome.HTTPRequests == 0 && decodeDelta == 0 && createdDelta == 0 && cachePreserved && unchanged && outcome.JSErrors == 0
		if eligible {
			anyEligible = true
		}
		decision[outcome.Name] = map[string]any{"privateResidualReduction": privateReduction, "gpuResidualReduction": gpuReduction, "withinBaselinePlus150MiB": withinBaseline, "httpRequestsAfterReset": outcome.HTTPRequests, "httpBytesAfterReset": outcome.HTTPBytes, "decodeDeltaAfterReset": decodeDelta, "bitmapCreateDeltaAfterReset": createdDelta, "indexedDBReadDeltaAfterReset": idbReadDelta, "counterResetByReload": counterReset, "imageBitmapCachePreserved": cachePreserved, "cameraSceneCanvasUnchanged": unchanged, "resetFrameCount": outcome.ResetFrameCount, "resetMaxFrameMs": outcome.ResetMaxFrameMS, "resetFrameMetricAvailable": outcome.ResetFrameMetricAvailable, "eligibleForProductionLifecycle": eligible}
		t.Logf("%-31s %10.1f %9.1f %8.0f%%/%3.0f%% %7d %9d %7t %7t %10t", outcome.Name, float64(outcome.After10.PrivateBytes)/mib, float64(outcome.After10.GPUPrivateBytes)/mib, privateReduction*100, gpuReduction*100, outcome.HTTPRequests, decodeDelta, cachePreserved, unchanged, eligible)
	}
	report["decision"] = decision
	report["productionLifecycleCandidateFound"] = anyEligible
	if !anyEligible {
		report["nextExperiment"] = "decode churn"
	}
	persist()
}

func runBrowserMemoryLongSessionProfile(t *testing.T, output, chrome, hostURL string, report map[string]any, rows *[]map[string]any, persist func()) {
	t.Helper()
	report["profile"] = "browser-memory-long-session"
	report["largeAsset"] = "one 10000x10000 tiled scene image"
	report["comparison"] = "repeated open/sweep/release cycles in one Chromium process and one page"

	cycles := 10
	if raw := os.Getenv("ATLAS_BROWSER_MEMORY_LONG_CYCLES"); raw != "" {
		var parsed int
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err != nil || parsed < 2 || parsed > 50 {
			t.Fatalf("ATLAS_BROWSER_MEMORY_LONG_CYCLES must be an integer from 2 to 50, got %q", raw)
		}
		cycles = parsed
	}
	report["cycles"] = cycles
	report["releasePolicy"] = "campaign home + clear decoded RAM cache while preserving IndexedDB + forced JS GC + 10s settle"

	profileJSON(t, hostURL+"/__bench/scene", "POST", map[string]any{"Count": 0, "MapWidth": 10000, "MapElements": 1, "DistinctMaps": true})

	browserLog, err := os.OpenFile(filepath.Join(output, "browser.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer browserLog.Close()

	profileDir, err := os.MkdirTemp(output, ".chrome-long-session-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(profileDir)

	args := []string{"--no-sandbox", "--headless=new", "--no-first-run", "--no-default-browser-check", "--disable-background-timer-throttling", "--disable-renderer-backgrounding", "--disable-backgrounding-occluded-windows", "--remote-debugging-port=0", "--user-data-dir=" + profileDir, "about:blank"}
	browser := exec.Command(chrome, args...)
	browser.Stdout = browserLog
	browser.Stderr = browserLog
	if err := browser.Start(); err != nil {
		t.Fatal(err)
	}
	browserExit := make(chan error, 1)
	go func() { browserExit <- browser.Wait() }()
	browserExited := false
	defer func() {
		if !browserExited && browser.Process != nil {
			_ = browser.Process.Kill()
			<-browserExit
		}
	}()

	readBrowserLog := func() string {
		_ = browserLog.Sync()
		b, _ := os.ReadFile(filepath.Join(output, "browser.log"))
		return strings.TrimSpace(string(b))
	}
	var port, endpoint string
	devToolsFromLog := func() bool {
		const marker = "DevTools listening on ws://127.0.0.1:"
		for _, line := range strings.Split(readBrowserLog(), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, marker) {
				continue
			}
			rest := strings.TrimPrefix(line, marker)
			slash := strings.IndexByte(rest, '/')
			if slash <= 0 {
				continue
			}
			port = rest[:slash]
			endpoint = rest[slash:]
			return true
		}
		return false
	}
	startupDeadline := time.NewTimer(30 * time.Second)
	defer startupDeadline.Stop()
	startupTicker := time.NewTicker(100 * time.Millisecond)
	defer startupTicker.Stop()
startupWait:
	for {
		select {
		case err := <-browserExit:
			browserExited = true
			t.Fatalf("browser exited before DevTools startup: %v\n%s", err, readBrowserLog())
		case <-startupTicker.C:
			if b, err := os.ReadFile(filepath.Join(profileDir, "DevToolsActivePort")); err == nil {
				lines := strings.Split(strings.TrimSpace(string(b)), "\n")
				if len(lines) >= 2 {
					port = strings.TrimSpace(lines[0])
					endpoint = strings.TrimSpace(lines[1])
					break startupWait
				}
			}
			// Chrome can expose a working DevTools endpoint without leaving
			// DevToolsActivePort in the requested profile directory. The endpoint
			// printed by Chrome is authoritative and equivalent for CDP startup.
			if devToolsFromLog() {
				break startupWait
			}
		case <-startupDeadline.C:
			if devToolsFromLog() {
				break startupWait
			}
			t.Fatalf("browser did not expose a DevTools endpoint within 30s\n%s", readBrowserLog())
		}
	}

	control := profileDial(t, "ws://127.0.0.1:"+port+endpoint)
	report["browser"] = json.RawMessage(control.call("Browser.getVersion", map[string]any{}))
	report["gpu"] = json.RawMessage(control.call("SystemInfo.getInfo", map[string]any{}))
	report["viewport"] = "1440x1000 DPR1, headless, GPU not disabled"
	persist()

	var tabs []struct {
		Type string `json:"type"`
		URL  string `json:"webSocketDebuggerUrl"`
	}
	b := profileJSON(t, "http://127.0.0.1:"+port+"/json", "GET", nil)
	json.Unmarshal(b, &tabs)
	pageURL := ""
	for _, tab := range tabs {
		if tab.Type == "page" && tab.URL != "" {
			pageURL = tab.URL
			break
		}
	}
	if pageURL == "" {
		t.Fatal("browser page target not found")
	}

	page := profileDial(t, pageURL)
	page.call("Runtime.enable", map[string]any{})
	page.call("Network.enable", map[string]any{})
	page.call("Page.enable", map[string]any{})
	page.call("Performance.enable", map[string]any{})
	page.call("HeapProfiler.enable", map[string]any{})
	page.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1440, "height": 1000, "deviceScaleFactor": 1, "mobile": false})
	page.call("Page.navigate", map[string]any{"url": hostURL})
	time.Sleep(300 * time.Millisecond)
	ready := page.eval(`(async()=>{for(let i=0;i<400&&!window.__atlasProbe;i++)await new Promise(r=>setTimeout(r,50));if(!window.__atlasProbe)return false;__atlasProbe.enter({session:'load',key:'load-key'});for(let i=0;i<400&&!__atlasProbe.connected();i++)await new Promise(r=>setTimeout(r,50));return __atlasProbe.connected()})()`)
	if string(ready) != "true" {
		state := page.eval(`JSON.stringify({href:location.href,readyState:document.readyState,probe:!!window.__atlasProbe,connected:!!window.__atlasProbe&&__atlasProbe.connected()})`)
		t.Fatalf("profile campaign did not connect: %s", state)
	}

	browserMem := func() (map[string]any, browserMemoryTotals) {
		var info struct {
			ProcessInfo []struct {
				ID   int     `json:"id"`
				Type string  `json:"type"`
				CPU  float64 `json:"cpuTime"`
			} `json:"processInfo"`
		}
		json.Unmarshal(control.call("SystemInfo.getProcessInfo", map[string]any{}), &info)
		var totals browserMemoryTotals
		var processes []any
		groups := map[string]map[string]uint64{}
		for _, p := range info.ProcessInfo {
			v := readProcessStats(p.ID)
			totals.WorkingSet += v.WorkingSet
			totals.PrivateBytes += v.PrivateBytes
			group := groups[p.Type]
			if group == nil {
				group = map[string]uint64{}
				groups[p.Type] = group
			}
			group["workingSet"] += v.WorkingSet
			group["privateBytes"] += v.PrivateBytes
			group["count"]++
			processes = append(processes, map[string]any{"id": p.ID, "type": p.Type, "cpuTime": p.CPU, "workingSet": v.WorkingSet, "privateBytes": v.PrivateBytes})
		}
		totals.RendererPrivateBytes = groups["renderer"]["privateBytes"]
		totals.GPUPrivateBytes = groups["GPU"]["privateBytes"]
		return map[string]any{"workingSetSum": totals.WorkingSet, "privateBytesSum": totals.PrivateBytes, "groups": groups, "processes": processes}, totals
	}

	type workloadSample struct {
		FPS   float64 `json:"fps"`
		Frame struct {
			P95 float64 `json:"p95"`
			Max float64 `json:"max"`
		} `json:"frame"`
		DrawImagesPerFrame float64 `json:"drawImagesPerFrame"`
		Decodes            int64   `json:"decodes"`
		DecodeMS           float64 `json:"decodeMs"`
		LODFallbacks       int64   `json:"lodFallbacks"`
		Degradations       int64   `json:"degradations"`
	}
	type checkpointResult struct {
		Browser   browserMemoryTotals
		Lifecycle browserMemoryLifecycle
		JSHeap    float64
	}

	const mib = float64(1024 * 1024)
	var csv strings.Builder
	csv.WriteString("cycle,stage,private_mib,working_set_mib,renderer_private_mib,gpu_private_mib,js_heap_mib,atlas_bitmap_mib,live_bitmaps,bitmap_created,bitmap_closed,idb_reads,decode_count,cycle_decode_delta,cycle_idb_read_delta,cycle_bitmap_create_delta,http_requests,http_mib,fps,frame_p95_ms,frame_max_ms,draw_images_per_frame,workload_decodes,workload_decode_ms,lod_fallbacks,degradations,dom_nodes,js_event_listeners,peak_private_mib,js_errors\n")

	checkpoint := func(cycle int, stage string, decodeDelta, diskReadDelta, bitmapCreateDelta int64, httpRequests, httpBytes int64, workload workloadSample, peakPrivate uint64) checkpointResult {
		lifeRaw := page.eval(`__atlasProbe.lifecycle()`)
		var life browserMemoryLifecycle
		json.Unmarshal(lifeRaw, &life)
		heapRaw := page.call("Runtime.getHeapUsage", map[string]any{})
		var heap struct {
			UsedSize float64 `json:"usedSize"`
		}
		json.Unmarshal(heapRaw, &heap)
		domRaw := page.call("Memory.getDOMCounters", map[string]any{})
		var dom struct {
			Nodes            int `json:"nodes"`
			JSEventListeners int `json:"jsEventListeners"`
		}
		json.Unmarshal(domRaw, &dom)
		browserRaw, totals := browserMem()
		row := map[string]any{
			"name":                   fmt.Sprintf("cycle_%02d_%s", cycle, stage),
			"cycle":                  cycle,
			"stage":                  stage,
			"lifecycle":              json.RawMessage(lifeRaw),
			"heap":                   json.RawMessage(heapRaw),
			"dom":                    json.RawMessage(domRaw),
			"browser":                browserRaw,
			"cycleDecodeDelta":       decodeDelta,
			"cycleDiskReadDelta":     diskReadDelta,
			"cycleBitmapCreateDelta": bitmapCreateDelta,
			"httpRequests":           httpRequests,
			"httpBytes":              httpBytes,
			"workload":               workload,
			"peakPrivateBytes":       peakPrivate,
			"errors":                 append([]string{}, page.Errors...),
		}
		*rows = append(*rows, row)
		persist()
		csv.WriteString(fmt.Sprintf("%d,%s,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f,%d,%d,%d,%d,%d,%d,%d,%d,%d,%.3f,%.2f,%.3f,%.3f,%.3f,%d,%.3f,%d,%d,%d,%d,%.2f,%d\n",
			cycle, stage, float64(totals.PrivateBytes)/mib, float64(totals.WorkingSet)/mib, float64(totals.RendererPrivateBytes)/mib, float64(totals.GPUPrivateBytes)/mib, heap.UsedSize/mib, float64(life.Images.BitmapBytes)/mib, life.Images.LiveBitmaps, life.Images.BitmapCreated, life.Images.BitmapClosed, life.Images.DiskReads, life.Images.DecodeCount, decodeDelta, diskReadDelta, bitmapCreateDelta, httpRequests, float64(httpBytes)/mib, workload.FPS, workload.Frame.P95, workload.Frame.Max, workload.DrawImagesPerFrame, workload.Decodes, workload.DecodeMS, workload.LODFallbacks, workload.Degradations, dom.Nodes, dom.JSEventListeners, float64(peakPrivate)/mib, len(page.Errors)))
		if err := os.WriteFile(filepath.Join(output, "summary.csv"), []byte(csv.String()), 0600); err != nil {
			t.Fatal(err)
		}
		return checkpointResult{Browser: totals, Lifecycle: life, JSHeap: heap.UsedSize}
	}

	page.call("HeapProfiler.collectGarbage", map[string]any{})
	time.Sleep(1500 * time.Millisecond)
	baseline := checkpoint(0, "campaign_home_baseline", 0, 0, 0, 0, 0, workloadSample{}, 0)
	previousResidual := baseline
	residualPrivate := make([]uint64, 0, cycles)
	residualGPU := make([]uint64, 0, cycles)
	residualRenderer := make([]uint64, 0, cycles)

	for cycle := 1; cycle <= cycles; cycle++ {
		t.Logf("browser memory long-session cycle %d/%d", cycle, cycles)
		page.resetNet()
		opened := page.eval(`(async()=>{__atlasProbe.openScene('load-scene');for(let i=0;i<200&&!__atlasProbe.ready();i++)await new Promise(r=>setTimeout(r,50));if(!__atlasProbe.ready())return false;__atlasProbe.fit();for(let i=0;i<400;i++){if(__atlasProbe.layerSettled(1)){await new Promise(r=>setTimeout(r,750));if(__atlasProbe.layerSettled(1))return true}await new Promise(r=>setTimeout(r,100))}return false})()`)
		if string(opened) != "true" {
			t.Fatalf("cycle %d: memory fixture did not settle: %s", cycle, page.eval(`JSON.stringify(__atlasProbe.layerStatus())`))
		}

		page.eval(`__atlasProbe.memorySweep();true`)
		var peakPrivate uint64
		until := time.Now().Add(20 * time.Second)
		for time.Now().Before(until) {
			_, current := browserMem()
			if current.PrivateBytes > peakPrivate {
				peakPrivate = current.PrivateBytes
			}
			time.Sleep(250 * time.Millisecond)
		}
		settled := page.eval(`(async()=>{__atlasProbe.stop();for(let i=0;i<200;i++){if(__atlasProbe.layerSettled(1)){await new Promise(r=>setTimeout(r,500));if(__atlasProbe.layerSettled(1))return true}await new Promise(r=>setTimeout(r,100))}return false})()`)
		if string(settled) != "true" {
			t.Fatalf("cycle %d: memory fixture did not settle after sweep: %s", cycle, page.eval(`JSON.stringify(__atlasProbe.layerStatus())`))
		}

		workloadRaw := page.eval(`__atlasProbe.sample()`)
		var workload workloadSample
		json.Unmarshal(workloadRaw, &workload)
		lifeRaw := page.eval(`__atlasProbe.lifecycle()`)
		var loadedLife browserMemoryLifecycle
		json.Unmarshal(lifeRaw, &loadedLife)
		decodeDelta := loadedLife.Images.DecodeCount - previousResidual.Lifecycle.Images.DecodeCount
		diskReadDelta := loadedLife.Images.DiskReads - previousResidual.Lifecycle.Images.DiskReads
		bitmapCreateDelta := loadedLife.Images.BitmapCreated - previousResidual.Lifecycle.Images.BitmapCreated
		checkpoint(cycle, "loaded_after_sweep", decodeDelta, diskReadDelta, bitmapCreateDelta, page.Requests, page.HTTPBytes, workload, peakPrivate)

		releaseStarted := time.Now()
		cleared := page.eval(`(async()=>{__atlasProbe.stop();__atlasProbe.home();__atlasProbe.clearDecodedImages();return true})()`)
		if string(cleared) != "true" {
			t.Fatalf("cycle %d: image cache cleanup failed", cycle)
		}
		page.call("HeapProfiler.collectGarbage", map[string]any{})
		if wait := 10*time.Second - time.Since(releaseStarted); wait > 0 {
			time.Sleep(wait)
		}
		residual := checkpoint(cycle, "residual_after_10s", 0, 0, 0, 0, 0, workloadSample{}, 0)
		if residual.Lifecycle.Images.TrackedBytes != 0 || residual.Lifecycle.Images.LiveBitmaps != 0 {
			t.Fatalf("cycle %d: decoded image cache did not clear: tracked=%d live=%d", cycle, residual.Lifecycle.Images.TrackedBytes, residual.Lifecycle.Images.LiveBitmaps)
		}
		previousResidual = residual
		residualPrivate = append(residualPrivate, residual.Browser.PrivateBytes)
		residualGPU = append(residualGPU, residual.Browser.GPUPrivateBytes)
		residualRenderer = append(residualRenderer, residual.Browser.RendererPrivateBytes)
	}

	slope := func(values []uint64) float64 {
		if len(values) < 2 {
			return 0
		}
		var sumX, sumY, sumXY, sumXX float64
		for i, value := range values {
			x := float64(i + 1)
			y := float64(value)
			sumX += x
			sumY += y
			sumXY += x * y
			sumXX += x * x
		}
		n := float64(len(values))
		den := n*sumXX - sumX*sumX
		if den == 0 {
			return 0
		}
		return (n*sumXY - sumX*sumY) / den
	}
	growth := func(values []uint64) int64 {
		if len(values) < 2 {
			return 0
		}
		return int64(values[len(values)-1]) - int64(values[0])
	}
	report["longSessionTrend"] = map[string]any{
		"residualPrivateFirstToLastBytes":    growth(residualPrivate),
		"residualGPUFirstToLastBytes":        growth(residualGPU),
		"residualRendererFirstToLastBytes":   growth(residualRenderer),
		"residualPrivateSlopeBytesPerCycle":  slope(residualPrivate),
		"residualGPUSlopeBytesPerCycle":      slope(residualGPU),
		"residualRendererSlopeBytesPerCycle": slope(residualRenderer),
	}
	if len(page.Errors) > 0 {
		t.Fatalf("browser JavaScript errors during long-session profile: %v", page.Errors)
	}
	t.Logf("long-session residual slope: private %.1f MiB/cycle, GPU %.1f MiB/cycle, renderer %.1f MiB/cycle", slope(residualPrivate)/mib, slope(residualGPU)/mib, slope(residualRenderer)/mib)
	persist()
}

func TestProfileAtlas(t *testing.T) {
	runProfileAtlas(t, os.Getenv("ATLAS_PROFILE"), "full")
}

func TestProfileLayerBake(t *testing.T) {
	runProfileAtlas(t, os.Getenv("ATLAS_LAYER_BAKE_PROFILE"), "layer")
}

func TestProfileLayerBakeBreakEven(t *testing.T) {
	runProfileAtlas(t, os.Getenv("ATLAS_LAYER_BAKE_BREAK_EVEN_PROFILE"), "layer-bake")
}

func TestProfileBrowserMemory(t *testing.T) {
	runProfileAtlas(t, os.Getenv("ATLAS_BROWSER_MEMORY_PROFILE"), "memory")
}

func TestProfileBrowserMemoryLongSession(t *testing.T) {
	runProfileAtlas(t, os.Getenv("ATLAS_BROWSER_MEMORY_LONG_PROFILE"), "memory-long")
}

func runProfileAtlas(t *testing.T, output, profileMode string) {
	if output == "" {
		if profileMode == "layer" {
			t.Skip("set ATLAS_LAYER_BAKE_PROFILE to output directory")
		}
		if profileMode == "layer-bake" {
			t.Skip("set ATLAS_LAYER_BAKE_BREAK_EVEN_PROFILE to output directory")
		}
		if profileMode == "memory" {
			t.Skip("set ATLAS_BROWSER_MEMORY_PROFILE to output directory")
		}
		if profileMode == "memory-long" {
			t.Skip("set ATLAS_BROWSER_MEMORY_LONG_PROFILE to output directory")
		}
		t.Skip("set ATLAS_PROFILE to output directory")
	}
	if os.Getenv("ATLAS_PROFILE_HOST") != "" {
		t.Skip("host")
	}
	if (profileMode == "memory" || profileMode == "memory-long") && runtime.GOOS != "windows" {
		t.Skip("browser Private Bytes attribution currently requires Windows process counters")
	}
	chrome := os.Getenv("ATLAS_CHROME")
	if chrome == "" {
		t.Fatal("ATLAS_CHROME required")
	}
	os.MkdirAll(output, 0755)
	root := t.TempDir()
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestProfileHost$", "-test.timeout=30m")
	cmd.Env = append(os.Environ(), "ATLAS_PROFILE_HOST="+root)
	logfile, _ := os.Create(filepath.Join(output, "host.log"))
	cmd.Stdout = logfile
	cmd.Stderr = logfile
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait(); logfile.Close() }()
	var host struct {
		URL string `json:"url"`
		PID int    `json:"pid"`
	}
	for i := 0; i < 200; i++ {
		if b, e := os.ReadFile(filepath.Join(root, "host.json")); e == nil {
			json.Unmarshal(b, &host)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if host.URL == "" {
		t.Fatal("host startup failed")
	}
	report := map[string]any{"go": runtime.Version(), "os": runtime.GOOS, "cores": runtime.NumCPU(), "origin": host.URL}
	var rows []map[string]any
	persist := func() {
		report["rows"] = rows
		b, _ := json.MarshalIndent(report, "", "  ")
		os.WriteFile(filepath.Join(output, "results.json"), b, 0600)
	}
	// Upload deterministic detailed JPEG maps. Source generation is driver-side.
	type mapSpec struct{ width, height, seed int }
	mapDimensions := []mapSpec{{4096, 4096, 0}, {8192, 8192, 0}, {10000, 10000, 0}, {32768, 3000, 0}}
	if profileMode == "layer" || profileMode == "layer-bake" {
		mapDimensions = nil
	} else if profileMode == "memory" || profileMode == "memory-long" {
		mapDimensions = []mapSpec{{10000, 10000, 0}}
	}
	var memoryJPEG []byte
	for _, dim := range mapDimensions {
		if profileMode == "full" && os.Getenv("ATLAS_PROFILE_SKIP_MAPS") != "" && dim.width != 4096 {
			continue
		}
		w, h := dim.width, dim.height
		t.Logf("map %dx%d seed %d", w, h, dim.seed)
		var encoded []byte
		if (profileMode == "memory" || profileMode == "memory-long") && len(memoryJPEG) > 0 {
			// A JPEG comment changes the content-addressed asset ID while leaving
			// decoded pixels identical. This models separately stored copies without
			// allocating another 400 MiB source image in the test driver.
			encoded = make([]byte, 0, len(memoryJPEG)+5)
			encoded = append(encoded, memoryJPEG[:2]...)
			encoded = append(encoded, 0xff, 0xfe, 0x00, 0x03, byte(dim.seed))
			encoded = append(encoded, memoryJPEG[2:]...)
		} else {
			img := image.NewRGBA(image.Rect(0, 0, w, h))
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					off := y*img.Stride + x*4
					v := uint8((x*13 + y*7 + (x*y)%251) % 256)
					img.Pix[off] = v
					img.Pix[off+1] = uint8(x/16 + y/8)
					img.Pix[off+2] = uint8(y/32 + x/8)
					img.Pix[off+3] = 255
				}
			}
			var b bytes.Buffer
			jpeg.Encode(&b, img, &jpeg.Options{Quality: 75})
			encoded = append([]byte(nil), b.Bytes()...)
			if profileMode == "memory" || profileMode == "memory-long" {
				memoryJPEG = append([]byte(nil), encoded...)
			}
			img = nil
			runtime.GC()
			debug.FreeOSMemory()
		}
		start := time.Now()
		before := readProcessStats(host.PID)
		var peak uint64
		done := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				case <-time.After(25 * time.Millisecond):
					v := readProcessStats(host.PID).WorkingSet
					if v > peak {
						peak = v
					}
				}
			}
		}()
		req, _ := http.NewRequest("POST", host.URL+"/api/upload?session=load&scene=load-scene&kind=map", bytes.NewReader(encoded))
		req.Header.Set("Authorization", "Bearer load-key")
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		response, _ := io.ReadAll(res.Body)
		res.Body.Close()
		elapsed := time.Since(start).Seconds()
		close(done)
		wg.Wait()
		after := readProcessStats(host.PID)
		rowName := fmt.Sprintf("map_%dx%d", w, h)
		if profileMode == "memory" || profileMode == "memory-long" {
			rowName += fmt.Sprintf("_seed_%d", dim.seed)
		}
		rows = append(rows, map[string]any{"name": rowName, "seconds": elapsed, "uploadBytes": len(encoded), "status": res.StatusCode, "response": string(response), "serverCPUSeconds": after.CPUSeconds - before.CPUSeconds, "serverPeakWorkingSet": peak, "worker": json.RawMessage(profileJSON(t, host.URL+"/__bench/worker", "GET", nil))})
		persist()
		if res.StatusCode != 200 {
			t.Fatal(string(response))
		}
		encoded = nil
		runtime.GC()
		debug.FreeOSMemory()
	}
	memoryJPEG = nil
	runtime.GC()
	debug.FreeOSMemory()
	if profileMode == "memory" {
		runBrowserMemoryCausalProfile(t, output, chrome, host.URL, report, &rows, persist)
		profileJSON(t, host.URL+"/__bench/stop", "POST", nil)
		cmd.Wait()
		persist()
		return
	}
	if profileMode == "memory-long" {
		runBrowserMemoryLongSessionProfile(t, output, chrome, host.URL, report, &rows, persist)
		profileJSON(t, host.URL+"/__bench/stop", "POST", nil)
		cmd.Wait()
		persist()
		return
	}
	// Start Chrome only after costly fixture preparation. Keeping an idle headless
	// browser alive while three large pyramids are built made the CDP baseline
	// depend on unrelated CPU pressure and could trigger its startup watchdog.
	profile, err := os.MkdirTemp(output, ".chrome-profile-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(profile)
	chromeArgs := []string{"--headless=new", "--no-first-run", "--no-default-browser-check", "--disable-background-timer-throttling", "--disable-renderer-backgrounding", "--disable-backgrounding-occluded-windows", "--remote-debugging-port=0", "--user-data-dir=" + profile, "about:blank"}
	if profileMode == "memory" {
		// The browser has a disposable local-only profile and loads only the
		// isolated localhost fixture. Some managed Windows environments deny the
		// GPU child process before page startup unless its sandbox is disabled.
		chromeArgs = append([]string{"--no-sandbox"}, chromeArgs...)
	}
	browser := exec.Command(chrome, chromeArgs...)
	browserLog, _ := os.Create(filepath.Join(output, "browser.log"))
	browser.Stdout = browserLog
	browser.Stderr = browserLog
	if e := browser.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { browser.Process.Kill(); browser.Wait(); browserLog.Close() }()
	var port, endpoint string
	for i := 0; i < 100; i++ {
		if b, e := os.ReadFile(filepath.Join(profile, "DevToolsActivePort")); e == nil {
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) >= 2 {
				port = strings.TrimSpace(lines[0])
				endpoint = strings.TrimSpace(lines[1])
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if port == "" {
		_ = browserLog.Sync()
		if data, e := os.ReadFile(filepath.Join(output, "browser.log")); e == nil {
			const marker = "DevTools listening on ws://127.0.0.1:"
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, marker) {
					continue
				}
				rest := strings.TrimPrefix(line, marker)
				slash := strings.IndexByte(rest, '/')
				if slash > 0 {
					port = rest[:slash]
					endpoint = rest[slash:]
					break
				}
			}
		}
	}
	if port == "" {
		_ = browserLog.Sync()
		data, _ := os.ReadFile(filepath.Join(output, "browser.log"))
		t.Fatalf("browser startup: no DevTools endpoint\n%s", strings.TrimSpace(string(data)))
	}
	control := profileDial(t, "ws://127.0.0.1:"+port+endpoint)
	report["browser"] = json.RawMessage(control.call("Browser.getVersion", map[string]any{}))
	report["viewport"] = "1440x1000 DPR1, headless, GPU not disabled"
	report["gpu"] = json.RawMessage(control.call("SystemInfo.getInfo", map[string]any{}))
	persist()
	// Restore 4096 map for identical token density in all runs.
	// All maps are in the isolated host; use its state to choose the first map.
	initialTokens := 100
	if profileMode != "full" {
		initialTokens = 0
	}
	profileJSON(t, host.URL+"/__bench/scene", "POST", map[string]int{"Count": initialTokens, "Images": 0})
	var pages []*profileCDP
	controls := []*profileCDP{control}
	pageCount := 0
	newPage := func() *profileCDP {
		if pageCount > 0 {
			profileDir := t.TempDir()
			b := exec.Command(chrome, "--headless=new", "--no-first-run", "--no-default-browser-check", "--disable-background-timer-throttling", "--disable-renderer-backgrounding", "--disable-backgrounding-occluded-windows", "--remote-debugging-port=0", "--user-data-dir="+profileDir, "about:blank")
			if e := b.Start(); e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { b.Process.Kill(); b.Wait() })
			var ep string
			port = ""
			for i := 0; i < 100; i++ {
				if data, e := os.ReadFile(filepath.Join(profileDir, "DevToolsActivePort")); e == nil {
					lines := strings.Split(strings.TrimSpace(string(data)), "\n")
					port = strings.TrimSpace(lines[0])
					ep = strings.TrimSpace(lines[1])
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if port == "" {
				t.Fatal("additional browser startup")
			}
			control = profileDial(t, "ws://127.0.0.1:"+port+ep)
			controls = append(controls, control)
		}
		pageCount++
		target := control.call("Target.createTarget", map[string]any{"url": "about:blank"})
		var id struct {
			ID string `json:"targetId"`
		}
		json.Unmarshal(target, &id)
		var tabs []struct {
			ID  string `json:"id"`
			URL string `json:"webSocketDebuggerUrl"`
		}
		b := profileJSON(t, "http://127.0.0.1:"+port+"/json", "GET", nil)
		json.Unmarshal(b, &tabs)
		var url string
		for _, tab := range tabs {
			if tab.ID == id.ID {
				url = tab.URL
			}
		}
		d := profileDial(t, url)
		d.call("Runtime.enable", map[string]any{})
		d.call("Network.enable", map[string]any{})
		d.call("Page.enable", map[string]any{})
		d.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1440, "height": 1000, "deviceScaleFactor": 1, "mobile": false})
		d.call("Page.navigate", map[string]any{"url": host.URL})
		time.Sleep(300 * time.Millisecond)
		ready := d.eval(`(async()=>{for(let i=0;i<100&&!window.__atlasProbe;i++)await new Promise(r=>setTimeout(r,50));__atlasProbe.enter({session:'load',key:'load-key'});for(let i=0;i<100&&!__atlasProbe.connected();i++)await new Promise(r=>setTimeout(r,50));__atlasProbe.openScene('load-scene');for(let i=0;i<100&&!__atlasProbe.ready();i++)await new Promise(r=>setTimeout(r,50));return __atlasProbe.ready()})()`)
		if string(ready) != "true" {
			t.Fatal("profile scene did not open")
		}
		return d
	}
	pages = append(pages, newPage())
	browserMem := func() map[string]any {
		var info struct {
			ProcessInfo []struct {
				ID   int     `json:"id"`
				Type string  `json:"type"`
				CPU  float64 `json:"cpuTime"`
			} `json:"processInfo"`
		}
		var ws, private uint64
		var processes []any
		groups := map[string]map[string]uint64{}
		for _, browserControl := range controls {
			info.ProcessInfo = nil
			json.Unmarshal(browserControl.call("SystemInfo.getProcessInfo", map[string]any{}), &info)
			for _, p := range info.ProcessInfo {
				v := readProcessStats(p.ID)
				ws += v.WorkingSet
				private += v.PrivateBytes
				group := groups[p.Type]
				if group == nil {
					group = map[string]uint64{}
					groups[p.Type] = group
				}
				group["workingSet"] += v.WorkingSet
				group["privateBytes"] += v.PrivateBytes
				group["count"]++
				processes = append(processes, map[string]any{"id": p.ID, "type": p.Type, "cpuTime": p.CPU, "workingSet": v.WorkingSet, "privateBytes": v.PrivateBytes})
			}
		}
		return map[string]any{"workingSetSum": ws, "privateBytesSum": private, "groups": groups, "processes": processes}
	}
	measure := func(name, mode string, seconds int) {
		t.Logf("scenario %s (%ds)", name, seconds)
		for _, p := range pages {
			p.eval(`__atlasProbe.stop();__atlasProbe.reset();true`)
			p.resetNet()
		}
		for i, p := range pages {
			m := mode
			if mode == "move" && i > 0 {
				m = "receive"
			}
			js, _ := json.Marshal(m)
			p.eval(`__atlasProbe.start(` + string(js) + `);true`)
		}
		start := time.Now()
		before := readProcessStats(host.PID)
		pages[0].call("Profiler.enable", map[string]any{})
		pages[0].call("Profiler.start", map[string]any{})
		var peak uint64
		for time.Since(start) < time.Duration(seconds)*time.Second {
			v := readProcessStats(host.PID).WorkingSet
			if v > peak {
				peak = v
			}
			time.Sleep(250 * time.Millisecond)
		}
		after := readProcessStats(host.PID)
		duration := time.Since(start).Seconds()
		cpuProfile := pages[0].call("Profiler.stop", map[string]any{})
		os.WriteFile(filepath.Join(output, name+".cpuprofile.json"), cpuProfile, 0600)
		var clients []any
		for _, p := range pages {
			p.eval(`__atlasProbe.stop();true`)
			sample := p.eval(`__atlasProbe.sample()`)
			var checked struct {
				FPS float64 `json:"fps"`
			}
			json.Unmarshal(sample, &checked)
			if checked.FPS == 0 {
				t.Fatalf("inactive browser invalidates %s: %s", name, sample)
			}
			clients = append(clients, map[string]any{"metrics": json.RawMessage(sample), "httpBytes": p.HTTPBytes, "httpLoads": p.Requests, "wsIn": p.WSIn, "wsOut": p.WSOut, "errors": p.Errors})
		}
		rows = append(rows, map[string]any{"name": name, "seconds": duration, "clients": clients, "browser": browserMem(), "serverCPUSeconds": after.CPUSeconds - before.CPUSeconds, "serverCPUPercentOneCore": 100 * (after.CPUSeconds - before.CPUSeconds) / duration, "serverWorkingSet": after.WorkingSet, "serverPeakWorkingSet": peak, "serverPrivateBytes": after.PrivateBytes})
		persist()
	}

	if profileMode == "layer-bake" {
		report["profile"] = "visual-layer-bake-break-even"
		report["comparison"] = "same static-layer scale and spatial envelope rendered as individual SceneElements vs one test-only tiled baked proxy; proxy pixels are synthetic and production bake is not implemented"
		report["decisionMetric"] = "compare render/pan draw CPU p95 and drawImagesPerFrame; load/network/decoded-memory deltas are reported separately"
		report["limitations"] = "the baked proxy measures steady-state representation cost, not bake-generation CPU/time or pixel-perfect output equivalence"
		type layerRuntimeSample struct {
			FPS                float64 `json:"fps"`
			DrawImagesPerFrame float64 `json:"drawImagesPerFrame"`
			Bitmaps            int     `json:"bitmaps"`
			BitmapBytes        uint64  `json:"bitmapBytes"`
			PeakBitmapBytes    uint64  `json:"peakBitmapBytes"`
			Decodes            int     `json:"decodes"`
			DecodeMS           float64 `json:"decodeMs"`
			LODFallbacks       int     `json:"lodFallbacks"`
			Degradations       int     `json:"degradations"`
			StateBytes         int     `json:"stateBytes"`
			Frame              struct {
				Count int     `json:"count"`
				Mean  float64 `json:"mean"`
				P50   float64 `json:"p50"`
				P95   float64 `json:"p95"`
				P99   float64 `json:"p99"`
				Max   float64 `json:"max"`
			} `json:"frame"`
		}
		type layerVariantResult struct {
			Representation       string             `json:"representation"`
			Setup                map[string]any     `json:"setup"`
			Load                 layerRuntimeSample `json:"load"`
			LoadHTTPBytes        int64              `json:"loadHTTPBytes"`
			LoadHTTPLoads        int64              `json:"loadHTTPLoads"`
			LoadBrowserPrivate   uint64             `json:"loadBrowserPrivateBytes"`
			Render               layerRuntimeSample `json:"render"`
			RenderHTTPBytes      int64              `json:"renderHTTPBytes"`
			RenderHTTPLoads      int64              `json:"renderHTTPLoads"`
			RenderBrowserPrivate uint64             `json:"renderBrowserPrivateBytes"`
			Pan                  layerRuntimeSample `json:"pan"`
			PanHTTPBytes         int64              `json:"panHTTPBytes"`
			PanHTTPLoads         int64              `json:"panHTTPLoads"`
			PanBrowserPrivate    uint64             `json:"panBrowserPrivateBytes"`
		}
		type bakeScenario struct {
			name                                   string
			count, images, sourceSize, elementSize int
			layout, family                         string
		}
		scenarios := make([]bakeScenario, 0, 9)
		for _, count := range []int{25, 50, 100, 200, 500, 1000} {
			scenarios = append(scenarios, bakeScenario{fmt.Sprintf("dense_varied_%04d", count), count, min(64, count), 512, 256, "dense", "dense-varied"})
		}
		scenarios = append(scenarios,
			bakeScenario{"dense_shared_0200", 200, 1, 512, 256, "dense", "dense-shared"},
			bakeScenario{"dense_shared_1000", 1000, 1, 512, 256, "dense", "dense-shared"},
			bakeScenario{"grid_varied_0200", 200, 64, 512, 256, "grid", "grid-varied"},
		)
		report["scenarioFamilies"] = map[string]any{
			"dense-varied": "break-even sweep: 25, 50, 100, 200, 500, 1000 elements with up to 64 unique source assets",
			"dense-shared": "control for element-count cost when all elements reuse one source asset",
			"grid-varied":  "sparse-layout control for visibility/culling behavior",
		}
		page := pages[0]
		browserPrivate := func(raw map[string]any) uint64 {
			if raw == nil {
				return 0
			}
			value, _ := raw["privateBytesSum"].(uint64)
			return value
		}
		parseSample := func(raw []byte) layerRuntimeSample {
			var sample layerRuntimeSample
			if err := json.Unmarshal(raw, &sample); err != nil {
				t.Fatalf("decode layer profile sample: %v", err)
			}
			return sample
		}
		measureLayer := func(name, mode string, seconds int) (layerRuntimeSample, int64, int64, uint64) {
			measure(name, mode, seconds)
			row := rows[len(rows)-1]
			clients, ok := row["clients"].([]any)
			if !ok || len(clients) != 1 {
				t.Fatalf("unexpected layer client metrics for %s", name)
			}
			client, ok := clients[0].(map[string]any)
			if !ok {
				t.Fatalf("unexpected layer client row for %s", name)
			}
			raw, ok := client["metrics"].(json.RawMessage)
			if !ok {
				t.Fatalf("missing layer metrics for %s", name)
			}
			httpBytes, _ := client["httpBytes"].(int64)
			httpLoads, _ := client["httpLoads"].(int64)
			browser, _ := row["browser"].(map[string]any)
			return parseSample(raw), httpBytes, httpLoads, browserPrivate(browser)
		}
		pctReduction := func(before, after float64) float64 {
			if before <= 0 {
				return 0
			}
			return 100 * (before - after) / before
		}
		var comparisons []map[string]any
		var summary strings.Builder
		summary.WriteString("scenario,family,count,layout,unique_assets,representation,phase,fps,frame_mean_ms,frame_p95_ms,frame_max_ms,draw_images_per_frame,bitmap_mib,peak_bitmap_mib,decodes,decode_ms,http_loads,http_mib,browser_private_mib,state_bytes\n")
		writeSummary := func(scenario bakeScenario, variant layerVariantResult, phase string, sample layerRuntimeSample, httpLoads, httpBytes int64, private uint64) {
			fmt.Fprintf(&summary, "%s,%s,%d,%s,%d,%s,%s,%.3f,%.3f,%.3f,%.3f,%.3f,%.3f,%.3f,%d,%.3f,%d,%.3f,%.3f,%d\n", scenario.name, scenario.family, scenario.count, scenario.layout, scenario.images, variant.Representation, phase, sample.FPS, sample.Frame.Mean, sample.Frame.P95, sample.Frame.Max, sample.DrawImagesPerFrame, float64(sample.BitmapBytes)/(1024*1024), float64(sample.PeakBitmapBytes)/(1024*1024), sample.Decodes, sample.DecodeMS, httpLoads, float64(httpBytes)/(1024*1024), float64(private)/(1024*1024), sample.StateBytes)
		}
		for _, scenario := range scenarios {
			t.Logf("bake break-even scenario %s", scenario.name)
			variants := map[string]layerVariantResult{}
			for _, representation := range []string{"elements", "baked-proxy"} {
				page.eval(`(async()=>{__atlasProbe.stop();await __atlasProbe.clearImages();__atlasProbe.reset();__atlasProbe.measuring=true;return true})()`)
				page.resetNet()
				setupRaw := profileJSON(t, host.URL+"/__bench/layer", "POST", map[string]any{"Count": scenario.count, "Images": scenario.images, "SourceSize": scenario.sourceSize, "ElementSize": scenario.elementSize, "Layout": scenario.layout, "Representation": representation})
				var setup map[string]any
				if err := json.Unmarshal(setupRaw, &setup); err != nil {
					t.Fatalf("decode layer setup %s/%s: %v", scenario.name, representation, err)
				}
				revisionValue, okRevision := setup["revision"].(float64)
				renderedValue, okRendered := setup["renderedElements"].(float64)
				if !okRevision || !okRendered {
					t.Fatalf("invalid layer setup response for %s/%s: %s", scenario.name, representation, setupRaw)
				}
				revision := uint64(revisionValue)
				renderedElements := int(renderedValue)
				waitJS := fmt.Sprintf(`(async()=>{for(let i=0;i<400;i++){const s=__atlasProbe.lifecycle().sceneState;if(s.sceneRevision===%d)break;await new Promise(r=>setTimeout(r,100))}const s=__atlasProbe.lifecycle().sceneState;if(s.sceneRevision!==%d)throw Error('layer revision did not arrive: '+JSON.stringify({sceneState:s,layer:__atlasProbe.layerStatus()}));__atlasProbe.fit();for(let i=0;i<400;i++){if(__atlasProbe.layerSettled(%d)){await new Promise(r=>setTimeout(r,500));if(__atlasProbe.layerSettled(%d))return __atlasProbe.sample()}await new Promise(r=>setTimeout(r,100))}throw Error('layer profile did not settle: '+JSON.stringify({sceneState:__atlasProbe.lifecycle().sceneState,layer:__atlasProbe.layerStatus()}))})()`, revision, revision, renderedElements, renderedElements)
				coldRaw := page.eval(waitJS)
				page.eval(`__atlasProbe.stop();true`)
				loadBrowser := browserMem()
				load := parseSample(coldRaw)
				variant := layerVariantResult{Representation: representation, Setup: setup, Load: load, LoadHTTPBytes: page.HTTPBytes, LoadHTTPLoads: page.Requests, LoadBrowserPrivate: browserPrivate(loadBrowser)}
				rows = append(rows, map[string]any{"name": scenario.name + "_" + representation + "_load", "scenario": scenario.name, "family": scenario.family, "representation": representation, "setup": json.RawMessage(setupRaw), "metrics": json.RawMessage(coldRaw), "httpBytes": page.HTTPBytes, "httpLoads": page.Requests, "wsIn": page.WSIn, "wsOut": page.WSOut, "browser": loadBrowser, "errors": append([]string{}, page.Errors...)})
				persist()
				variant.Render, variant.RenderHTTPBytes, variant.RenderHTTPLoads, variant.RenderBrowserPrivate = measureLayer(scenario.name+"_"+representation+"_render", "render", 5)
				variant.Pan, variant.PanHTTPBytes, variant.PanHTTPLoads, variant.PanBrowserPrivate = measureLayer(scenario.name+"_"+representation+"_pan", "pan", 6)
				variants[representation] = variant
				writeSummary(scenario, variant, "load", variant.Load, variant.LoadHTTPLoads, variant.LoadHTTPBytes, variant.LoadBrowserPrivate)
				writeSummary(scenario, variant, "render", variant.Render, variant.RenderHTTPLoads, variant.RenderHTTPBytes, variant.RenderBrowserPrivate)
				writeSummary(scenario, variant, "pan", variant.Pan, variant.PanHTTPLoads, variant.PanHTTPBytes, variant.PanBrowserPrivate)
			}
			unbaked, baked := variants["elements"], variants["baked-proxy"]
			comparison := map[string]any{
				"scenario": scenario.name, "family": scenario.family, "count": scenario.count, "layout": scenario.layout, "uniqueAssets": scenario.images,
				"renderFrameP95UnbakedMs": unbaked.Render.Frame.P95, "renderFrameP95BakedMs": baked.Render.Frame.P95, "renderFrameP95SavedMs": unbaked.Render.Frame.P95 - baked.Render.Frame.P95, "renderFrameP95ReductionPct": pctReduction(unbaked.Render.Frame.P95, baked.Render.Frame.P95),
				"panFrameP95UnbakedMs": unbaked.Pan.Frame.P95, "panFrameP95BakedMs": baked.Pan.Frame.P95, "panFrameP95SavedMs": unbaked.Pan.Frame.P95 - baked.Pan.Frame.P95, "panFrameP95ReductionPct": pctReduction(unbaked.Pan.Frame.P95, baked.Pan.Frame.P95),
				"renderDrawImagesPerFrameUnbaked": unbaked.Render.DrawImagesPerFrame, "renderDrawImagesPerFrameBaked": baked.Render.DrawImagesPerFrame, "renderDrawImageReductionPct": pctReduction(unbaked.Render.DrawImagesPerFrame, baked.Render.DrawImagesPerFrame),
				"panDrawImagesPerFrameUnbaked": unbaked.Pan.DrawImagesPerFrame, "panDrawImagesPerFrameBaked": baked.Pan.DrawImagesPerFrame, "panDrawImageReductionPct": pctReduction(unbaked.Pan.DrawImagesPerFrame, baked.Pan.DrawImagesPerFrame),
				"loadHTTPBytesUnbaked": unbaked.LoadHTTPBytes, "loadHTTPBytesBaked": baked.LoadHTTPBytes, "loadHTTPBytesDelta": baked.LoadHTTPBytes - unbaked.LoadHTTPBytes,
				"loadBitmapBytesUnbaked": unbaked.Load.BitmapBytes, "loadBitmapBytesBaked": baked.Load.BitmapBytes, "loadBitmapBytesDelta": int64(baked.Load.BitmapBytes) - int64(unbaked.Load.BitmapBytes),
				"loadBrowserPrivateUnbaked": unbaked.LoadBrowserPrivate, "loadBrowserPrivateBaked": baked.LoadBrowserPrivate, "loadBrowserPrivateDelta": int64(baked.LoadBrowserPrivate) - int64(unbaked.LoadBrowserPrivate),
				"sourceElementJSONBytes": unbaked.Setup["sourceElementJSONBytes"], "unbakedSnapshotBytes": unbaked.Setup["snapshotBytes"], "bakedSnapshotBytes": baked.Setup["snapshotBytes"], "bakedProxySourceBytes": baked.Setup["bakedProxySourceBytes"],
			}
			comparisons = append(comparisons, comparison)
			rows = append(rows, map[string]any{"name": scenario.name + "_comparison", "comparison": comparison})
			persist()
		}
		report["bakeComparisons"] = comparisons
		thresholds := []float64{10, 25, 50}
		denseThresholds := map[string]any{}
		for _, threshold := range thresholds {
			for _, comparison := range comparisons {
				if comparison["family"] != "dense-varied" {
					continue
				}
				if comparison["renderFrameP95ReductionPct"].(float64) >= threshold {
					denseThresholds[fmt.Sprintf("firstRenderP95ReductionAtLeast%.0fPctElements", threshold)] = comparison["count"]
					break
				}
			}
			for _, comparison := range comparisons {
				if comparison["family"] != "dense-varied" {
					continue
				}
				if comparison["panFrameP95ReductionPct"].(float64) >= threshold {
					denseThresholds[fmt.Sprintf("firstPanP95ReductionAtLeast%.0fPctElements", threshold)] = comparison["count"]
					break
				}
			}
			for _, comparison := range comparisons {
				if comparison["family"] != "dense-varied" {
					continue
				}
				if comparison["renderFrameP95ReductionPct"].(float64) >= threshold && comparison["panFrameP95ReductionPct"].(float64) >= threshold {
					denseThresholds[fmt.Sprintf("firstRenderAndPanP95ReductionAtLeast%.0fPctElements", threshold)] = comparison["count"]
					break
				}
			}
		}
		report["denseVariedThresholds"] = denseThresholds
		if err := os.WriteFile(filepath.Join(output, "summary.csv"), []byte(summary.String()), 0600); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"mutex", "heap", "block"} {
			b := profileJSON(t, host.URL+"/__bench/profile?name="+name, "GET", nil)
			os.WriteFile(filepath.Join(output, "server."+name+".pprof"), b, 0600)
		}
		profileJSON(t, host.URL+"/__bench/stop", "POST", nil)
		cmd.Wait()
		cpu, _ := os.ReadFile(filepath.Join(root, "server.cpu.pprof"))
		os.WriteFile(filepath.Join(output, "server.cpu.pprof"), cpu, 0600)
		if len(page.Errors) > 0 {
			t.Fatalf("browser JavaScript errors during bake break-even profile: %v", page.Errors)
		}
		persist()
		return
	}

	if profileMode == "layer" {
		report["profile"] = "visual-layer-bake-baseline"
		scenarios := []struct {
			name                                   string
			count, images, sourceSize, elementSize int
			layout                                 string
		}{
			{"dense_200_shared_256", 200, 1, 512, 256, "dense"},
			{"dense_500_shared_256", 500, 1, 512, 256, "dense"},
			{"dense_200_assets32_256", 200, 32, 512, 256, "dense"},
			{"dense_500_assets64_256", 500, 64, 512, 256, "dense"},
			{"dense_500_assets64_512", 500, 64, 512, 512, "dense"},
			{"grid_500_assets64_256", 500, 64, 512, 256, "grid"},
		}
		page := pages[0]
		for _, scenario := range scenarios {
			t.Logf("layer scenario %s", scenario.name)
			page.eval(`(async()=>{__atlasProbe.stop();await __atlasProbe.clearImages();__atlasProbe.reset();__atlasProbe.measuring=true;return true})()`)
			page.resetNet()
			setup := profileJSON(t, host.URL+"/__bench/layer", "POST", map[string]any{"Count": scenario.count, "Images": scenario.images, "SourceSize": scenario.sourceSize, "ElementSize": scenario.elementSize, "Layout": scenario.layout})
			loadedMinimum := max(1, scenario.count*9/10)
			waitJS := fmt.Sprintf(`(async()=>{__atlasProbe.fit();for(let i=0;i<400;i++){if(__atlasProbe.layerSettled(%d)){await new Promise(r=>setTimeout(r,500));if(__atlasProbe.layerSettled(%d))return __atlasProbe.sample()}await new Promise(r=>setTimeout(r,100))}throw Error('layer profile did not settle: '+JSON.stringify(__atlasProbe.layerStatus()))})()`, loadedMinimum, loadedMinimum)
			cold := page.eval(waitJS)
			page.eval(`__atlasProbe.stop();true`)
			rows = append(rows, map[string]any{"name": scenario.name + "_load", "setup": setup, "metrics": json.RawMessage(cold), "httpBytes": page.HTTPBytes, "httpLoads": page.Requests, "wsIn": page.WSIn, "wsOut": page.WSOut, "browser": browserMem(), "errors": page.Errors})
			persist()
			measure(scenario.name+"_render", "render", 5)
			measure(scenario.name+"_pan", "pan", 6)
		}
		for _, name := range []string{"mutex", "heap", "block"} {
			b := profileJSON(t, host.URL+"/__bench/profile?name="+name, "GET", nil)
			os.WriteFile(filepath.Join(output, "server."+name+".pprof"), b, 0600)
		}
		profileJSON(t, host.URL+"/__bench/stop", "POST", nil)
		cmd.Wait()
		cpu, _ := os.ReadFile(filepath.Join(root, "server.cpu.pprof"))
		os.WriteFile(filepath.Join(output, "server.cpu.pprof"), cpu, 0600)
		persist()
		return
	}
	for _, width := range []int{4096, 8192, 10000, 32768} {
		if os.Getenv("ATLAS_PROFILE_SKIP_MAPS") != "" && width != 4096 {
			continue
		}
		profileJSON(t, host.URL+"/__bench/scene", "POST", map[string]int{"Count": 100, "MapWidth": width})
		pages[0].eval(`__atlasProbe.fit();true`)
		measure(fmt.Sprintf("map_%d_pan", width), "pan", 8)
	}
	for _, count := range []int{100, 500, 1000, 2000} {
		profileJSON(t, host.URL+"/__bench/scene", "POST", map[string]int{"Count": count, "Images": 0})
		pages[0].eval(fmt.Sprintf(`(async()=>{for(let i=0;i<100&&!__atlasProbe.scene(%d);i++)await new Promise(r=>setTimeout(r,50));__atlasProbe.fit();return true})()`, count))
		measure(fmt.Sprintf("tokens_%d_render", count), "render", 8)
		measure(fmt.Sprintf("tokens_%d_pan", count), "pan", 8)
		rows = append(rows, map[string]any{"name": fmt.Sprintf("storage_%d", count), "metrics": profileJSON(t, host.URL+"/__bench/storage", "GET", nil)})
		persist()
	}
	pages = append(pages, newPage(), newPage())
	for _, p := range pages {
		p.eval(`__atlasProbe.view();true`)
	} // Accessible only through eval? module variables require probe below.
	measure("2000_tokens_3_clients_move", "move", 30)
	for _, p := range pages {
		p.eval(`__atlasProbe.fit();true`)
	}
	profileJSON(t, host.URL+"/__bench/scene", "POST", map[string]int{"Count": 2000, "Images": 128})
	measure("2000_tokens_128_images_cold", "render", 15)
	measure("2000_tokens_128_images_pan", "pan", 20)
	pages[0].call("Page.reload", map[string]any{})
	time.Sleep(500 * time.Millisecond)
	pages[0].eval(`(async()=>{for(let i=0;i<100&&!window.__atlasProbe;i++)await new Promise(r=>setTimeout(r,50));__atlasProbe.enter({session:'load',key:'load-key'});for(let i=0;i<100&&!__atlasProbe.connected();i++)await new Promise(r=>setTimeout(r,50));__atlasProbe.openScene('load-scene');for(let i=0;i<100&&!__atlasProbe.ready();i++)await new Promise(r=>setTimeout(r,50));__atlasProbe.fit();return true})()`)
	measure("warm_reopen", "render", 10)
	for i := 0; i < 6; i++ {
		measure(fmt.Sprintf("long_session_%02d", i+1), "pan", 30)
	}
	for _, name := range []string{"mutex", "heap", "block"} {
		b := profileJSON(t, host.URL+"/__bench/profile?name="+name, "GET", nil)
		os.WriteFile(filepath.Join(output, "server."+name+".pprof"), b, 0600)
	}
	profileJSON(t, host.URL+"/__bench/stop", "POST", nil)
	cmd.Wait()
	cpu, _ := os.ReadFile(filepath.Join(root, "server.cpu.pprof"))
	os.WriteFile(filepath.Join(output, "server.cpu.pprof"), cpu, 0600)
	persist()
}
