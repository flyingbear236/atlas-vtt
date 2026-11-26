package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCampaignMediaVirtualPaths(t *testing.T) {
	c := CampaignMedia{Files: map[string]CampaignMediaEntry{"Props/Barrels/barrel.png": {AssetID: "a", AddedAt: 7}, "Tavern/barrel.png": {AssetID: "b"}}, Directories: []string{"Tavern", "Empty"}}
	run := func(req mediaOperation) {
		t.Helper()
		var err error
		c, err = applyMediaOperation(c, req)
		if err != nil {
			t.Fatal(err)
		}
	}
	run(mediaOperation{Type: "copy", Paths: []string{"Props/Barrels/barrel.png"}, Target: "Tavern"})
	if len(c.Files) != 3 || c.Files["Tavern/barrel (2).png"].AssetID != "a" {
		t.Fatal(c.Files)
	}
	run(mediaOperation{Type: "copy", Paths: []string{"Props/Barrels/barrel.png"}, Target: "Tavern"})
	if len(c.Files) != 3 {
		t.Fatal("repeated copy made duplicate links")
	}
	run(mediaOperation{Type: "move", Paths: []string{"Props/Barrels/barrel.png"}, Target: "Empty"})
	if c.Files["Empty/barrel.png"].AssetID != "a" || c.Files["Props/Barrels/barrel.png"].AssetID != "" {
		t.Fatal(c.Files)
	}
	run(mediaOperation{Type: "folderMove", Path: "Empty", Target: "Tavern/Collection"})
	run(mediaOperation{Type: "rename", Path: "Tavern/Collection/barrel.png", Target: "Tavern/Collection/Renamed.png"})
	if c.Files["Tavern/Collection/Renamed.png"].AssetID != "a" {
		t.Fatal(c.Files)
	}
	run(mediaOperation{Type: "folderDelete", Path: "Tavern/Collection"})
	if len(c.Files) != 2 {
		t.Fatal(c.Files)
	}
	run(mediaOperation{Type: "folderDelete", Path: "Tavern"})
	if len(c.Files) != 2 || c.Files["barrel.png"].AssetID != "b" || c.Files["barrel (2).png"].AssetID != "a" {
		t.Fatal(c.Files)
	}
	if _, err := applyMediaOperation(c, mediaOperation{Type: "folderCreate", Target: "../escape"}); err == nil {
		t.Fatal("accepted traversal")
	}
	if _, err := newMediaNamespace(CampaignMedia{Files: map[string]CampaignMediaEntry{"A/x.png": {}, "a/y.png": {}}}); err == nil {
		t.Fatal("accepted case-conflicting parents")
	}
	if _, err := newMediaNamespace(CampaignMedia{Files: map[string]CampaignMediaEntry{"file.png": {}, "file.png/x.png": {}}}); err == nil {
		t.Fatal("accepted file as folder")
	}
}

func TestCampaignMediaFolderLimits(t *testing.T) {
	c := CampaignMedia{Files: map[string]CampaignMediaEntry{}}
	for i := 0; i < maxLibraryDirectories+1; i++ {
		c.Files[fmt.Sprintf("folder-%d/image.png", i)] = CampaignMediaEntry{AssetID: "a"}
	}
	if _, err := newMediaNamespace(c); err == nil {
		t.Fatal("implicit folders escaped limit")
	}
}

func TestCampaignMediaPlacementLayer(t *testing.T) {
	scene := newScene("scene", "Scene")
	floor := firstFloorID(scene)
	scene.Layers = map[string]Layer{}
	add := func(id, kind string, order int) {
		scene.Layers[id] = Layer{ID: id, FloorID: floor, Name: id, Kind: kind, Order: order, Visible: true, Opacity: 1}
	}
	add("bottom", layerKindVisual, 0)
	add("below", layerKindVisual, 1)
	add("tokens", layerKindTokens, 2)
	add("above", layerKindVisual, 3)
	check := func(selected, want string) {
		t.Helper()
		got, err := mediaPlacementLayer(scene, floor, selected)
		if err != nil || got.ID != want {
			t.Fatalf("got %s, %v; want %s", got.ID, err, want)
		}
	}
	check("tokens", "below")
	check("bottom", "bottom")
	delete(scene.Layers, "below")
	check("tokens", "bottom")
	delete(scene.Layers, "bottom")
	check("tokens", "above")
	delete(scene.Layers, "above")
	if _, err := mediaPlacementLayer(scene, floor, "tokens"); err == nil {
		t.Fatal("accepted floor without visual layer")
	}
}

func TestCampaignMediaPreviews(t *testing.T) {
	root := t.TempDir()
	a := Asset{ID: "a", RenderMode: renderModeTiled, Levels: 4}
	dir := filepath.Join(root, "assets", a.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	im := image.NewNRGBA(image.Rect(0, 0, 512, 256))
	im.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	f, err := os.Create(filepath.Join(dir, "3_0_0.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err = png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err = prepareMediaPreviews(root, a); err != nil {
		t.Fatal(err)
	}
	f, err = os.Open(filepath.Join(dir, "media-thumb.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	thumb, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if thumb.Bounds().Dx() != 192 || thumb.Bounds().Dy() != 96 {
		t.Fatal(thumb.Bounds())
	}
	if _, _, _, alpha := thumb.At(191, 95).RGBA(); alpha != 0 {
		t.Fatal("lost alpha")
	}
}

// Exercise the real HTTP entrypoints, persistence, runtime visibility and object
// conversion without requiring libvips for this test (a fixture representation).
func TestCampaignMediaHTTPPlacementAndPersistence(t *testing.T) {
	f := newDiceCommandFixture(t)
	ss := f.session
	asset := Asset{ID: "catalog-scene-image", SourceID: "source", RepresentationVersion: representationVersion, Filename: "barrel.png", MimeType: "image/png", Kind: assetKindScene, RenderMode: renderModeBitmap, Width: 100, Height: 60, RetentionPolicy: assetReclaimable}
	f.server.mu.Lock()
	ss.Assets[asset.ID] = asset
	ss.MediaLibrary = CampaignMedia{Files: map[string]CampaignMediaEntry{"Props/Barrels/barrel.png": {AssetID: asset.ID}}, Directories: []string{"Tavern"}}
	f.server.dirty = true
	if err := f.server.saveLocked(); err != nil {
		f.server.mu.Unlock()
		t.Fatal(err)
	}
	f.server.mu.Unlock()
	req := func(method, path, key string, body any, status int) []byte {
		t.Helper()
		var r io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r = bytes.NewReader(b)
		}
		q := "?"
		if strings.Contains(path, "?") {
			q = "&"
		}
		request, err := http.NewRequest(method, f.host.URL+path+q+"session="+url.QueryEscape(ss.ID), r)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+key)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		out, _ := io.ReadAll(response.Body)
		if response.StatusCode != status {
			t.Fatalf("%s: %d != %d: %s", path, response.StatusCode, status, out)
		}
		return out
	}
	req("GET", "/api/media-library", f.alice["key"], nil, 403)
	req("GET", "/api/media-library/image?path=Props%2FBarrels%2Fbarrel.png&variant=original", f.alice["key"], nil, 403)
	req("POST", "/api/media-library/operation", f.gm["key"], mediaOperation{Revision: 0, Type: "copy", Paths: []string{"Props/Barrels/barrel.png"}, Target: "Tavern"}, 200)
	req("POST", "/api/media-library/operation", f.gm["key"], mediaOperation{Revision: 0, Type: "delete", Paths: []string{"Props/Barrels/barrel.png"}}, 409)
	floor := firstFloorID(f.sceneA)
	layer := layerIDByKind(f.sceneA, floor, layerKindTokens)
	placement := mediaPlacement{Paths: []string{"Props/Barrels/barrel.png", "Tavern/barrel.png"}, SceneID: f.sceneA.ID, FloorID: floor, LayerID: layer, X: 120, Y: 200}
	req("POST", "/api/media-library/place", f.alice["key"], placement, 403)
	req("POST", "/api/media-library/place", f.gm["key"], placement, 200)
	f.server.mu.Lock()
	if len(f.sceneA.Elements) != 2 || f.sceneA.runtime.elementIndexed != 2 || f.sceneA.runtime.assetAll[asset.ID] != 2 {
		f.server.mu.Unlock()
		t.Fatal("wrong scene runtime")
	}
	var objectID string
	for id, e := range f.sceneA.Elements {
		if e.AssetID != asset.ID || e.Transform.Width != 100 || e.Transform.Height != 60 || e.Interaction != nil {
			f.server.mu.Unlock()
			t.Fatal(e)
		}
		if e.Transform.X == 70 && e.Transform.Y == 170 {
			objectID = id
		}
	}
	if objectID == "" {
		f.server.mu.Unlock()
		t.Fatal("wrong placement coordinates")
	}
	obj := f.sceneA.Elements[objectID]
	obj.Interaction = &ObjectInteraction{Hover: ObjectMessage{Text: "secret"}, Reactions: map[string]ObjectReaction{"inspect": {Any: ObjectMessage{Text: "key"}}}}
	f.sceneA.Elements[objectID] = obj
	f.server.dirty = true
	f.server.mu.Unlock()
	req("POST", "/api/media-library/operation", f.gm["key"], mediaOperation{Revision: 1, Type: "delete", Paths: []string{"Props/Barrels/barrel.png", "Tavern/barrel.png"}}, 200)
	f.server.mu.Lock()
	refreshAssetOrphans(ss, time.Now())
	if ss.Assets[asset.ID].OrphanSince != nil || objectReaction(f.sceneA.Elements[objectID], "inspect", nil).Text != "key" {
		f.server.mu.Unlock()
		t.Fatal("scene/object lost after deleting catalog links")
	}
	f.server.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(f.root, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]*Session
	if err = json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored[ss.ID].MediaLibrary.Revision != 2 || len(stored[ss.ID].Scenes[f.sceneA.ID].Elements) != 2 {
		t.Fatal("persistence missing")
	}
}

func TestCampaignMediaZipImport(t *testing.T) {
	if _, err := findVips(); err != nil {
		t.Skip(err)
	}
	f := newDiceCommandFixture(t)
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	data := pipelinePNG(t, 2048, 1024)
	for _, path := range []string{"Props/Barrels/prop_0042.png", "Copies/prop_0042.png"} {
		entry, err := z.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	entry, _ := z.Create("../escape.png")
	entry.Write(data)
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("zip:pack.zip", "pack.zip")
	if err != nil {
		t.Fatal(err)
	}
	part.Write(archive.Bytes())
	writer.Close()
	request, _ := http.NewRequest("POST", f.host.URL+"/api/media-library/import?session="+f.session.ID, &body)
	request.Header.Set("Authorization", "Bearer "+f.gm["key"])
	request.Header.Set("Content-Type", writer.FormDataContentType())
	res, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var result mediaImportResult
	if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&result) != nil {
		t.Fatalf("bad response: %d", res.StatusCode)
	}
	if result.Imported != 2 || result.Skipped != 1 || len(result.Catalog.Files) != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	a, b := result.Catalog.Files[0], result.Catalog.Files[1]
	if a.AssetID != b.AssetID || a.Width != 2048 || a.Height != 1024 {
		t.Fatal(a, b)
	}
	original, err := os.ReadFile(filepath.Join(f.root, "assets", a.AssetID, "original"))
	if err != nil || !bytes.Equal(original, data) {
		t.Fatal("original not preserved", err)
	}
}

func TestCampaignMediaRasterFormatAllowlist(t *testing.T) {
	for _, tt := range []struct{ header, want string }{{"GIF89a", "gif"}, {"RIFFabcdWEBP", "webp"}, {"BMabcdef", "bmp"}, {"\x00\x00\x00\x18ftypavif\x00\x00\x00\x00avif", "avif"}, {"%PDF-1.7", ""}, {"<svg></svg>", ""}, {"RIFFabcdWAVE", ""}} {
		if got := additionalMediaFormat([]byte(tt.header)); got != tt.want {
			t.Fatalf("%q: got %q want %q", tt.header, got, tt.want)
		}
	}
}
