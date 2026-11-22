package main

import (
	"os"
	"testing"
)

func TestDefinitionExampleUsesV04(t *testing.T) {
	data, err := os.ReadFile("examples/definitions/fantasy-ruleset.toml")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := ParseRulesetTOML(data)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.usesV04() || len(snapshot.actionPlans) != 3 || len(snapshot.Characters) != 2 {
		t.Fatal("example must contain v0.4 characters and executable actions")
	}
}
