package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUniversalMediaPartPolicy(t *testing.T) {
	a := Asset{ID: "a", Kind: assetKindScene, RepresentationVersion: universalRepresentationVersion, RenderMode: renderModeBitmap, Width: 128, Height: 96}
	for _, name := range []string{"scene", "image.png", "token.png", "avatar.png", "hover.png", "artwork.png", "media-view.png"} {
		if actual := universalPartName(a, name); actual != "media-thumb.png" {
			t.Fatalf("%s: %s", name, actual)
		}
	}
	a.Width = 400
	a.Height = 200
	if universalPartName(a, "scene") != "avatar.png" || universalPartName(a, "hover.png") != "hover.png" {
		t.Fatal("wrong native-size reuse")
	}
	a.Width = 8192
	a.Height = 4096
	a.RenderMode = renderModeTiled
	if universalPartName(a, "scene") != "scene" || universalPartName(a, "artwork.png") != "artwork.png" || mediaPyramidLevels(a.Width, a.Height) != 5 {
		t.Fatal("large raster lost its independent representations")
	}
}

func TestUniversalMediaDeferredRepresentations(t *testing.T) {
	if _, err := findVips(); err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	data := pipelinePNG(t, 2048, 2048)
	ctx := context.Background()
	a, err := prepareCampaignMediaReader(ctx, root, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !universalMedia(a) || a.RenderMode != renderModeTiled || mediaReady(root, a, "scene") || mediaReady(root, a, "avatar.png") || !mediaReady(root, a, "media-thumb.png") {
		t.Fatal("import eagerly created a derivative or failed to create thumb")
	}
	original, err := os.ReadFile(filepath.Join(root, "assets", a.ID, "original"))
	if err != nil || !bytes.Equal(original, data) {
		t.Fatal("original changed", err)
	}
	if err = ensureUniversalPartLocked(ctx, root, a, "avatar.png"); err != nil {
		t.Fatal(err)
	}
	if mediaReady(root, a, "scene") || !mediaReady(root, a, "avatar.png") {
		t.Fatal("avatar generated scene tiles")
	}
	b, err := prepareCampaignMediaReader(ctx, root, bytes.NewReader(data))
	if err != nil || a.ID != b.ID {
		t.Fatal("resource not shared", err)
	}
	if err = ensureUniversalPartLocked(ctx, root, a, "scene"); err != nil {
		t.Fatal(err)
	}
	if !mediaReady(root, a, "scene") {
		t.Fatal("scene not prepared")
	}
}

func TestUniversalMediaQuickUploadsUseRoot(t *testing.T) {
	ss := &Session{}
	first := Asset{ID: "first"}
	second := Asset{ID: "second"}
	if err := addUnsortedMedia(ss, first, "image.png"); err != nil {
		t.Fatal(err)
	}
	if err := addUnsortedMedia(ss, first, "image.png"); err != nil {
		t.Fatal(err)
	}
	if err := addUnsortedMedia(ss, second, "image.png"); err != nil {
		t.Fatal(err)
	}
	if len(ss.MediaLibrary.Files) != 2 || ss.MediaLibrary.Files["image.png"].AssetID != first.ID || ss.MediaLibrary.Files["image (2).png"].AssetID != second.ID {
		t.Fatalf("unexpected root entries: %+v", ss.MediaLibrary.Files)
	}
	if len(ss.MediaLibrary.Directories) != 0 {
		t.Fatal("quick upload created a folder")
	}
}

func TestMediaUploadFilename(t *testing.T) {
	for _, tc := range []struct{ raw, fallback, want string }{
		{"Мой меч.PNG", "image.png", "Мой меч.PNG"},
		{`C:\Users\GM\My Sword.webp`, "image.png", "My Sword.webp"},
		{"/home/gm/Свиток.jpg", "image.png", "Свиток.jpg"},
		{"my:portrait.png", "image.png", "my_portrait.png"},
		{"CON.png", "image.png", "_CON.png"},
		{"", "saved.webp", "saved.webp"},
		{"", "", "image.png"},
	} {
		if got := mediaUploadFilename(tc.raw, tc.fallback); got != tc.want {
			t.Errorf("%q: got %q; want %q", tc.raw, got, tc.want)
		}
	}
	long := mediaUploadFilename(strings.Repeat("ю", 240)+".png", "image.png")
	if err := validateLibraryPath(long); err != nil || !strings.HasSuffix(long, ".png") {
		t.Fatal(long, err)
	}
	ss := &Session{}
	if err := addUnsortedMedia(ss, Asset{ID: "uuid-one", Filename: "Меч.png"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := addUnsortedMedia(ss, Asset{ID: "uuid-two", Filename: "Меч.png"}, ""); err != nil {
		t.Fatal(err)
	}
	if ss.MediaLibrary.Files["Меч.png"].AssetID != "uuid-one" || ss.MediaLibrary.Files["Меч (2).png"].AssetID != "uuid-two" {
		t.Fatal(ss.MediaLibrary.Files)
	}
}
