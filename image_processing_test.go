package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestStreamedTilesPreservePixelsAndOddEdges(t *testing.T) {
	root := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 1025, 515))
	for y := 0; y < 515; y++ {
		for x := 0; x < 1025; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), uint8(x / 512 * 50), 255})
		}
	}
	var data bytes.Buffer
	png.Encode(&data, img)
	a, e := prepare(root, data.Bytes(), "map")
	if e != nil {
		t.Fatal(e)
	}
	for _, tile := range []struct {
		name string
		x, y int
	}{{"0_0_0.png", 0, 0}, {"0_1_0.png", 512, 0}, {"0_2_1.png", 1024, 512}} {
		f, e := os.Open(filepath.Join(root, "assets", a.ID, tile.name))
		if e != nil {
			t.Fatal(e)
		}
		decoded, _, e := image.Decode(f)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		for y := 0; y < decoded.Bounds().Dy(); y++ {
			for x := 0; x < decoded.Bounds().Dx(); x++ {
				want := img.RGBAAt(tile.x+x, tile.y+y)
				got := color.RGBAModel.Convert(decoded.At(x, y)).(color.RGBA)
				if got != want {
					t.Fatalf("%s at %d,%d: got %v want %v", tile.name, x, y, got, want)
				}
			}
		}
	}
}
