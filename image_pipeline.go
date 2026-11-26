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
	"image/gif"
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
	uploadLimit         int64 = 256 << 20
	maxMapPixels        int64 = 150_000_000
	maxMapSide                = 32768
	maxTokenPixels      int64 = 25_000_000
	maxTokenSide              = 8192
	avatarUploadLimit   int64 = 10 << 20
	maxAvatarPixels     int64 = 25_000_000
	maxAvatarSide             = 8192
	maxAvatarOutputSide       = 512
	// A bitmap may consume at most one seventh of the smallest decoded-asset
	// budget (56 MiB after the token-artwork reserve). Larger rasters use LOD.
	bitmapSceneDecodedBytes        int64 = 8 << 20
	bitmapSceneMaxSide                   = 8192
	representationVersion                = "raster-v2-png-tile512"
	universalRepresentationVersion       = "raster-v3-shared-png-tile512"
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
var errAvatarUploadSize = errors.New("Максимальный размер avatar: 10 МиБ")
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
	return prepareRasterReader(ctx, root, input, kind, false)
}

// The GM catalog accepts the additional raster formats supported by the installed
// libvips build. Existing scene/token/avatar upload contracts remain unchanged.
func prepareCampaignMediaReader(ctx context.Context, root string, input io.Reader) (Asset, error) {
	return prepareRasterReader(ctx, root, input, assetKindScene, true)
}
func prepareRasterReader(ctx context.Context, root string, input io.Reader, kind string, mediaFormats bool) (Asset, error) {
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
	limit := uploadLimitForKind(kind)
	n, copyErr := io.Copy(io.MultiWriter(f, sourceHash), io.LimitReader(contextReader{ctx, input}, limit+1))
	closeErr := f.Close()
	if err = validateUploadSizeForKind(kind, n); err != nil {
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
		if !mediaFormats {
			return Asset{}, errors.New("Поддерживаются PNG и JPEG")
		}
		cfg, format, decodeErr = readAdditionalMediaConfig(ctx, uploadPath, assets)
		if decodeErr != nil {
			return Asset{}, decodeErr
		}
	}
	if err = validateImageDimensions(kind, cfg.Width, cfg.Height); err != nil {
		return Asset{}, err
	}
	sourceID := hex.EncodeToString(sourceHash.Sum(nil))
	mimeType := "image/png"
	filename := "image.png"
	switch format {
	case "jpeg":
		mimeType, filename = "image/jpeg", "image.jpg"
	case "webp", "gif", "avif", "bmp":
		mimeType, filename = "image/"+format, "image."+format
	}
	if kind == assetKindAvatar {
		mimeType = "image/png"
		filename = "avatar.png"
	}
	renderMode := renderModeBitmap
	if kind == assetKindScene {
		renderMode = representationPolicy(rasterMetadata{Width: cfg.Width, Height: cfg.Height}).Mode
	}
	a := Asset{SourceID: sourceID, RepresentationVersion: representationVersion, Filename: filename, MimeType: mimeType, Width: cfg.Width, Height: cfg.Height, Size: n, Kind: kind, RenderMode: renderMode, RetentionPolicy: assetReclaimable, CreatedAt: time.Now().Unix()}
	if mediaFormats {
		a.RepresentationVersion = universalRepresentationVersion
		if a.RenderMode == renderModeTiled {
			a.Levels = mediaPyramidLevels(a.Width, a.Height)
		}
	}
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
	if mediaFormats {
		err = generateMediaThumbnail(ctx, tool, stage, original, "media-thumb.png", 192)
	} else {
		err = prepareRepresentationFiles(ctx, tool, stage, original, &a)
	}
	if err != nil {
		return a, err
	}
	if err = ctx.Err(); err != nil {
		return a, err
	}
	if kind == assetKindAvatar {
		if err = os.Remove(original); err != nil && !errors.Is(err, os.ErrNotExist) {
			return a, err
		}
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

// Magic allowlist prevents libvips from being invoked on arbitrary document
// loaders (SVG, PDF, etc.). Dimensions are checked before full conversion.
func additionalMediaFormat(header []byte) string {
	if len(header) >= 6 && (string(header[:6]) == "GIF87a" || string(header[:6]) == "GIF89a") {
		return "gif"
	}
	if len(header) >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP" {
		return "webp"
	}
	if len(header) >= 2 && string(header[:2]) == "BM" {
		return "bmp"
	}
	if len(header) >= 16 && string(header[4:8]) == "ftyp" {
		if brand := string(header[8:12]); brand == "avif" || brand == "avis" {
			return "avif"
		}
		for i := 16; i+4 <= len(header); i += 4 {
			if brand := string(header[i : i+4]); brand == "avif" || brand == "avis" {
				return "avif"
			}
		}
	}
	return ""
}
func readAdditionalMediaConfig(ctx context.Context, filename, temp string) (image.Config, string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return image.Config{}, "", err
	}
	defer f.Close()
	var header [128]byte
	n, _ := io.ReadFull(f, header[:])
	format := additionalMediaFormat(header[:n])
	if format == "" {
		return image.Config{}, "", errors.New("Поддерживаются PNG, JPEG, WebP, GIF, AVIF и BMP")
	}
	if format == "gif" {
		if _, err = f.Seek(0, 0); err != nil {
			return image.Config{}, "", err
		}
		cfg, err := gif.DecodeConfig(f)
		return cfg, format, err
	}
	tool, err := findVips()
	if err != nil {
		return image.Config{}, "", err
	}
	name := "vipsheader"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	headerTool := filepath.Join(filepath.Dir(tool), name)
	if _, err = os.Stat(headerTool); err != nil {
		headerTool, err = exec.LookPath(name)
		if err != nil {
			return image.Config{}, "", fmt.Errorf("Для формата %s нужен vipsheader из комплекта libvips", format)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	field := func(key string) (int, error) {
		cmd := exec.CommandContext(ctx, headerTool, "-f", key, filename, "--vips-concurrency=1", "--vips-cache-max=0")
		configureImageProcess(cmd)
		cmd.Env = append(os.Environ(), "TMPDIR="+temp, "TEMP="+temp, "TMP="+temp)
		var stdout, stderr limitedOutput
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return 0, fmt.Errorf("Не удалось прочитать %s: %s (%w)", format, strings.TrimSpace(stderr.String()), err)
		}
		return strconv.Atoi(strings.TrimSpace(stdout.String()))
	}
	w, err := field("width")
	if err != nil {
		return image.Config{}, "", err
	}
	h, err := field("height")
	if err != nil {
		return image.Config{}, "", err
	}
	return image.Config{Width: w, Height: h}, format, nil
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
	if asset.Kind == assetKindAvatar {
		return runVips(ctx, tool, stage, "thumbnail", original, filepath.Join(stage, "avatar.png")+"[compression=6,keep=none]", strconv.Itoa(maxAvatarOutputSide), "--height="+strconv.Itoa(maxAvatarOutputSide), "--size=down", "--no-rotate", "--fail-on=error")
	}
	return runVips(ctx, tool, stage, "thumbnail", original, filepath.Join(stage, "image.png")+"[compression=6,keep=none]", strconv.Itoa(asset.Width), "--height="+strconv.Itoa(asset.Height), "--size=down", "--no-rotate", "--fail-on=error")
}

func validateUploadSize(size int64) error {
	if size > uploadLimit {
		return errUploadSize
	}
	return nil
}

func uploadLimitForKind(kind string) int64 {
	if kind == assetKindAvatar {
		return avatarUploadLimit
	}
	return uploadLimit
}

func validateUploadSizeForKind(kind string, size int64) error {
	if kind == assetKindAvatar {
		if size > avatarUploadLimit {
			return errAvatarUploadSize
		}
		return nil
	}
	return validateUploadSize(size)
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
	case assetKindAvatar:
		if width > maxAvatarSide || height > maxAvatarSide || int64(width)*int64(height) > maxAvatarPixels {
			return errors.New("Лимит avatar: 25 млн пикселей, сторона до 8192 px")
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
