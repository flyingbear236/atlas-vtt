package main

import (
	"os"
	"testing"
)

func TestDefinitionExampleCampaignExtensionsConflictWithUnifiedRegistry(t *testing.T) {
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
	_, err = PreviewCampaignDefinitionsImport(DefinitionRegistries{
		Ruleset: ruleset.Registry,
		Campaign: CampaignRegistry{
			Stats:   map[string]StatDefinition{},
			Actions: map[string]ActionDefinition{},
			Presets: map[string]CharacterPresetDefinition{},
		},
	}, campaignData)
	if err == nil {
		t.Fatal("legacy campaign overrides were accepted by the unified registry")
	}
}
