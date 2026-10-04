package main

import (
	"os"
	"testing"
)

func TestDefinitionExampleDocuments(t *testing.T) {
	rulesetData, err := os.ReadFile("examples/definitions/fantasy-ruleset.toml")
	if err != nil {
		t.Fatal(err)
	}
	ruleset, err := ParseRulesetTOML(rulesetData)
	if err != nil {
		t.Fatalf("parse example ruleset: %v", err)
	}

	campaignData, err := os.ReadFile("examples/definitions/fantasy-campaign-extensions.toml")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewCampaignDefinitionsImport(DefinitionRegistries{
		Ruleset: ruleset.Registry,
		Campaign: CampaignRegistry{
			Stats:   map[string]StatDefinition{},
			Actions: map[string]ActionDefinition{},
			Presets: map[string]CharacterPresetDefinition{},
		},
	}, campaignData)
	if err != nil {
		t.Fatalf("preview example campaign extensions: %v", err)
	}
	if preview.Registry.Presets["goblin"].Name != "Гоблин Гринвуда" {
		t.Fatal("campaign example did not create the expected preset overlay")
	}
	if preview.Registry.Actions["sword_attack"].Rolls[1].Count != 2 {
		t.Fatal("campaign example did not create the expected action overlay")
	}
	if _, ok := preview.Registry.Presets["tavern_keeper"]; !ok {
		t.Fatal("campaign example did not add its campaign-only preset")
	}
}
