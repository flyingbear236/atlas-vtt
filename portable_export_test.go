package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func portableExportFixture(t *testing.T) *Session {
	t.Helper()
	files := map[string]string{}
	for i := 0; i < 10; i++ {
		files[fmt.Sprintf("source-%02d.toml", i)] = fmt.Sprintf("schema_version = 1\n# note %d\n\n[stats.ability%d]\nname = 'Ability %d'\ntype = 'integer'  # Keep formatting\n", i, i, i)
	}
	files["goblins.toml"] = "schema_version = 1\n# Editable goblin\n[stats.hp]\nname = 'Hit Points'\ntype = 'integer'\nkind = 'resource'\n\n[presets.goblin]\nname = 'Goblin'\nkind = 'monster'\n[presets.goblin.stats]\nhp = 7  # Important comment\n"
	ruleset, err := CompileRulesetFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	return &Session{Ruleset: ruleset, CampaignDefinitions: emptyCampaignRegistry(), CharacterInstances: map[string]CharacterInstance{
		"survivor":     {ID: "survivor", Name: "Survivor", Persistent: true, PresetID: "goblin", ResourceCurrent: map[string]StatValue{"hp": IntegerStatValue(4)}},
		"materialized": {ID: "materialized", Name: "Independent", Persistent: true, PresetID: "goblin", Materialized: &CharacterMechanicalState{Stats: map[string]StatValue{}, ResourceInitial: map[string]StatValue{"hp": IntegerStatValue(7)}}, ResourceCurrent: map[string]StatValue{"hp": IntegerStatValue(4)}},
		"temporary":    {ID: "temporary", Name: "Temporary", PresetID: "goblin"},
	}, CharacterSources: map[string]CharacterTOMLSource{}}
}

func unpackPortable(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, file := range reader.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		buf := new(bytes.Buffer)
		_, err = buf.ReadFrom(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[file.Name] = buf.Bytes()
	}
	return out
}

func TestPortableExportRoundTripSourcesAndCharacters(t *testing.T) {
	ss := portableExportFixture(t)
	var zipBytes bytes.Buffer
	if err := ExportPortablePackage(context.Background(), snapshotPortableExport(ss), t.TempDir(), false, &zipBytes); err != nil {
		t.Fatal(err)
	}
	entries := unpackPortable(t, zipBytes.Bytes())
	if len(entries) != 14 {
		t.Fatalf("unexpected archive entries: %d", len(entries))
	} // 11 originals + 2 characters + manifest
	for name, original := range ss.Ruleset.Files {
		if string(entries["ruleset/"+name]) != original.Source {
			t.Fatalf("source %s changed", name)
		}
	}
	if len(entries["characters/"+strings.TrimPrefix(CharacterTOMLFileName("temporary"), "characters/")]) != 0 {
		t.Fatal("temporary character exported")
	}
	registry, err := MergeDefinitionRegistries(sessionRegistries(ss))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"survivor", "materialized"} {
		instance, err := DecodeCharacterTOML(entries[CharacterTOMLFileName(id)], nil, registry)
		if err != nil {
			t.Fatal(err)
		}
		if instance.ResourceCurrent["hp"].Integer() != 4 {
			t.Fatalf("HP incorrect for %s", id)
		}
		if (instance.Materialized == nil) != (ss.CharacterInstances[id].Materialized == nil) {
			t.Fatalf("materialization changed for %s", id)
		}
	}
	var manifest portableExportManifest
	if err := toml.Unmarshal(entries["manifest.toml"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.FormatVersion != 1 || manifest.IncludesMedia || len(manifest.RulesetFiles) != 11 || len(manifest.CharacterFiles) != 2 {
		t.Fatalf("manifest mismatch: %#v", manifest)
	}
}

func TestPortableExportPreservesSourceEdits(t *testing.T) {
	ss := portableExportFixture(t)
	preset := ss.Ruleset.Registry.Presets["goblin"]
	preset.Stats["hp"] = IntegerStatValue(9)
	ss.Ruleset.Registry.Presets["goblin"] = preset
	var buf bytes.Buffer
	if err := ExportPortablePackage(context.Background(), snapshotPortableExport(ss), t.TempDir(), false, &buf); err != nil {
		t.Fatal(err)
	}
	files := unpackPortable(t, buf.Bytes())
	if !strings.Contains(string(files["ruleset/goblins.toml"]), "hp = 9  # Important comment") {
		t.Fatalf("comment lost: %s", files["ruleset/goblins.toml"])
	}
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("source-%02d.toml", i)
		if string(files["ruleset/"+name]) != ss.Ruleset.Files[name].Source {
			t.Fatalf("unrelated %s changed", name)
		}
	}
}

func TestPortableExportMediaToggleAndConversion(t *testing.T) {
	ss := portableExportFixture(t)
	ss.Ruleset.Media = map[string]string{"media/a.png": "image1", "media/b.jpg": "image1"}
	ss.Assets = map[string]Asset{"image1": {ID: "image1", Kind: assetKindAvatar}}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets", "image1"), 0700); err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&input, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "image1", "avatar.png"), input.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	for _, include := range []bool{false, true} {
		var zipped bytes.Buffer
		if err := ExportPortablePackage(context.Background(), snapshotPortableExport(ss), root, include, &zipped); err != nil {
			t.Fatal(err)
		}
		files := unpackPortable(t, zipped.Bytes())
		if _, ok := files["media/a.png"]; ok != include {
			t.Fatalf("media toggle failed: %v", include)
		}
		if include {
			if !bytes.Equal(files["media/a.png"], input.Bytes()) {
				t.Fatal("PNG bytes changed")
			}
			if !bytes.HasPrefix(files["media/b.jpg"], []byte{0xff, 0xd8, 0xff}) {
				t.Fatal("JPEG path contains non-JPEG bytes")
			}
		}
	}
	if err := os.Remove(filepath.Join(root, "assets", "image1", "avatar.png")); err != nil {
		t.Fatal(err)
	}
	var zipped bytes.Buffer
	if err := ExportPortablePackage(context.Background(), snapshotPortableExport(ss), root, true, &zipped); err == nil {
		t.Fatal("missing media was silently ignored")
	}
}

func TestPortableExportEmptyCampaignGeneratedDefinitions(t *testing.T) {
	ss := &Session{Ruleset: RulesetSnapshot{Registry: emptyRulesetRegistry()}, CampaignDefinitions: emptyCampaignRegistry(), CharacterInstances: map[string]CharacterInstance{}}
	ss.CampaignDefinitions.Stats["luck"] = StatDefinition{ID: "luck", Name: "Luck", Type: StatTypeInteger}
	ss.CharacterInstances["unique"] = CharacterInstance{ID: "unique", Name: "New", Persistent: true, Materialized: &CharacterMechanicalState{Stats: map[string]StatValue{"luck": IntegerStatValue(3)}}}
	var zipped bytes.Buffer
	if err := ExportPortablePackage(context.Background(), snapshotPortableExport(ss), t.TempDir(), false, &zipped); err != nil {
		t.Fatal(err)
	}
	files := unpackPortable(t, zipped.Bytes())
	if len(files["campaign/generated-definitions.toml"]) == 0 || len(files[CharacterTOMLFileName("unique")]) == 0 {
		t.Fatal("UI-created content omitted")
	}
}

func TestPortableExportSnapshotStaysConsistent(t *testing.T) {
	ss := portableExportFixture(t)
	snapshot := snapshotPortableExport(ss)
	original := ss.CharacterInstances["survivor"]
	original.ResourceCurrent["hp"] = IntegerStatValue(1)
	ss.CharacterInstances["survivor"] = original
	preset := ss.Ruleset.Registry.Presets["goblin"]
	preset.Stats["hp"] = IntegerStatValue(100)
	ss.Ruleset.Registry.Presets["goblin"] = preset
	var zipped bytes.Buffer
	if err := ExportPortablePackage(context.Background(), snapshot, t.TempDir(), false, &zipped); err != nil {
		t.Fatal(err)
	}
	files := unpackPortable(t, zipped.Bytes())
	if !strings.Contains(string(files[CharacterTOMLFileName("survivor")]), "hp = 4") {
		t.Fatalf("export observed later current HP: %s", files[CharacterTOMLFileName("survivor")])
	}
	if !strings.Contains(string(files["ruleset/goblins.toml"]), "hp = 7") {
		t.Fatal("export observed later preset mutation")
	}
}

func TestPortableExportImportedCharacterKeepsComments(t *testing.T) {
	ss := portableExportFixture(t)
	instance := ss.CharacterInstances["survivor"]
	registry, err := MergeDefinitionRegistries(sessionRegistries(ss))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := EncodeCharacterTOML(instance, ss.Ruleset.Media, registry)
	if err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(string(canonical), "hp = 4", "hp = 5 # custom GM note", 1)
	ss.CharacterSources["survivor"] = CharacterTOMLSource{FileName: "characters/goblin.toml", Source: source}
	var zipped bytes.Buffer
	if err := ExportPortablePackage(context.Background(), snapshotPortableExport(ss), t.TempDir(), false, &zipped); err != nil {
		t.Fatal(err)
	}
	files := unpackPortable(t, zipped.Bytes())
	if !strings.Contains(string(files["characters/goblin.toml"]), "hp = 4 # custom GM note") {
		t.Fatalf("character comment lost: %s", files["characters/goblin.toml"])
	}
}

func TestPortableExportAmbiguousSourceFailsWithoutReformatting(t *testing.T) {
	ss := portableExportFixture(t)
	original := ss.Ruleset.Files["goblins.toml"]
	// The TOML parser accepts multiline strings; the source-preserving writer
	// intentionally refuses an ambiguous rewrite rather than deleting comments.
	original.Source = strings.Replace(original.Source, "# Editable goblin", "# Editable goblin\n# ''' multiline syntax needs explicit resolution", 1)
	// Make an actual valid multiline-string assignment inside a preset.
	original.Source = strings.Replace(original.Source, "name = 'Goblin'", "name = '''Goblin'''", 1)
	original.Digest = rulesetFileDigest(original.Source)
	ss.Ruleset.Files["goblins.toml"] = original
	preset := ss.Ruleset.Registry.Presets["goblin"]
	preset.Stats["hp"] = IntegerStatValue(8)
	ss.Ruleset.Registry.Presets["goblin"] = preset
	var zipped bytes.Buffer
	if err := ExportPortablePackage(context.Background(), snapshotPortableExport(ss), t.TempDir(), false, &zipped); err == nil {
		t.Fatal("ambiguous source edit succeeded")
	}
	if ss.Ruleset.Files["goblins.toml"].Source != original.Source {
		t.Fatal("original source was modified")
	}
}

func TestPortableExportGeneratedMediaPathAndUnsafeID(t *testing.T) {
	ss := portableExportFixture(t)
	assetID := "image9"
	instance := ss.CharacterInstances["survivor"]
	instance.AvatarAssetID = &assetID
	ss.CharacterInstances["survivor"] = instance
	ss.Assets = map[string]Asset{assetID: {ID: assetID, Kind: assetKindAvatar}}
	var archive bytes.Buffer
	if err := ExportPortablePackage(context.Background(), snapshotPortableExport(ss), t.TempDir(), false, &archive); err != nil {
		t.Fatal(err)
	}
	files := unpackPortable(t, archive.Bytes())
	if !bytes.Contains(files[CharacterTOMLFileName("survivor")], []byte("media/character-image9.png")) {
		t.Fatal("missing generated media mapping")
	}
	var manifest portableExportManifest
	if err := toml.Unmarshal(files["manifest.toml"], &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.MediaFiles) != 1 || manifest.MediaFiles[0] != "media/character-image9.png" {
		t.Fatalf("missing media manifest path: %+v", manifest)
	}
	if _, err := mediaSourcePath(t.TempDir(), Asset{ID: "../../etc", Kind: assetKindAvatar}); err == nil {
		t.Fatal("unsafe asset path accepted")
	}
}
