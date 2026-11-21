package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

const v04Stats = `# Comments and formatting are part of the library file.
[stat.basic.strength]
name = "Strength"
default = 16 # inferred integer
bonus = { formula = "floor((value - 10) / 2)" }
kind = "custom"
"free.field" = { info = ["custom", 9007199254740993, 2.0, 1979-05-27] }
notes.lines = "a\nb"
[stat.hp]
default = 100
resource = true
[stat."basic.strength"]
default = "quoted path segment"
[stat.open]
default = false
[stat.proficiency]
default = { formula = "stat.basic.strength.bonus" }
`

const v04Actions = `[action.weapon.attack]
name = "Attack"
[action.weapon.attack.hit]
target = "select(character)"
roll = "1d20 + stat.basic.strength.bonus"
var."saving throw DC" = "8 + stat.proficiency"
success = "roll >= target.stat.hp"
rolls = "custom legacy name"
[action.weapon.attack.hit.on.success]
message = "Hit!"
apply = { path = "target.stat.hp.current", formula = "value - roll" }
[action.weapon.attack.hit.on.fail]
message = "Miss!"
[action.weapon.attack.damage]
roll = "dice(source.damage)"
apply = { path = "actor.stat.basic.strength.base", formula = "value - 1" }
`

const v04Objects = `[effect.proficiency.weapon]
target = ["action.weapon.attack.hit.roll", "stat.basic.strength.bonus"]
formula = "target + stat.proficiency"
priority = 2
when = "source.weapon_group == 'martial'"
scope = "source"
stack = "unique"
[object.weapons.runic_sword]
actions = ["action.weapon.attack"]
effects = ["effect.proficiency.weapon"]
damage = "1d8"
two_handed = true
[object.empty]
`

const v04Character = `[character.party.hero]
name = "Hero"
actions = ["action.weapon.attack"]
effects = ["effect.proficiency.weapon"]
portrait = { formula = "source.art" }
[character.party.hero.stat.basic.strength]
base = 17
[character.party.hero.stat.hp]
current = 85
[[character.party.hero.inventory]]
id = "item-001"
object = "object.weapons.runic_sword"
equipped = true
inscription = "custom inventory field"
[[character.party.hero.inventory]]
id = "item-002"
object = "object.weapons.runic_sword"
`

func TestRulesetV04EntitiesAndRoundTrip(t *testing.T) {
	files := map[string]string{
		"rules/core/stats.toml": v04Stats, "rules/actions.toml": v04Actions,
		"rules/items.toml": v04Objects, "party/hero.toml": v04Character,
	}
	snapshot, err := CompileRulesetFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ExecutionReady() || snapshot.executionDiagnostic() == "" {
		t.Fatal("v0.4 must report unsupported execution")
	}
	strength := snapshot.Registry.Stats["basic.strength"]
	if strength.Type != StatTypeInteger || strength.Default.Integer() != 16 || strength.V04.values["kind"] != "custom" {
		t.Fatalf("wrong inferred stat or lost custom field: %+v", strength)
	}
	if open := snapshot.Registry.Stats["open"]; open.Type != StatTypeBoolean || open.Default.Boolean() ||
		snapshot.Registry.Stats["hp"].Kind != StatKindResource ||
		snapshot.Registry.Stats["proficiency"].Type != "" || snapshot.Registry.Stats["proficiency"].Default != nil ||
		snapshot.Registry.Stats["\"basic.strength\""].Type != StatTypeString {
		t.Fatal("incorrect literal, resource or formula stat projection")
	}
	if ids := sortedDefinitionIDs(snapshot.Registry.Actions); !slices.Equal(ids, []string{"weapon.attack", "weapon.attack.damage", "weapon.attack.hit"}) {
		t.Fatalf("expected group and two actions, got %v", ids)
	}
	if snapshot.Registry.Actions["weapon.attack.hit"].V04.values["rolls"] != "custom legacy name" ||
		snapshot.Registry.Effects["proficiency.weapon"] == nil || snapshot.Registry.Objects["weapons.runic_sword"] == nil {
		t.Fatal("lost action custom data, effects or objects")
	}
	items := snapshot.Characters["party.hero"].values["inventory"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["inscription"] != "custom inventory field" || len(snapshot.Registry.Presets) != 0 {
		t.Fatal("lost character inventory or interpreted character as a preset")
	}
	for name, source := range files {
		if got, err := RenderRulesetFileTOML(snapshot, name); err != nil || got != source {
			t.Fatalf("source %q changed: %v", name, err)
		}
	}

	data, err := ExportRulesetSnapshotTOML(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("schema_version")) {
		t.Fatal("v0.4 unexpectedly gained schema_version")
	}
	roundTrip, err := ParseRulesetTOML(data)
	if err != nil || !equalRulesetV04Documents(snapshot.v04Document(nil), roundTrip.v04Document(nil)) {
		t.Fatalf("canonical round-trip differed: %v\n%s", err, data)
	}
	exact, err := ExportRulesetSnapshotTOML(roundTrip)
	if err != nil || !bytes.Equal(exact, data) {
		t.Fatalf("single-file export changed its original source: %v", err)
	}

	persisted, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var loaded RulesetSnapshot
	if err := json.Unmarshal(persisted, &loaded); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRulesetSnapshot(loaded); err != nil {
		t.Fatal(err)
	}
	custom := loaded.Registry.Stats["basic.strength"].V04.values["free.field"].(map[string]any)["info"].([]any)
	if custom[1] != int64(9007199254740993) || custom[2] != float64(2) || custom[3] != (toml.LocalDate{Year: 1979, Month: 5, Day: 27}) {
		t.Fatalf("persisted custom fields changed: %#v", custom)
	}
	if !equalRulesetV04Documents(snapshot.v04Document(nil), loaded.v04Document(nil)) {
		t.Fatal("persisted registry changed")
	}
}

func TestRulesetV04Validation(t *testing.T) {
	for _, tc := range []struct{ name, source, errorContains string }{
		{"stat missing default", "[stat.basic.hp]\nname = 'HP'\n", "default"},
		{"stat wrong default type", "[stat.hp]\ndefault = 3.0\ntype = 'integer'\n", "default"},
		{"stat formula type", "[stat.hp]\ndefault = {formula = 3}\n", "formula"},
		{"resource type", "[stat.hp]\ndefault = 1\nresource = 'yes'\n", "resource"},
		{"duplicate same file", "[stat.hp]\ndefault = 1\n[stat.hp]\ndefault = 2\n", ""},
		{"effect missing formula", "[effect.bonus]\ntarget = 'stat.hp'\n", "formula"},
		{"effect unknown stat field", "[stat.hp]\ndefault = 1\n[effect.bad]\ntarget = 'stat.hp.missing'\nformula = 'target + 1'\n", "target"},
		{"effect unknown action roll", "[action.r]\nmessage = 'hi'\n[effect.bad]\ntarget = 'action.r.roll'\nformula = 'target + 1'\n", "target"},
		{"action empty group", "[action.weapon.attack]\nname = 'Attack'\n", "executable descendant"},
		{"action invalid target", "[action.check]\ntarget = 'select(enemy)'\nroll = '1d20'\n", "target selector"},
		{"action var type", "[action.check]\nroll = '1d20'\nvar.dc = 15\n", "var.dc"},
		{"old rolls have no semantics", "[action.check]\nrolls = ['not a v0.4 roll']\n", "executable descendant"},
		{"outcome needs parent success", "[action.check]\nroll = '1d20'\n[action.check.on.success]\nmessage = 'ok'\n", "parent success"},
		{"outcome empty", "[action.check]\nroll = '1d20'\nsuccess = 'true'\n[action.check.on.fail]\ncustom = true\n", "on.fail"},
		{"outcome not referenceable", "[action.check]\nroll = '1d20'\nsuccess = 'true'\n[action.check.on.fail]\nmessage = 'fail'\n[object.x]\nactions = ['action.check.on.fail']\n", "unknown action"},
		{"apply missing formula", "[action.a]\napply = {path = 'actor.x'}\n", "formula"},
		{"apply non-resource", "[stat.hp]\ndefault = 1\n[action.a]\napply = {path = 'actor.stat.hp.current', formula = 'value + 1'}\n", "resource current"},
		{"object unknown action", "[object.x]\nactions = ['action.missing']\n", "unknown action"},
		{"object unknown effect", "[object.x]\neffects = ['effect.missing']\n", "unknown effect"},
		{"character unknown effect", "[character.x]\neffects = ['effect.missing']\n", "unknown effect"},
		{"character unknown stat", "[character.x.stat.basic.hp]\nbase = 2\n", "unknown stat"},
		{"character stat wrong type", "[stat.hp]\ndefault = 1\n[character.x.stat.hp]\nbase = 2.0\n", "incompatible type"},
		{"character computed base", "[stat.hp]\ndefault = {formula='4'}\n[character.x.stat.hp]\nbase = 2\n", "cannot override base"},
		{"inventory missing id", "[[character.x.inventory]]\nobject = 'object.missing'\n", "id"},
		{"inventory unknown object", "[[character.x.inventory]]\nid = 'one'\nobject = 'object.missing'\n", "unknown object"},
		{"inventory duplicate id", "[object.sword]\n[[character.x.inventory]]\nid = 'one'\nobject = 'object.sword'\n[[character.x.inventory]]\nid = 'one'\nobject = 'object.sword'\n", "duplicate inventory id"},
		{"legacy schema rejected", "schema_version = 1\n[stat.hp]\ndefault = 1\n", "schema_version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRulesetTOML([]byte(tc.source))
			if err == nil || !strings.Contains(err.Error(), tc.errorContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errorContains, err)
			}
		})
	}

	for kind, content := range map[string]string{
		"stat":      "[stat.deep.nested.x]\ndefault = 1\n",
		"effect":    "[effect.deep.nested.x]\ntarget = 'stat.hp'\nformula = 'target'\n",
		"action":    "[action.deep.nested.x]\nmessage = 'x'\n",
		"object":    "[object.deep.nested.x]\n",
		"character": "[character.deep.nested.x]\n",
	} {
		t.Run("duplicate "+kind, func(t *testing.T) {
			_, err := CompileRulesetFiles(map[string]string{"a.toml": content, "b.toml": content})
			var conflict *RulesetDefinitionConflictError
			if !errors.As(err, &conflict) || conflict.Kind != kind || conflict.ID != "deep.nested.x" || conflict.FirstFile != "a.toml" || conflict.SecondFile != "b.toml" {
				t.Fatalf("unexpected duplicate error: %v", err)
			}
		})
	}
}

func TestRulesetV04DeepPathsAndNamespaceConflicts(t *testing.T) {
	id := strings.Repeat("branch.", 180) + "leaf"
	source := fmt.Sprintf("[stat.%s]\ndefault = 10\nresource = true\n[character.hero.stat.%s]\ncurrent = 5\n", id, id)
	snapshot, err := CompileRulesetFiles(map[string]string{
		"stats.toml": source, "action.toml": "[action.combat]\nname = 'Combat'\n",
		"child.toml": fmt.Sprintf("[action.combat.%s]\nroll = '1d20'\n[stat.namespace.default.hp]\ndefault = 2\n", id),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Registry.Stats[id].V04 == nil || snapshot.Registry.Actions["combat."+id].V04 == nil ||
		snapshot.Registry.Stats["namespace.default.hp"].V04 == nil {
		t.Fatal("deep ID was truncated or flattened")
	}
	data, err := ExportRulesetSnapshotTOML(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseRulesetTOML(data); err != nil {
		t.Fatal(err)
	}
	if _, err := CompileRulesetFiles(map[string]string{
		"a.toml": "[object.foo]\nbar = 1\n", "b.toml": "[object.foo.bar.child]\n",
	}); err == nil || !strings.Contains(err.Error(), "conflicts with a field") {
		t.Fatalf("field collision across sources: %v", err)
	}
}

func TestRulesetV04HTTPInstall(t *testing.T) {
	server := testServer(t, t.TempDir())
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Ruleset v0.4"})
	extraEffect := "[effect.second]\ntarget = 'action.weapon.attack.hit.roll'\nformula = 'target * 2'\npriority = 2\n"
	files := map[string]string{
		"core.toml": v04Stats + v04Actions, "items.toml": v04Objects + extraEffect, "hero.toml": v04Character,
	}
	status, body := rulesetPackageHTTP(t, host.URL, "/api/definitions/ruleset/preview", gm, files, nil)
	if status != http.StatusOK {
		t.Fatalf("preview: %d %s", status, body)
	}
	var preview definitionPreviewResponse
	if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatal(err)
	}
	if preview.ExecutionDiagnostic == "" || len(preview.RulesetWarnings) != 1 ||
		!slices.Equal(preview.RulesetDiff.Characters.Added, []string{"party.hero"}) {
		t.Fatalf("incorrect preview diagnostics or diff: %+v", preview)
	}
	apply := url.Values{
		"expectedRevision": {fmt.Sprint(preview.RegistryRevision)}, "digest": {preview.Digest}, "key": {"v04-install"},
	}
	status, body = rulesetPackageHTTP(t, host.URL, "/api/definitions/ruleset/apply", gm, files, apply)
	if status != http.StatusOK {
		t.Fatalf("apply: %d %s", status, body)
	}
	status, registry := readDefinitionsHTTP(t, host.URL, gm)
	if status != http.StatusOK || registry.Ruleset.Files["hero.toml"].Source != v04Character ||
		registry.Ruleset.Characters["party.hero"] == nil || registry.ExecutionDiagnostic == "" {
		t.Fatalf("ruleset not installed: %d %+v", status, registry)
	}
	files["items.toml"] = strings.ReplaceAll(files["items.toml"], "[object.weapons.runic_sword]", "[object.weapons.renamed]")
	status, body = rulesetPackageHTTP(t, host.URL, "/api/definitions/ruleset/preview", gm, files, nil)
	if status != http.StatusBadRequest || !bytes.Contains(body, []byte("unknown object")) {
		t.Fatalf("invalid replacement preview: %d %s", status, body)
	}
	_, current := readDefinitionsHTTP(t, host.URL, gm)
	if current.RegistryRevision != registry.RegistryRevision || current.Ruleset.Files["items.toml"].Source != v04Objects+extraEffect {
		t.Fatal("invalid replacement changed the installed ruleset")
	}
}

func TestRulesetV04ZIPLibraryAndExecutionBoundary(t *testing.T) {
	saved := NewCharacterInstance("party.hero", nil)
	saved.Persistent = true
	savedTOML, err := EncodeCharacterTOML(saved, nil, EffectiveRegistry{})
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string][]byte{
		"rules/stats.toml": []byte(v04Stats), "rules/actions.toml": []byte(v04Actions),
		"rules/items.toml": []byte(v04Objects), "party/hero.toml": []byte(v04Character),
		"saved/hero.toml": savedTOML,
	}
	archive, err := plainArchive(t, entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Ruleset) != 4 || len(archive.Characters) != 1 || len(archive.Unknown) != 0 {
		t.Fatalf("wrong ZIP TOML classification: %+v", archive)
	}
	base := &Session{RegistryRevision: 1, CharacterRevision: 1, CharacterSources: map[string]CharacterTOMLSource{}}
	opts := portableImportOptions{Mode: "replace"}
	if issues, incomplete := diagnosePortableImport(base, archive, opts); len(issues) != 0 || incomplete {
		t.Fatalf("unexpected ZIP diagnostics: %+v", issues)
	}
	candidate, err := buildPortableImportCandidate(t.Context(), base, archive, opts, t.TempDir(), map[string]Asset{}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(base.CharacterInstances) != 0 || len(base.Ruleset.Files) != 0 || len(candidate.Characters) != 1 ||
		candidate.Ruleset.Characters["party.hero"] == nil || candidate.Characters["party.hero"].Name != saved.Name ||
		!slices.Equal(candidate.Preview.RulesetDiff.Characters.Added, []string{"party.hero"}) {
		t.Fatal("unexpected character import or mutation of base")
	}
	restored := &Session{Ruleset: candidate.Ruleset, CharacterInstances: candidate.Characters, CharacterSources: candidate.Sources,
		Assets: candidate.Assets, RegistryRevision: 1, CharacterRevision: 1}
	var result bytes.Buffer
	if err := ExportPortablePackage(t.Context(), snapshotPortableExport(restored), t.TempDir(), false, &result); err != nil {
		t.Fatal(err)
	}
	exported := unpackPortable(t, result.Bytes())
	for path, wanted := range entries {
		if !bytes.Equal(exported[path], wanted) {
			t.Fatalf("ZIP did not preserve %s", path)
		}
	}

	if _, err := resolveRoll(restored, nil, nil, RollRequest{ActionID: "weapon.attack.hit"}); err == nil || err.Error() != rulesetV04ExecutionDiagnostic {
		t.Fatalf("expected execution diagnostic, got %v", err)
	}
	changed := cloneLibrarySession(restored)
	if err := applyLibraryOperation(changed, libraryRequest{Operation: "deleteFile", Path: "rules/items.toml"}); err == nil {
		t.Fatal("library deleted an object/effect referenced by another file")
	}
	if len(restored.Ruleset.Registry.Objects) != 2 || len(restored.Ruleset.Files) != 4 {
		t.Fatal("failed library operation changed the live ruleset")
	}
	changed = cloneLibrarySession(restored)
	if err := applyLibraryOperation(changed, libraryRequest{Operation: "moveFile", Path: "rules/items.toml", Target: "items/armory.toml"}); err != nil {
		t.Fatal(err)
	}
	owner, err := definitionFileOwner(changed.Ruleset, "object", "weapons.runic_sword")
	if err != nil || owner != "items/armory.toml" || changed.Ruleset.Files[owner].Source != v04Objects {
		t.Fatalf("move lost source ownership: %q, %v", owner, err)
	}
}
