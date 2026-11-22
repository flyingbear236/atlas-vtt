package main

import (
	"bytes"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// A portable ZIP can contain both ruleset declarations and the live instance
// with the same ID. This comment marks ATLAS's nested saves without adding a
// field to the v0.4 grammar.
const nestedCharacterSaveHeader = "# ATLAS persistent character\n"

func isNestedCharacterSave(data []byte) bool {
	return bytes.HasPrefix(data, []byte(nestedCharacterSaveHeader)) ||
		bytes.HasPrefix(data, []byte(strings.ReplaceAll(nestedCharacterSaveHeader, "\n", "\r\n")))
}

func nestedCharacterDocument(data []byte, root map[string]any) (string, *RulesetFields, error) {
	doc, err := decodeRulesetV04Source(data, root)
	if err != nil {
		return "", nil, err
	}
	if len(root) != 1 || len(doc["character"]) != 1 {
		return "", nil, fmt.Errorf("character save requires exactly one character")
	}
	for id, fields := range doc["character"] {
		return id, fields, nil
	}
	return "", nil, fmt.Errorf("missing character table")
}

func decodeNestedCharacter(id string, fields *RulesetFields, registry EffectiveRegistry) (CharacterInstance, error) {
	s := RulesetSnapshot{Registry: RulesetRegistry{Stats: registry.Stats, Actions: registry.Actions, Effects: registry.Effects, Objects: registry.Objects, statPlan: registry.statPlan}}
	if err := s.validateV04Character(fields.values); err != nil {
		return CharacterInstance{}, err
	}
	instance := NewCharacterInstance(id, nil)
	var err error
	instance.Name, err = rulesetString(fields.values, "name", false)
	if err != nil {
		return CharacterInstance{}, err
	}
	instance.Persistent = true
	instance.Materialized.ActionIDs = rulesetGrantIDs(fields.values["actions"])
	instance.EffectIDs = rulesetGrantIDs(fields.values["effects"])
	instance.Inventory, err = decodeInventory(fields.values["inventory"])
	if err != nil {
		return CharacterInstance{}, err
	}
	custom := maps.Clone(fields.values)
	for _, key := range rulesetReservedFields["character"] {
		delete(custom, key)
	}
	instance.V04, err = newRulesetFields(custom)
	if err != nil {
		return CharacterInstance{}, err
	}
	if stats, ok := fields.values["stat"].(map[string]any); ok {
		instance.ResourceCurrent = map[string]StatValue{}
		err = s.walkV04CharacterStats(nil, stats, func(id, key string, def StatDefinition, value StatValue) {
			switch {
			case key == "current":
				instance.ResourceCurrent[id] = value
			case def.IsResource():
				instance.Materialized.ResourceInitial[id] = value
			default:
				instance.Materialized.Stats[id] = value
			}
		})
		if err != nil {
			return CharacterInstance{}, err
		}
	}
	return validateDecodedCharacter(instance, registry)
}

func decodeInventory(raw any) ([]InventoryItem, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("inventory must be an array")
	}
	items := make([]InventoryItem, 0, len(list))
	for _, raw := range list {
		fields, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("inventory item must be a table")
		}
		id, err := rulesetString(fields, "id", true)
		if err != nil {
			return nil, err
		}
		ref, err := rulesetString(fields, "object", true)
		if err != nil {
			return nil, err
		}
		path, err := parseRulesetPath(ref)
		if err != nil || len(path) < 2 || path[0] != "object" {
			return nil, fmt.Errorf("inventory %q has invalid object reference %q", id, ref)
		}
		equipped, ok := fields["equipped"].(bool)
		if !ok && fields["equipped"] != nil {
			return nil, fmt.Errorf("inventory %q equipped must be boolean", id)
		}
		item := InventoryItem{ID: id, ObjectID: rulesetPath(path[1:]), Equipped: equipped}
		custom := maps.Clone(fields)
		delete(custom, "id")
		delete(custom, "object")
		delete(custom, "equipped")
		if len(custom) > 0 {
			item.Fields, err = newRulesetFields(custom)
			if err != nil {
				return nil, err
			}
		}
		items = append(items, item)
	}
	return items, nil
}

func inventoryTOML(items []InventoryItem) []any {
	if len(items) == 0 {
		return nil
	}
	result := make([]any, 0, len(items))
	for _, item := range items {
		fields := map[string]any{}
		if item.Fields != nil {
			maps.Copy(fields, item.Fields.values)
		}
		fields["id"], fields["object"], fields["equipped"] = item.ID, "object."+item.ObjectID, item.Equipped
		result = append(result, fields)
	}
	return result
}

func canEncodeNestedCharacter(instance CharacterInstance, registry EffectiveRegistry) bool {
	if instance.V04 == nil || instance.PresetID != "" || instance.Materialized == nil || instance.AvatarAssetID != nil || instance.ArtworkPath != "" {
		return false
	}
	parts, err := parseRulesetPath(instance.ID)
	if err != nil || rulesetPath(parts) != instance.ID {
		return false
	}
	for _, id := range instance.Materialized.ActionIDs {
		if registry.Actions[id].V04 == nil {
			return false
		}
	}
	for _, values := range []map[string]StatValue{instance.Materialized.Stats, instance.Materialized.ResourceInitial, instance.ResourceCurrent} {
		for id := range values {
			if registry.Stats[id].V04 == nil {
				return false
			}
		}
	}
	return true
}

func encodeNestedCharacter(instance CharacterInstance) ([]byte, error) {
	var out bytes.Buffer
	out.WriteString(nestedCharacterSaveHeader)
	fields := maps.Clone(instance.V04.values)
	if instance.Name != "" {
		fields["name"] = instance.Name
	}
	for kind, ids := range map[string][]string{"action": instance.Materialized.ActionIDs, "effect": instance.EffectIDs} {
		if len(ids) > 0 {
			references := make([]string, len(ids))
			for i, id := range ids {
				references[i] = kind + "." + id
			}
			fields[kind+"s"] = references
		}
	}
	prefix := "character." + instance.ID
	fmt.Fprintf(&out, "[%s]\n", prefix)
	encoder := toml.NewEncoder(&out).SetTablesInline(true)
	if err := encoder.Encode(fields); err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, values := range []map[string]StatValue{instance.Materialized.Stats, instance.Materialized.ResourceInitial, instance.ResourceCurrent} {
		for id := range values {
			ids[id] = true
		}
	}
	for _, id := range sortedDefinitionIDs(ids) {
		values := map[string]any{}
		for _, sources := range []map[string]StatValue{instance.Materialized.Stats, instance.Materialized.ResourceInitial} {
			if value, ok := sources[id]; ok {
				values["base"] = value.Interface()
			}
		}
		if value, ok := instance.ResourceCurrent[id]; ok {
			values["current"] = value.Interface()
		}
		fmt.Fprintf(&out, "\n[%s.stat.%s]\n", prefix, id)
		if err := encoder.Encode(values); err != nil {
			return nil, err
		}
	}
	for _, item := range inventoryTOML(instance.Inventory) {
		fmt.Fprintf(&out, "\n[[%s.inventory]]\n", prefix)
		if err := encoder.Encode(item); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

func seedRulesetCharacters(instances map[string]CharacterInstance, previous map[string]*RulesetFields, ruleset RulesetSnapshot, registry EffectiveRegistry) (map[string]CharacterInstance, error) {
	created := map[string]CharacterInstance{}
	for _, id := range sortedDefinitionIDs(ruleset.Characters) {
		if _, exists := instances[id]; exists || previous[id] != nil {
			continue
		}
		instance, err := decodeNestedCharacter(id, ruleset.Characters[id], registry)
		if err != nil {
			return nil, fmt.Errorf("character.%s: %w", id, err)
		}
		created[id] = instance
	}
	return created, nil
}

// Rewrite just the equipment flags in authored array tables. The caller
// verifies the resulting instance before exporting the edited source.
func rewriteNestedEquipment(source []byte, instance CharacterInstance) []byte {
	type inventoryTable struct {
		id        string
		headerEnd int
		flag      unstable.Range
	}
	var tables []inventoryTable
	var table *inventoryTable
	var parser unstable.Parser
	parser.Reset(source)
	for parser.NextExpression() {
		node := parser.Expression()
		switch node.Kind {
		case unstable.ArrayTable, unstable.Table:
			table = nil
			if node.Kind == unstable.ArrayTable && rulesetPath(rulesetTOMLKey(node)) == "character."+instance.ID+".inventory" {
				// Locate the end of the header, including any inline comment.
				keys := node.Key()
				for keys.Next() && !keys.IsLast() {
				}
				last := keys.Node().Raw
				end := int(last.Offset + last.Length)
				if nl := bytes.IndexByte(source[end:], '\n'); nl >= 0 {
					end += nl + 1
				} else {
					end = len(source)
				}
				tables = append(tables, inventoryTable{headerEnd: end})
				table = &tables[len(tables)-1]
			}
		case unstable.KeyValue:
			if table == nil {
				continue
			}
			key := rulesetTOMLKey(node)
			if len(key) != 1 {
				continue
			}
			value := node.Value()
			switch key[0] {
			case "id":
				table.id = string(value.Data)
			case "equipped":
				if value.Kind == unstable.Bool {
					table.flag = parser.Range(value.Data)
				}
			}
		}
	}
	if parser.Error() != nil || len(tables) != len(instance.Inventory) {
		return nil
	}
	var out bytes.Buffer
	last := 0
	for i, table := range tables {
		item := instance.Inventory[i]
		if table.id != item.ID {
			return nil
		}
		if table.flag.Length != 0 {
			start := int(table.flag.Offset)
			out.Write(source[last:start])
			out.WriteString(strconv.FormatBool(item.Equipped))
			last = start + int(table.flag.Length)
		} else if item.Equipped && table.headerEnd < len(source) {
			out.Write(source[last:table.headerEnd])
			out.WriteString("equipped = true\n")
			last = table.headerEnd
		}
	}
	out.Write(source[last:])
	return out.Bytes()
}
