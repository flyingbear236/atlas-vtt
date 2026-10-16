package main

import (
	"errors"
	"fmt"
	"strings"
)

const generatedDefinitionsFile = "generated-definitions.toml"

func campaignRegistryEmpty(reg CampaignRegistry) bool {
	return len(reg.Stats) == 0 && len(reg.Actions) == 0 && len(reg.Presets) == 0
}

// A generated file is an ordinary source owner: it participates in the same
// manifest, validation and source-preserving export as uploaded TOML files.
func ensureGeneratedDefinitionFile(snapshot *RulesetSnapshot) (string, error) {
	if snapshot.Files == nil {
		snapshot.Files = map[string]RulesetFileManifest{}
	}
	name := generatedDefinitionsFile
	if _, exists := snapshot.Files[name]; exists {
		// Existing generated file is reusable only when it was created by ATLAS.
		// Caller chooses another name if a user-owned file occupies this name.
		if strings.HasPrefix(snapshot.Files[name].Source, "# ATLAS generated definitions\n") {
			return name, nil
		}
	}
	for index := 1; index <= MaxRulesetFiles; index++ {
		if index > 1 {
			name = fmt.Sprintf("generated-definitions-%d.toml", index)
		}
		if _, exists := snapshot.Files[name]; exists {
			continue
		}
		if len(snapshot.Files) >= MaxRulesetFiles {
			return "", errors.New("cannot create generated TOML: maximum number of source files reached")
		}
		source := "# ATLAS generated definitions\nschema_version = 1\n"
		doc, err := decodeRulesetSourceFile(name, source)
		if err != nil {
			return "", err
		}
		snapshot.Files[name] = rulesetFileManifest(source, doc)
		return name, nil
	}
	return "", errors.New("cannot allocate generated TOML filename")
}

func definitionFileOwner(snapshot RulesetSnapshot, kind, id string) (string, error) {
	owner := ""
	for name, manifest := range snapshot.Files {
		var ids []string
		switch kind {
		case definitionKindStat:
			ids = manifest.Stats
		case definitionKindAction:
			ids = manifest.Actions
		case definitionKindPreset:
			ids = manifest.Presets
		default:
			return "", errors.New("unknown definition kind")
		}
		for _, candidate := range ids {
			if candidate == id {
				if owner != "" {
					return "", fmt.Errorf("%s %q has multiple owners: %q and %q", kind, id, owner, name)
				}
				owner = name
			}
		}
	}
	if owner == "" {
		return "", fmt.Errorf("%s %q has no TOML source owner", kind, id)
	}
	return owner, nil
}

func addGeneratedDefinitions(snapshot *RulesetSnapshot, additions CampaignRegistry) error {
	if campaignRegistryEmpty(additions) {
		return nil
	}
	name, err := ensureGeneratedDefinitionFile(snapshot)
	if err != nil {
		return err
	}
	manifest := snapshot.Files[name]
	for id, def := range additions.Stats {
		if _, exists := snapshot.Registry.Stats[id]; exists {
			return fmt.Errorf("duplicate stat %q: already owned by %q", id, mustDefinitionOwner(*snapshot, definitionKindStat, id))
		}
		snapshot.Registry.Stats[id] = cloneStatDefinition(def)
		manifest.Stats = append(manifest.Stats, id)
	}
	for id, def := range additions.Actions {
		if _, exists := snapshot.Registry.Actions[id]; exists {
			return fmt.Errorf("duplicate action %q: already owned by %q", id, mustDefinitionOwner(*snapshot, definitionKindAction, id))
		}
		snapshot.Registry.Actions[id] = cloneActionDefinition(def)
		manifest.Actions = append(manifest.Actions, id)
	}
	for id, def := range additions.Presets {
		if _, exists := snapshot.Registry.Presets[id]; exists {
			return fmt.Errorf("duplicate preset %q: already owned by %q", id, mustDefinitionOwner(*snapshot, definitionKindPreset, id))
		}
		snapshot.Registry.Presets[id] = clonePresetDefinition(def)
		manifest.Presets = append(manifest.Presets, id)
	}
	snapshot.Files[name] = manifest
	return nil
}

func mustDefinitionOwner(snapshot RulesetSnapshot, kind, id string) string {
	owner, err := definitionFileOwner(snapshot, kind, id)
	if err != nil {
		return "unknown"
	}
	return owner
}
