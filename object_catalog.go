package main

import "fmt"

type catalogItemChoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type catalogObjectEntry struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	Actions     []string              `json:"actions,omitempty"`
	Effects     []string              `json:"effects,omitempty"`
	Properties  []inventorySheetField `json:"properties,omitempty"`
}
type objectCatalog struct {
	Objects []catalogObjectEntry `json:"objects"`
	Actions []catalogItemChoice  `json:"actions"`
	Effects []catalogItemChoice  `json:"effects"`
}

func objectCatalogForEditor(registry EffectiveRegistry) objectCatalog {
	catalog := objectCatalog{Objects: []catalogObjectEntry{}, Actions: []catalogItemChoice{}, Effects: []catalogItemChoice{}}
	for _, id := range sortedDefinitionIDs(registry.Actions) {
		def := registry.Actions[id]
		if def.V04 != nil {
			catalog.Actions = append(catalog.Actions, catalogItemChoice{ID: id, Name: def.Name})
		}
	}
	for _, id := range sortedDefinitionIDs(registry.Effects) {
		def := registry.Effects[id]
		name, _ := def.values["name"].(string)
		if name == "" {
			name = id
		}
		catalog.Effects = append(catalog.Effects, catalogItemChoice{ID: id, Name: name})
	}
	for _, id := range sortedDefinitionIDs(registry.Objects) {
		def := registry.Objects[id]
		name, _ := def.values["name"].(string)
		if name == "" {
			name = id
		}
		desc, _ := def.values["description"].(string)
		entry := catalogObjectEntry{ID: id, Name: name, Description: desc}
		if registry.mechanics != nil {
			entry.Actions = append([]string(nil), registry.mechanics.objects[id].actions...)
			entry.Effects = append([]string(nil), registry.mechanics.objects[id].effects...)
		}
		for _, key := range sortedDefinitionIDs(def.values) {
			switch key {
			case "name", "description", "actions", "effects":
				continue
			}
			entry.Properties = append(entry.Properties, inventorySheetField{Path: []string{key}, Value: fmt.Sprint(def.values[key])})
		}
		catalog.Objects = append(catalog.Objects, entry)
	}
	return catalog
}
