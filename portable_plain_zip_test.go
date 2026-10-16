package main

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/png"
	"strings"
	"testing"
)

func plainZIP(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func plainArchive(t *testing.T, entries map[string][]byte) (portableImportArchive, error) {
	t.Helper()
	raw := plainZIP(t, entries)
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return readPortableImportArchive(zr)
}

func TestPlainZIPFileTypesAndRoundTrip(t *testing.T) {
	rules := `schema_version = 1
# user comment
[stats.hp]
name = "HP"
type = "integer"
kind = "resource"
[actions.bite]
name = "Bite"
[presets.goblin]
name = "Goblin"
kind = "monster"
artwork = "pictures/monsters/goblin.png"
[presets.goblin.stats]
hp = 7
`
	source, err := CompileRulesetFiles(map[string]string{"things/all.toml": rules})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: source.Registry, Campaign: emptyCampaignRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	char := CharacterInstance{ID: "char-1", Name: "Goblin", Persistent: true, PresetID: "goblin", ResourceCurrent: map[string]StatValue{"hp": IntegerStatValue(4)}}
	characterBytes, err := EncodeCharacterTOML(char, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, img); err != nil {
		t.Fatal(err)
	}
	input := map[string][]byte{
		"things/all.toml": []byte(rules), "players/goblin.toml": characterBytes,
		"pictures/monsters/goblin.png": imageBytes.Bytes(),
		"notes/unknown.toml":           []byte("[custom]\nkey = 1\n"),
		"empty/folder/":                {}, "instructions.txt": []byte("skip me"),
	}
	imported, err := plainArchive(t, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.Ruleset) != 1 || len(imported.Characters) != 1 || len(imported.Media) != 1 || len(imported.Unknown) != 1 || len(imported.Skipped) != 1 || len(imported.Directories) != 1 {
		t.Fatalf("unexpected classification: %+v", imported)
	}
	base := &Session{RegistryRevision: 1, CharacterRevision: 1, Ruleset: RulesetSnapshot{Registry: emptyRulesetRegistry()}, CampaignDefinitions: emptyCampaignRegistry(), CharacterInstances: map[string]CharacterInstance{}, CharacterSources: map[string]CharacterTOMLSource{}, Assets: map[string]Asset{}}
	candidate, err := buildPortableImportCandidate(context.Background(), base, imported, portableImportOptions{Mode: "replace"}, t.TempDir(), map[string]Asset{}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Characters["char-1"].ResourceCurrent["hp"].Integer() != 4 {
		t.Fatal("current HP lost")
	}
	if candidate.Ruleset.Registry.Presets["goblin"].Artwork != "pictures/monsters/goblin.png" {
		t.Fatal("artwork path rewritten")
	}
	if candidate.Ruleset.Files["things/all.toml"].Source != rules {
		t.Fatal("source comment changed")
	}
	if candidate.Unknown["notes/unknown.toml"] != "[custom]\nkey = 1\n" {
		t.Fatal("opaque TOML changed")
	}
	if !slicesContains(candidate.Directories, "empty/folder") {
		t.Fatal("lost empty directory")
	}
	restored := &Session{Ruleset: candidate.Ruleset, CampaignDefinitions: emptyCampaignRegistry(), CharacterInstances: candidate.Characters, CharacterSources: candidate.Sources, Assets: candidate.Assets, UnknownTOML: candidate.Unknown, LibraryDirectories: candidate.Directories}
	var output bytes.Buffer
	if err := ExportPortablePackage(context.Background(), snapshotPortableExport(restored), t.TempDir(), false, &output); err != nil {
		t.Fatal(err)
	}
	zipContents := unpackPortable(t, output.Bytes())
	if _, has := zipContents["manifest.toml"]; has {
		t.Fatal("exported legacy manifest")
	}
	if string(zipContents["things/all.toml"]) != rules {
		t.Fatal("roundtrip changed original TOML")
	}
	if string(zipContents["notes/unknown.toml"]) != "[custom]\nkey = 1\n" {
		t.Fatal("roundtrip changed opaque TOML")
	}
	if _, has := zipContents["empty/folder/"]; !has {
		t.Fatal("missing empty folder")
	}
	if _, has := zipContents["pictures/monsters/goblin.png"]; has {
		t.Fatal("includeMedia=false exported image")
	}
	if len(zipContents["players/goblin.toml"]) == 0 {
		t.Fatal("character absent")
	}
}

func TestPlainZIPRejectsUnsafeAndSchemas(t *testing.T) {
	for _, tt := range []struct {
		name    string
		entries map[string][]byte
	}{
		{"traversal", map[string][]byte{"../secret.toml": []byte("a=1")}},
		{"mixed", map[string][]byte{"mix.toml": []byte("schema_version=1\n[character]\nid='a'\n[stats.hp]\ntype='integer'\n")}},
		{"bad known", map[string][]byte{"bad.toml": []byte("schema_version=1\n[stats.hp]\ntype=99\n")}},
		{"invalid TOML", map[string][]byte{"bad.toml": []byte("[broken\n")}},
		{"unsafe folder", map[string][]byte{"dir/../bad/": nil}},
		{"file folder", map[string][]byte{"x": []byte("text"), "x/file.toml": []byte("[opaque]\nx=1")}},
		{"old atlas", map[string][]byte{"manifest.toml": []byte("format_version=1\nruleset_files=['ruleset/a.toml']\n"), "ruleset/a.toml": []byte("schema_version=1\n[stats.hp]\ntype='integer'")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := plainArchive(t, tt.entries); err == nil {
				t.Fatal("unsafe or invalid ZIP accepted")
			}
		})
	}
}

func TestPlainZIPFullReplacePreservesOtherLibraries(t *testing.T) {
	base := libraryFixture(t)
	newSource := strings.Replace(libraryRulesTOML, "# Do not change this comment", "# new source", 1)
	a, err := plainArchive(t, map[string][]byte{"newpath/updated.toml": []byte(newSource)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := buildPortableImportCandidate(context.Background(), base, a, portableImportOptions{Mode: "replace"}, t.TempDir(), cloneAssetRegistry(base.Assets), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Ruleset.Files) != 1 || result.Ruleset.Files["newpath/updated.toml"].Source != newSource {
		t.Fatal("ruleset not replaced")
	}
	if _, ok := result.Characters["character-a12f"]; !ok {
		t.Fatal("existing character removed")
	}
	if result.Unknown["notes/unknown.toml"] == "" || !slicesContains(result.Directories, "empty/sub") {
		t.Fatal("unrelated library data removed")
	}
	if result.Ruleset.Media["pictures/monsters/goblin.png"] != "img1" {
		t.Fatal("media unexpectedly removed")
	}
}

func TestPortableImportOptionsDigestIsStable(t *testing.T) {
	a := portableImportOptions{Mode: "selected", SelectedFiles: []string{"b.toml", "a.toml"}, Characters: map[string]string{"a": "skip", "b": "replace"}}
	b := portableImportOptions{Mode: "selected", SelectedFiles: []string{"a.toml", "b.toml"}, Characters: map[string]string{"b": "replace", "a": "skip"}}
	if portableOptionsDigest(a) != portableOptionsDigest(b) {
		t.Fatal("options digest depends on order")
	}
	b.Media = map[string]string{"portrait.png": "replace"}
	if portableOptionsDigest(a) == portableOptionsDigest(b) {
		t.Fatal("choices not included in digest")
	}
}

func TestPlainZIPExactArtworkAndOpaqueCollision(t *testing.T) {
	root := t.TempDir()
	rules := strings.ReplaceAll(libraryRulesTOML, "pictures/monsters/goblin.png", "pictures/monsters/goblin.png")
	imported, err := plainArchive(t, map[string][]byte{
		"rules.toml":         []byte(rules),
		"other/goblin.png":   []byte("image placeholder for preview"),
		"notes/unknown.toml": []byte("[new]\nvalue = 1\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	base := libraryFixture(t)
	// The same basename elsewhere is not a fallback for the precise path.
	delete(base.Ruleset.Media, "pictures/monsters/goblin.png")
	result, err := buildPortableImportCandidate(context.Background(), base, imported, portableImportOptions{Mode: "replace"}, root, cloneAssetRegistry(base.Assets), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slicesContains(result.Preview.MissingMedia, "pictures/monsters/goblin.png") {
		t.Fatal("missing exact artwork path was not reported")
	}
	if result.Ruleset.Media["pictures/monsters/goblin.png"] != "" {
		t.Fatal("image linked by basename")
	}
	if len(result.Preview.ConflictingUnknown) != 1 {
		t.Fatal("opaque TOML conflict not reported")
	}
	if result.Unknown["notes/unknown.toml"] != base.UnknownTOML["notes/unknown.toml"] {
		t.Fatal("unresolved conflict overwrote opaque TOML")
	}
	_, err = buildPortableImportCandidate(context.Background(), base, imported, portableImportOptions{Mode: "replace"}, root, cloneAssetRegistry(base.Assets), nil, true)
	if err == nil || !strings.Contains(err.Error(), "requires keep or replace") {
		t.Fatalf("no explicit opaque choice required: %v", err)
	}
	result, err = buildPortableImportCandidate(context.Background(), base, imported, portableImportOptions{Mode: "replace", Unknown: map[string]string{"notes/unknown.toml": "replace"}}, root, cloneAssetRegistry(base.Assets), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Unknown["notes/unknown.toml"] != "[new]\nvalue = 1\n" {
		t.Fatal("opaque replacement did not apply")
	}
}

func TestPlainZIPCompressionRatioLimit(t *testing.T) {
	raw := plainZIP(t, map[string][]byte{"unrelated.bin": bytes.Repeat([]byte{0}, 12<<20)})
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = readPortableImportArchive(zr); err == nil {
		t.Fatal("ZIP bomb-like entry accepted")
	}
}
