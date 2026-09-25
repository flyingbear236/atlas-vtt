package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	uploadLimit    int64 = 256 << 20
	maxMapPixels   int64 = 150_000_000
	maxMapSide           = 32768
	maxTokenPixels int64 = 25_000_000
	maxTokenSide         = 8192
	// A bitmap may consume at most one seventh of the smallest decoded-asset
	// budget (56 MiB after the token-artwork reserve). Larger rasters use LOD.
	bitmapSceneDecodedBytes int64 = 8 << 20
	bitmapSceneMaxSide            = 8192
	representationVersion         = "raster-v2-png-tile512"
)

type rasterMetadata struct {
	Width  int
	Height int
}

type representationPlan struct {
	Mode                  string
	PixelCount            int64
	EstimatedDecodedBytes int64
}

// representationPolicy is the single policy for every raster SceneElement.
// Compressed size is intentionally absent: decoded memory and dimensions are
// what determine whether viewport-local LOD is useful.
func representationPolicy(metadata rasterMetadata) representationPlan {
	pixels := int64(metadata.Width) * int64(metadata.Height)
	decoded := pixels * 4
	mode := renderModeBitmap
	if decoded > bitmapSceneDecodedBytes || metadata.Width > bitmapSceneMaxSide || metadata.Height > bitmapSceneMaxSide {
		mode = renderModeTiled
	}
	return representationPlan{Mode: mode, PixelCount: pixels, EstimatedDecodedBytes: decoded}
}

func representationID(sourceID, kind, version, mode string) string {
	hash := sha256.New()
	for _, value := range []string{"atlas-representation", sourceID, kind, version, mode} {
		io.WriteString(hash, value)
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

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
	var ok bool
	if kind, ok = canonicalAssetKind(kind); !ok {
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
	sourceHash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, sourceHash), io.LimitReader(contextReader{ctx, input}, uploadLimit+1))
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
	sourceID := hex.EncodeToString(sourceHash.Sum(nil))
	mimeType := "image/png"
	filename := "image.png"
	if format == "jpeg" {
		mimeType = "image/jpeg"
		filename = "image.jpg"
	}
	renderMode := renderModeBitmap
	if kind == assetKindScene {
		renderMode = representationPolicy(rasterMetadata{Width: cfg.Width, Height: cfg.Height}).Mode
	}
	a := Asset{SourceID: sourceID, RepresentationVersion: representationVersion, Filename: filename, MimeType: mimeType, Width: cfg.Width, Height: cfg.Height, Size: n, Kind: kind, RenderMode: renderMode, RetentionPolicy: assetReclaimable, CreatedAt: time.Now().Unix()}
	a.ID = representationID(a.SourceID, a.Kind, a.RepresentationVersion, a.RenderMode)
	dest := filepath.Join(assets, a.ID)
	if metadata, e := os.ReadFile(filepath.Join(dest, "meta.json")); e == nil {
		var cached Asset
		if json.Unmarshal(metadata, &cached) != nil || cached.ID != a.ID || cached.SourceID != a.SourceID || cached.RepresentationVersion != a.RepresentationVersion || cached.Width != a.Width || cached.Height != a.Height || cached.Kind != a.Kind || cached.RenderMode != a.RenderMode {
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
	err = prepareRepresentationFiles(ctx, tool, stage, original, &a)
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

func prepareRepresentationFiles(ctx context.Context, tool, stage, original string, asset *Asset) error {
	return prepareRepresentationFilesWithOptions(ctx, tool, stage, original, asset, false)
}

func prepareRepresentationFilesWithOptions(ctx context.Context, tool, stage, original string, asset *Asset, sparse bool) error {
	if asset.RenderMode == renderModeTiled {
		prefix := filepath.Join(stage, "pyramid")
		skipBlanks := "-1"
		if sparse {
			skipBlanks = "0"
		}
		args := []string{"dzsave", original + "[access=sequential,fail-on=error]", prefix,
			"--tile-size=512", "--overlap=0", "--depth=onetile", "--suffix=.png[compression=6]", "--keep=none", "--skip-blanks=" + skipBlanks}
		if sparse {
			args = append(args, "--background", "0 0 0 0")
		}
		err := runVips(ctx, tool, stage, args...)
		if err != nil {
			return err
		}
		asset.Levels, asset.TilePresence, err = flattenPyramidFiles(stage, asset.Width, asset.Height, sparse)
		return err
	}
	if asset.Kind == assetKindToken {
		w, h := asset.Width, asset.Height
		for w > 512 || h > 512 {
			w = (w + 1) / 2
			h = (h + 1) / 2
		}
		return runVips(ctx, tool, stage, "thumbnail", original, filepath.Join(stage, "token.png")+"[compression=6,keep=none]", strconv.Itoa(w), "--height="+strconv.Itoa(h), "--size=down", "--no-rotate", "--fail-on=error")
	}
	return runVips(ctx, tool, stage, "thumbnail", original, filepath.Join(stage, "image.png")+"[compression=6,keep=none]", strconv.Itoa(asset.Width), "--height="+strconv.Itoa(asset.Height), "--size=down", "--no-rotate", "--fail-on=error")
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
	case assetKindToken:
		if width > maxTokenSide || height > maxTokenSide || int64(width)*int64(height) > maxTokenPixels {
			return errors.New("Лимит изображения токена: 25 млн пикселей, сторона до 8192 px")
		}
	case assetKindScene, assetKindLegacyMap:
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
	levels, _, err := flattenPyramidFiles(stage, width, height, false)
	return levels, err
}

func flattenPyramidFiles(stage string, width, height int, sparse bool) (int, string, error) {
	levels := 1
	for w, h := width, height; w > 512 || h > 512; {
		levels++
		w = (w + 1) / 2
		h = (h + 1) / 2
	}
	w, h := width, height
	presence := make([]string, levels)
	for z := 0; z < levels; z++ {
		nx, ny := (w+511)/512, (h+511)/512
		bits := make([]byte, (nx*ny+7)/8)
		for y := 0; y < h; y += 512 {
			for x := 0; x < w; x += 512 {
				from := filepath.Join(stage, "pyramid_files", strconv.Itoa(levels-1-z), fmt.Sprintf("%d_%d.png", x/512, y/512))
				f, err := os.Open(from)
				if err != nil {
					if sparse && errors.Is(err, os.ErrNotExist) {
						continue
					}
					return 0, "", err
				}
				cfg, _, err := image.DecodeConfig(f)
				f.Close()
				if err != nil {
					return 0, "", err
				}
				if cfg.Width != min(512, w-x) || cfg.Height != min(512, h-y) {
					return 0, "", errors.New("libvips: неверный размер тайла")
				}
				if err = os.Rename(from, filepath.Join(stage, fmt.Sprintf("%d_%d_%d.png", z, x/512, y/512))); err != nil {
					return 0, "", err
				}
				index := (y/512)*nx + x/512
				bits[index/8] |= 1 << uint(index%8)
			}
		}
		if sparse {
			presence[z] = base64.StdEncoding.EncodeToString(bits)
		}
		w = (w + 1) / 2
		h = (h + 1) / 2
	}
	if err := os.RemoveAll(filepath.Join(stage, "pyramid_files")); err != nil {
		return 0, "", err
	}
	if err := os.Remove(filepath.Join(stage, "pyramid.dzi")); err != nil {
		return 0, "", err
	}
	if !sparse {
		return levels, "", nil
	}
	return levels, strings.Join(presence, "."), nil
}

func validTilePresence(asset Asset) bool {
	if asset.TilePresence == "" {
		return true
	}
	presence := strings.Split(asset.TilePresence, ".")
	if asset.RenderMode != renderModeTiled || len(presence) != asset.Levels {
		return false
	}
	w, h := asset.Width, asset.Height
	for z, encoded := range presence {
		bits, err := base64.StdEncoding.DecodeString(encoded)
		nx, ny := (w+511)/512, (h+511)/512
		if err != nil || len(bits) != (nx*ny+7)/8 {
			return false
		}
		if z+1 < asset.Levels {
			w, h = (w+1)/2, (h+1)/2
		}
	}
	return true
}
