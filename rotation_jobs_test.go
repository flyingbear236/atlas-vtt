package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type rotationFixture struct {
	server  *Server
	session *Session
	scene   *Scene
	member  *Member
	peer    *peer
	element SceneElement
	source  Asset
}

func newRotationFixture(t *testing.T, transform Transform) rotationFixture {
	t.Helper()
	s := testServer(t, t.TempDir())
	member := &Member{ID: "gm", Name: "GM", Role: "gm", Secret: "secret"}
	scene := newScene("scene", "Rotation")
	scene.Published = true
	floorID := firstFloorID(scene)
	layerID := firstLayerID(scene, floorID)
	source := Asset{ID: "source", SourceID: "source-bytes", RepresentationVersion: representationVersion, Filename: "source.png", MimeType: "image/png", Width: 120, Height: 60, Kind: assetKindScene, RenderMode: renderModeBitmap, RetentionPolicy: assetReclaimable}
	element := SceneElement{ID: "element", FloorID: floorID, LayerID: layerID, AssetID: source.ID, Name: "Map", Transform: transform, ZOrder: 7, Visible: true, Opacity: .65}
	scene.Elements[element.ID] = element
	scene.rebuildRuntime()
	session := &Session{ID: "campaign", Name: "Campaign", Invite: "invite", Members: map[string]*Member{member.ID: member}, Assets: map[string]Asset{source.ID: source}, Keys: map[string]string{member.Secret: member.ID}, Receipts: map[string]Receipt{}, CampaignRevision: 1, Scenes: map[string]*Scene{scene.ID: scene}}
	s.sessions[session.ID] = session
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		t.Fatal(err)
	}
	p := &peer{member: member, session: session.ID, sceneID: scene.ID, out: make(chan any, 128)}
	return rotationFixture{s, session, scene, member, p, element, source}
}

func fakeRotationAsset(t *testing.T, root string, source Asset, recipe rotationRecipe, recipeHash string) (Asset, error) {
	t.Helper()
	asset := Asset{
		ID: representationID("derived:"+recipeHash, assetKindScene, representationVersion, renderModeBitmap), SourceID: "derived:" + recipeHash,
		RepresentationVersion: representationVersion, Filename: "derived.png", MimeType: "image/png", Width: recipe.Plan.OutputWidth, Height: recipe.Plan.OutputHeight,
		Kind: assetKindScene, RenderMode: renderModeBitmap, RetentionPolicy: assetReclaimable,
		Provenance: &AssetProvenance{Operation: "fixRotation", SourceAssetID: source.ID, RecipeHash: recipeHash, RecipeVersion: recipe.Version, Transform: recipe.Transform, PixelDensity: recipe.Plan.PixelDensity},
	}
	dir := filepath.Join(root, "assets", asset.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Asset{}, err
	}
	data, err := json.Marshal(asset)
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "meta.json"), data, 0600)
	}
	return asset, err
}

func receiveRotationAck(t *testing.T, p *peer) map[string]any {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case raw := <-p.out:
			message, ok := raw.(map[string]any)
			if ok && message["type"] == "ack" {
				return message
			}
		case <-timer.C:
			t.Fatal("rotation ack timeout")
		}
	}
}

func runRotationCommand(t *testing.T, fixture rotationFixture, p *peer, command Command) {
	t.Helper()
	fixture.server.mu.Lock()
	fixture.server.rotationCommand(fixture.session, p, command)
	fixture.server.mu.Unlock()
}

func TestFixedRotationPlanPreservesCenterAndAnisotropicScale(t *testing.T) {
	asset := Asset{Width: 120, Height: 60}
	transform := Transform{X: -100, Y: -20, Width: 240, Height: 60, Rotation: 90}
	plan, err := planFixedRotation(asset, transform)
	if err != nil {
		t.Fatal(err)
	}
	if plan.PixelDensity != .5 || plan.ScaledWidth != 120 || plan.ScaledHeight != 30 || plan.OutputWidth != 30 || plan.OutputHeight != 120 {
		t.Fatalf("unexpected raster plan: %#v", plan)
	}
	if math.Abs(plan.WorldX-(-10)) > 1e-9 || math.Abs(plan.WorldY-(-110)) > 1e-9 || math.Abs(plan.WorldWidth-60) > 1e-9 || math.Abs(plan.WorldHeight-240) > 1e-9 {
		t.Fatalf("world footprint changed: %#v", plan)
	}
	beforeX, beforeY := transform.X+transform.Width/2, transform.Y+transform.Height/2
	afterX, afterY := plan.WorldX+plan.WorldWidth/2, plan.WorldY+plan.WorldHeight/2
	if beforeX != afterX || beforeY != afterY {
		t.Fatalf("center moved from %v,%v to %v,%v", beforeX, beforeY, afterX, afterY)
	}
	transform.Rotation = 37
	if arbitrary, err := planFixedRotation(asset, transform); err != nil || arbitrary.WorldX >= 0 || arbitrary.WorldY >= 0 || arbitrary.OutputWidth <= 0 || arbitrary.OutputHeight <= 0 {
		t.Fatalf("arbitrary negative-coordinate rotation: %#v, %v", arbitrary, err)
	}
}

func TestFixedRotationPipelinePreservesVisualOrientation(t *testing.T) {
	root := t.TempDir()
	input := image.NewNRGBA(image.Rect(0, 0, 120, 60))
	colors := [4]color.NRGBA{{R: 240, A: 255}, {G: 240, A: 255}, {B: 240, A: 255}, {R: 240, G: 240, A: 255}}
	for y := 0; y < 60; y++ {
		for x := 0; x < 120; x++ {
			input.SetNRGBA(x, y, colors[(y/30)*2+x/60])
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, input); err != nil {
		t.Fatal(err)
	}
	source, err := prepare(root, encoded.Bytes(), assetKindScene)
	if err != nil {
		t.Fatal(err)
	}
	transform := Transform{X: -100, Y: -20, Width: 240, Height: 60, Rotation: 90}
	plan, err := planFixedRotation(source, transform)
	if err != nil {
		t.Fatal(err)
	}
	recipe, hash := fixedRotationRecipe(source, transform, plan)
	derived, err := prepareFixedRotation(context.Background(), root, source, recipe, hash)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(root, "assets", derived.ID, "image.png"))
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := png.Decode(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Bounds().Dx() != 30 || rotated.Bounds().Dy() != 120 {
		t.Fatalf("rotated bitmap dimensions = %v", rotated.Bounds())
	}
	for _, sample := range []struct {
		x, y int
		want color.NRGBA
	}{
		{5, 15, colors[2]}, {24, 15, colors[0]}, {5, 104, colors[3]}, {24, 104, colors[1]},
	} {
		got := color.NRGBAModel.Convert(rotated.At(sample.x, sample.y)).(color.NRGBA)
		if absByte(got.R, sample.want.R) > 4 || absByte(got.G, sample.want.G) > 4 || absByte(got.B, sample.want.B) > 4 || got.A < 250 {
			t.Fatalf("visual orientation changed at %d,%d: got %#v want %#v", sample.x, sample.y, got, sample.want)
		}
	}
	if derived.Provenance == nil || publicAsset(derived).Provenance != nil {
		t.Fatal("derived provenance was not persisted privately")
	}
}

func absByte(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func TestFixedRotationTiledOutputRecordsTransparentTiles(t *testing.T) {
	root := t.TempDir()
	input := image.NewNRGBA(image.Rect(0, 0, 2048, 2048))
	for y := 992; y < 1056; y++ {
		for x := 992; x < 1056; x++ {
			input.SetNRGBA(x, y, color.NRGBA{R: 220, G: 120, B: 40, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, input); err != nil {
		t.Fatal(err)
	}
	source, err := prepare(root, encoded.Bytes(), assetKindScene)
	if err != nil {
		t.Fatal(err)
	}
	transform := Transform{X: -300, Y: -200, Width: 2048, Height: 2048, Rotation: 37}
	plan, err := planFixedRotation(source, transform)
	if err != nil {
		t.Fatal(err)
	}
	recipe, hash := fixedRotationRecipe(source, transform, plan)
	derived, err := prepareFixedRotation(context.Background(), root, source, recipe, hash)
	if err != nil {
		t.Fatal(err)
	}
	if derived.RenderMode != renderModeTiled || derived.TilePresence == "" || !validTilePresence(derived) {
		t.Fatalf("sparse tiled manifest missing: %#v", derived)
	}
	original, err := os.Open(filepath.Join(root, "assets", derived.ID, "original"))
	if err != nil {
		t.Fatal(err)
	}
	raster, err := png.Decode(original)
	original.Close()
	if err != nil {
		t.Fatal(err)
	}
	corner := color.NRGBAModel.Convert(raster.At(0, 0)).(color.NRGBA)
	center := color.NRGBAModel.Convert(raster.At(raster.Bounds().Dx()/2, raster.Bounds().Dy()/2)).(color.NRGBA)
	if corner.A != 0 || center.A == 0 {
		t.Fatalf("derived alpha damaged: corner=%#v center=%#v", corner, center)
	}
	w, h := derived.Width, derived.Height
	missing, present := 0, 0
	for z, encodedLevel := range strings.Split(derived.TilePresence, ".") {
		bits, err := base64.StdEncoding.DecodeString(encodedLevel)
		if err != nil {
			t.Fatal(err)
		}
		nx, ny := (w+511)/512, (h+511)/512
		for y := 0; y < ny; y++ {
			for x := 0; x < nx; x++ {
				index := y*nx + x
				exists := bits[index/8]&(1<<uint(index%8)) != 0
				_, statErr := os.Stat(filepath.Join(root, "assets", derived.ID, strings.Join([]string{strconv.Itoa(z), strconv.Itoa(x), strconv.Itoa(y)}, "_")+".png"))
				if exists {
					present++
					if statErr != nil {
						t.Fatalf("manifest points to missing tile %d/%d/%d: %v", z, x, y, statErr)
					}
				} else {
					missing++
					if !errors.Is(statErr, os.ErrNotExist) {
						t.Fatalf("empty tile exists or stat failed %d/%d/%d: %v", z, x, y, statErr)
					}
				}
			}
		}
		w, h = (w+1)/2, (h+1)/2
	}
	if missing == 0 || present == 0 {
		t.Fatalf("sparse pyramid did not contain both empty and present tiles: missing=%d present=%d", missing, present)
	}
}

func TestRotationReliableCommitRestartAndRetry(t *testing.T) {
	fixture := newRotationFixture(t, Transform{X: -100, Y: -20, Width: 240, Height: 60, Rotation: 90})
	originalPreparer := fixedRotationPreparer
	fixedRotationPreparer = func(ctx context.Context, root string, source Asset, recipe rotationRecipe, hash string) (Asset, error) {
		return fakeRotationAsset(t, root, source, recipe, hash)
	}
	defer func() { fixedRotationPreparer = originalPreparer }()
	command := Command{Type: "elementFixRotation", Client: "editor", Seq: 1, SceneID: fixture.scene.ID, Element: SceneElement{ID: fixture.element.ID}}
	beforeRevision := fixture.scene.Revision
	runRotationCommand(t, fixture, fixture.peer, command)
	ack := receiveRotationAck(t, fixture.peer)
	if issue, _ := ack["error"].(string); issue != "" {
		t.Fatal(issue)
	}
	got := fixture.scene.Elements[fixture.element.ID]
	if got.Transform.Rotation != 0 || got.AssetID == fixture.source.ID || got.Name != fixture.element.Name || got.Opacity != fixture.element.Opacity || got.ZOrder != fixture.element.ZOrder || got.FloorID != fixture.element.FloorID || got.LayerID != fixture.element.LayerID {
		t.Fatalf("rotation commit damaged element state: %#v", got)
	}
	if fixture.scene.Revision != beforeRevision+1 || fixture.session.Receipts["gm:editor"].Seq != 1 {
		t.Fatalf("revision/receipt not committed: revision=%d receipt=%#v", fixture.scene.Revision, fixture.session.Receipts["gm:editor"])
	}
	derived := fixture.session.Assets[got.AssetID]
	if derived.Provenance == nil || derived.Provenance.SourceAssetID != fixture.source.ID || fixture.session.Assets[fixture.source.ID].ID == "" {
		t.Fatalf("derived provenance/source retention missing: %#v", derived)
	}
	writes := fixture.server.writes
	runRotationCommand(t, fixture, fixture.peer, command)
	if duplicate := receiveRotationAck(t, fixture.peer); duplicate["error"] != "" || fixture.server.writes != writes {
		t.Fatalf("completed command was not deduplicated: %#v writes=%d/%d", duplicate, fixture.server.writes, writes)
	}

	restarted := testServer(t, fixture.server.root)
	restartedSession := restarted.sessions[fixture.session.ID]
	restartedScene := restartedSession.Scenes[fixture.scene.ID]
	restartedPeer := &peer{member: restartedSession.Members[fixture.member.ID], session: restartedSession.ID, sceneID: restartedScene.ID, out: make(chan any, 128)}
	restarted.mu.Lock()
	restarted.rotationCommand(restartedSession, restartedPeer, command)
	restarted.mu.Unlock()
	if retry := receiveRotationAck(t, restartedPeer); retry["error"] != "" {
		t.Fatalf("restart forgot receipt: %#v", retry)
	}
	name := "Renamed after restart"
	restarted.mu.Lock()
	restarted.contentCommand(restartedSession, restartedPeer, Command{Type: "elementUpdate", Client: "editor", Seq: 2, SceneID: restartedScene.ID, Element: SceneElement{ID: got.ID}, ElementProperties: ElementProperties{Name: &name}})
	restarted.mu.Unlock()
	if next := receiveRotationAck(t, restartedPeer); next["error"] != "" || restartedScene.Elements[got.ID].Name != name {
		t.Fatalf("next reliable seq rejected after restart: %#v", next)
	}
}

func TestRotationJobRevalidatesRelevantStateAndAllowsUnrelatedEdit(t *testing.T) {
	for _, test := range []struct {
		name       string
		mutate     func(*rotationFixture)
		wantCommit bool
	}{
		{"rename", func(f *rotationFixture) {
			e := f.scene.Elements[f.element.ID]
			e.Name = "Concurrent rename"
			f.scene.Elements[e.ID] = e
			f.scene.Revision++
		}, true},
		{"transform", func(f *rotationFixture) {
			e := f.scene.Elements[f.element.ID]
			e.Transform.X++
			f.scene.Elements[e.ID] = e
			f.scene.Revision++
		}, false},
		{"delete", func(f *rotationFixture) { delete(f.scene.Elements, f.element.ID); f.scene.Revision++ }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRotationFixture(t, Transform{X: 10, Y: 20, Width: 120, Height: 60, Rotation: 37})
			started, release := make(chan struct{}), make(chan struct{})
			originalPreparer := fixedRotationPreparer
			fixedRotationPreparer = func(ctx context.Context, root string, source Asset, recipe rotationRecipe, hash string) (Asset, error) {
				close(started)
				select {
				case <-release:
					return fakeRotationAsset(t, root, source, recipe, hash)
				case <-ctx.Done():
					return Asset{}, ctx.Err()
				}
			}
			defer func() { fixedRotationPreparer = originalPreparer }()
			command := Command{Type: "elementFixRotation", Client: "race", Seq: 1, SceneID: fixture.scene.ID, Element: SceneElement{ID: fixture.element.ID}}
			runRotationCommand(t, fixture, fixture.peer, command)
			<-started
			fixture.server.mu.Lock()
			test.mutate(&fixture)
			fixture.server.mu.Unlock()
			close(release)
			ack := receiveRotationAck(t, fixture.peer)
			issue, _ := ack["error"].(string)
			if test.wantCommit && issue != "" {
				t.Fatalf("unrelated edit invalidated job: %s", issue)
			}
			if !test.wantCommit && issue == "" {
				t.Fatal("stale job committed")
			}
			if !test.wantCommit {
				tracked := false
				for assetID, asset := range fixture.session.Assets {
					if assetID != fixture.source.ID && asset.Provenance != nil && asset.OrphanSince != nil {
						tracked = true
					}
				}
				if !tracked {
					t.Fatal("unused completed result was not tracked as reclaimable")
				}
			}
			if test.name == "rename" {
				got := fixture.scene.Elements[fixture.element.ID]
				if got.Name != "Concurrent rename" || got.Transform.Rotation != 0 {
					t.Fatalf("concurrent rename lost: %#v", got)
				}
			}
		})
	}
}

func TestRotationPendingDuplicateAndGracefulShutdown(t *testing.T) {
	fixture := newRotationFixture(t, Transform{Width: 120, Height: 60, Rotation: 15})
	started := make(chan struct{})
	originalPreparer := fixedRotationPreparer
	fixedRotationPreparer = func(ctx context.Context, _ string, _ Asset, _ rotationRecipe, _ string) (Asset, error) {
		close(started)
		<-ctx.Done()
		return Asset{}, ctx.Err()
	}
	defer func() { fixedRotationPreparer = originalPreparer }()
	command := Command{Type: "elementFixRotation", Client: "shutdown", Seq: 1, SceneID: fixture.scene.ID, Element: SceneElement{ID: fixture.element.ID}}
	runRotationCommand(t, fixture, fixture.peer, command)
	<-started
	second := &peer{member: fixture.member, session: fixture.session.ID, sceneID: fixture.scene.ID, out: make(chan any, 128)}
	runRotationCommand(t, fixture, second, command)
	stopped := make(chan error, 1)
	go func() { stopped <- fixture.server.stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("graceful shutdown did not cancel and join image job")
	}
	if fixture.session.Receipts["gm:shutdown"].Seq != 0 || fixture.scene.Elements[fixture.element.ID].AssetID != fixture.source.ID || len(fixture.server.rotationJobs) != 0 {
		t.Fatal("cancelled job was committed or left pending")
	}
	select {
	case fixture.server.imageJobs <- struct{}{}:
		<-fixture.server.imageJobs
	default:
		t.Fatal("image worker slot leaked")
	}
}

func TestRotationFailureAndNoOpLeaveElementUntouched(t *testing.T) {
	for _, test := range []struct {
		name      string
		rotation  float64
		prepare   func(context.Context, string, Asset, rotationRecipe, string) (Asset, error)
		wantCalls int
	}{
		{"failure", 20, func(context.Context, string, Asset, rotationRecipe, string) (Asset, error) {
			return Asset{}, errors.New("worker failed")
		}, 1},
		{"no-op", 0, func(context.Context, string, Asset, rotationRecipe, string) (Asset, error) {
			return Asset{}, errors.New("must not run")
		}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRotationFixture(t, Transform{Width: 120, Height: 60, Rotation: test.rotation})
			calls := 0
			originalPreparer := fixedRotationPreparer
			fixedRotationPreparer = func(ctx context.Context, root string, source Asset, recipe rotationRecipe, hash string) (Asset, error) {
				calls++
				return test.prepare(ctx, root, source, recipe, hash)
			}
			defer func() { fixedRotationPreparer = originalPreparer }()
			revision := fixture.scene.Revision
			runRotationCommand(t, fixture, fixture.peer, Command{Type: "elementFixRotation", Client: "failure", Seq: 1, SceneID: fixture.scene.ID, Element: SceneElement{ID: fixture.element.ID}})
			ack := receiveRotationAck(t, fixture.peer)
			if calls != test.wantCalls || fixture.scene.Revision != revision || fixture.scene.Elements[fixture.element.ID] != fixture.element || len(fixture.session.Assets) != 1 {
				t.Fatalf("failed/no-op job changed state: calls=%d ack=%#v element=%#v", calls, ack, fixture.scene.Elements[fixture.element.ID])
			}
			if test.rotation != 0 && ack["error"] == "" {
				t.Fatal("processing failure was acknowledged as success")
			}
		})
	}
}
