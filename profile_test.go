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
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	base := s.routes()
	mux := http.NewServeMux()
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		b, _ := web.ReadFile("web/app.js")
		w.Write(b)
		io.WriteString(w, profileProbe)
	})
	mux.HandleFunc("POST /__bench/scene", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Count, Images, MapWidth int }
		json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		defer s.mu.Unlock()
		if req.MapWidth == 0 {
			req.MapWidth = 4096
		}
		floorID, layerID := firstFloorID(scene), firstLayerID(scene, firstFloorID(scene))
		scene.Elements = map[string]SceneElement{}
		for _, a := range ss.Assets {
			if a.Kind == "map" && a.Width == req.MapWidth {
				scene.Elements["map"] = SceneElement{ID: "map", FloorID: floorID, LayerID: layerID, AssetID: a.ID, Name: "Map", Transform: Transform{Width: float64(a.Width), Height: float64(a.Height)}, Visible: true, Opacity: 1}
				break
			}
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

func TestProfileAtlas(t *testing.T) {
	output := os.Getenv("ATLAS_PROFILE")
	if output == "" {
		t.Skip("set ATLAS_PROFILE to output directory")
	}
	if os.Getenv("ATLAS_PROFILE_HOST") != "" {
		t.Skip("host")
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
	profile := t.TempDir()
	browser := exec.Command(chrome, "--headless=new", "--no-first-run", "--no-default-browser-check", "--disable-background-timer-throttling", "--disable-renderer-backgrounding", "--disable-backgrounding-occluded-windows", "--remote-debugging-port=0", "--user-data-dir="+profile, "about:blank")
	if e := browser.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { browser.Process.Kill(); browser.Wait() }()
	var port, endpoint string
	for i := 0; i < 100; i++ {
		if b, e := os.ReadFile(filepath.Join(profile, "DevToolsActivePort")); e == nil {
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			port = strings.TrimSpace(lines[0])
			endpoint = strings.TrimSpace(lines[1])
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if port == "" {
		t.Fatal("browser startup")
	}
	control := profileDial(t, "ws://127.0.0.1:"+port+endpoint)
	report := map[string]any{"go": runtime.Version(), "os": runtime.GOOS, "cores": runtime.NumCPU(), "browser": json.RawMessage(control.call("Browser.getVersion", map[string]any{})), "viewport": "1440x1000 DPR1, headless, GPU not disabled", "origin": host.URL}
	report["gpu"] = json.RawMessage(control.call("SystemInfo.getInfo", map[string]any{}))
	var rows []map[string]any
	persist := func() {
		report["rows"] = rows
		b, _ := json.MarshalIndent(report, "", "  ")
		os.WriteFile(filepath.Join(output, "results.json"), b, 0600)
	}
	// Upload deterministic detailed JPEG maps. Source generation is driver-side.
	for _, dim := range [][2]int{{4096, 4096}, {8192, 8192}, {10000, 10000}, {32768, 3000}} {
		if os.Getenv("ATLAS_PROFILE_SKIP_MAPS") != "" && dim[0] != 4096 {
			continue
		}
		w, h := dim[0], dim[1]
		t.Logf("map %dx%d", w, h)
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
		img = nil
		runtime.GC()
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
		req, _ := http.NewRequest("POST", host.URL+"/api/upload?session=load&scene=load-scene&kind=map", bytes.NewReader(b.Bytes()))
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
		rows = append(rows, map[string]any{"name": fmt.Sprintf("map_%dx%d", w, h), "seconds": elapsed, "uploadBytes": b.Len(), "status": res.StatusCode, "response": string(response), "serverCPUSeconds": after.CPUSeconds - before.CPUSeconds, "serverPeakWorkingSet": peak, "worker": json.RawMessage(profileJSON(t, host.URL+"/__bench/worker", "GET", nil))})
		persist()
		if res.StatusCode != 200 {
			t.Fatal(string(response))
		}
	}
	// Restore 4096 map for identical token density in all runs.
	// All maps are in the isolated host; use its state to choose the first map.
	profileJSON(t, host.URL+"/__bench/scene", "POST", map[string]int{"Count": 100, "Images": 0})
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
		d.eval(`(async()=>{for(let i=0;i<100&&!window.__atlasProbe;i++)await new Promise(r=>setTimeout(r,50));__atlasProbe.enter({session:'load',key:'load-key'});for(let i=0;i<100&&!__atlasProbe.ready();i++)await new Promise(r=>setTimeout(r,50));return __atlasProbe.ready()})()`)
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
		for _, browserControl := range controls {
			json.Unmarshal(browserControl.call("SystemInfo.getProcessInfo", map[string]any{}), &info)
			for _, p := range info.ProcessInfo {
				v := readProcessStats(p.ID)
				ws += v.WorkingSet
				private += v.PrivateBytes
				processes = append(processes, p)
			}
		}
		return map[string]any{"workingSetSum": ws, "privateBytesSum": private, "processes": processes}
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
	pages[0].eval(`(async()=>{for(let i=0;i<100&&!window.__atlasProbe;i++)await new Promise(r=>setTimeout(r,50));__atlasProbe.enter({session:'load',key:'load-key'});for(let i=0;i<100&&!__atlasProbe.ready();i++)await new Promise(r=>setTimeout(r,50));__atlasProbe.fit();return true})()`)
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
