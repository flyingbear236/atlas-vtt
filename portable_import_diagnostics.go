package main

import (
	"bytes"
	"fmt"
	"slices"
	"sort"

	"github.com/pelletier/go-toml/v2"
)

// The diagnostic pass is read-only. It reports independent problems in a ZIP
// before the usual all-or-nothing candidate validation and commit.
const maxPortableImportIssues = 100

func appendImportIssue(issues []portableImportIssue, file, message string) []portableImportIssue {
	if len(issues) < maxPortableImportIssues {
		return append(issues, portableImportIssue{File: file, Message: message})
	}
	if len(issues) == maxPortableImportIssues {
		return append(issues, portableImportIssue{Message: "Найдено более 100 проблем. Исправьте показанные и повторите проверку."})
	}
	return issues
}

// diagnosePortableImport deliberately does not try to recover from unsafe ZIP
// structure: readPortableImportArchive rejects it before this function runs.
func diagnosePortableImport(base *Session, archive portableImportArchive, opts portableImportOptions) ([]portableImportIssue, bool) {
	issues := append([]portableImportIssue(nil), archive.Diagnostics...)
	incomplete := len(issues) > 0
	files := map[string]string{}
	if opts.Mode == "selected" {
		original, err := RulesetSnapshotWithSources(base.Ruleset)
		if err != nil {
			return appendImportIssue(issues, "", err.Error()), true
		}
		for name := range original.Files {
			content, err := RenderRulesetFileTOML(original, name)
			if err != nil {
				issues = appendImportIssue(issues, name, err.Error())
				incomplete = true
				continue
			}
			files[name] = content
		}
	}
	selected := map[string]bool{}
	for _, name := range opts.SelectedFiles {
		if selected[name] {
			issues = appendImportIssue(issues, name, "Файл выбран несколько раз")
		}
		selected[name] = true
		if _, ok := archive.Ruleset[name]; !ok {
			issues = appendImportIssue(issues, name, "Выбранного TOML нет среди корректных definitions архива")
		}
	}
	for name, content := range archive.Ruleset {
		if opts.Mode == "replace" || selected[name] {
			files[name] = content
		}
	}
	if opts.Mode != "replace" && opts.Mode != "selected" {
		return appendImportIssue(issues, "", "Недопустимый режим импорта"), true
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	statOwners, actionOwners, presetOwners := map[string]string{}, map[string]string{}, map[string]string{}
	type documentAtPath struct {
		path string
		doc  definitionsTOMLDocument
	}
	docs := make([]documentAtPath, 0, len(names))
	for _, name := range names {
		doc, err := decodeRulesetSourceFile(name, files[name])
		if err != nil {
			// A malformed incoming source is already included in archive.Diagnostics.
			issues = appendImportIssue(issues, name, err.Error())
			incomplete = true
			continue
		}
		docs = append(docs, documentAtPath{name, doc})
		for _, entry := range []struct {
			kind string
			ids  []string
			seen map[string]string
		}{
			{"stat", sortedDefinitionIDs(doc.Stats), statOwners},
			{"action", sortedDefinitionIDs(doc.Actions), actionOwners},
			{"preset", sortedDefinitionIDs(doc.Presets), presetOwners},
		} {
			for _, id := range entry.ids {
				if first := entry.seen[id]; first != "" {
					issues = appendImportIssue(issues, name, fmt.Sprintf("Дублирующееся определение %s %q: файлы %q и %q", entry.kind, id, first, name))
				} else {
					entry.seen[id] = name
				}
			}
		}
	}
	// Validate independent definitions even if other files contain duplicates.
	// References may be incomplete if one TOML could not be decoded.
	if !incomplete {
		stats := make(map[string]StatDefinition, len(statOwners))
		for _, entry := range docs {
			for _, id := range sortedDefinitionIDs(entry.doc.Stats) {
				raw := entry.doc.Stats[id]
				def := StatDefinition{ID: id, Name: raw.Name, Type: StatType(raw.Type), Kind: StatKind(raw.Kind), Group: raw.Group}
				if raw.Default != nil {
					value, err := tomlStatValue(raw.Default, def.Type)
					if err != nil {
						issues = appendImportIssue(issues, entry.path, fmt.Sprintf("Характеристика %q: значение по умолчанию: %v", id, err))
					} else {
						def.Default = &value
					}
				}
				if err := ValidateStatDefinition(def); err != nil {
					issues = appendImportIssue(issues, entry.path, err.Error())
				}
				stats[id] = def
			}
		}
		for _, entry := range docs {
			for _, id := range sortedDefinitionIDs(entry.doc.Actions) {
				raw := entry.doc.Actions[id]
				def := ActionDefinition{ID: id, Name: raw.Name, Description: raw.Description, Group: raw.Group, Tags: raw.Tags}
				for _, roll := range raw.Rolls {
					def.Rolls = append(def.Rolls, RollSpec{ID: roll.ID, Name: roll.Name, Count: roll.Count, Sides: roll.Sides, ModifierStat: roll.ModifierStat, ModifierFixed: roll.ModifierFixed})
				}
				if err := validateActionDefinition(def, stats); err != nil {
					issues = appendImportIssue(issues, entry.path, err.Error())
				}
			}
			for _, id := range sortedDefinitionIDs(entry.doc.Presets) {
				raw := entry.doc.Presets[id]
				for _, stat := range sortedDefinitionIDs(raw.Stats) {
					definition, exists := stats[stat]
					if !exists {
						issues = appendImportIssue(issues, entry.path, fmt.Sprintf("Шаблон %q: неизвестная характеристика %q", id, stat))
						continue
					}
					if _, err := tomlStatValue(raw.Stats[stat], definition.Type); err != nil {
						issues = appendImportIssue(issues, entry.path, fmt.Sprintf("Шаблон %q: характеристика %q: %v", id, stat, err))
					}
				}
				for _, action := range raw.Actions {
					if actionOwners[action] == "" {
						issues = appendImportIssue(issues, entry.path, fmt.Sprintf("Шаблон %q: неизвестное действие %q", id, action))
					}
				}
			}
		}
	}
	// Character TOML types and duplicate IDs are independent of ruleset
	// validation. Check them even when definitions are broken.
	characterNames := make([]string, 0, len(archive.Characters))
	for name := range archive.Characters {
		characterNames = append(characterNames, name)
	}
	slices.Sort(characterNames)
	characters := map[string]string{}
	validCharacters := make([]string, 0, len(characterNames))
	for _, name := range characterNames {
		var doc characterTOMLDocument
		if err := toml.NewDecoder(bytes.NewReader(archive.Characters[name])).DisallowUnknownFields().Decode(&doc); err != nil {
			issues = appendImportIssue(issues, name, err.Error())
			continue
		}
		if doc.SchemaVersion != 1 {
			issues = appendImportIssue(issues, name, fmt.Sprintf("Неизвестная версия TOML персонажа: %d", doc.SchemaVersion))
		}
		if !doc.Character.Persistent {
			issues = appendImportIssue(issues, name, "TOML персонажа требует persistent = true")
		}
		if first := characters[doc.Character.ID]; first != "" {
			issues = appendImportIssue(issues, name, fmt.Sprintf("Повтор ID персонажа %q в %q и %q", doc.Character.ID, first, name))
		} else {
			characters[doc.Character.ID] = name
		}
		validCharacters = append(validCharacters, name)
	}
	if len(issues) != 0 {
		return compactImportIssues(issues), true
	}

	ruleset, err := CompileRulesetFiles(files)
	if err != nil {
		return []portableImportIssue{{Message: err.Error()}}, true
	}
	registry, err := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: ruleset.Registry})
	if err != nil {
		return []portableImportIssue{{Message: err.Error()}}, true
	}
	for _, name := range validCharacters {
		if _, err := DecodeCharacterTOML(archive.Characters[name], ruleset.Media, registry); err != nil {
			issues = appendImportIssue(issues, name, err.Error())
		}
	}
	return compactImportIssues(issues), len(issues) != 0
}

func compactImportIssues(issues []portableImportIssue) []portableImportIssue {
	seen := map[portableImportIssue]bool{}
	result := make([]portableImportIssue, 0, len(issues))
	for _, issue := range issues {
		if !seen[issue] && len(result) <= maxPortableImportIssues {
			result = append(result, issue)
			seen[issue] = true
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].File != result[j].File {
			return result[i].File < result[j].File
		}
		return result[i].Message < result[j].Message
	})
	return result
}

func portableImportWarnings(a portableImportArchive, missing []string) []portableImportIssue {
	warnings := make([]portableImportIssue, 0, len(a.Unknown)+len(a.Skipped)+len(missing))
	for name := range a.Unknown {
		warnings = appendImportIssue(warnings, name, "Неизвестный TOML: сохраняется без интерпретации")
	}
	for _, name := range a.Skipped {
		warnings = appendImportIssue(warnings, name, "Неподдерживаемый формат: файл будет пропущен")
	}
	for _, name := range missing {
		warnings = appendImportIssue(warnings, name, "Изображение отсутствует: ссылка будет сохранена")
	}
	slices.SortFunc(warnings, func(a, b portableImportIssue) int {
		if a.File != b.File {
			if a.File < b.File {
				return -1
			}
			return 1
		}
		if a.Message < b.Message {
			return -1
		}
		if a.Message > b.Message {
			return 1
		}
		return 0
	})
	return warnings
}
