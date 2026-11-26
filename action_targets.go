package main

import (
	"cmp"
	"slices"
)

func inventoryItemName(objects map[string]*RulesetFields, item InventoryItem) string {
	if definition := objects[item.ObjectID]; definition != nil {
		if name, ok := definition.values["name"].(string); ok && name != "" {
			return name
		}
	}
	return item.ObjectID
}

type actionTargetOption struct {
	ActionInstanceRef
	Name string `json:"name"`
}

func (s *Server) sendActionTargets(session *Session, p *peer, command Command) {
	response := map[string]any{"type": "actionTargets", "sceneId": p.sceneID, "seq": command.Seq}
	defer func() { s.send(p, response) }()
	a := audienceForAction(session, p)
	request := command.Action
	if request == nil || request.Target != nil || command.SceneID != p.sceneID || !a.seesCharacter(request.CharacterID, true) {
		response["error"] = "Действие недоступно"
		return
	}
	selector := session.Ruleset.actionPlans[request.ActionID].selector
	if selector != "select(character)" && selector != "select(object)" {
		response["error"] = "Действие не использует выбор цели"
		return
	}
	var run actionExecution
	if err := run.prepare(session, p.sceneID, p.member, *request, nil); err != nil {
		response["error"] = "Действие или источник недоступны"
		return
	}
	options := []actionTargetOption{}
	if selector == "select(object)" {
		// The region is already spatially indexed and bounded by the current subscription.
		if p.region != nil {
			for _, e := range a.scene.ensureRuntime().queryElements(*p.region) {
				if a.seesObject(e.ID) {
					options = append(options, actionTargetOption{ActionInstanceRef: ActionInstanceRef{ElementID: e.ID}, Name: objectName(e)})
				}
			}
		}
		response["targets"] = options
		return
	}
	seen := map[string]bool{}
	for _, token := range a.scene.Tokens {
		id := token.CharacterInstanceID
		if seen[id] {
			continue
		}
		seen[id] = true
		if !a.seesCharacter(id, false) {
			continue
		}
		instance, ok := session.CharacterInstances[id]
		if !ok {
			continue
		}
		options = append(options, actionTargetOption{ActionInstanceRef{CharacterID: id}, instance.Name})
	}
	slices.SortFunc(options, func(a, b actionTargetOption) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.CharacterID, b.CharacterID), cmp.Compare(a.ItemID, b.ItemID))
	})
	response["targets"] = options
}
