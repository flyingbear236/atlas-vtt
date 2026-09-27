package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

type canvasBrowserMemory struct {
	WorkingSet   uint64                       `json:"workingSet"`
	PrivateBytes uint64                       `json:"privateBytes"`
	Groups       map[string]map[string]uint64 `json:"groups"`
	Processes    []map[string]any             `json:"processes"`
}

type canvasAtlasMemory struct {
	Entries           int   `json:"entries"`
	BitmapBytes       int64 `json:"bitmapBytes"`
	FallbackBytes     int64 `json:"fallbackBytes"`
	ArtworkBytes      int64 `json:"artworkBytes"`
	CanvasBytes       int64 `json:"canvasBytes"`
	Pending           int   `json:"pending"`
	ActiveLoads       int   `json:"activeLoads"`
	TotalCreated      int   `json:"totalCreated"`
	TotalCreatedBytes int64 `json:"totalCreatedBytes"`
	TotalClosed       int   `json:"totalClosed"`
	TotalClosedBytes  int64 `json:"totalClosedBytes"`
}

type canvasMemoryPoint struct {
	Variant          string              `json:"variant"`
	Stage            string              `json:"stage"`
	Browser          canvasBrowserMemory `json:"browser"`
	Atlas            canvasAtlasMemory   `json:"atlas"`
	JSHeapBytes      float64             `json:"jsHeapBytes"`
	PeakPrivateBytes uint64              `json:"peakPrivateBytes,omitempty"`
}

type canvasProfileBrowser struct {
	cmd     *exec.Cmd
	log     *os.File
	profile string
	control *profileCDP
	page    *profileCDP
}

func startCanvasProfileBrowser(t *testing.T, chrome, output, name, hostURL string) *canvasProfileBrowser {
	t.Helper()
	profile, err := os.MkdirTemp(output, ".canvas-"+name+"-")
	if err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(output, "browser-"+name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--no-sandbox", "--headless=new", "--no-first-run", "--no-default-browser-check", "--disable-background-timer-throttling", "--disable-renderer-backgrounding", "--disable-backgrounding-occluded-windows", "--remote-debugging-port=0", "--user-data-dir=" + profile, "about:blank"}
	cmd := exec.Command(chrome, args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err = cmd.Start(); err != nil {
		logFile.Close()
		os.RemoveAll(profile)
		t.Fatal(err)
	}
	browser := &canvasProfileBrowser{cmd: cmd, log: logFile, profile: profile}
	var port, endpoint string
	for i := 0; i < 150; i++ {
		if data, readErr := os.ReadFile(filepath.Join(profile, "DevToolsActivePort")); readErr == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			port, endpoint = strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if port == "" {
		browser.close()
		t.Fatal("browser startup")
	}
	browser.control = profileDial(t, "ws://127.0.0.1:"+port+endpoint)
	target := browser.control.call("Target.createTarget", map[string]any{"url": "about:blank"})
	var targetID struct {
		ID string `json:"targetId"`
	}
	json.Unmarshal(target, &targetID)
	var tabs []struct {
		ID  string `json:"id"`
		URL string `json:"webSocketDebuggerUrl"`
	}
	data := profileJSON(t, "http://127.0.0.1:"+port+"/json", "GET", nil)
	json.Unmarshal(data, &tabs)
	for _, tab := range tabs {
		if tab.ID == targetID.ID {
			browser.page = profileDial(t, tab.URL)
			break
		}
	}
	if browser.page == nil {
		browser.close()
		t.Fatal("profile target missing")
	}
	page := browser.page
	page.call("Runtime.enable", map[string]any{})
	page.call("Network.enable", map[string]any{})
	page.call("Page.enable", map[string]any{})
	page.call("Performance.enable", map[string]any{})
	page.call("HeapProfiler.enable", map[string]any{})
	page.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1440, "height": 1000, "deviceScaleFactor": 1, "mobile": false})
	page.call("Page.navigate", map[string]any{"url": hostURL})
	ready := page.eval(`(async()=>{for(let i=0;i<200&&!window.__atlasProbe;i++)await new Promise(r=>setTimeout(r,50));if(!window.__atlasProbe)return false;__atlasProbe.enter({session:'load',key:'load-key'});for(let i=0;i<200&&!__atlasProbe.connected();i++)await new Promise(r=>setTimeout(r,50));return __atlasProbe.connected()})()`)
	if string(ready) != "true" {
		browser.close()
		t.Fatal("campaign home did not open")
	}
	return browser
}

func (b *canvasProfileBrowser) close() {
	if b.page != nil {
		b.page.c.Close()
	}
	if b.control != nil {
		b.control.c.Close()
	}
	if b.cmd != nil && b.cmd.Process != nil {
		b.cmd.Process.Kill()
		b.cmd.Wait()
	}
	if b.log != nil {
		b.log.Close()
	}
	if b.profile != "" {
		os.RemoveAll(b.profile)
	}
}

func canvasBrowserStats(control *profileCDP) canvasBrowserMemory {
	var info struct {
		ProcessInfo []struct {
			ID   int     `json:"id"`
			Type string  `json:"type"`
			CPU  float64 `json:"cpuTime"`
		} `json:"processInfo"`
	}
	json.Unmarshal(control.call("SystemInfo.getProcessInfo", map[string]any{}), &info)
	result := canvasBrowserMemory{Groups: map[string]map[string]uint64{}}
	for _, process := range info.ProcessInfo {
		stats := readProcessStats(process.ID)
		result.WorkingSet += stats.WorkingSet
		result.PrivateBytes += stats.PrivateBytes
		group := result.Groups[process.Type]
		if group == nil {
			group = map[string]uint64{}
			result.Groups[process.Type] = group
		}
		group["workingSet"] += stats.WorkingSet
		group["privateBytes"] += stats.PrivateBytes
		group["count"]++
		result.Processes = append(result.Processes, map[string]any{"id": process.ID, "type": process.Type, "cpuTime": process.CPU, "workingSet": stats.WorkingSet, "privateBytes": stats.PrivateBytes})
	}
	return result
}

func canvasMemoryCheckpoint(t *testing.T, browser *canvasProfileBrowser, variant, stage string, collectGarbage bool, peak uint64) canvasMemoryPoint {
	t.Helper()
	if collectGarbage {
		browser.page.call("HeapProfiler.collectGarbage", map[string]any{})
		time.Sleep(1500 * time.Millisecond)
	}
	atlasJSON := browser.page.eval(`__atlasProbe.imageMemory()`)
	var atlas canvasAtlasMemory
	json.Unmarshal(atlasJSON, &atlas)
	heapJSON := browser.page.call("Runtime.getHeapUsage", map[string]any{})
	var heap struct {
		UsedSize float64 `json:"usedSize"`
	}
	json.Unmarshal(heapJSON, &heap)
	return canvasMemoryPoint{Variant: variant, Stage: stage, Browser: canvasBrowserStats(browser.control), Atlas: atlas, JSHeapBytes: heap.UsedSize, PeakPrivateBytes: peak}
}

func TestProfileCanvasLifecycle(t *testing.T) {
	output := os.Getenv("ATLAS_CANVAS_LIFECYCLE_PROFILE")
	if output == "" {
		t.Skip("set ATLAS_CANVAS_LIFECYCLE_PROFILE to output directory")
	}
	if os.Getenv("ATLAS_PROFILE_HOST") != "" {
		t.Skip("host")
	}
	chrome := os.Getenv("ATLAS_CHROME")
	if chrome == "" {
		t.Fatal("ATLAS_CHROME required")
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	executable, _ := os.Executable()
	hostCommand := exec.Command(executable, "-test.run=^TestProfileHost$", "-test.timeout=15m")
	hostCommand.Env = append(os.Environ(), "ATLAS_PROFILE_HOST="+root)
	hostLog, _ := os.Create(filepath.Join(output, "host.log"))
	hostCommand.Stdout, hostCommand.Stderr = hostLog, hostLog
	if err := hostCommand.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { hostCommand.Process.Kill(); hostCommand.Wait(); hostLog.Close() }()
	var host struct {
		URL string `json:"url"`
	}
	for i := 0; i < 200; i++ {
		if data, err := os.ReadFile(filepath.Join(root, "host.json")); err == nil {
			json.Unmarshal(data, &host)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if host.URL == "" {
		t.Fatal("host startup failed")
	}

	t.Log("prepare one 10000x10000 tiled asset")
	imageData := image.NewRGBA(image.Rect(0, 0, 10000, 10000))
	for y := 0; y < 10000; y++ {
		for x := 0; x < 10000; x++ {
			offset := y*imageData.Stride + x*4
			value := uint8((x*13 + y*7 + (x*y)%251) % 256)
			imageData.Pix[offset] = value
			imageData.Pix[offset+1] = uint8(x/16 + y/8)
			imageData.Pix[offset+2] = uint8(y/32 + x/8)
			imageData.Pix[offset+3] = 255
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, imageData, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	imageData = nil
	runtime.GC()
	debug.FreeOSMemory()
	request, _ := http.NewRequest("POST", host.URL+"/api/upload?session=load&scene=load-scene&kind=map", bytes.NewReader(encoded.Bytes()))
	request.Header.Set("Authorization", "Bearer load-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("large asset: %s", responseBody)
	}
	encoded.Reset()
	runtime.GC()
	debug.FreeOSMemory()
	profileJSON(t, host.URL+"/__bench/scene", "POST", map[string]any{"Count": 0, "MapWidth": 10000, "MapElements": 1, "DistinctMaps": true})

	report := map[string]any{"profile": "canvas-lifecycle-causal", "asset": "10000x10000 tiled", "sweepSeconds": 20}
	var points []canvasMemoryPoint
	strategies := map[string]json.RawMessage{}
	persist := func() {
		report["points"] = points
		report["strategies"] = strategies
		data, _ := json.MarshalIndent(report, "", "  ")
		os.WriteFile(filepath.Join(output, "results.json"), data, 0600)
	}

	variants := []string{"control", "backing-reset", "context-replacement", "reload"}
	for index, variant := range variants {
		t.Logf("canvas lifecycle variant %s", variant)
		browser := startCanvasProfileBrowser(t, chrome, output, variant, host.URL)
		if index == 0 {
			report["browser"] = json.RawMessage(browser.control.call("Browser.getVersion", map[string]any{}))
			report["gpu"] = json.RawMessage(browser.control.call("SystemInfo.getInfo", map[string]any{}))
		}
		page := browser.page
		points = append(points, canvasMemoryCheckpoint(t, browser, variant, "campaign_home_baseline", true, 0))
		opened := page.eval(`(async()=>{__atlasProbe.openScene('load-scene');for(let i=0;i<300&&!__atlasProbe.ready();i++)await new Promise(r=>setTimeout(r,50));if(!__atlasProbe.ready())return false;__atlasProbe.fit();for(let i=0;i<400;i++){if(__atlasProbe.layerSettled(1)){await new Promise(r=>setTimeout(r,750));if(__atlasProbe.layerSettled(1))return true}await new Promise(r=>setTimeout(r,100))}return false})()`)
		if string(opened) != "true" {
			browser.close()
			t.Fatalf("%s scene did not settle", variant)
		}
		page.eval(`__atlasProbe.memorySweep();true`)
		var peak uint64
		until := time.Now().Add(20 * time.Second)
		for time.Now().Before(until) {
			memory := canvasBrowserStats(browser.control)
			if memory.PrivateBytes > peak {
				peak = memory.PrivateBytes
			}
			time.Sleep(250 * time.Millisecond)
		}
		page.eval(`__atlasProbe.stop();true`)
		page.eval(`(async()=>{for(let i=0;i<200;i++){if(__atlasProbe.layerSettled(1))return true;await new Promise(r=>setTimeout(r,100))}return false})()`)
		points = append(points, canvasMemoryCheckpoint(t, browser, variant, "after_pan_zoom", false, peak))
		beforeActivity := page.eval(`__atlasProbe.imageMemory()`)
		page.resetNet()
		var strategyResult json.RawMessage
		switch variant {
		case "control":
			strategyResult = page.eval(`({kind:'control',before:__atlasProbe.surfaceState(),after:__atlasProbe.surfaceState(),durationMs:0})`)
		case "backing-reset":
			strategyResult = page.eval(`__atlasProbe.resetBackingStore()`)
		case "context-replacement":
			strategyResult = page.eval(`__atlasProbe.replaceSurface()`)
		case "reload":
			page.call("Page.reload", map[string]any{})
			strategyResult = page.eval(`(async()=>{for(let i=0;i<200&&!window.__atlasProbe;i++)await new Promise(r=>setTimeout(r,50));return {kind:'reload',ready:!!window.__atlasProbe}})()`)
		}
		time.Sleep(time.Second)
		afterActivity := page.eval(`__atlasProbe.imageMemory()`)
		var beforeCounters, afterCounters canvasAtlasMemory
		json.Unmarshal(beforeActivity, &beforeCounters)
		json.Unmarshal(afterActivity, &afterCounters)
		strategies[variant] = strategyResult
		if variant == "backing-reset" || variant == "context-replacement" {
			var surface struct {
				Before, After struct {
					Camera        struct{ X, Y, Scale float64 } `json:"camera"`
					Width, Height int                           `json:"width"`
					Handlers      bool                          `json:"handlers"`
					Signature     string                        `json:"signature"`
				} `json:"before"`
			}
			json.Unmarshal(strategyResult, &surface)
			if surface.Before.Camera != surface.After.Camera || surface.Before.Width != surface.After.Width || surface.Before.Height != surface.After.Height || !surface.After.Handlers || surface.Before.Signature != surface.After.Signature {
				browser.close()
				t.Fatalf("%s changed visible surface: %s", variant, strategyResult)
			}
			if page.Requests != 0 || afterCounters.TotalCreated != beforeCounters.TotalCreated {
				browser.close()
				t.Fatalf("%s reloaded resources: HTTP=%d created=%d", variant, page.Requests, afterCounters.TotalCreated-beforeCounters.TotalCreated)
			}
		}
		points = append(points, canvasMemoryCheckpoint(t, browser, variant, "after_strategy", true, 0))
		if variant != "reload" {
			page.eval(`(async()=>{__atlasProbe.home();await __atlasProbe.clearImages();return true})()`)
		} else {
			page.eval(`__atlasProbe.clearImages()`)
		}
		points = append(points, canvasMemoryCheckpoint(t, browser, variant, "after_cleanup_1_5s", true, 0))
		time.Sleep(8500 * time.Millisecond)
		points = append(points, canvasMemoryCheckpoint(t, browser, variant, "after_cleanup_10s", false, 0))
		if len(page.Errors) > 0 {
			browser.close()
			t.Fatalf("%s browser JavaScript errors: %v", variant, page.Errors)
		}
		browser.close()
		persist()
	}

	find := func(variant, stage string) canvasMemoryPoint {
		for _, point := range points {
			if point.Variant == variant && point.Stage == stage {
				return point
			}
		}
		return canvasMemoryPoint{}
	}
	controlBase := find("control", "campaign_home_baseline").Browser.PrivateBytes
	controlFinal := find("control", "after_cleanup_10s").Browser.PrivateBytes
	controlRetained := float64(max(controlFinal, controlBase) - controlBase)
	decisions := map[string]any{}
	for _, variant := range []string{"backing-reset", "context-replacement"} {
		base := find(variant, "campaign_home_baseline").Browser.PrivateBytes
		final := find(variant, "after_cleanup_10s").Browser.PrivateBytes
		retained := float64(max(final, base) - base)
		reduction := 0.0
		if controlRetained > 0 {
			reduction = 1 - retained/controlRetained
		}
		decisions[variant] = map[string]any{"baselineBytes": base, "finalBytes": final, "retainedBytes": retained, "reductionVsControl": reduction, "qualified": reduction >= .5 || final <= base+150*1024*1024}
	}
	report["decision"] = decisions
	var csv strings.Builder
	csv.WriteString("variant,stage,private_mib,working_set_mib,renderer_private_mib,gpu_private_mib,js_heap_mib,bitmap_mib,created_mib,closed_mib,peak_private_mib\n")
	for _, point := range points {
		mib := func(value uint64) float64 { return float64(value) / (1024 * 1024) }
		csv.WriteString(fmt.Sprintf("%s,%s,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f\n", point.Variant, point.Stage, mib(point.Browser.PrivateBytes), mib(point.Browser.WorkingSet), mib(point.Browser.Groups["renderer"]["privateBytes"]), mib(point.Browser.Groups["GPU"]["privateBytes"]), point.JSHeapBytes/(1024*1024), float64(point.Atlas.BitmapBytes)/(1024*1024), float64(point.Atlas.TotalCreatedBytes)/(1024*1024), float64(point.Atlas.TotalClosedBytes)/(1024*1024), mib(point.PeakPrivateBytes)))
	}
	os.WriteFile(filepath.Join(output, "summary.csv"), []byte(csv.String()), 0600)
	persist()
	profileJSON(t, host.URL+"/__bench/stop", "POST", nil)
	hostCommand.Wait()
}
