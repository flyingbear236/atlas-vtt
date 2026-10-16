package main

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archiveForImport(t *testing.T, source *Session) (portableImportArchive, []byte) {
	t.Helper()
	var zipData bytes.Buffer
	if err := ExportPortablePackage(context.Background(), snapshotPortableExport(source), t.TempDir(), false, &zipData); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(zipData.Bytes()), int64(zipData.Len()))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := readPortableImportArchive(z)
	if err != nil {
		t.Fatal(err)
	}
	return archive, zipData.Bytes()
}
func TestPortableImportRoundTripNoMedia(t *testing.T) {
	source := portableExportFixture(t)
	archive, _ := archiveForImport(t, source)
	if len(archive.Ruleset) != 11 || len(archive.Characters) != 2 {
		t.Fatalf("archive lost files or persistent characters: %d %d", len(archive.Ruleset), len(archive.Characters))
	}
	empty := &Session{RegistryRevision: 1, CharacterRevision: 1, Ruleset: RulesetSnapshot{Registry: emptyRulesetRegistry()}, CampaignDefinitions: emptyCampaignRegistry(), CharacterInstances: map[string]CharacterInstance{}, CharacterSources: map[string]CharacterTOMLSource{}, Assets: map[string]Asset{}}
	result, err := buildPortableImportCandidate(context.Background(), empty, archive, portableImportOptions{Mode: "replace"}, t.TempDir(), cloneAssetRegistry(empty.Assets), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Ruleset.Files) != 11 || len(result.Characters) != 2 {
		t.Fatal("imported definitions or characters missing")
	}
	survivor := result.Characters["survivor"]
	if survivor.Materialized != nil || survivor.ResourceCurrent["hp"].Integer() != 4 {
		t.Fatalf("inheritance or HP lost: %+v", survivor)
	}
	independent := result.Characters["materialized"]
	if independent.Materialized == nil || independent.Materialized.ResourceInitial["hp"].Integer() != 7 {
		t.Fatal("materialization or initial HP lost")
	}
	for name, manifest := range result.Ruleset.Files {
		if manifest.Source != archive.Ruleset[name] {
			t.Fatalf("source changed on import: %s", name)
		}
	}
	imported := &Session{Ruleset: result.Ruleset, CampaignDefinitions: result.Campaign, CharacterInstances: result.Characters, CharacterSources: result.Sources, Assets: result.Assets}
	nextArchive, _ := archiveForImport(t, imported)
	for name, text := range archive.Ruleset {
		if nextArchive.Ruleset[name] != text {
			t.Fatalf("roundtrip changed TOML %s", name)
		}
	}
	if len(nextArchive.Characters) != 2 {
		t.Fatalf("lost character during roundtrip")
	}
}
func TestPortableImportConflictsAndSelectiveReplace(t *testing.T) {
	source := portableExportFixture(t)
	archive, _ := archiveForImport(t, source)
	base := cloneForPortableImport(source)
	base.RegistryRevision = 1
	base.CharacterRevision = 1
	base.Assets = map[string]Asset{}
	_, err := buildPortableImportCandidate(context.Background(), base, archive, portableImportOptions{Mode: "replace"}, t.TempDir(), map[string]Asset{}, nil, true)
	if err == nil || !strings.Contains(err.Error(), "requires skip or replace") {
		t.Fatalf("expected character conflict, got %v", err)
	}
	choices := map[string]string{"materialized": "skip", "survivor": "skip"}
	result, err := buildPortableImportCandidate(context.Background(), base, archive, portableImportOptions{Mode: "selected", SelectedFiles: []string{"goblins.toml"}, Characters: choices}, t.TempDir(), map[string]Asset{}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Ruleset.Files) != 11 || len(result.Preview.ReplacedFiles) != 1 || result.Preview.ReplacedFiles[0] != "goblins.toml" {
		t.Fatalf("selective replacement changed other files: %+v", result.Preview)
	}
	if len(result.Characters) != 3 {
		t.Fatal("nonpersistent existing character removed")
	}
}
func TestPortableImportRejectUnsafeArchive(t *testing.T) {
	for _, test := range []struct {
		name  string
		names []string
	}{{"traversal", []string{"../escape.toml"}}, {"duplicate", []string{"goblins.toml", "goblins.toml"}}, {"case-insensitive duplicate", []string{"Goblins.toml", "goblins.toml"}}} {
		t.Run(test.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := zip.NewWriter(&buf)
			for _, name := range test.names {
				f, err := w.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = f.Write([]byte("fake"))
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := readPortableImportArchive(r); err == nil {
				t.Fatal("unsafe ZIP accepted")
			}
		})
	}
}

func TestPortableImportMediaConflict(t *testing.T) {
	root := t.TempDir()
	var oldData, newData bytes.Buffer
	for _, out := range []*bytes.Buffer{&oldData, &newData} {
		img := image.NewRGBA(image.Rect(0, 0, 2, 2))
		if out == &oldData {
			img.Set(0, 0, color.RGBA{R: 255, A: 255})
		} else {
			img.Set(0, 0, color.RGBA{G: 255, A: 255})
		}
		if err := png.Encode(out, img); err != nil {
			t.Fatal(err)
		}
	}
	oldID := "old-image"
	dir := filepath.Join(root, "assets", oldID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "avatar.png"), oldData.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, e := zw.Create("pictures/goblin.png")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write(newData.Bytes()); e != nil {
		t.Fatal(e)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := readPortableImportArchive(reader)
	if err != nil {
		t.Fatal(err)
	}
	base := &Session{RegistryRevision: 1, CharacterRevision: 1, Ruleset: RulesetSnapshot{Registry: emptyRulesetRegistry(), Media: map[string]string{"pictures/goblin.png": oldID}}, CampaignDefinitions: emptyCampaignRegistry(), CharacterInstances: map[string]CharacterInstance{}, Assets: map[string]Asset{oldID: {ID: oldID, Kind: assetKindAvatar}}}
	result, err := buildPortableImportCandidate(context.Background(), base, archive, portableImportOptions{Mode: "replace"}, root, cloneAssetRegistry(base.Assets), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Preview.ConflictingMedia) != 1 {
		t.Fatalf("missing media conflict: %+v", result.Preview)
	}
	_, err = buildPortableImportCandidate(context.Background(), base, archive, portableImportOptions{Mode: "replace"}, root, cloneAssetRegistry(base.Assets), nil, true)
	if err == nil || !strings.Contains(err.Error(), "requires keep or replace") {
		t.Fatalf("media was silently replaced: %v", err)
	}
	result, err = buildPortableImportCandidate(context.Background(), base, archive, portableImportOptions{Mode: "replace", Media: map[string]string{"pictures/goblin.png": "keep"}}, root, cloneAssetRegistry(base.Assets), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ruleset.Media["pictures/goblin.png"] != oldID {
		t.Fatal("existing image replaced despite keep choice")
	}
}
