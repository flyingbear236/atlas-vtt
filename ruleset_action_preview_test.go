package main

import (
	"os"
	"strings"
	"testing"
)

func previewPart(preview ActionPreview, label string) (string, bool) {
	for _, part := range preview.Parts {
		if part.Label == label {
			return part.Value, true
		}
	}
	return "", false
}

func findActionPreview(t *testing.T, list []ActionPreview, actionID string) ActionPreview {
	t.Helper()
	for _, preview := range list {
		if preview.ActionID == actionID && preview.SourceID == "" {
			return preview
		}
	}
	t.Fatalf("missing preview for %s", actionID)
	return ActionPreview{}
}

func TestActionPreviewUpdatesAfterEquipmentAndCondition(t *testing.T) {
	source, err := os.ReadFile("examples/definitions/ruleset-v04-playground.toml")
	if err != nil {
		t.Fatal(err)
	}
	ruleset, err := ParseRulesetTOML(source)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: ruleset.Registry})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := decodeNestedCharacter("tester", ruleset.Characters["tester"], registry)
	if err != nil {
		t.Fatal(err)
	}
	show := func() ActionPreview {
		t.Helper()
		state := mustEffectiveCharacter(t, instance, nil, registry)
		return findActionPreview(t, actionPreviewsForSheet(ruleset, instance, state, registry), "lab.skill")
	}
	preview := show()
	if value, ok := previewPart(preview, "Атлетика"); !ok || value != "5" {
		t.Fatalf("equipped rings: %#v", preview)
	}
	if value, ok := previewPart(preview, "Мастерство"); !ok || value != "3" {
		t.Fatalf("enabled mastery: %#v", preview)
	}
	if preview.Parts[0].Label != "1d20" {
		t.Fatalf("dice not symbolic: %#v", preview)
	}
	for i := range instance.Inventory {
		if instance.Inventory[i].ID == "ring-a" || instance.Inventory[i].ID == "ring-b" {
			instance.Inventory[i].Equipped = false
		}
	}
	preview = show()
	if value, ok := previewPart(preview, "Атлетика"); !ok || value != "3" {
		t.Fatalf("unequipped rings: %#v", preview)
	}
	training, ok := instance.V04.values["training"].(map[string]any)
	if !ok {
		t.Fatal("missing training context")
	}
	training["ready"] = false
	preview = show()
	if _, ok := previewPart(preview, "Мастерство"); ok {
		t.Fatalf("disabled conditional effect still visible: %#v", preview)
	}
}

func TestActionPreviewDoesNotAssumeSelectedTarget(t *testing.T) {
	source, err := os.ReadFile("examples/definitions/ruleset-v04-playground.toml")
	if err != nil {
		t.Fatal(err)
	}
	ruleset, err := ParseRulesetTOML(source)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: ruleset.Registry})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := decodeNestedCharacter("tester", ruleset.Characters["tester"], registry)
	if err != nil {
		t.Fatal(err)
	}
	state := mustEffectiveCharacter(t, instance, nil, registry)
	previews := actionPreviewsForSheet(ruleset, instance, state, registry)
	for _, preview := range previews {
		if preview.ActionID != "weapon.strike.hit" {
			continue
		}
		if !strings.Contains(strings.Join(preview.Notes, " "), "цели") {
			t.Fatalf("target-dependent modifier was silently omitted: %#v", preview)
		}
		return
	}
	t.Fatal("missing weapon action preview")
}

func TestActionPreviewCannotRollIndirectVariable(t *testing.T) {
	access, err := parseExpression("var.surprise")
	if err != nil {
		t.Fatal(err)
	}
	random, err := parseExpression("1d20")
	if err != nil {
		t.Fatal(err)
	}
	frame := ExpressionFrame{Locals: map[string]*Expression{
		"var.surprise": {root: random, Type: exprNumber},
	}}
	if _, err := previewEvalNode(&frame, access); err == nil || !strings.Contains(err.Error(), "rolls are unavailable") {
		t.Fatalf("preview drew a die through var reference: %v", err)
	}
}
