package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	for _, kind := range []string{"map", "token"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			a, err := prepare(root, data, kind)
			if err != nil {
				t.Fatal(err)
			}
			if a.RenderMode != "bitmap" {
				t.Fatalf("small %s chose %q instead of bitmap", kind, a.RenderMode)
			}
			name := "0_0_0.png"
			if kind == "map" {
				name = "image.png"
			}
			if kind == "token" {
				name = "token.png"
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
