package main

import (
	"errors"
	"fmt"
	"strings"
)

// applyRulesetDefinitionCommand edits the live registry, not the TOML source.
// Original file bytes and ownership remain in RulesetFileManifest until an
// explicit replacement/upload. The source view is built only on request.
func applyRulesetDefinitionCommand(session *Session, command Command) (RulesetSnapshot, bool, error) {
	if command.ExpectedRegistryRevision == nil || *command.ExpectedRegistryRevision != session.RegistryRevision {
		return RulesetSnapshot{}, false, fmt.Errorf("stale registry revision: current is %d", session.RegistryRevision)
	}
	original := session.Ruleset
	if len(original.Files) == 0 && rulesetSnapshotHasContent(original) {
		var err error
		original, err = RulesetSnapshotWithSources(original)
		if err != nil {
			return RulesetSnapshot{}, false, err
		}
	}
	candidate := cloneRulesetSnapshot(original)
	kind, id := command.DefinitionKind, command.DefinitionID
	if !effectiveHasDefinition(EffectiveRegistry{Stats: original.Registry.Stats, Actions: original.Registry.Actions, Presets: original.Registry.Presets}, kind, id) {
		return RulesetSnapshot{}, false, errors.New("ruleset definition not found")
	}
	if definitionExistsInCampaign(session.CampaignDefinitions, kind, id) {
		return RulesetSnapshot{}, false, errors.New("campaign override must be edited separately")
	}
	owner := ""
	for filename, manifest := range original.Files {
		var ids []string
		switch kind {
		case definitionKindStat:
			ids = manifest.Stats
		case definitionKindAction:
			ids = manifest.Actions
		case definitionKindPreset:
			ids = manifest.Presets
		default:
			return RulesetSnapshot{}, false, errors.New("unknown definition kind")
		}
		for _, candidateID := range ids {
			if candidateID == id {
				if owner != "" {
					return RulesetSnapshot{}, false, errors.New("multiple source owners")
				}
				owner = filename
			}
		}
	}
	if owner == "" {
		return RulesetSnapshot{}, false, errors.New("source owner not found")
	}
	effective, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		return RulesetSnapshot{}, false, err
	}
	if command.Type == "definitionRulesetUpdate" {
		switch kind {
		case definitionKindStat:
			if command.StatDefinition == nil || command.StatDefinition.ID != id {
				return RulesetSnapshot{}, false, errors.New("stat definition and id required")
			}
			if old := original.Registry.Stats[id]; old.Type != command.StatDefinition.Type {
				if refs := definitionReferences(session, effective, kind, id); len(refs) > 0 {
					return RulesetSnapshot{}, false, fmt.Errorf("cannot change type of referenced stat %q: %s", id, strings.Join(refs, ", "))
				}
			}
			candidate.Registry.Stats[id] = cloneStatDefinition(*command.StatDefinition)
		case definitionKindAction:
			if command.ActionDefinition == nil || command.ActionDefinition.ID != id {
				return RulesetSnapshot{}, false, errors.New("action definition and id required")
			}
			candidate.Registry.Actions[id] = cloneActionDefinition(*command.ActionDefinition)
		case definitionKindPreset:
			if command.PresetDefinition == nil || command.PresetDefinition.ID != id {
				return RulesetSnapshot{}, false, errors.New("preset definition and id required")
			}
			candidate.Registry.Presets[id] = clonePresetDefinition(*command.PresetDefinition)
		}
	} else if command.Type == "definitionRulesetDelete" {
		if refs := definitionReferences(session, effective, kind, id); len(refs) > 0 {
			return RulesetSnapshot{}, false, fmt.Errorf("definition %q is referenced by %s", id, strings.Join(refs, ", "))
		}
		manifest := candidate.Files[owner]
		remove := func(ids []string) []string {
			out := make([]string, 0, len(ids))
			for _, item := range ids {
				if item != id {
					out = append(out, item)
				}
			}
			return out
		}
		switch kind {
		case definitionKindStat:
			delete(candidate.Registry.Stats, id)
			manifest.Stats = remove(manifest.Stats)
		case definitionKindAction:
			delete(candidate.Registry.Actions, id)
			manifest.Actions = remove(manifest.Actions)
		case definitionKindPreset:
			delete(candidate.Registry.Presets, id)
			manifest.Presets = remove(manifest.Presets)
		}
		candidate.Files[owner] = manifest
	} else {
		return RulesetSnapshot{}, false, errors.New("unknown ruleset command")
	}
	if err := validateDefinitionState(session, candidate, session.CampaignDefinitions); err != nil {
		return RulesetSnapshot{}, false, err
	}
	// Validate rewritten candidate before committing, without replacing source.
	rendered, err := RenderRulesetFileTOML(candidate, owner)
	if err != nil {
		return RulesetSnapshot{}, false, fmt.Errorf("%s %q in %s: %w", kind, id, owner, err)
	}
	pkg := rulesetPackage{Files: map[string]string{}, Media: candidate.Media}
	for filename := range candidate.Files {
		if filename == owner {
			pkg.Files[filename] = rendered
			continue
		}
		pkg.Files[filename], err = RenderRulesetFileTOML(candidate, filename)
		if err != nil {
			return RulesetSnapshot{}, false, err
		}
	}
	check, err := compileRulesetPackage(pkg)
	if err != nil {
		return RulesetSnapshot{}, false, err
	}
	if !equalRulesetRegistries(check.Registry, candidate.Registry) {
		return RulesetSnapshot{}, false, errors.New("TOML output differs from updated registry")
	}
	return candidate, !equalRulesetRegistries(original.Registry, candidate.Registry), nil
}
