package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	uploadLimit       int64 = 256 << 20
	maxMapPixels      int64 = 150_000_000
	maxMapSide              = 32768
	maxTokenPixels    int64 = 25_000_000
	maxTokenSide            = 8192
	bitmapSceneSide         = 1024
	bitmapScenePixels int64 = 1_048_576
)

var errUploadSize = errors.New("Максимальный размер файла: 256 МиБ")
var errVipsMissing = errors.New("Не найден libvips: выполните scripts/install-vips.ps1 или задайте ATLAS_VIPS")

// Tests can observe the separate worker without adding public diagnostic routes.
var imageWorkerObserver func(*exec.Cmd) func()

func prepare(root string, data []byte, kind string) (Asset, error) {
	return prepareReader(context.Background(), root, bytes.NewReader(data), kind)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// Compressed input goes straight to disk. Neither it nor a complete decoded
// map is retained by Go. Only a completed pyramid is published under its hash.
func prepareReader(ctx context.Context, root string, input io.Reader, kind string) (Asset, error) {
	if kind != "map" && kind != "token" {
		return Asset{}, errors.New("Неизвестный тип изображения")
	}
	assets, err := filepath.Abs(filepath.Join(root, "assets"))
	if err != nil {
		return Asset{}, err
	}
	if err = os.MkdirAll(assets, 0755); err != nil {
		return Asset{}, err
	}
	f, err := os.CreateTemp(assets, ".upload-*")
	if err != nil {
		return Asset{}, err
	}
	uploadPath := f.Name()
	defer os.Remove(uploadPath)
	hash := sha256.New()
	io.WriteString(hash, kind)
	n, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(contextReader{ctx, input}, uploadLimit+1))
	closeErr := f.Close()
	if err = validateUploadSize(n); err != nil {
		return Asset{}, err
	}
	if copyErr != nil {
		return Asset{}, copyErr
	}
	if closeErr != nil {
		return Asset{}, closeErr
	}
	if err = ctx.Err(); err != nil {
		return Asset{}, err
	}
	f, err = os.Open(uploadPath)
	if err != nil {
		return Asset{}, err
	}
	cfg, format, decodeErr := image.DecodeConfig(f)
	f.Close()
	if decodeErr != nil || (format != "png" && format != "jpeg") {
		return Asset{}, errors.New("Поддерживаются PNG и JPEG")
	}
	if err = validateImageDimensions(kind, cfg.Width, cfg.Height); err != nil {
		return Asset{}, err
	}
	mimeType := "image/png"
	filename := "image.png"
	if format == "jpeg" {
		mimeType = "image/jpeg"
		filename = "image.jpg"
	}
	renderMode := "bitmap"
	if kind == "map" && (cfg.Width > bitmapSceneSide || cfg.Height > bitmapSceneSide || int64(cfg.Width)*int64(cfg.Height) > bitmapScenePixels) {
		renderMode = "tiled"
	}
	a := Asset{ID: hex.EncodeToString(hash.Sum(nil)), Filename: filename, MimeType: mimeType, Width: cfg.Width, Height: cfg.Height, Size: n, Kind: kind, RenderMode: renderMode, RetentionPolicy: assetReclaimable, CreatedAt: time.Now().Unix()}
	dest := filepath.Join(assets, a.ID)
	if metadata, e := os.ReadFile(filepath.Join(dest, "meta.json")); e == nil {
		var cached Asset
		if json.Unmarshal(metadata, &cached) != nil || cached.ID != a.ID || cached.Width != a.Width || cached.Height != a.Height || cached.Kind != a.Kind {
			return a, errors.New("Повреждены метаданные ассета")
		}
		if cached.RetentionPolicy == "" {
			cached.RetentionPolicy = assetReclaimable
		}
		if cached.MimeType == "" {
			cached.MimeType = mimeType
		}
		if cached.Filename == "" {
			cached.Filename = filename
		}
		if cached.Size == 0 {
			cached.Size = n
		}
		if cached.RenderMode == "" {
			if cached.Kind == "map" && cached.Levels > 0 {
				cached.RenderMode = "tiled"
			} else {
				cached.RenderMode = "bitmap"
			}
		}
		if err = ctx.Err(); err != nil {
			return a, err
		}
		return cached, nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return a, e
	}
	tool, err := findVips()
	if err != nil {
		return a, err
	}
	stage, err := os.MkdirTemp(assets, ".prepare-*")
	if err != nil {
		return a, err
	}
	defer os.RemoveAll(stage)
	original := filepath.Join(stage, "original")
	if err = os.Rename(uploadPath, original); err != nil {
		return a, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if a.RenderMode == "tiled" {
		prefix := filepath.Join(stage, "pyramid")
		err = runVips(ctx, tool, stage, "dzsave", original+"[access=sequential,fail-on=error]", prefix,
			"--tile-size=512", "--overlap=0", "--depth=onetile", "--suffix=.png[compression=6]", "--keep=none", "--skip-blanks=-1")
		if err == nil {
			a.Levels, err = flattenPyramid(stage, cfg.Width, cfg.Height)
		}
	} else if kind == "token" {
		w, h := cfg.Width, cfg.Height
		for w > 512 || h > 512 {
			w = (w + 1) / 2
			h = (h + 1) / 2
		}
		err = runVips(ctx, tool, stage, "thumbnail", original, filepath.Join(stage, "token.png")+"[compression=6,keep=none]", strconv.Itoa(w), "--height="+strconv.Itoa(h), "--size=down", "--no-rotate", "--fail-on=error")
	} else {
		err = runVips(ctx, tool, stage, "thumbnail", original, filepath.Join(stage, "image.png")+"[compression=6,keep=none]", strconv.Itoa(cfg.Width), "--height="+strconv.Itoa(cfg.Height), "--size=down", "--no-rotate", "--fail-on=error")
	}
	if err != nil {
		return a, err
	}
	if err = ctx.Err(); err != nil {
		return a, err
	}
	metadata, _ := json.Marshal(a)
	if err = os.WriteFile(filepath.Join(stage, "meta.json"), metadata, 0600); err != nil {
		return a, err
	}
	if err = os.Rename(stage, dest); err != nil {
		return a, fmt.Errorf("Не удалось опубликовать ассет: %w", err)
	}
	return a, nil
}

func validateUploadSize(size int64) error {
	if size > uploadLimit {
		return errUploadSize
	}
	return nil
}

func validateImageDimensions(kind string, width, height int) error {
	if width < 1 || height < 1 {
		return errors.New("Пустое изображение")
	}
	switch kind {
	case "token":
		if width > maxTokenSide || height > maxTokenSide || int64(width)*int64(height) > maxTokenPixels {
			return errors.New("Лимит изображения токена: 25 млн пикселей, сторона до 8192 px")
		}
	case "map":
		if width > maxMapSide || height > maxMapSide || int64(width)*int64(height) > maxMapPixels {
			return errors.New("Лимит карты: 150 млн пикселей, сторона до 32768 px")
		}
	default:
		return errors.New("Неизвестный тип изображения")
	}
	return nil
}

func findVips() (string, error) {
	if configured := os.Getenv("ATLAS_VIPS"); configured != "" {
		p, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("%w: %s", errVipsMissing, configured)
		}
		return p, nil
	}
	name := "vips"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var candidates []string
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "vips", "bin", name))
	}
	candidates = append(candidates, filepath.Join(".deps", "libvips-8.18.6", "vips-dev-8.18", "bin", name))
	for _, candidate := range candidates {
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return filepath.Abs(candidate)
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", errVipsMissing
}

// libvips uses sequential scanline/tile evaluation. The disk threshold also
// handles loaders requiring random access. Its cache limit is NOT an RSS cap.
func runVips(ctx context.Context, tool, temp string, args ...string) error {
	args = append(args, "--vips-concurrency=1", "--vips-cache-max=0", "--vips-cache-max-memory=16777216", "--vips-cache-max-files=16", "--vips-disc-threshold=16777216")
	cmd := exec.CommandContext(ctx, tool, args...)
	configureImageProcess(cmd)
	cmd.Env = append(os.Environ(), "TMPDIR="+temp, "TEMP="+temp, "TMP="+temp)
	var output limitedOutput
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("Запуск libvips: %w", err)
	}
	var observed func()
	if imageWorkerObserver != nil {
		observed = imageWorkerObserver(cmd)
	}
	err := cmd.Wait()
	if observed != nil {
		observed()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("libvips: %w: %s", err, strings.TrimSpace(output.String()))
	}
	return nil
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if left := 8192 - b.Len(); left > 0 {
		b.Buffer.Write(p[:min(left, n)])
	}
	return n, nil
}

func flattenPyramid(stage string, width, height int) (int, error) {
	levels := 1
	for w, h := width, height; w > 512 || h > 512; {
		levels++
		w = (w + 1) / 2
		h = (h + 1) / 2
	}
	w, h := width, height
	for z := 0; z < levels; z++ {
		for y := 0; y < h; y += 512 {
			for x := 0; x < w; x += 512 {
				from := filepath.Join(stage, "pyramid_files", strconv.Itoa(levels-1-z), fmt.Sprintf("%d_%d.png", x/512, y/512))
				f, err := os.Open(from)
				if err != nil {
					return 0, err
				}
				cfg, _, err := image.DecodeConfig(f)
				f.Close()
				if err != nil {
					return 0, err
				}
				if cfg.Width != min(512, w-x) || cfg.Height != min(512, h-y) {
					return 0, errors.New("libvips: неверный размер тайла")
				}
				if err = os.Rename(from, filepath.Join(stage, fmt.Sprintf("%d_%d_%d.png", z, x/512, y/512))); err != nil {
					return 0, err
				}
			}
		}
		w = (w + 1) / 2
		h = (h + 1) / 2
	}
	if err := os.RemoveAll(filepath.Join(stage, "pyramid_files")); err != nil {
		return 0, err
	}
	if err := os.Remove(filepath.Join(stage, "pyramid.dzi")); err != nil {
		return 0, err
	}
	return levels, nil
}
