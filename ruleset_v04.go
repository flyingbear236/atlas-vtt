package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

const v04RollCommandIssue = "use actionExecute for Ruleset v0.4 actions"

var rulesetV04Kinds = [...]string{"stat", "action", "effect", "object", "character"}

// RulesetFields is immutable. TOML encoding in persisted JSON preserves types
// that JSON cannot distinguish, including integers, dates and non-finite floats.
type RulesetFields struct {
	values map[string]any
	toml   string
}

func newRulesetFields(values map[string]any) (*RulesetFields, error) {
	var out bytes.Buffer
	if err := toml.NewEncoder(&out).SetTablesInline(true).Encode(values); err != nil {
		return nil, err
	}
	return &RulesetFields{values: values, toml: out.String()}, nil
}

func (f RulesetFields) MarshalJSON() ([]byte, error) { return json.Marshal(f.toml) }

func (f *RulesetFields) UnmarshalJSON(data []byte) error {
	var source string
	if err := json.Unmarshal(data, &source); err != nil {
		return err
	}
	values := map[string]any{}
	if err := toml.Unmarshal([]byte(source), &values); err != nil {
		return err
	}
	canonical, err := newRulesetFields(values)
	if err == nil {
		*f = *canonical
	}
	return err
}

func equalRulesetFields(a, b *RulesetFields) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.toml == b.toml
}

type rulesetV04Document map[string]map[string]*RulesetFields

func hasRulesetV04Roots(root map[string]any) bool {
	for _, kind := range rulesetV04Kinds {
		if _, exists := root[kind]; exists {
			return true
		}
	}
	return false
}

type rulesetTablePath struct {
	explicit bool
	array    bool
	children map[string]*rulesetTablePath
}

func (p *rulesetTablePath) add(parts []string, array bool) {
	for _, part := range parts {
		if p.array {
			return
		}
		if p.children == nil {
			p.children = map[string]*rulesetTablePath{}
		}
		if p.children[part] == nil {
			p.children[part] = &rulesetTablePath{}
		}
		p = p.children[part]
	}
	p.explicit, p.array = !array, array
}

func rulesetTOMLKey(node *unstable.Node) []string {
	var parts []string
	for keys := node.Key(); keys.Next(); {
		parts = append(parts, string(keys.Node().Data))
	}
	return parts
}

func rulesetPath(parts []string) string {
	var path strings.Builder
	for i, part := range parts {
		if i != 0 {
			path.WriteByte('.')
		}
		if part != "" && strings.IndexFunc(part, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
		}) == -1 {
			path.WriteString(part)
		} else {
			quoted, _ := json.Marshal(part)
			path.Write(quoted)
		}
	}
	return path.String()
}

func parseRulesetPath(path string) ([]string, error) {
	input := []byte("[" + path + "]")
	var parser unstable.Parser
	parser.Reset(input)
	if !parser.NextExpression() || parser.Expression().Kind != unstable.Table {
		return nil, fmt.Errorf("invalid path %q", path)
	}
	node := parser.Expression()
	parts := rulesetTOMLKey(node)
	// Ensure the last closing bracket belongs to this expression.
	for keys := node.Key(); keys.Next(); {
		if keys.IsLast() {
			raw := keys.Node().Raw
			if strings.TrimSpace(string(input[raw.Offset+raw.Length:])) != "]" {
				return nil, fmt.Errorf("invalid path %q", path)
			}
		}
	}
	if parser.NextExpression() || parser.Error() != nil {
		return nil, fmt.Errorf("invalid path %q", path)
	}
	return parts, nil
}

var rulesetReservedFields = map[string][]string{
	"stat":      {"name", "description", "type", "default", "resource"},
	"effect":    {"name", "description", "target", "formula", "priority", "when", "scope", "stack"},
	"action":    {"name", "description", "target", "roll", "var", "success", "message", "apply", "on"},
	"object":    {"name", "description", "actions", "effects"},
	"character": {"name", "actions", "effects", "stat", "inventory"},
}

// Reserved subtables can imply a containing entity when the author uses only
// a nested header. The field's shape distinguishes it from a namespace segment.
func implicitRulesetField(kind, key string, table map[string]any) bool {
	switch kind + "." + key {
	case "stat.default":
		return table["formula"] != nil
	case "action.apply":
		return table["path"] != nil || table["formula"] != nil
	case "action.on":
		return table["success"] != nil || table["fail"] != nil
	case "character.stat":
		return hasSavedStatFields(table)
	}
	return false
}

func hasSavedStatFields(table map[string]any) bool {
	for key, value := range table {
		if field, ok := value.(map[string]any); ok {
			if hasSavedStatFields(field) {
				return true
			}
		} else if key == "base" || key == "current" {
			return true
		}
	}
	return false
}

func decodeRulesetV04(data []byte, root map[string]any) (rulesetV04Document, error) {
	var parser unstable.Parser
	parser.Reset(data)
	paths := &rulesetTablePath{}
	for parser.NextExpression() {
		expr := parser.Expression()
		if expr.Kind == unstable.Table || expr.Kind == unstable.ArrayTable {
			paths.add(rulesetTOMLKey(expr), expr.Kind == unstable.ArrayTable)
		}
	}
	if err := parser.Error(); err != nil {
		return nil, err
	}
	doc := rulesetV04Document{}
	for _, kind := range rulesetV04Kinds {
		raw, exists := root[kind]
		if !exists {
			continue
		}
		namespace, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s must be a table of definitions", kind)
		}
		entities := map[string]*RulesetFields{}
		path := paths.children[kind]
		for _, id := range sortedDefinitionIDs(namespace) {
			child := (*rulesetTablePath)(nil)
			if path != nil {
				child = path.children[id]
			}
			if err := collectRulesetEntities(kind, []string{id}, namespace[id], child, entities); err != nil {
				return nil, err
			}
		}
		doc[kind] = entities
	}
	return doc, nil
}

// Explicit headers declare entities. Reserved subtables belong to the enclosing
// entity. Dotted assignments and inline tables can describe arbitrary fields.
func collectRulesetEntities(kind string, parts []string, raw any, path *rulesetTablePath, entities map[string]*RulesetFields) error {
	if err := validateID(parts[len(parts)-1]); err != nil {
		return fmt.Errorf("%s.%s: invalid path segment: %w", kind, rulesetPath(parts), err)
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%s.%s must be a definition table", kind, rulesetPath(parts))
	}
	if path == nil {
		path = &rulesetTablePath{explicit: true}
	}
	entity := path.explicit || len(path.children) == 0
	for field, value := range table {
		child := path.children[field]
		nested, isTable := value.(map[string]any)
		// Implicit TOML parents with fields (via dotted assignments or
		// reserved subtables) can also declare an entity.
		if !isTable || child == nil || implicitRulesetField(kind, field, nested) {
			entity = true
		}
	}
	var fields map[string]any
	if entity {
		fields = make(map[string]any, len(table))
	}
	for _, field := range sortedDefinitionIDs(table) {
		value := table[field]
		child := path.children[field]
		if entity && (child == nil || child.array || slices.Contains(rulesetReservedFields[kind], field)) {
			fields[field] = value
			continue
		}
		if err := collectRulesetEntities(kind, append(parts, field), value, child, entities); err != nil {
			return err
		}
	}
	if entity {
		id := rulesetPath(parts)
		definition, err := newRulesetFields(fields)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", kind, id, err)
		}
		entities[id] = definition
	}
	return nil
}

func rulesetV04Source(data []byte) (rulesetV04Document, bool, error) {
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		return nil, false, err
	}
	if !hasRulesetV04Roots(root) || isSavedCharacterTOML(root) {
		return nil, false, nil
	}
	doc, err := decodeRulesetV04Source(data, root)
	return doc, true, err
}

func decodeRulesetV04Source(data []byte, root map[string]any) (rulesetV04Document, error) {
	for key := range root {
		if !slices.Contains(rulesetV04Kinds[:], key) {
			return nil, fmt.Errorf("unexpected v0.4 root field %q", key)
		}
	}
	return decodeRulesetV04(data, root)
}

func rulesetV04Manifest(source string, doc rulesetV04Document) RulesetFileManifest {
	return RulesetFileManifest{
		Digest: rulesetFileDigest(source), Source: source, V04: true,
		Stats: sortedDefinitionIDs(doc["stat"]), Actions: sortedDefinitionIDs(doc["action"]),
		Effects: sortedDefinitionIDs(doc["effect"]), Objects: sortedDefinitionIDs(doc["object"]),
		Characters: sortedDefinitionIDs(doc["character"]),
	}
}

func encodeRulesetV04(doc rulesetV04Document) []byte {
	var out strings.Builder
	for _, kind := range rulesetV04Kinds {
		for _, id := range sortedDefinitionIDs(doc[kind]) {
			fmt.Fprintf(&out, "[%s.%s]\n%s\n", kind, id, doc[kind][id].toml)
		}
	}
	if out.Len() == 0 {
		return []byte("[stat]\n")
	}
	return []byte(out.String())
}

func (s RulesetSnapshot) v04Document(manifest *RulesetFileManifest) rulesetV04Document {
	doc := rulesetV04Document{
		"stat": {}, "action": {}, "effect": s.Registry.Effects,
		"object": s.Registry.Objects, "character": s.Characters,
	}
	for id, stat := range s.Registry.Stats {
		if stat.V04 != nil {
			doc["stat"][id] = stat.V04
		}
	}
	for id, action := range s.Registry.Actions {
		if action.V04 != nil {
			doc["action"][id] = action.V04
		}
	}
	if manifest == nil {
		return doc
	}
	for kind, ids := range map[string][]string{
		"stat": manifest.Stats, "action": manifest.Actions, "effect": manifest.Effects,
		"object": manifest.Objects, "character": manifest.Characters,
	} {
		owned := make(map[string]*RulesetFields, len(ids))
		for _, id := range ids {
			owned[id] = doc[kind][id]
		}
		doc[kind] = owned
	}
	return doc
}

func (s RulesetSnapshot) usesV04() bool {
	for _, file := range s.Files {
		if file.V04 {
			return true
		}
	}
	return false
}

func (s RulesetSnapshot) executionDiagnostic() string {
	if s.usesV04() && s.actionPlans == nil {
		return "Ruleset v0.4 actions have not been compiled"
	}
	return ""
}

func (s RulesetSnapshot) ExecutionReady() bool { return s.executionDiagnostic() == "" }

func equalRulesetV04Documents(a, b rulesetV04Document) bool {
	for _, kind := range rulesetV04Kinds {
		if !maps.EqualFunc(a[kind], b[kind], equalRulesetFields) {
			return false
		}
	}
	return true
}

func rulesetString(fields map[string]any, key string, required bool) (string, error) {
	raw, exists := fields[key]
	if !exists && !required {
		return "", nil
	}
	value, ok := raw.(string)
	if required && (!ok || strings.TrimSpace(value) == "") {
		return "", fmt.Errorf("%s must be a nonempty string", key)
	}
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return value, nil
}

func rulesetExpression(fields map[string]any, key string) error {
	if _, exists := fields[key]; !exists {
		return nil
	}
	_, err := rulesetString(fields, key, true)
	return err
}

func rulesetStrings(raw any, field string, scalar bool) ([]string, error) {
	if value, ok := raw.(string); ok && scalar && strings.TrimSpace(value) != "" {
		return []string{value}, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", field)
	}
	result := make([]string, 0, len(list))
	for _, item := range list {
		value, ok := item.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s entries must be nonempty strings", field)
		}
		result = append(result, value)
	}
	return result, nil
}

func rulesetStatType(raw any) StatType {
	switch raw.(type) {
	case int64:
		return StatTypeInteger
	case bool:
		return StatTypeBoolean
	case string:
		return StatTypeString
	default:
		return ""
	}
}

func decodeRulesetV04Stat(id string, fields *RulesetFields) (StatDefinition, error) {
	f := fields.values
	name, err := rulesetString(f, "name", false)
	if err != nil {
		return StatDefinition{}, err
	}
	if _, err := rulesetString(f, "description", false); err != nil {
		return StatDefinition{}, err
	}
	def := StatDefinition{ID: id, Name: name, V04: fields}
	if raw, exists := f["type"]; exists {
		switch raw {
		case "integer", "boolean", "string":
			def.Type = StatType(raw.(string))
		default:
			return def, fmt.Errorf("type must be integer, boolean or string")
		}
	}
	if raw, exists := f["resource"]; exists {
		resource, ok := raw.(bool)
		if !ok {
			return def, fmt.Errorf("resource must be boolean")
		}
		if resource {
			def.Kind = StatKindResource
		}
	}
	raw := f["default"]
	if formula, ok := raw.(map[string]any); ok {
		if len(formula) != 1 {
			return def, fmt.Errorf("default must be a scalar or { formula = ... }")
		}
		_, err := rulesetString(formula, "formula", true)
		return def, err
	}
	inferred := rulesetStatType(raw)
	if inferred == "" {
		return def, fmt.Errorf("default must be an integer, boolean, string or formula")
	}
	if def.Type == "" {
		def.Type = inferred
	} else if def.Type != inferred {
		return def, fmt.Errorf("default has type %q, expected %q", inferred, def.Type)
	}
	value, err := tomlStatValue(raw, def.Type)
	def.Default = &value
	return def, err
}

func decodeRulesetV04Action(id string, fields *RulesetFields) (ActionDefinition, error) {
	name, err := rulesetString(fields.values, "name", false)
	if err != nil {
		return ActionDefinition{}, err
	}
	description, err := rulesetString(fields.values, "description", false)
	return ActionDefinition{ID: id, Name: name, Description: description, V04: fields}, err
}

func (s RulesetSnapshot) hasV04Reference(kind, id string) bool {
	switch kind {
	case "stat":
		return s.Registry.Stats[id].V04 != nil
	case "action":
		return s.Registry.Actions[id].V04 != nil
	case "effect":
		return s.Registry.Effects[id] != nil
	case "object":
		return s.Registry.Objects[id] != nil
	}
	return false
}

func (s RulesetSnapshot) validateV04Reference(kind, ref string) error {
	parts, err := parseRulesetPath(ref)
	if err != nil {
		return err
	}
	if len(parts) < 2 || parts[0] != kind || !s.hasV04Reference(kind, rulesetPath(parts[1:])) {
		return fmt.Errorf("unknown %s reference %q", kind, ref)
	}
	return nil
}

func (s RulesetSnapshot) validateV04Grants(f map[string]any) error {
	for _, grant := range []struct{ field, kind string }{{"actions", "action"}, {"effects", "effect"}} {
		if raw, exists := f[grant.field]; exists {
			refs, err := rulesetStrings(raw, grant.field, false)
			if err != nil {
				return err
			}
			for _, ref := range refs {
				if err := s.validateV04Reference(grant.kind, ref); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s RulesetSnapshot) validateV04Effect(f map[string]any) error {
	if _, err := rulesetString(f, "formula", true); err != nil {
		return err
	}
	if err := rulesetExpression(f, "when"); err != nil {
		return err
	}
	if raw, exists := f["priority"]; exists {
		if _, ok := raw.(int64); !ok {
			return fmt.Errorf("priority must be an integer")
		}
	}
	for _, option := range []struct{ key, a, b string }{{"scope", "actor", "source"}, {"stack", "repeat", "unique"}} {
		if raw, exists := f[option.key]; exists && raw != option.a && raw != option.b {
			return fmt.Errorf("%s must be %q or %q", option.key, option.a, option.b)
		}
	}
	targets, err := rulesetStrings(f["target"], "target", true)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("target must contain at least one path")
	}
	for _, target := range targets {
		parts, err := parseRulesetPath(target)
		if err != nil {
			return err
		}
		if !s.hasV04EffectTarget(parts) {
			return fmt.Errorf("unknown or non-effectable target %q", target)
		}
	}
	return nil
}

func (s RulesetSnapshot) effectPriorityWarnings() []string {
	type targetPriority struct {
		target   string
		priority int64
	}
	var warnings []string
	first := map[targetPriority]string{}
	for _, id := range sortedDefinitionIDs(s.Registry.Effects) {
		f := s.Registry.Effects[id].values
		targets, _ := rulesetStrings(f["target"], "target", true)
		priority, _ := f["priority"].(int64)
		for _, target := range targets {
			parts, _ := parseRulesetPath(target)
			key := targetPriority{rulesetPath(parts), priority}
			previous, found := first[key]
			switch {
			case !found:
				first[key] = id
			case previous != "" && previous != id:
				warnings = append(warnings, fmt.Sprintf("Effects %q and %q both target %q at priority %d; formula order may affect the result", previous, id, key.target, priority))
				first[key] = ""
			}
		}
	}
	return warnings
}

func (s RulesetSnapshot) hasV04EffectTarget(parts []string) bool {
	if len(parts) < 2 {
		return false
	}
	switch parts[0] {
	case "stat":
		for end := len(parts); end > 1; end-- {
			stat := s.Registry.Stats[rulesetPath(parts[1:end])]
			if stat.V04 == nil {
				continue
			}
			if end == len(parts) {
				return true
			}
			field := parts[end]
			if slices.Contains(rulesetReservedFields["stat"], field) || field == "base" || field == "current" {
				return false
			}
			value := any(stat.V04.values)
			for _, key := range parts[end:] {
				table, ok := value.(map[string]any)
				if !ok {
					return false
				}
				if _, formula := table["formula"].(string); formula && len(table) == 1 {
					return false
				}
				value = table[key]
			}
			return value != nil
		}
	case "action":
		if len(parts) > 2 && parts[len(parts)-1] == "roll" {
			action := s.Registry.Actions[rulesetPath(parts[1:len(parts)-1])]
			return action.V04 != nil && action.V04.values["roll"] != nil
		}
	}
	return false
}

func objectWritePath(parts []string, selector string) bool {
	return parts[0] == "source" || parts[0] == "target" && (selector == "source" || selector == "select(object)")
}

func (s RulesetSnapshot) validateV04Apply(raw any, selector string) error {
	fields, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("apply must be a table")
	}
	path, err := rulesetString(fields, "path", true)
	if err != nil {
		return err
	}
	if _, err := rulesetString(fields, "formula", true); err != nil {
		return fmt.Errorf("apply.%w", err)
	}
	parts, err := parseRulesetPath(path)
	if err != nil {
		return err
	}
	if len(parts) < 2 || !slices.Contains([]string{"target", "actor", "source"}, parts[0]) {
		return fmt.Errorf("apply.path %q must address a saved instance field", path)
	}
	if parts[1] != "stat" || objectWritePath(parts, selector) {
		// The concrete instance and its custom fields are resolved at execution.
		return nil
	}
	if len(parts) >= 4 {
		stat := s.Registry.Stats[rulesetPath(parts[2:len(parts)-1])]
		field := parts[len(parts)-1]
		if stat.V04 != nil && (field == "base" && stat.Default != nil || field == "current" && stat.IsResource()) {
			return nil
		}
	}
	return fmt.Errorf("apply.path %q must address an editable stat base or resource current", path)
}

func (s RulesetSnapshot) validateV04Consequences(fields map[string]any, selector string) error {
	if _, err := rulesetString(fields, "message", false); err != nil {
		return err
	}
	if raw, exists := fields["apply"]; exists {
		return s.validateV04Apply(raw, selector)
	}
	return nil
}

func rulesetExecutable(f map[string]any) bool {
	return f["roll"] != nil || f["message"] != nil || f["apply"] != nil
}

func (s RulesetSnapshot) validateV04Action(action *RulesetFields, hasExecutableChild bool) error {
	f := action.values
	executable := rulesetExecutable(f)
	if !executable {
		if !hasExecutableChild {
			return fmt.Errorf("requires roll, message, apply or an executable descendant")
		}
		for _, field := range []string{"target", "var", "success", "on"} {
			if _, exists := f[field]; exists {
				return fmt.Errorf("group cannot define %s", field)
			}
		}
	} else if hasExecutableChild {
		return fmt.Errorf("action with executable descendants must be a group")
	}
	if raw, exists := f["target"]; exists {
		switch raw {
		case "select(character)", "select(object)", "self", "source":
		default:
			return fmt.Errorf("invalid target selector")
		}
	}
	for _, key := range []string{"roll", "success"} {
		if err := rulesetExpression(f, key); err != nil {
			return err
		}
	}
	if raw, exists := f["var"]; exists {
		vars, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("var must be a table")
		}
		for key := range vars {
			if err := rulesetExpression(vars, key); err != nil {
				return fmt.Errorf("var.%w", err)
			}
		}
	}
	selector, _ := f["target"].(string)
	if err := s.validateV04Consequences(f, selector); err != nil {
		return err
	}
	if raw, exists := f["on"]; exists {
		outcomes, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("on must be a table")
		}
		for _, outcome := range []string{"success", "fail"} {
			raw, exists := outcomes[outcome]
			if !exists {
				continue
			}
			fields, ok := raw.(map[string]any)
			if !ok || (fields["message"] == nil && fields["apply"] == nil) {
				return fmt.Errorf("on.%s requires message or apply", outcome)
			}
			if f["success"] == nil {
				return fmt.Errorf("on.%s requires a parent success condition", outcome)
			}
			if err := s.validateV04Consequences(fields, selector); err != nil {
				return fmt.Errorf("on.%s: %w", outcome, err)
			}
		}
	}
	return nil
}

func (s RulesetSnapshot) validateV04Character(f map[string]any) error {
	if err := s.validateV04Grants(f); err != nil {
		return err
	}
	if raw, exists := f["stat"]; exists {
		table, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("stat must be a table")
		}
		if err := s.validateV04CharacterStats(nil, table); err != nil {
			return err
		}
	}
	if raw, exists := f["inventory"]; exists {
		items, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("inventory must be an array of tables")
		}
		seen := make(map[string]bool, len(items))
		for _, item := range items {
			fields, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("inventory items must be tables")
			}
			id, err := rulesetString(fields, "id", true)
			if err != nil {
				return fmt.Errorf("inventory: %w", err)
			}
			if seen[id] {
				return fmt.Errorf("duplicate inventory id %q", id)
			}
			seen[id] = true
			ref, err := rulesetString(fields, "object", true)
			if err != nil {
				return fmt.Errorf("inventory %q: %w", id, err)
			}
			if err := s.validateV04Reference("object", ref); err != nil {
				return fmt.Errorf("inventory %q: %w", id, err)
			}
			if raw, exists := fields["equipped"]; exists {
				if _, ok := raw.(bool); !ok {
					return fmt.Errorf("inventory %q: equipped must be boolean", id)
				}
			}
		}
	}
	return nil
}

func (s RulesetSnapshot) validateV04CharacterStats(parts []string, table map[string]any) error {
	return s.walkV04CharacterStats(parts, table, nil)
}

func (s RulesetSnapshot) walkV04CharacterStats(parts []string, table map[string]any, store func(string, string, StatDefinition, StatValue)) error {
	if len(parts) != 0 && len(table) == 0 && s.Registry.Stats[rulesetPath(parts)].V04 == nil {
		return fmt.Errorf("unknown stat %q", rulesetPath(parts))
	}
	for _, key := range sortedDefinitionIDs(table) {
		raw := table[key]
		nested, isTable := raw.(map[string]any)
		if isTable && key != "base" && key != "current" {
			if err := s.walkV04CharacterStats(append(parts, key), nested, store); err != nil {
				return err
			}
			continue
		}
		id := rulesetPath(parts)
		def := s.Registry.Stats[id]
		if isTable && def.V04 == nil {
			if err := s.walkV04CharacterStats(append(parts, key), nested, store); err != nil {
				return err
			}
			continue
		}
		if def.V04 == nil {
			return fmt.Errorf("unknown stat %q", id)
		}
		switch key {
		case "base":
			if def.Default == nil {
				return fmt.Errorf("computed stat %q cannot override base", id)
			}
		case "current":
			if !def.IsResource() {
				return fmt.Errorf("stat %q is not a resource", id)
			}
		default:
			return fmt.Errorf("stat %q has unknown saved field %q", id, key)
		}
		typ := rulesetStatType(raw)
		expected := def.Type
		if expected == "" && s.Registry.statPlan != nil {
			expected = s.Registry.statPlan.types[id]
		}
		if typ == "" || (expected != "" && expected != typ) {
			return fmt.Errorf("stat %q %s has incompatible type", id, key)
		}
		value, err := tomlStatValue(raw, typ)
		if err != nil {
			return fmt.Errorf("stat %q %s: %w", id, key, err)
		}
		if store != nil {
			store(id, key, def, value)
		}
	}
	return nil
}

func validateRulesetV04(s RulesetSnapshot) error {
	doc := s.v04Document(nil)
	executable := make([]string, 0, len(doc["action"]))
	for _, id := range sortedDefinitionIDs(doc["action"]) {
		if rulesetExecutable(doc["action"][id].values) {
			executable = append(executable, id)
		}
	}
	for _, kind := range rulesetV04Kinds {
		ids := sortedDefinitionIDs(doc[kind])
		for _, id := range ids {
			fields := doc[kind][id]
			if fields == nil {
				return fmt.Errorf("%s.%s has no fields", kind, id)
			}
			if err := validateV04Namespace(kind, id, fields, ids); err != nil {
				return err
			}
			_, err := rulesetString(fields.values, "name", false)
			if err == nil && kind != "character" {
				_, err = rulesetString(fields.values, "description", false)
			}
			if err == nil {
				switch kind {
				case "stat":
					var projected StatDefinition
					projected, err = decodeRulesetV04Stat(id, fields)
					if err == nil && !equalStatDefinition(s.Registry.Stats[id], projected) {
						err = fmt.Errorf("fields differ from the compiled stat")
					}
				case "action":
					var projected ActionDefinition
					projected, err = decodeRulesetV04Action(id, fields)
					if err == nil && !equalActionDefinition(s.Registry.Actions[id], projected) {
						err = fmt.Errorf("fields differ from the compiled action")
					}
					if err == nil {
						err = s.validateV04Action(fields, hasRulesetDescendant(executable, id))
					}
				case "effect":
					err = s.validateV04Effect(fields.values)
				case "object":
					err = s.validateV04Grants(fields.values)
				case "character":
					err = s.validateV04Character(fields.values)
				}
			}
			if err != nil {
				return fmt.Errorf("%s.%s: %w", kind, id, err)
			}
		}
	}
	return nil
}

func hasRulesetDescendant(sortedIDs []string, id string) bool {
	prefix := id + "."
	index := sort.SearchStrings(sortedIDs, prefix)
	return index < len(sortedIDs) && strings.HasPrefix(sortedIDs[index], prefix)
}

func validateV04Namespace(kind, id string, fields *RulesetFields, ids []string) error {
	if kind == "stat" {
		reserved := []string{"base"}
		if resource, _ := fields.values["resource"].(bool); resource {
			reserved = append(reserved, "current")
		}
		for _, field := range reserved {
			address := id + "." + field
			_, child := slices.BinarySearch(ids, address)
			if _, exists := fields.values[field]; exists || child || hasRulesetDescendant(ids, address) {
				return fmt.Errorf("stat.%s conflicts with a stat value address", address)
			}
		}
	}
	if !hasRulesetDescendant(ids, id) {
		return nil
	}
	check := func(field string) error {
		child := id + "." + rulesetPath([]string{field})
		if _, found := slices.BinarySearch(ids, child); found || hasRulesetDescendant(ids, child) {
			return fmt.Errorf("%s.%s conflicts with a field of %s.%s", kind, child, kind, id)
		}
		return nil
	}
	for _, field := range rulesetReservedFields[kind] {
		if err := check(field); err != nil {
			return err
		}
	}
	for _, field := range sortedDefinitionIDs(fields.values) {
		if err := check(field); err != nil {
			return err
		}
	}
	return nil
}

func compileRulesetV04(files map[string]string, names []string, documents map[string]rulesetV04Document) (RulesetSnapshot, error) {
	snapshot := RulesetSnapshot{
		Registry: emptyRulesetRegistry(),
		Files:    make(map[string]RulesetFileManifest, len(files)),
	}
	merged := rulesetV04Document{}
	for _, kind := range rulesetV04Kinds {
		merged[kind] = map[string]*RulesetFields{}
		owners := map[string]string{}
		for _, name := range names {
			for _, id := range sortedDefinitionIDs(documents[name][kind]) {
				if first := owners[id]; first != "" {
					return RulesetSnapshot{}, &RulesetDefinitionConflictError{Kind: kind, ID: id, FirstFile: first, SecondFile: name}
				}
				owners[id] = name
				merged[kind][id] = documents[name][kind][id]
			}
		}
		for _, id := range sortedDefinitionIDs(merged[kind]) {
			fields := merged[kind][id]
			var err error
			switch kind {
			case "stat":
				snapshot.Registry.Stats[id], err = decodeRulesetV04Stat(id, fields)
			case "action":
				snapshot.Registry.Actions[id], err = decodeRulesetV04Action(id, fields)
			}
			if err != nil {
				return RulesetSnapshot{}, fmt.Errorf("%s: %s.%s: %w", owners[id], kind, id, err)
			}
		}
	}
	snapshot.Registry.Effects = merged["effect"]
	snapshot.Registry.Objects = merged["object"]
	snapshot.Characters = merged["character"]
	for _, name := range names {
		snapshot.Files[name] = rulesetV04Manifest(files[name], documents[name])
	}
	if err := snapshot.prepareRulesetExpressions(); err != nil {
		return RulesetSnapshot{}, fmt.Errorf("ruleset package: %w", err)
	}
	return snapshot, nil
}
