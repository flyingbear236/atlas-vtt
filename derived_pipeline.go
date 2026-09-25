package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	rotationRecipeVersion  = "fix-rotation-v1"
	derivedJobTimeout      = 5 * time.Minute
	derivedTempBudgetBytes = int64(4 << 30)
)

var fixedRotationPreparer = prepareFixedRotation

// AssetProvenance stays server-side when asset metadata is sent to a client.
// It keeps source assets reachable and gives later derived operations a stable
// recipe without introducing a database or a second asset store.
type AssetProvenance struct {
	Operation     string    `json:"operation"`
	SourceAssetID string    `json:"sourceAssetId"`
	RecipeHash    string    `json:"recipeHash"`
	RecipeVersion string    `json:"recipeVersion"`
	Transform     Transform `json:"transform"`
	PixelDensity  float64   `json:"pixelDensity"`
}

type rotationPlan struct {
	PixelDensity       float64
	ScaledWidth        int
	ScaledHeight       int
	OutputWidth        int
	OutputHeight       int
	WorldX             float64
	WorldY             float64
	WorldWidth         float64
	WorldHeight        float64
	EstimatedTempBytes int64
}

type rotationRecipe struct {
	Version   string       `json:"version"`
	AssetID   string       `json:"assetId"`
	Width     int          `json:"width"`
	Height    int          `json:"height"`
	Transform Transform    `json:"transform"`
	Plan      rotationPlan `json:"plan"`
}

func normalizedRotation(angle float64) float64 {
	angle = math.Mod(angle, 360)
	if angle < 0 {
		angle += 360
	}
	if math.Abs(angle-360) < 1e-9 || math.Abs(angle) < 1e-9 {
		return 0
	}
	return angle
}

func planFixedRotation(asset Asset, transform Transform) (rotationPlan, error) {
	angle := normalizedRotation(transform.Rotation)
	if angle == 0 {
		return rotationPlan{}, errors.New("Ротация уже зафиксирована")
	}
	radians := angle * math.Pi / 180
	c, sine := math.Abs(math.Cos(radians)), math.Abs(math.Sin(radians))
	worldWidth := c*transform.Width + sine*transform.Height
	worldHeight := sine*transform.Width + c*transform.Height
	if !validNumber(worldWidth) || !validNumber(worldHeight) || worldWidth <= 0 || worldHeight <= 0 {
		return rotationPlan{}, errors.New("Некорректная геометрия результата")
	}
	density := math.Min(float64(asset.Width)/transform.Width, float64(asset.Height)/transform.Height)
	if density <= 0 || math.IsNaN(density) || math.IsInf(density, 0) {
		return rotationPlan{}, errors.New("Некорректная плотность изображения")
	}
	// Never upscale either source axis. Reduce density further when the rotated
	// bounding box would exceed the ordinary scene-raster limits.
	density = math.Min(density, float64(maxMapSide-2)/worldWidth)
	density = math.Min(density, float64(maxMapSide-2)/worldHeight)
	density = math.Min(density, math.Sqrt(float64(maxMapPixels-4)/worldWidth/worldHeight))
	if density <= 0 {
		return rotationPlan{}, errors.New("Результат не помещается в лимиты изображения")
	}
	scaledWidth := max(1, int(math.Round(transform.Width*density)))
	scaledHeight := max(1, int(math.Round(transform.Height*density)))
	// libvips rotate rounds the enclosing output size to the nearest pixel.
	outputWidth := max(1, int(math.Round(c*float64(scaledWidth)+sine*float64(scaledHeight))))
	outputHeight := max(1, int(math.Round(sine*float64(scaledWidth)+c*float64(scaledHeight))))
	if err := validateImageDimensions(assetKindScene, outputWidth, outputHeight); err != nil {
		return rotationPlan{}, fmt.Errorf("Результат фиксации: %w", err)
	}
	sourcePixels := int64(asset.Width) * int64(asset.Height)
	scaledPixels := int64(scaledWidth) * int64(scaledHeight)
	outputPixels := int64(outputWidth) * int64(outputHeight)
	// Native intermediates plus a conservative allowance for PNG and pyramid
	// output. This is a disk-work budget, not a claimed hard RSS limit.
	tempBytes := (sourcePixels+scaledPixels)*4 + outputPixels*10
	if tempBytes > derivedTempBudgetBytes {
		return rotationPlan{}, fmt.Errorf("Для фиксации требуется около %.1f ГиБ временного места; лимит %.0f ГиБ", float64(tempBytes)/(1<<30), float64(derivedTempBudgetBytes)/(1<<30))
	}
	centerX, centerY := transform.X+transform.Width/2, transform.Y+transform.Height/2
	return rotationPlan{
		PixelDensity: density, ScaledWidth: scaledWidth, ScaledHeight: scaledHeight,
		OutputWidth: outputWidth, OutputHeight: outputHeight,
		WorldX: centerX - worldWidth/2, WorldY: centerY - worldHeight/2,
		WorldWidth: worldWidth, WorldHeight: worldHeight, EstimatedTempBytes: tempBytes,
	}, nil
}

func fixedRotationRecipe(asset Asset, transform Transform, plan rotationPlan) (rotationRecipe, string) {
	recipe := rotationRecipe{Version: rotationRecipeVersion, AssetID: asset.ID, Width: asset.Width, Height: asset.Height, Transform: transform, Plan: plan}
	encoded, _ := json.Marshal(recipe)
	sum := sha256.Sum256(encoded)
	return recipe, hex.EncodeToString(sum[:])
}

func publicAsset(asset Asset) Asset {
	asset.Provenance = nil
	return asset
}

func vipsHeader(ctx context.Context, tool, field, path string) (string, error) {
	name := "vipsheader"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	header := filepath.Join(filepath.Dir(tool), name)
	cmd := exec.CommandContext(ctx, header, "-f", field, path)
	configureImageProcess(cmd)
	var output limitedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("vipsheader: %w: %s", err, strings.TrimSpace(output.String()))
	}
	return strings.TrimSpace(output.String()), nil
}

func ensureRotationAlpha(ctx context.Context, tool, stage, source string) (string, error) {
	interpretation, err := vipsHeader(ctx, tool, "interpretation", source)
	if err != nil {
		return "", err
	}
	current := source
	if strings.EqualFold(interpretation, "cmyk") {
		current = filepath.Join(stage, "srgb.v")
		if err = runVips(ctx, tool, stage, "colourspace", source, current, "srgb"); err != nil {
			return "", err
		}
	}
	bandsText, err := vipsHeader(ctx, tool, "bands", current)
	if err != nil {
		return "", err
	}
	bands, err := strconv.Atoi(bandsText)
	if err != nil {
		return "", errors.New("libvips: неизвестное число каналов")
	}
	if bands == 2 || bands == 4 {
		return current, nil
	}
	if bands != 1 && bands != 3 {
		return "", fmt.Errorf("Неподдерживаемое число каналов: %d", bands)
	}
	withAlpha := filepath.Join(stage, "alpha.v")
	if err = runVips(ctx, tool, stage, "addalpha", current, withAlpha); err != nil {
		return "", err
	}
	return withAlpha, nil
}

func prepareFixedRotation(ctx context.Context, root string, source Asset, recipe rotationRecipe, recipeHash string) (Asset, error) {
	mode := representationPolicy(rasterMetadata{Width: recipe.Plan.OutputWidth, Height: recipe.Plan.OutputHeight}).Mode
	asset := Asset{
		SourceID: "derived:" + recipeHash, RepresentationVersion: representationVersion,
		Filename: source.Filename + " — rotation", MimeType: "image/png",
		Width: recipe.Plan.OutputWidth, Height: recipe.Plan.OutputHeight,
		Kind: assetKindScene, RenderMode: mode, RetentionPolicy: assetReclaimable, CreatedAt: time.Now().Unix(),
		Provenance: &AssetProvenance{Operation: "fixRotation", SourceAssetID: source.ID, RecipeHash: recipeHash, RecipeVersion: recipe.Version, Transform: recipe.Transform, PixelDensity: recipe.Plan.PixelDensity},
	}
	asset.ID = representationID(asset.SourceID, asset.Kind, asset.RepresentationVersion, asset.RenderMode)
	assetsRoot, err := filepath.Abs(filepath.Join(root, "assets"))
	if err != nil {
		return Asset{}, err
	}
	dest := filepath.Join(assetsRoot, asset.ID)
	if metadata, readErr := os.ReadFile(filepath.Join(dest, "meta.json")); readErr == nil {
		var cached Asset
		if json.Unmarshal(metadata, &cached) != nil || cached.ID != asset.ID || cached.Provenance == nil || cached.Provenance.RecipeHash != recipeHash {
			return Asset{}, errors.New("Повреждены метаданные derived asset")
		}
		return cached, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return Asset{}, readErr
	}
	tool, err := findVips()
	if err != nil {
		return Asset{}, err
	}
	stage, err := os.MkdirTemp(assetsRoot, ".derived-*")
	if err != nil {
		return Asset{}, err
	}
	defer os.RemoveAll(stage)
	ctx, cancel := context.WithTimeout(ctx, derivedJobTimeout)
	defer cancel()
	sourcePath := filepath.Join(assetsRoot, source.ID, "original")
	current, err := ensureRotationAlpha(ctx, tool, stage, sourcePath)
	if err != nil {
		return Asset{}, err
	}
	if recipe.Plan.ScaledWidth != source.Width || recipe.Plan.ScaledHeight != source.Height {
		scaled := filepath.Join(stage, "scaled.v")
		hScale := float64(recipe.Plan.ScaledWidth) / float64(source.Width)
		vScale := float64(recipe.Plan.ScaledHeight) / float64(source.Height)
		if err = runVips(ctx, tool, stage, "resize", current, scaled, strconv.FormatFloat(hScale, 'g', 17, 64), "--vscale", strconv.FormatFloat(vScale, 'g', 17, 64)); err != nil {
			return Asset{}, err
		}
		current = scaled
	}
	derivedPNG := filepath.Join(stage, "derived.png")
	angle := normalizedRotation(recipe.Transform.Rotation)
	if err = runVips(ctx, tool, stage, "rotate", current, derivedPNG+"[compression=6,keep=none]", strconv.FormatFloat(angle, 'g', 17, 64), "--background", "0 0 0 0"); err != nil {
		return Asset{}, err
	}
	f, err := os.Open(derivedPNG)
	if err != nil {
		return Asset{}, err
	}
	config, _, decodeErr := image.DecodeConfig(f)
	f.Close()
	if decodeErr != nil || config.Width != asset.Width || config.Height != asset.Height {
		return Asset{}, fmt.Errorf("libvips: ожидался результат %dx%d, получен %dx%d", asset.Width, asset.Height, config.Width, config.Height)
	}
	if stat, statErr := os.Stat(derivedPNG); statErr == nil {
		asset.Size = stat.Size()
	}
	original := filepath.Join(stage, "original")
	if err = os.Rename(derivedPNG, original); err != nil {
		return Asset{}, err
	}
	if err = prepareRepresentationFilesWithOptions(ctx, tool, stage, original, &asset, true); err != nil {
		return Asset{}, err
	}
	metadata, _ := json.Marshal(asset)
	if err = os.WriteFile(filepath.Join(stage, "meta.json"), metadata, 0600); err != nil {
		return Asset{}, err
	}
	if err = os.Rename(stage, dest); err != nil {
		return Asset{}, fmt.Errorf("Не удалось опубликовать derived asset: %w", err)
	}
	return asset, nil
}
