package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

const libraryRulesTOML = `schema_version = 1
# Do not change this comment
[stats.hp]
name = "HP" # source comment
type = "integer"
kind = "resource"

[presets.goblin]
name = "Гоблин"
kind = "monster"
artwork = "pictures/monsters/goblin.png" # image comment
[presets.goblin.stats]
hp = 7
`

func libraryFixture(t *testing.T) *Session {
	t.Helper()
	registry, err := CompileRulesetFiles(map[string]string{"dnd/stats.toml": libraryRulesTOML})
	if err != nil {
		t.Fatal(err)
	}
	registry.Media = map[string]string{"pictures/monsters/goblin.png": "img1"}
	return &Session{RegistryRevision: 1, CharacterRevision: 1, CampaignRevision: 1,
		Ruleset: registry, Assets: map[string]Asset{"img1": {ID: "img1", Kind: assetKindAvatar}},
		CharacterInstances: map[string]CharacterInstance{"character-a12f": {ID: "character-a12f", Name: "Валера", Persistent: true, PresetID: "goblin", ResourceCurrent: map[string]StatValue{"hp": IntegerStatValue(6)}}},
		CharacterSources:   map[string]CharacterTOMLSource{}, Scenes: map[string]*Scene{},
		LibraryDirectories: []string{"empty/sub"}, UnknownTOML: map[string]string{"notes/unknown.toml": "# retained\n[custom]\nvalue=1\n"}}
}
func libraryApply(t *testing.T, s *Session, op, p, target string) {
	t.Helper()
	if err := applyLibraryOperation(s, libraryRequest{Operation: op, Path: p, Target: target}); err != nil {
		t.Fatalf("%s %s -> %s: %v", op, p, target, err)
	}
}
func TestLibraryNestedAndEmptyFolders(t *testing.T) {
	s := libraryFixture(t)
	libraryApply(t, s, "createDirectory", "empty/new", "")
	tr := libraryTree(s)
	for _, d := range []string{"empty", "empty/sub", "empty/new", "dnd", "pictures", "pictures/monsters"} {
		if !slicesContains(tr.Directories, d) {
			t.Fatalf("missing folder %q: %+v", d, tr.Directories)
		}
	}
	libraryApply(t, s, "deleteDirectory", "empty/new", "")
	if slicesContains(libraryTree(s).ExplicitDirectories, "empty/new") {
		t.Fatal("deleted empty folder remains")
	}
}
func slicesContains(a []string, p string) bool {
	for _, v := range a {
		if v == p {
			return true
		}
	}
	return false
}

func TestLibraryRenameAndFolderMovePreserveTOMLAndIDs(t *testing.T) {
	s := libraryFixture(t)
	original := s.Ruleset.Files["dnd/stats.toml"]
	libraryApply(t, s, "renameFile", "dnd/stats.toml", "dnd/характеристики.toml")
	libraryApply(t, s, "moveDirectory", "dnd", "monsters/dnd")
	manifest := s.Ruleset.Files["monsters/dnd/характеристики.toml"]
	if manifest.Source != original.Source || manifest.Digest != original.Digest {
		t.Fatal("moving rewrote source")
	}
	if _, ok := s.Ruleset.Registry.Stats["hp"]; !ok {
		t.Fatal("lost stat ID")
	}
	if _, ok := s.Ruleset.Registry.Presets["goblin"]; !ok {
		t.Fatal("lost preset ID")
	}
	if _, ok := s.Ruleset.Files["dnd/stats.toml"]; ok {
		t.Fatal("old path still active")
	}
	text, err := RenderRulesetFileTOML(s.Ruleset, "monsters/dnd/характеристики.toml")
	if err != nil || text != libraryRulesTOML {
		t.Fatalf("changed TOML during move: %v %q", err, text)
	}
}
func TestLibraryMoveDirectoryCarriesNestedAndEmpty(t *testing.T) {
	s := libraryFixture(t)
	libraryApply(t, s, "createDirectory", "pictures/monsters/unused", "")
	libraryApply(t, s, "moveDirectory", "pictures", "library/art")
	if s.Ruleset.Media["library/art/monsters/goblin.png"] != "img1" {
		t.Fatal("image not moved")
	}
	if !slicesContains(s.LibraryDirectories, "library/art/monsters/unused") {
		t.Fatal("explicit child directory not moved")
	}
	if s.Ruleset.Registry.Presets["goblin"].Artwork != "library/art/monsters/goblin.png" {
		t.Fatal("artwork not changed")
	}
}
func TestLibraryMoveImageUpdatesArtworkWithSameAsset(t *testing.T) {
	s := libraryFixture(t)
	s.CharacterInstances["character-a12f"] = CharacterInstance{ID: "character-a12f", Name: "Валера", Persistent: true, PresetID: "goblin", ResourceCurrent: map[string]StatValue{"hp": IntegerStatValue(6)}, ArtworkPath: "pictures/monsters/goblin.png", ResourceCurrent: map[string]StatValue{"hp": IntegerStatValue(6)}}
	before := s.Ruleset.Files["dnd/stats.toml"].Source
	libraryApply(t, s, "moveFile", "pictures/monsters/goblin.png", "pictures/goblin.png")
	if s.Ruleset.Media["pictures/goblin.png"] != "img1" {
		t.Fatal("asset ID changed")
	}
	if s.Ruleset.Registry.Presets["goblin"].Artwork != "pictures/goblin.png" {
		t.Fatal("preset artwork not updated")
	}
	if s.CharacterInstances["character-a12f"].ArtworkPath != "pictures/goblin.png" {
		t.Fatal("character artwork not updated")
	}
	if s.Ruleset.Files["dnd/stats.toml"].Source != before {
		t.Fatal("original source overwritten")
	}
	edited, err := RenderRulesetFileTOML(s.Ruleset, "dnd/stats.toml")
	if err != nil || !strings.Contains(edited, `artwork = "pictures/goblin.png" # image comment`) {
		t.Fatalf("source preserving artwork: %v %s", err, edited)
	}
	editedChar, err := CurrentCharacterTOML(s, "character-a12f")
	if err != nil || !bytes.Contains(editedChar, []byte("pictures/goblin.png")) {
		t.Fatalf("character export artwork: %v %s", err, editedChar)
	}
}
func TestLibraryMoveUnknownTOMLIsOpaque(t *testing.T) {
	s := libraryFixture(t)
	original := s.UnknownTOML["notes/unknown.toml"]
	libraryApply(t, s, "moveFile", "notes/unknown.toml", "pictures/custom.toml")
	if s.UnknownTOML["pictures/custom.toml"] != original {
		t.Fatal("unknown TOML changed")
	}
}
func TestLibraryPathsRejectDangerousAndCollidingNames(t *testing.T) {
	invalid := []string{"", "../escape.toml", "/absolute/path", "C:/absolute", "bad\\path", "foo//bar", "foo/..", "foo/CON.txt", "foo/com1.txt", "foo/NUL", "dot.", "bad?.png", "file\x00.png", "foo/\n.toml", strings.Repeat("a", 256)}
	for _, p := range invalid {
		if err := validateLibraryPath(p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
	for _, p := range []string{"players/Валера-a12f.toml", "pictures/чёрный дракон.webp", "a/b/c"} {
		if err := validateLibraryPath(p); err != nil {
			t.Errorf("rejected %q: %v", p, err)
		}
	}
	s := libraryFixture(t)
	for _, req := range []libraryRequest{
		{Operation: "moveFile", Path: "dnd/stats.toml", Target: "notes/unknown.toml"},
		{Operation: "moveFile", Path: "dnd/stats.toml", Target: "NOTES/UNKNOWN.TOML"},
		{Operation: "moveDirectory", Path: "pictures", Target: "pictures/inside"},
		{Operation: "moveFile", Path: "notes/unknown.toml", Target: "pictures/monsters/goblin.png"},
		{Operation: "createDirectory", Path: "DND"},
		{Operation: "createDirectory", Path: "dnd/stats.toml/nested"},
	} {
		before, _ := json.Marshal(s)
		candidate := cloneLibrarySession(s)
		if err := applyLibraryOperation(candidate, req); err == nil {
			t.Errorf("accepted unsafe operation %+v", req)
		}
		after, _ := json.Marshal(s)
		if !bytes.Equal(before, after) {
			t.Fatal("failed operation changed live session")
		}
	}
}
func TestLibraryCannotDeleteReferencedImage(t *testing.T) {
	s := libraryFixture(t)
	err := applyLibraryOperation(cloneLibrarySession(s), libraryRequest{Operation: "deleteFile", Path: "pictures/monsters/goblin.png"})
	if err == nil || !strings.Contains(err.Error(), "preset:goblin") {
		t.Fatalf("expected dependency list: %v", err)
	}
}
func TestLibraryCharacterFileReadableNameAndManualOverride(t *testing.T) {
	s := libraryFixture(t)
	if err := syncLibraryCharacterPaths(s); err != nil {
		t.Fatal(err)
	}
	source := s.CharacterSources["character-a12f"]
	if !strings.Contains(source.FileName, "Валера-") || !source.AutoName {
		t.Fatalf("missing readable path: %+v", source)
	}
	libraryApply(t, s, "renameFile", source.FileName, "characters/Свой файл.toml")
	char := s.CharacterInstances["character-a12f"]
	char.Name = "Новое имя"
	s.CharacterInstances[char.ID] = char
	if err := syncLibraryCharacterPaths(s); err != nil {
		t.Fatal(err)
	}
	if actual := s.CharacterSources[char.ID].FileName; actual != "characters/Свой файл.toml" {
		t.Fatalf("manual name changed: %q", actual)
	}
	s2 := libraryFixture(t)
	if err := syncLibraryCharacterPaths(s2); err != nil {
		t.Fatal(err)
	}
	char = s2.CharacterInstances[char.ID]
	char.Name = "Новое имя"
	s2.CharacterInstances[char.ID] = char
	if err := syncLibraryCharacterPaths(s2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s2.CharacterSources[char.ID].FileName, "Новое имя-") {
		t.Fatal("auto name did not follow display name")
	}
}
func TestLibraryCharacterGeneratedCollisionExtendsSuffix(t *testing.T) {
	s := libraryFixture(t)
	char := s.CharacterInstances["character-a12f"]
	occupied := characterSuggestedPath(char, 8)
	s.UnknownTOML[occupied] = "[custom]\na=1\n"
	if err := syncLibraryCharacterPaths(s); err != nil {
		t.Fatal(err)
	}
	next := s.CharacterSources[char.ID].FileName
	if next == occupied || !strings.HasPrefix(next, strings.TrimSuffix(occupied, ".toml")) {
		t.Fatalf("incorrect suffix extension %q -> %q", occupied, next)
	}
}
func TestLibraryNonPersistentCharacterNotInTreeOrExport(t *testing.T) {
	s := libraryFixture(t)
	s.CharacterInstances["temp"] = NewCharacterInstance("temp", nil)
	if err := syncLibraryCharacterPaths(s); err != nil {
		t.Fatal(err)
	}
	for _, f := range libraryTree(s).Files {
		if f.CharacterID == "temp" {
			t.Fatal("temporary character file in tree")
		}
	}
	files, manifest, err := portableExportFiles(&portableExportSnapshot{Session: *s})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range manifest.CharacterFiles {
		if strings.Contains(name, "temp") {
			t.Fatal("temporary character exported")
		}
	}
	_ = files
}
func TestLibraryJSONRoundTripPreservesDirectoriesAndSources(t *testing.T) {
	s := libraryFixture(t)
	if err := syncLibraryCharacterPaths(s); err != nil {
		t.Fatal(err)
	}
	libraryApply(t, s, "moveDirectory", "pictures", "art")
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var next Session
	if err = json.Unmarshal(data, &next); err != nil {
		t.Fatal(err)
	}
	if err = validateLibrary(&next); err != nil {
		t.Fatal(err)
	}
	if len(next.LibraryDirectories) == 0 || next.Ruleset.Media["art/monsters/goblin.png"] != "img1" || next.CharacterSources["character-a12f"].FileName == "" {
		t.Fatal("paths did not survive reload")
	}
}
func TestLibraryExportKeepsEmptyFolderAndUnknownTOML(t *testing.T) {
	s := libraryFixture(t)
	if err := syncLibraryCharacterPaths(s); err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotPortableExport(s)
	files, manifest, err := portableExportFiles(&snapshot, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slicesContains(manifest.Directories, "empty/sub") {
		t.Fatal("empty folder not in manifest")
	}
	if !bytes.Equal(files["unknown/notes/unknown.toml"], []byte(s.UnknownTOML["notes/unknown.toml"])) {
		t.Fatal("opaque TOML not preserved")
	}
	var buf bytes.Buffer
	if err := ExportPortablePackage(context.Background(), snapshot, "", false, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("unknown/notes/unknown.toml")) {
		t.Fatal("opaque file absent from ZIP")
	}
}
func TestLibraryArtworkSourcePreservedForImportedCharacter(t *testing.T) {
	s := libraryFixture(t)
	source := `schema_version = 1
# custom character comment
[character]
id = "character-a12f"
name = "Валера"
preset_id = "goblin"
persistent = true
materialized = false
artwork = "pictures/monsters/goblin.png" # imported artwork
[character.resource_current]
hp = 6 # damage
`
	s.CharacterSources["character-a12f"] = CharacterTOMLSource{FileName: "players/Валера-a12f.toml", Source: source}
	inst := s.CharacterInstances["character-a12f"]
	inst.ArtworkPath = "pictures/monsters/goblin.png"
	s.CharacterInstances[inst.ID] = inst
	libraryApply(t, s, "renameFile", "pictures/monsters/goblin.png", "pictures/monsters/new.png")
	got, err := CurrentCharacterTOML(s, inst.ID)
	if err != nil || !strings.Contains(string(got), `artwork = "pictures/monsters/new.png" # imported artwork`) || !strings.Contains(string(got), "# custom character comment") {
		t.Fatalf("character source was not minimally edited: %v %s", err, got)
	}
	if s.CharacterSources[inst.ID].Source != source {
		t.Fatal("imported original source overwritten")
	}
}
func TestLibrarySaveRestartWithEmptyFolder(t *testing.T) {
	root := t.TempDir()
	s, err := newServer(root)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/sessions", strings.NewReader(`{"Name":"Library"}`))
	record := httptest.NewRecorder()
	s.create(record, request)
	if record.Code != 200 {
		t.Fatalf("create campaign: %d %s", record.Code, record.Body.String())
	}
	var ss *Session
	for _, session := range s.sessions {
		ss = session
	}
	if ss == nil {
		t.Fatal("no session created")
	}
	ss.LibraryDirectories = []string{"gallery/пусто"}
	ss.UnknownTOML = map[string]string{"gallery/info.toml": `# custom
[value]
x=1
`}
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newServer(root)
	if err != nil {
		t.Fatal(err)
	}
	saved := reopened.sessions[ss.ID]
	if !slicesContains(saved.LibraryDirectories, "gallery/пусто") || saved.UnknownTOML["gallery/info.toml"] != ss.UnknownTOML["gallery/info.toml"] {
		t.Fatal("library structure changed across restart")
	}
}
func TestLibraryArtworkMissingPathRetained(t *testing.T) {
	s := libraryFixture(t)
	preset := s.Ruleset.Registry.Presets["goblin"]
	preset.Artwork = "pictures/missing.png"
	s.Ruleset.Registry.Presets["goblin"] = preset
	artwork := resolvePresetArtwork(&preset, s.Ruleset, s.Assets)
	if artwork.Status != "missing" || artwork.Path != "pictures/missing.png" {
		t.Fatalf("missing artwork path lost: %+v", artwork)
	}
	rendered, err := RenderRulesetFileTOML(s.Ruleset, "dnd/stats.toml")
	if err != nil || !strings.Contains(rendered, "pictures/missing.png") {
		t.Fatalf("missing path lost during render: %v %s", err, rendered)
	}
}
