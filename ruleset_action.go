package main

import "fmt"

type ActionExecutionRequest struct {
	CharacterID string             `json:"characterId"`
	ActionID    string             `json:"actionId"`
	SourceID    string             `json:"sourceId,omitempty"`
	Target      *ActionInstanceRef `json:"target,omitempty"`
}

// A selected scene object uses ElementID. CharacterID and ItemID identify
// character and inventory source instances; they are exclusive of ElementID.
type ActionInstanceRef struct {
	CharacterID string `json:"characterId"`
	ItemID      string `json:"itemId,omitempty"`
	ElementID   string `json:"elementId,omitempty"`
}

type ActionExecutionResult struct {
	// Provenance of the messages actually emitted, in the same order. The
	// server uses their compiled references to project interpolated values.
	messageTemplates []*actionMessage
	ActionID         string                 `json:"actionId"`
	Actor            ActionInstanceRef      `json:"actor"`
	Source           *ActionInstanceRef     `json:"source,omitempty"`
	Target           *ActionInstanceRef     `json:"target,omitempty"`
	Roll             *ActionExecutionRoll   `json:"roll,omitempty"`
	Variables        map[string]any         `json:"variables,omitempty"`
	Success          *bool                  `json:"success,omitempty"`
	Messages         []string               `json:"messages,omitempty"`
	Plan             *ActionChangePlan      `json:"plan,omitempty"`
	Skipped          []ActionExecutionIssue `json:"skipped,omitempty"`
	Trace            *ActionTrace           `json:"-"`
	Errors           []ActionExecutionIssue `json:"errors,omitempty"`
}

type ActionExecutionRoll struct {
	Base  int64       `json:"base"`
	Total int64       `json:"total"`
	Dice  []DiceGroup `json:"dice,omitempty"`
}

type ActionExecutionIssue struct {
	Operation string `json:"operation"`
	Message   string `json:"message"`
}

type actionExecution struct {
	session               *Session
	registry              EffectiveRegistry
	frame                 *ExpressionFrame
	refs                  map[string]ActionInstanceRef
	missingOptionalTarget bool
	plan                  ActionChangePlan
	result                ActionExecutionResult
	statTraces            map[string]*statCalculationTrace
}

// ExecuteRulesetAction reads a consistent server snapshot. Callers holding a
// live session must hold its server lock. The plan includes the input revisions
// so the commit path can reject stale execution results.
func ExecuteRulesetAction(session *Session, sceneID string, member *Member, req ActionExecutionRequest, rng DiceRNG) (ActionExecutionResult, error) {
	run := actionExecution{result: ActionExecutionResult{ActionID: req.ActionID, Actor: ActionInstanceRef{CharacterID: req.CharacterID}}}
	if err := run.prepare(session, sceneID, member, req, rng); err != nil {
		return run.failure("context", err)
	}
	action := session.Ruleset.actionPlans[req.ActionID]
	if program := run.frame.Locals["roll"]; program != nil && !run.skip("roll", program.UsesOptionalTarget) {
		roll := &ActionExecutionRoll{}
		modify := run.frame.modifyRoll
		run.frame.modifyRoll = func(base exprValue) (exprValue, error) {
			roll.Base = base.num.Num().Int64()
			if run.result.Trace != nil {
				run.result.Trace.RollBase = traceScalar(roll.Base)
			}
			if modify != nil {
				return modify(base)
			}
			return base, nil
		}
		run.frame.diceTrace = &roll.Dice
		value, err := run.frame.Evaluate(program)
		run.frame.diceTrace = nil
		if err != nil {
			return run.failure("roll", err)
		}
		roll.Total = value.num.Num().Int64()
		if run.result.Trace != nil {
			run.result.Trace.RollTotal = traceScalar(roll.Total)
		}
		run.result.Roll = roll
	}
	if action.success != nil && !run.skip("success", action.success.UsesOptionalTarget) {
		value, err := run.frame.Evaluate(action.success)
		if err != nil {
			return run.failure("success", err)
		}
		run.result.Success = &value.flag
	}
	if op, err := run.consequences("", action.direct); err != nil {
		return run.failure(op, err)
	}
	if run.result.Success != nil {
		prefix, block := "on.fail.", action.onFail
		if *run.result.Success {
			prefix, block = "on.success.", action.onSuccess
		}
		if op, err := run.consequences(prefix, block); err != nil {
			return run.failure(op, err)
		}
	} else if action.success != nil {
		if action.onSuccess != (actionConsequences{}) {
			run.skip("on.success", true)
		}
		if action.onFail != (actionConsequences{}) {
			run.skip("on.fail", true)
		}
	}
	if err := run.validatePlan(); err != nil {
		return run.failure("plan", err)
	}
	if len(run.plan.Changes) > 0 {
		run.result.Plan = &run.plan
	}
	run.collectVariables()
	if run.result.Trace != nil {
		run.result.Trace.SkippedOperations = run.result.Skipped
	}
	return run.result, nil
}

func (r *actionExecution) failure(op string, err error) (ActionExecutionResult, error) {
	r.collectVariables()
	r.result.Errors = append(r.result.Errors, ActionExecutionIssue{op, err.Error()})
	return r.result, fmt.Errorf("%s: %w", op, err)
}

func (r *actionExecution) collectVariables() {
	if r.frame == nil {
		return
	}
	for _, name := range sortedDefinitionIDs(r.frame.Locals) {
		if name == "roll" {
			continue
		}
		program := r.frame.Locals[name]
		if r.skip(name, program.UsesOptionalTarget) {
			continue
		}
		if cached, ok := r.frame.cache[program]; ok && cached.err == nil {
			if r.result.Variables == nil {
				r.result.Variables = map[string]any{}
			}
			r.result.Variables[name] = cached.value.scalar()
		}
	}
}

func (r *actionExecution) skip(op string, dependsOnTarget bool) bool {
	if !dependsOnTarget || !r.missingOptionalTarget {
		return false
	}
	r.result.Skipped = append(r.result.Skipped, ActionExecutionIssue{op, "optional target is missing"})
	return true
}

func (r *actionExecution) consequences(prefix string, block actionConsequences) (string, error) {
	if apply := block.apply; apply != nil && !r.skip(prefix+"apply", apply.path[0] == "target" || apply.formula.UsesOptionalTarget) {
		if err := r.apply(apply); err != nil {
			return prefix + "apply", err
		}
	}
	if message := block.message; message != nil && !r.skip(prefix+"message", message.usesTarget) {
		text, err := message.render(r.frame)
		if err != nil {
			return prefix + "message", err
		}
		r.result.Messages = append(r.result.Messages, text)
		r.result.messageTemplates = append(r.result.messageTemplates, message)
	}
	return "", nil
}

func (r *actionExecution) prepare(session *Session, sceneID string, member *Member, req ActionExecutionRequest, rng DiceRNG) error {
	if session == nil || member == nil {
		return fmt.Errorf("authenticated campaign member is required")
	}
	member = session.Members[member.ID]
	scene := session.Scenes[sceneID]
	if member == nil || !sceneVisible(scene, member) {
		return fmt.Errorf("scene is unavailable")
	}
	actor, exists := session.CharacterInstances[req.CharacterID]
	if !exists || !characterHasControllableToken(session, scene, member, req.CharacterID) {
		return fmt.Errorf("actor is unavailable in scene")
	}
	plan, ok := session.Ruleset.actionPlans[req.ActionID]
	if !ok {
		if action := session.Ruleset.Registry.Actions[req.ActionID]; action.V04 != nil {
			if rulesetExecutable(action.V04.values) {
				return fmt.Errorf("action %q has no compiled plan", req.ActionID)
			}
			return fmt.Errorf("action group %q cannot be executed", req.ActionID)
		}
		return fmt.Errorf("unknown v0.4 action %q", req.ActionID)
	}
	registry, err := rollRegistry(session)
	if err != nil {
		return err
	}
	r.statTraces = make(map[string]*statCalculationTrace)
	preset := registry.Presets[actor.PresetID]
	state, err := projectCharacterStatsWithTrace(actor, &preset, registry, nil, nil, false, r.statTraces)
	if err != nil {
		return err
	}
	bindings, source, err := characterActionBindings(actor, state, registry, ActionSource{ActionID: req.ActionID, SourceID: req.SourceID})
	if err != nil {
		return err
	}
	r.session, r.registry = session, registry
	r.refs = map[string]ActionInstanceRef{"actor": {CharacterID: actor.ID}}
	if source != nil {
		ref := ActionInstanceRef{CharacterID: actor.ID, ItemID: source.ID}
		r.refs["source"] = ref
		r.result.Source = &ref
	}
	if err := r.bindTarget(scene, member, plan.selector, req.Target, bindings, state); err != nil {
		return err
	}
	r.frame = session.Ruleset.actionFrameWithEffects(req.ActionID, bindings, rng, state.activeEffects)
	r.frame.effectOverrides = itemEffectOverrides(actor)
	r.result.Trace = &ActionTrace{}
	if def := registry.Actions[req.ActionID]; def.V04 != nil {
		r.result.Trace.RollFormula, _ = def.V04.values["roll"].(string)
		r.result.Trace.SuccessFormula, _ = def.V04.values["success"].(string)
	}
	r.frame.observeRead = r.appendTraceRead
	r.frame.traceEffect = r.appendTraceEffect
	r.frame.traceSkip = r.appendTraceSkip
	r.plan = ActionChangePlan{
		SessionID: session.ID, SceneID: scene.ID, SceneRevision: scene.Revision,
		RegistryRevision: session.RegistryRevision, CharacterRevision: session.CharacterRevision,
		CampaignRevision: session.CampaignRevision,
	}
	return nil
}

func (r *actionExecution) bindTarget(scene *Scene, member *Member, selector string, requested *ActionInstanceRef, bindings map[string]any, actor EffectiveCharacter) error {
	var ref ActionInstanceRef
	switch selector {
	case "self", "source":
		if requested != nil {
			return fmt.Errorf("target is determined by the action")
		}
		if selector == "self" {
			ref = r.refs["actor"]
		} else {
			ref = r.refs["source"]
			if ref.ItemID == "" {
				return fmt.Errorf("action requires an item source")
			}
		}
	case "select(object)":
		if requested == nil {
			r.missingOptionalTarget = true
			return nil
		}
		ref = *requested
		if ref.ElementID == "" || ref.CharacterID != "" || ref.ItemID != "" {
			return fmt.Errorf("selected target has the wrong instance type")
		}
		element, ok := scene.Elements[ref.ElementID]
		if !ok || !activeObject(element) || !scene.ensureRuntime().elementPublic(element) {
			return fmt.Errorf("selected object is unavailable")
		}
		bindings["target"] = map[string]any{"id": element.ID, "name": objectName(element)}
		r.refs["target"] = ref
		r.result.Target = &ref
		return nil
	case "select(character)":
		if requested == nil {
			r.missingOptionalTarget = true
			return nil
		}
		ref = *requested
		if ref.ItemID != "" || ref.ElementID != "" {
			return fmt.Errorf("selected target has the wrong instance type")
		}
		if !actionCharacterVisible(r.session, scene, member, ref.CharacterID) {
			return fmt.Errorf("selected character is unavailable")
		}
	default:
		if requested != nil {
			return fmt.Errorf("action has no target selector")
		}
		return nil
	}
	instance, ok := r.session.CharacterInstances[ref.CharacterID]
	if !ok {
		return fmt.Errorf("selected character is unavailable")
	}
	if ref.ItemID != "" {
		if selector == "source" {
			bindings["target"] = bindings["source"]
		} else {
			item, ok := findInventoryItem(instance, ref.ItemID)
			if !ok {
				return fmt.Errorf("selected object is unavailable")
			}
			if r.registry.Objects[item.ObjectID] == nil {
				return fmt.Errorf("selected object has unknown definition")
			}
			bindings["target"] = r.registry.itemBindings(item)
		}
	} else {
		state := actor
		if ref.CharacterID != actor.ID {
			var err error
			state, err = characterStatsForRegistry(instance, r.registry)
			if err != nil {
				return fmt.Errorf("target: %w", err)
			}
		}
		bindings["target"] = characterInstanceBindings(instance)
		for address, value := range state.StatValues {
			bindings["target."+address] = value
		}
	}
	r.refs["target"] = ref
	r.result.Target = &ref
	return nil
}

func actionCharacterVisible(session *Session, scene *Scene, member *Member, id string) bool {
	for ref := range session.characterReferences[id] {
		if ref.SceneID != scene.ID {
			continue
		}
		if token, ok := scene.Tokens[ref.TokenID]; ok && token.CharacterInstanceID == id && (!token.Hidden || memberIsGM(member)) {
			return true
		}
	}
	return false
}

func findInventoryItem(instance CharacterInstance, id string) (InventoryItem, bool) {
	for _, item := range instance.Inventory {
		if item.ID == id {
			return item, true
		}
	}
	return InventoryItem{}, false
}
