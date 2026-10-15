package main

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// sourceTOMLTable represents a table and its unmodified source lines. This is
// intentionally not a formatter: only changed value spans are rewritten.
type sourceTOMLTable struct {
	key, header string
	array       bool
	lines       []string
}

func sourceTOMLTables(source string) ([]sourceTOMLTable, error) {
	// When a changed document contains multiline strings, header-looking text
	// can be data. Reject the edit instead of risking corruption.
	if strings.Contains(source, `"""`) || strings.Contains(source, `'''`) {
		return nil, fmt.Errorf("multiline TOML strings cannot be safely rewritten")
	}
	tables := []sourceTOMLTable{{key: "", header: ""}}
	for _, line := range strings.SplitAfter(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "#") {
			parts, array, ok := parsePresetTableHeader(line)
			if !ok {
				return nil, fmt.Errorf("ambiguous TOML header %q", trimmed)
			}
			key := strings.Join(parts, "\x00")
			if array && len(parts) == 3 && parts[0] == "actions" && parts[2] == "rolls" {
				// Roll IDs, not array positions, determine stable ownership.
				key += "\x00@roll"
			} else if array && len(parts) == 1 && parts[0] == "stats" {
				key += "\x00@stat"
			} else if array {
				return nil, fmt.Errorf("unsupported TOML array table %q", trimmed)
			}
			tables = append(tables, sourceTOMLTable{key: key, header: line, array: array, lines: []string{line}})
			continue
		}
		tables[len(tables)-1].lines = append(tables[len(tables)-1].lines, line)
	}
	for i := range tables {
		table := &tables[i]
		if strings.HasSuffix(table.key, "\x00@roll") || table.key == "stats\x00@stat" {
			id, ok := sourceTableValue(*table, "id")
			if !ok {
				return nil, fmt.Errorf("array table has no id")
			}
			var decoded map[string]string
			if err := toml.Unmarshal([]byte("id = "+id), &decoded); err != nil {
				return nil, fmt.Errorf("invalid array table id: %w", err)
			}
			if table.key == "stats\x00@stat" {
				table.key = "stats\x00" + decoded["id"]
			} else {
				table.key = strings.TrimSuffix(table.key, "\x00@roll") + "\x00" + decoded["id"]
			}
		}
	}
	return tables, nil
}

// sourceValueRange parses one single-line key/value pair, ignoring strings and
// inline comments. It returns the exact value span (without spaces or comment).
func sourceValueRange(line string) (string, int, int, bool) {
	quoted, escaped := byte(0), false
	eq := -1
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quoted != 0 {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' && quoted == '"' {
				escaped = true
				continue
			}
			if c == quoted {
				quoted = 0
			}
		} else {
			if c == '"' || c == '\'' {
				quoted = c
			} else if c == '#' {
				return "", 0, 0, false
			} else if c == '=' {
				eq = i
				break
			}
		}
	}
	if eq < 0 {
		return "", 0, 0, false
	}
	rawKey := strings.TrimSpace(line[:eq])
	var keyData map[string]any
	if err := toml.Unmarshal([]byte(rawKey+" = 0"), &keyData); err != nil {
		return "", 0, 0, false
	}
	key := rawKey
	if len(keyData) == 1 {
		for k := range keyData {
			key = k
		}
	}
	begin := eq + 1
	for begin < len(line) && (line[begin] == ' ' || line[begin] == '\t') {
		begin++
	}
	quoted, escaped = 0, false
	level := 0
	end := len(line)
	for i := begin; i < len(line); i++ {
		c := line[i]
		if quoted != 0 {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' && quoted == '"' {
				escaped = true
				continue
			}
			if c == quoted {
				quoted = 0
			}
		} else {
			switch c {
			case '\'', '"':
				quoted = c
			case '[', '{':
				level++
			case ']', '}':
				level--
			case '#':
				if level == 0 {
					end = i
					i = len(line)
				}
			case '\r', '\n':
				if level == 0 {
					end = i
					i = len(line)
				}
			}
		}
	}
	if quoted != 0 || level != 0 {
		return "", 0, 0, false
	}
	for end > begin && (line[end-1] == ' ' || line[end-1] == '\t' || line[end-1] == '\r' || line[end-1] == '\n') {
		end--
	}
	return key, begin, end, end > begin
}

func sourceTableValue(table sourceTOMLTable, key string) (string, bool) {
	for _, line := range table.lines {
		k, a, b, ok := sourceValueRange(line)
		if ok && k == key {
			return line[a:b], true
		}
	}
	return "", false
}

func sourceValueEqual(a, b string) bool {
	var x, y map[string]any
	if toml.Unmarshal([]byte("v = "+a), &x) != nil || toml.Unmarshal([]byte("v = "+b), &y) != nil {
		return a == b
	}
	return reflect.DeepEqual(x, y)
}

func sourceValueStyle(original, next string) string {
	// A literal string stays literal where the new value can be represented.
	if strings.HasPrefix(original, "'") && strings.HasSuffix(original, "'") && strings.HasPrefix(next, "\"") && strings.HasSuffix(next, "\"") {
		var data map[string]string
		if toml.Unmarshal([]byte("v = "+next), &data) == nil && !strings.Contains(data["v"], "'") && !strings.ContainsAny(data["v"], "\n\r") {
			return "'" + data["v"] + "'"
		}
	}
	return next
}

func rewriteTOMLTable(old, want sourceTOMLTable) (string, error) {
	desired := map[string]string{}
	order := []string{}
	for _, line := range want.lines {
		key, a, b, ok := sourceValueRange(line)
		if ok {
			if _, exists := desired[key]; exists {
				return "", fmt.Errorf("duplicate key %q", key)
			}
			desired[key] = line[a:b]
			order = append(order, key)
		}
	}
	result := make([]string, 0, len(old.lines)+len(order))
	start := 0
	if old.header != "" {
		result = append(result, old.header)
		start = 1
	}
	seen := map[string]bool{}
	for _, line := range old.lines[start:] {
		key, a, b, ok := sourceValueRange(line)
		if !ok {
			result = append(result, line)
			continue
		}
		if seen[key] {
			return "", fmt.Errorf("duplicate key %q", key)
		}
		seen[key] = true
		next, exists := desired[key]
		if !exists {
			if (key == "kind" || key == "group" || key == "description" || key == "tags" || key == "modifier_fixed" || key == "modifier_stat" || key == "artwork" || key == "avatar") && (sourceValueEqual(line[a:b], `""`) || sourceValueEqual(line[a:b], `0`) || sourceValueEqual(line[a:b], `[]`) || sourceValueEqual(line[a:b], `false`)) {
				result = append(result, line)
				continue
			}
			if old.key == "" || (strings.HasPrefix(old.key, "stats\x00") && old.array && key == "id") {
				result = append(result, line)
			}
			continue
		}
		if sourceValueEqual(line[a:b], next) {
			result = append(result, line)
		} else {
			result = append(result, line[:a]+sourceValueStyle(line[a:b], next)+line[b:])
		}
	}
	newline := "\n"
	for _, line := range old.lines {
		if strings.HasSuffix(line, "\r\n") {
			newline = "\r\n"
			break
		}
	}
	// Insert new assignments before trailing comments for the next definition.
	pos := len(result)
	for pos > start {
		t := strings.TrimSpace(result[pos-1])
		if t != "" && !strings.HasPrefix(t, "#") {
			break
		}
		pos--
	}
	if pos > 0 && !strings.HasSuffix(result[pos-1], "\n") {
		result[pos-1] += newline
	}
	for _, key := range order {
		if !seen[key] {
			result = append(result[:pos], append([]string{key + " = " + desired[key] + newline}, result[pos:]...)...)
			pos++
		}
	}
	return strings.Join(result, ""), nil
}

func sameSourceDefinitionTable(baseline, target definitionsTOMLDocument, key string) bool {
	parts := strings.Split(key, "\x00")
	if key == "" {
		return baseline.SchemaVersion == target.SchemaVersion && reflect.DeepEqual(baseline.Ruleset, target.Ruleset)
	}
	if len(parts) < 2 {
		return false
	}
	id := parts[1]
	switch parts[0] {
	case "stats":
		a, ok := baseline.Stats[id]
		b, yes := target.Stats[id]
		return ok && yes && reflect.DeepEqual(a, b)
	case "actions":
		a, ok := baseline.Actions[id]
		b, yes := target.Actions[id]
		if !ok || !yes {
			return false
		}
		if len(parts) == 2 {
			a.Rolls = nil
			b.Rolls = nil
			return a.Name == b.Name && a.Description == b.Description && a.Group == b.Group && slices.Equal(a.Tags, b.Tags)
		}
		if len(parts) == 4 && parts[2] == "rolls" {
			var old, new *rollTOMLSpec
			for i := range a.Rolls {
				if a.Rolls[i].ID == parts[3] {
					old = &a.Rolls[i]
					break
				}
			}
			for i := range b.Rolls {
				if b.Rolls[i].ID == parts[3] {
					new = &b.Rolls[i]
					break
				}
			}
			return old != nil && new != nil && reflect.DeepEqual(*old, *new)
		}
	case "presets":
		a, ok := baseline.Presets[id]
		b, yes := target.Presets[id]
		if !ok || !yes {
			return false
		}
		if len(parts) == 2 {
			a.Stats = nil
			b.Stats = nil
			return reflect.DeepEqual(a, b)
		}
		if len(parts) == 3 && parts[2] == "stats" {
			return maps.EqualFunc(a.Stats, b.Stats, func(x, y any) bool { return reflect.DeepEqual(x, y) })
		}
	}
	return false
}

func equalRulesetRegistries(left, right RulesetRegistry) bool {
	if len(left.Stats) != len(right.Stats) || len(left.Actions) != len(right.Actions) || len(left.Presets) != len(right.Presets) {
		return false
	}
	for id, a := range left.Stats {
		b, ok := right.Stats[id]
		if !ok || !equalStatDefinition(a, b) {
			return false
		}
	}
	for id, a := range left.Actions {
		b, ok := right.Actions[id]
		if !ok || !equalActionDefinition(a, b) {
			return false
		}
	}
	for id, a := range left.Presets {
		b, ok := right.Presets[id]
		if !ok || !equalPresetDefinition(a, b) {
			return false
		}
	}
	return true
}

func sameTOMLDocument(left, right definitionsTOMLDocument) bool {
	if left.SchemaVersion != right.SchemaVersion || !reflect.DeepEqual(left.Ruleset, right.Ruleset) {
		return false
	}
	if len(left.Stats) != len(right.Stats) || len(left.Actions) != len(right.Actions) || len(left.Presets) != len(right.Presets) {
		return false
	}
	for id, a := range left.Stats {
		b, ok := right.Stats[id]
		if !ok || !reflect.DeepEqual(a, b) {
			return false
		}
	}
	for id, a := range left.Actions {
		b, ok := right.Actions[id]
		if !ok || a.Name != b.Name || a.Description != b.Description || a.Group != b.Group || !slices.Equal(a.Tags, b.Tags) || !slices.Equal(a.Rolls, b.Rolls) {
			return false
		}
	}
	for id, a := range left.Presets {
		b, ok := right.Presets[id]
		if !ok || a.Name != b.Name || a.Kind != b.Kind || a.Avatar != b.Avatar || a.Artwork != b.Artwork || !slices.Equal(a.Actions, b.Actions) || !maps.EqualFunc(a.Stats, b.Stats, func(x, y any) bool { return reflect.DeepEqual(x, y) }) {
			return false
		}
	}
	return true
}

// RenderRulesetFileTOML creates a current source-preserving view on demand.
// Manifest.Source is always the original uploaded file, never the output here.
func RenderRulesetFileTOML(snapshot RulesetSnapshot, name string) (string, error) {
	manifest, ok := snapshot.Files[name]
	if !ok || manifest.Source == "" {
		// Upgrade old saved sessions which predate persisted TOML source.
		upgraded, err := RulesetSnapshotWithSources(snapshot)
		if err != nil {
			return "", err
		}
		manifest, ok = upgraded.Files[name]
		if !ok || manifest.Source == "" {
			return "", fmt.Errorf("unknown ruleset file %q", name)
		}
		snapshot = upgraded
	}
	source := manifest.Source
	// Fast path: the original is already the current state.
	desiredRegistry := rulesetFileRegistry(snapshot, manifest)
	var meta *RulesetMetadata
	if manifest.HasMetadata && snapshot.Metadata != (RulesetMetadata{}) {
		copy := snapshot.Metadata
		meta = &copy
	}
	desired, err := encodeDefinitionsDocumentTOML(desiredRegistry, meta)
	if err != nil {
		return "", err
	}
	// Verify normalized TOML values first. For unchanged documents, preserve
	// the original bytes even when the source uses unusual TOML styling.
	baseline, err := decodeRulesetSourceFile(name, source)
	if err != nil {
		return "", err
	}
	targetDoc, err := decodeDefinitionsTOMLDocument(desired)
	if err != nil {
		return "", err
	}
	if sameTOMLDocument(baseline, targetDoc) {
		return source, nil
	}
	oldTables, err := sourceTOMLTables(source)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	newTables, err := sourceTOMLTables(string(desired))
	if err != nil {
		return "", err
	}
	target := map[string]sourceTOMLTable{}
	for _, table := range newTables {
		if _, ok := target[table.key]; ok {
			return "", fmt.Errorf("duplicate target table %q", table.key)
		}
		target[table.key] = table
	}
	existing := map[string]bool{}
	var out strings.Builder
	for _, table := range oldTables {
		want, ok := target[table.key]
		if !ok {
			// Retain comments, but remove header/data of deleted definitions.
			for _, line := range table.lines {
				if strings.HasPrefix(strings.TrimSpace(line), "#") || strings.TrimSpace(line) == "" {
					out.WriteString(line)
				}
			}
			continue
		}
		if existing[table.key] {
			return "", fmt.Errorf("duplicate source table %q", table.key)
		}
		existing[table.key] = true
		if sameSourceDefinitionTable(baseline, targetDoc, table.key) {
			out.WriteString(strings.Join(table.lines, ""))
			continue
		}
		merged, err := rewriteTOMLTable(table, want)
		if err != nil {
			return "", fmt.Errorf("%s %q: %w", name, table.key, err)
		}
		out.WriteString(merged)
	}
	keys := make([]string, 0, len(target))
	for key := range target {
		if !existing[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "" {
			return "", fmt.Errorf("cannot safely add root TOML fields")
		}
		if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
			out.WriteString("\n")
		}
		out.WriteString("\n")
		out.WriteString(strings.Join(target[key].lines, ""))
	}
	result := out.String()
	if len(result) > MaxDefinitionDocumentBytes {
		return "", fmt.Errorf("%s: TOML exceeds size limit", name)
	}
	check, err := decodeRulesetSourceFile(name, result)
	if err != nil {
		return "", fmt.Errorf("%s: rewritten TOML invalid: %w", name, err)
	}
	if !sameTOMLDocument(check, targetDoc) {
		return "", fmt.Errorf("%s: rewritten TOML does not match current definitions", name)
	}
	return result, nil
}
