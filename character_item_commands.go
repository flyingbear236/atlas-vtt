package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// CharacterObjectDraft is a bounded authoring input. References point to
// existing definitions; they cannot introduce executable TOML expressions.
type CharacterObjectDraft struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Properties  map[string]string `json:"properties,omitempty"`
	Actions     []string          `json:"actions,omitempty"`
	Effects     []string          `json:"effects,omitempty"`
}

var itemPropertyName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

func objectDraftFields(draft *CharacterObjectDraft, ruleset RulesetSnapshot) (*RulesetFields, error) {
	if draft == nil {
		return nil, errors.New("object draft required")
	}
	if err := validateText("object name", draft.Name, MaxDefinitionNameRunes, false); err != nil {
		return nil, err
	}
	if err := validateText("object description", draft.Description, 4096, true); err != nil {
		return nil, err
	}
	if len(draft.Properties) > 24 || len(draft.Actions) > 48 || len(draft.Effects) > 48 {
		return nil, errors.New("too many object properties or grants")
	}
	fields := map[string]any{"name": strings.TrimSpace(draft.Name)}
	if draft.Description != "" {
		fields["description"] = draft.Description
	}
	if len(draft.Actions) > 0 {
		fields["actions"] = make([]string, 0, len(draft.Actions))
	}
	if len(draft.Effects) > 0 {
		fields["effects"] = make([]string, 0, len(draft.Effects))
	}
	for _, id := range draft.Actions {
		if err := ruleset.validateV04Reference("action", "action."+id); err != nil {
			return nil, err
		}
		if slices.Contains(fields["actions"].([]string), "action."+id) {
			return nil, fmt.Errorf("duplicate action %q", id)
		}
		fields["actions"] = append(fields["actions"].([]string), "action."+id)
	}
	for _, id := range draft.Effects {
		if err := ruleset.validateV04Reference("effect", "effect."+id); err != nil {
			return nil, err
		}
		if slices.Contains(fields["effects"].([]string), "effect."+id) {
			return nil, fmt.Errorf("duplicate effect %q", id)
		}
		fields["effects"] = append(fields["effects"].([]string), "effect."+id)
	}
	for key, raw := range draft.Properties {
		if !itemPropertyName.MatchString(key) || key == "name" || key == "description" || key == "actions" || key == "effects" || key == "id" || key == "equipped" || key == "object" || key == "effect_overrides" {
			return nil, fmt.Errorf("invalid property %q", key)
		}
		if err := validateText("property value", raw, 512, true); err != nil {
			return nil, err
		}
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			fields[key] = n
		} else {
			fields[key] = raw
		}
	}
	return newRulesetFields(fields)
}

const createdObjectsHeader = "# ATLAS created objects\n"

// Add to the actual v0.4 ruleset package, so the object survives restart,
// imports/exports and retains the same mechanics as handwritten TOML objects.
func addCreatedObject(session *Session, draft *CharacterObjectDraft, mutation *characterMutation) (string, error) {
	if !session.Ruleset.usesV04() {
		return "", errors.New("creating objects requires a v0.4 ruleset")
	}
	fields, err := objectDraftFields(draft, session.Ruleset)
	if err != nil {
		return "", err
	}
	sources := make(map[string]string, len(session.Ruleset.Files)+1)
	for name, manifest := range session.Ruleset.Files {
		if manifest.Source == "" {
			return "", fmt.Errorf("ruleset file %q has no source", name)
		}
		sources[name] = manifest.Source
	}
	filename := "atlas-created-objects.toml"
	for i := 1; i <= MaxRulesetFiles; i++ {
		source, exists := sources[filename]
		if !exists || strings.HasPrefix(source, createdObjectsHeader) {
			break
		}
		filename = fmt.Sprintf("atlas-created-objects-%d.toml", i+1)
	}
	if _, exists := sources[filename]; !exists && len(sources) >= MaxRulesetFiles {
		return "", errors.New("ruleset file limit reached")
	}
	objectID := "campaign.item_" + id()
	definition := "[object." + objectID + "]\n" + fields.toml + "\n"
	if source := sources[filename]; source != "" {
		sources[filename] += "\n" + definition
	} else {
		sources[filename] = createdObjectsHeader + definition
	}
	next, err := CompileRulesetFiles(sources)
	if err != nil {
		return "", err
	}
	mutation.campaignTouched = true
	session.Ruleset = next
	session.RegistryRevision++
	mutation.registryChanged = true
	return objectID, nil
}

func applyCharacterItemAdd(session *Session, command Command, mutation *characterMutation) error {
	instance, exists := session.CharacterInstances[command.CharacterID]
	if !exists {
		return errors.New("character not found")
	}
	count := command.ItemCount
	if count < 1 || count > 20 {
		return errors.New("item count must be 1–20")
	}
	if len(instance.Inventory)+count > 2000 {
		return errors.New("inventory limit reached")
	}
	objectID := command.ObjectID
	if command.Type == "characterItemCreate" {
		var err error
		objectID, err = addCreatedObject(session, command.ObjectDraft, mutation)
		if err != nil {
			return err
		}
	} else if command.ObjectDraft != nil {
		return errors.New("draft only allowed when creating an object")
	}
	if session.Ruleset.Registry.Objects[objectID] == nil {
		return fmt.Errorf("unknown object %q", objectID)
	}
	instance.Inventory = slices.Clone(instance.Inventory)
	equipped := command.Equipped != nil && *command.Equipped
	for i := 0; i < count; i++ {
		instance.Inventory = append(instance.Inventory, InventoryItem{ID: "item_" + id(), ObjectID: objectID, Equipped: equipped})
	}
	mutation.setInstance(instance)
	mutation.resultCharacterID = instance.ID
	return nil
}
