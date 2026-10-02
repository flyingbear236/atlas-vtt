package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pipelinePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), 150, uint8(30 + (x+y)%220)})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func assertNoPartialAssets(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial assets remain: %v", entries)
	}
}

func TestStreamingPipelineAlphaAndTokenSize(t *testing.T) {
	data := pipelinePNG(t, 32, 16)
	want, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	prepared := map[string]Asset{}
	for _, kind := range []string{"map", "token", "avatar"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			a, err := prepare(root, data, kind)
			if err != nil {
				t.Fatal(err)
			}
			if a.RenderMode != "bitmap" {
				t.Fatalf("small %s chose %q instead of bitmap", kind, a.RenderMode)
			}
			if a.SourceID == "" || a.RepresentationVersion != representationVersion {
				t.Fatalf("missing immutable representation identity: %#v", a)
			}
			if kind == "map" && a.Kind != assetKindScene {
				t.Fatalf("legacy map upload was not normalized to scene raster: %q", a.Kind)
			}
			prepared[kind] = a
			name := "0_0_0.png"
			if kind == "map" {
				name = "image.png"
			}
			if kind == "token" {
				name = "token.png"
			}
			if kind == "avatar" {
				name = "avatar.png"
			}
			f, err := os.Open(filepath.Join(root, "assets", a.ID, name))
			if err != nil {
				t.Fatal(err)
			}
			got, err := png.Decode(f)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if got.Bounds() != want.Bounds() {
				t.Fatal("small image was resized")
			}
			for y := 0; y < 16; y++ {
				for x := 0; x < 32; x++ {
					if color.NRGBAModel.Convert(got.At(x, y)) != color.NRGBAModel.Convert(want.At(x, y)) {
						t.Fatalf("alpha/pixel changed at %d,%d", x, y)
					}
				}
			}
		})
	}
	if prepared["map"].SourceID != prepared["token"].SourceID || prepared["map"].ID == prepared["token"].ID {
		t.Fatal("source and prepared representation identities were not separated")
	}
}

func TestAvatarPipelinePNGJPEGLargeAndNoOriginal(t *testing.T) {
	inputs := map[string][]byte{"png": pipelinePNG(t, 48, 32)}
	large := image.NewGray(image.Rect(0, 0, 5000, 4000))
	var jpegBody bytes.Buffer
	if err := jpeg.Encode(&jpegBody, large, &jpeg.Options{Quality: 70}); err != nil {
		t.Fatal(err)
	}
	inputs["large-jpeg"] = jpegBody.Bytes()
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			asset, err := prepare(root, input, assetKindAvatar)
			if err != nil {
				t.Fatal(err)
			}
			if asset.Kind != assetKindAvatar || asset.RenderMode != renderModeBitmap || asset.MimeType != "image/png" {
				t.Fatalf("unexpected avatar metadata: %#v", asset)
			}
			path := filepath.Join(root, "assets", asset.ID)
			entries, err := os.ReadDir(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 {
				t.Fatalf("avatar representation retained extra files: %v", entries)
			}
			file, err := os.Open(filepath.Join(path, "avatar.png"))
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := png.Decode(file)
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Bounds().Dx() > maxAvatarOutputSide || decoded.Bounds().Dy() > maxAvatarOutputSide {
				t.Fatalf("avatar output exceeds %d: %v", maxAvatarOutputSide, decoded.Bounds())
			}
			if _, err := os.Stat(filepath.Join(path, "original")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("avatar original was retained: %v", err)
			}
		})
	}
}

func TestRepresentationPolicyThreshold(t *testing.T) {
	for _, tc := range []struct {
		name       string
		width      int
		height     int
		mode       string
		decodedMiB int64
	}{
		{"512 square", 512, 512, renderModeBitmap, 1},
		{"1024 square", 1024, 1024, renderModeBitmap, 4},
		{"2048 square", 2048, 2048, renderModeTiled, 16},
		{"4096 square", 4096, 4096, renderModeTiled, 64},
		{"target battlemap", 11220, 11516, renderModeTiled, 492},
		{"long narrow bitmap", 8192, 128, renderModeBitmap, 4},
		{"long narrow dimension guard", 9000, 128, renderModeTiled, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := representationPolicy(rasterMetadata{Width: tc.width, Height: tc.height})
			if plan.Mode != tc.mode || plan.PixelCount != int64(tc.width)*int64(tc.height) || plan.EstimatedDecodedBytes/(1<<20) != tc.decodedMiB {
				t.Fatalf("policy(%dx%d) = %#v, want mode=%s decoded=%d MiB", tc.width, tc.height, plan, tc.mode, tc.decodedMiB)
			}
		})
	}
}

func TestRepresentationIdentityIncludesRoleModeAndVersion(t *testing.T) {
	const source = "source-hash"
	base := representationID(source, assetKindScene, representationVersion, renderModeBitmap)
	for name, candidate := range map[string]string{
		"role":    representationID(source, assetKindToken, representationVersion, renderModeBitmap),
		"mode":    representationID(source, assetKindScene, representationVersion, renderModeTiled),
		"version": representationID(source, assetKindScene, representationVersion+"-next", renderModeBitmap),
	} {
		if candidate == base {
			t.Fatalf("%s did not change immutable representation ID", name)
		}
	}
}

func TestPreparedRepresentationIsReusedForSameSource(t *testing.T) {
	root := t.TempDir()
	input := pipelinePNG(t, 128, 96)
	workerCalls := 0
	imageWorkerObserver = func(*exec.Cmd) func() {
		workerCalls++
		return nil
	}
	defer func() { imageWorkerObserver = nil }()
	first, err := prepare(root, input, assetKindScene)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepare(root, input, assetKindScene)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.SourceID != second.SourceID || workerCalls != 1 {
		t.Fatalf("prepared representation was duplicated: first=%#v second=%#v workers=%d", first, second, workerCalls)
	}
}

func TestStreamingPipelineFailureCleanup(t *testing.T) {
	data := pipelinePNG(t, 64, 64)
	t.Run("missing worker", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("ATLAS_VIPS", filepath.Join(root, "missing-vips.exe"))
		if _, err := prepare(root, data, "map"); !errors.Is(err, errVipsMissing) {
			t.Fatalf("missing worker: %v", err)
		}
		assertNoPartialAssets(t, root)
	})
	t.Run("truncated input", func(t *testing.T) {
		root := t.TempDir()
		if _, err := prepare(root, data[:50], "map"); err == nil {
			t.Fatal("truncated PNG accepted")
		}
		assertNoPartialAssets(t, root)
	})
	t.Run("cancel running worker", func(t *testing.T) {
		root := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		observed := false
		imageWorkerObserver = func(cmd *exec.Cmd) func() { cancel(); return func() { observed = cmd.ProcessState != nil } }
		defer func() { imageWorkerObserver = nil }()
		if _, err := prepareReader(ctx, root, bytes.NewReader(data), "map"); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
		if !observed {
			t.Fatal("worker not reaped")
		}
		assertNoPartialAssets(t, root)
	})
}

func TestUploadSizeLimits(t *testing.T) {
	if uploadLimit != 256<<20 {
		t.Fatalf("upload limit = %d, want 256 MiB", uploadLimit)
	}
	for _, tc := range []struct {
		name    string
		size    int64
		wantErr bool
	}{
		{"above old limit", 65 << 20, false},
		{"at new limit", 256 << 20, false},
		{"above new limit", (256 << 20) + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUploadSize(tc.size)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateUploadSize(%d) = %v, wantErr=%v", tc.size, err, tc.wantErr)
			}
		})
	}
}

func TestAvatarInputLimitsBeforeWorker(t *testing.T) {
	if avatarUploadLimit != 10<<20 || maxAvatarPixels != 25_000_000 || maxAvatarSide != 8192 || maxAvatarOutputSide != 512 {
		t.Fatal("avatar limits diverged from A5")
	}
	if validateUploadSizeForKind(assetKindAvatar, avatarUploadLimit) != nil || !errors.Is(validateUploadSizeForKind(assetKindAvatar, avatarUploadLimit+1), errAvatarUploadSize) {
		t.Fatal("avatar byte limit is not inclusive")
	}
	for _, size := range [][2]uint32{{8193, 1}, {5001, 5000}} {
		root := t.TempDir()
		data := pipelinePNG(t, 2, 2)
		binary.BigEndian.PutUint32(data[16:20], size[0])
		binary.BigEndian.PutUint32(data[20:24], size[1])
		binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
		if _, err := prepare(root, data, assetKindAvatar); err == nil {
			t.Fatalf("avatar dimensions %dx%d accepted", size[0], size[1])
		}
		assertNoPartialAssets(t, root)
	}
}

func TestAvatarPipelineCancellationCleanup(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	observed := false
	imageWorkerObserver = func(cmd *exec.Cmd) func() { cancel(); return func() { observed = cmd.ProcessState != nil } }
	defer func() { imageWorkerObserver = nil }()
	if _, err := prepareReader(ctx, root, bytes.NewReader(pipelinePNG(t, 64, 64)), assetKindAvatar); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel avatar: %v", err)
	}
	if !observed {
		t.Fatal("avatar worker was not reaped")
	}
	assertNoPartialAssets(t, root)
}

func TestMapDimensionLimitsIncludeLargeBattlemapCornerCase(t *testing.T) {
	if got := int64(11220) * 11516; got != 129_209_520 {
		t.Fatalf("corner-case pixel count changed: %d", got)
	}
	for _, tc := range []struct {
		name    string
		w, h    int
		wantErr bool
	}{
		{"hubble-sized map", 11220, 11516, false},
		{"exactly 150 MP", 15000, 10000, false},
		{"above 150 MP", 15001, 10000, true},
		{"side over limit", 32769, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateImageDimensions("map", tc.w, tc.h)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateImageDimensions(map, %d, %d) = %v, wantErr=%v", tc.w, tc.h, err, tc.wantErr)
			}
		})
	}
}

func TestStreamingPipelineKeepsMapLimitsBeforeWorker(t *testing.T) {
	for _, size := range [][2]uint32{{15001, 10000}, {32769, 1}} {
		root := t.TempDir()
		data := pipelinePNG(t, 2, 2)
		binary.BigEndian.PutUint32(data[16:20], size[0])
		binary.BigEndian.PutUint32(data[20:24], size[1])
		binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
		if _, err := prepare(root, data, "map"); err == nil {
			t.Fatal("map limit exceeded")
		}
		assertNoPartialAssets(t, root)
	}
}

func TestFrontendUploadLimitMatchesBackend(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("web", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	const frontendLimit = "const UPLOAD_LIMIT_BYTES=256*1024*1024;"
	if !strings.Contains(string(b), frontendLimit) {
		t.Fatalf("frontend upload limit does not match backend %d bytes", uploadLimit)
	}
}

func TestProfileRepresentationPolicy(t *testing.T) {
	if os.Getenv("ATLAS_REPRESENTATION_PROFILE") == "" {
		t.Skip("set ATLAS_REPRESENTATION_PROFILE=1 for representation measurements")
	}
	tool, err := findVips()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, spec := range []struct {
		name          string
		width, height int
		compareBoth   bool
	}{
		{"square-512", 512, 512, true},
		{"square-1024", 1024, 1024, true},
		{"square-2048", 2048, 2048, true},
		{"square-4096", 4096, 4096, true},
		{"long-8192x128", 8192, 128, true},
		{"target-11220x11516", 11220, 11516, false},
	} {
		input := filepath.Join(root, spec.name+".png")
		if err := profilePNG(input, spec.width, spec.height, 8); err != nil {
			t.Fatal(err)
		}
		plan := representationPolicy(rasterMetadata{Width: spec.width, Height: spec.height})
		modes := []string{plan.Mode}
		if spec.compareBoth {
			modes = []string{renderModeBitmap, renderModeTiled}
		}
		for _, mode := range modes {
			stage, err := os.MkdirTemp(root, ".representation-*")
			if err != nil {
				t.Fatal(err)
			}
			asset := Asset{Kind: assetKindScene, Width: spec.width, Height: spec.height, RenderMode: mode}
			started := time.Now()
			err = prepareRepresentationFiles(context.Background(), tool, stage, input, &asset)
			elapsed := time.Since(started)
			var files int
			var bytes int64
			if err == nil {
				err = filepath.WalkDir(stage, func(path string, entry os.DirEntry, walkErr error) error {
					if walkErr != nil {
						return walkErr
					}
					if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".png") {
						info, infoErr := entry.Info()
						if infoErr != nil {
							return infoErr
						}
						files++
						bytes += info.Size()
					}
					return nil
				})
			}
			os.RemoveAll(stage)
			if err != nil {
				t.Fatalf("%s/%s: %v", spec.name, mode, err)
			}
			t.Logf("%s mode=%s policy=%s decoded=%.1fMiB prepare=%s files=%d bytes=%d", spec.name, mode, plan.Mode, float64(plan.EstimatedDecodedBytes)/(1<<20), elapsed.Round(time.Millisecond), files, bytes)
		}
		os.Remove(input)
	}
}
