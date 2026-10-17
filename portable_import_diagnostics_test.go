package main

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func testImportBase() *Session {
	return &Session{
		Ruleset:             RulesetSnapshot{Registry: emptyRulesetRegistry(), Files: map[string]RulesetFileManifest{}},
		CampaignDefinitions: emptyCampaignRegistry(),
		CharacterInstances:  map[string]CharacterInstance{},
	}
}

func TestPortableImportReportsIndependentDefinitionErrors(t *testing.T) {
	archive := portableImportArchive{Ruleset: map[string]string{
		"a.toml": "schema_version = 1\n[stats.strength]\ntype = \"integer\"\n[actions.strike]\nname = \"Strike\"\n",
		"b.toml": "schema_version = 1\n[stats.strength]\ntype = \"integer\"\n[actions.strike]\nname = \"Strike again\"\n",
		"c.toml": "schema_version = 1\n[stats.hp]\ntype = \"integer\"\ndefault = \"wrong type\"\n[presets.goblin]\nname = \"Goblin\"\n[presets.goblin.stats]\nmissing_stat = 4\n",
	}}
	issues, incomplete := diagnosePortableImport(testImportBase(), archive, portableImportOptions{Mode: "replace"})
	if !incomplete || len(issues) < 4 {
		t.Fatalf("expected several independent errors and incomplete candidate checks, got %#v", issues)
	}
	joined := ""
	for _, issue := range issues {
		joined += issue.File + ": " + issue.Message + "\n"
	}
	for _, want := range []string{"Дублирующееся определение stat", "Дублирующееся определение action", "значение по умолчанию", "неизвестная характеристика"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing diagnostic %q in %s", want, joined)
		}
	}
}

func TestPortableImportReadsAllMalformedTOMLsBeforePreview(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, contents := range map[string]string{
		"bad-syntax.toml": "[stats.bad\n",
		"bad-schema.toml": "schema_version = 1\n[actions.attack]\nname = 42\n",
		"valid.toml":      "schema_version = 1\n[stats.strength]\ntype = \"integer\"\n",
	} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := readPortableImportArchiveWithDiagnostics(reader, true)
	if err != nil {
		t.Fatalf("independent TOML errors must not abort scanning: %v", err)
	}
	if len(parsed.Diagnostics) != 2 || len(parsed.Ruleset) != 1 {
		t.Fatalf("expected two malformed TOMLs and one valid definition, got errors=%#v, files=%#v", parsed.Diagnostics, parsed.Ruleset)
	}
}
