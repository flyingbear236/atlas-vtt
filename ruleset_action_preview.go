package main

import (
	"fmt"
	"maps"
	"strings"
)

// ActionPreview describes the known parts of a roll without throwing dice or
// applying its consequences. Previews are projected from the same actor state
// as the character sheet, so changing equipment requires no dependency index.
type ActionPreview struct {
	ActionID string              `json:"actionId"`
	SourceID string              `json:"sourceId,omitempty"`
	Formula  string              `json:"formula,omitempty"`
	Parts    []ActionPreviewPart `json:"parts,omitempty"`
	Notes    []string            `json:"notes,omitempty"`
}

type ActionPreviewPart struct {
	Label string `json:"label"`
	Value string `json:"value,omitempty"`
	Sign  string `json:"sign,omitempty"`
}

func actionPreviewsForSheet(ruleset RulesetSnapshot, instance CharacterInstance, state EffectiveCharacter, registry EffectiveRegistry) []ActionPreview {
	if len(state.ActionSources) == 0 || len(ruleset.actionPlans) == 0 {
		return nil
	}
	previews := make([]ActionPreview, 0, len(state.ActionSources))
	base := make(map[string]any, len(state.StatValues)+1)
	base["actor"] = characterInstanceBindings(instance)
	for address, value := range state.StatValues {
		base[address] = value
	}
	sources := make(map[string]map[string]any, len(instance.Inventory))
	for _, item := range instance.Inventory {
		if item.Equipped {
			sources[item.ID] = registry.itemBindings(item)
		}
	}
	self := maps.Clone(base)
	self["target"] = base["actor"]
	for address, value := range state.StatValues {
		self["target."+address] = value
	}
	for _, choice := range state.ActionSources {
		if _, ok := ruleset.actionPlans[choice.ActionID]; !ok {
			continue
		}
		preview := ActionPreview{ActionID: choice.ActionID, SourceID: choice.SourceID}
		bindings := base
		if ruleset.actionPlans[choice.ActionID].selector == "self" {
			bindings = self
		}
		delete(bindings, "source")
		if ruleset.actionPlans[choice.ActionID].selector != "self" {
			delete(bindings, "target")
		}
		if choice.SourceID != "" {
			source, ok := sources[choice.SourceID]
			if !ok {
				continue
			}
			bindings["source"] = source
		}
		// A self/source selector is already determined by the actor. A selected
		// target is deliberately unknown until the player chooses one.
		switch ruleset.actionPlans[choice.ActionID].selector {
		case "self":
			// Prebuilt above.
		case "source":
			bindings["target"] = bindings["source"]
		}
		roll := ruleset.expressions["action."+choice.ActionID+".roll"]
		if roll == nil {
			continue
		}
		preview.Formula, _ = registry.Actions[choice.ActionID].V04.values["roll"].(string)
		frame := ruleset.actionFrameWithEffects(choice.ActionID, bindings, nil, nil)
		// Never evaluate the roll program: doing so would draw random dice.
		appendPreviewExpression(&preview, roll.root, frame, registry, 1)
		appendPreviewEffects(&preview, state.activeEffects.forTarget("action."+choice.ActionID+".roll"), frame, registry)
		previews = append(previews, preview)
	}
	return previews
}

func previewNeedsRollValue(n *exprNode) bool {
	if n == nil {
		return false
	}
	if n.op == "read" && (n.address == "target" || n.address == "roll") {
		return true
	}
	return previewNeedsRollValue(n.left) || previewNeedsRollValue(n.right)
}

func previewContainsDice(n *exprNode) bool {
	if n == nil {
		return false
	}
	return n.op == "throw" || n.op == "dice" || previewContainsDice(n.left) || previewContainsDice(n.right)
}

func previewLabel(address string, registry EffectiveRegistry) string {
	path, err := parseRulesetPath(address)
	if err == nil {
		if len(path) > 2 && path[0] == "actor" && path[1] == "stat" {
			path = path[1:]
		}
		if len(path) > 1 && path[0] == "stat" {
			for end := len(path); end > 1; end-- {
				id := rulesetPath(path[1:end])
				if definition, ok := registry.Stats[id]; ok {
					name := definition.Name
					if name == "" {
						name = id
					}
					if end == len(path) {
						return name
					}
					return name + " · " + rulesetPath(path[end:])
				}
			}
		}
	}
	return address
}

func previewNumber(value exprValue) (string, bool) {
	if value.typ != exprNumber || value.num == nil {
		return "", false
	}
	if value.num.IsInt() {
		return value.num.Num().String(), true
	}
	return value.num.RatString(), true
}

// A reference may resolve to a local variable containing dice. The guard
// prevents accidental random draws even through those indirect dependencies.
type previewNoDiceRNG struct{}

func (previewNoDiceRNG) Intn(int) (int, error) {
	return 0, fmt.Errorf("rolls are unavailable in previews")
}

func previewEvalNode(frame *ExpressionFrame, node *exprNode) (exprValue, error) {
	if previewContainsDice(node) {
		return exprValue{}, fmt.Errorf("random expression")
	}
	copy := &ExpressionFrame{Bindings: frame.Bindings, Locals: frame.Locals, RNG: previewNoDiceRNG{}}
	return copy.eval(node)
}

func appendPreviewExpression(preview *ActionPreview, node *exprNode, frame *ExpressionFrame, registry EffectiveRegistry, sign int) {
	if node == nil {
		return
	}
	if node.op == "+" || node.op == "-" {
		appendPreviewExpression(preview, node.left, frame, registry, sign)
		if node.op == "+" {
			appendPreviewExpression(preview, node.right, frame, registry, sign)
		} else {
			appendPreviewExpression(preview, node.right, frame, registry, -sign)
		}
		return
	}
	if node.op == "unary-" || node.op == "unary+" {
		if node.op == "unary-" {
			sign = -sign
		}
		appendPreviewExpression(preview, node.left, frame, registry, sign)
		return
	}
	part := ActionPreviewPart{Sign: "+"}
	if sign < 0 {
		part.Sign = "-"
	}
	switch node.op {
	case "throw":
		d := node.literal.(exprDice)
		part.Label = fmt.Sprintf("%dd%d", d.count, d.sides)
	case "dice":
		value, err := previewEvalNode(frame, node.left)
		if err != nil || value.typ != exprText {
			preview.Notes = appendPreviewNote(preview.Notes, "Кубики зависят от контекста действия")
			return
		}
		if _, err := parseExprDice(value.text); err != nil {
			preview.Notes = appendPreviewNote(preview.Notes, "Кубики зависят от контекста действия")
			return
		}
		part.Label = value.text
	case "read", "literal":
		part.Label = "Число"
		if node.op == "read" {
			part.Label = previewLabel(node.address, registry)
		}
		v, err := previewEvalNode(frame, node)
		if err != nil {
			if strings.HasPrefix(node.address, "target.") || strings.HasPrefix(node.address, "action.target.") {
				part.Value = "?"
				preview.Notes = appendPreviewNote(preview.Notes, "Есть значения, зависящие от выбранной цели")
			} else {
				preview.Notes = appendPreviewNote(preview.Notes, "Часть значений вычисляется при выполнении")
				return
			}
		} else if number, ok := previewNumber(v); ok {
			part.Value = number
		} else {
			preview.Notes = appendPreviewNote(preview.Notes, "Часть значений вычисляется при выполнении")
			return
		}
	default:
		// Keep non-additive formulas intact rather than pretending that a
		// multiplication, division or floor() is a plain additive bonus.
		preview.Notes = appendPreviewNote(preview.Notes, "Сложная часть формулы рассчитывается при выполнении")
		return
	}
	if len(preview.Parts) >= 48 {
		preview.Notes = appendPreviewNote(preview.Notes, "Слишком много слагаемых для предварительного просмотра")
		return
	}
	preview.Parts = append(preview.Parts, part)
}

func appendPreviewNote(notes []string, note string) []string {
	for _, current := range notes {
		if current == note {
			return notes
		}
	}
	return append(notes, note)
}

// Only effects of the form "target + expression" or "target - expression"
// can be shown as independent additive terms. Other effects stay in the real
// executor and are explicitly marked as requiring a full calculation.
func appendPreviewEffects(preview *ActionPreview, entries []*activeDefinition, frame *ExpressionFrame, registry EffectiveRegistry) {
	ctx := actionEffectContext(frame.Bindings)
	for _, effect := range entries {
		if effect.needsSource && !ctx.hasSource {
			continue
		}
		if effect.needsTarget && !ctx.hasTarget {
			preview.Notes = appendPreviewNote(preview.Notes, "Есть эффекты, зависящие от выбранной цели")
			continue
		}
		count := len(effect.sources)
		if effect.sourceScope {
			if ctx.sourceID == "" || !effect.grantedByItem(ctx.sourceID) {
				continue
			}
			count = 1
		}
		if effect.when != nil {
			if previewNeedsRollValue(effect.when.root) {
				preview.Notes = appendPreviewNote(preview.Notes, "Условие эффекта зависит от результата броска")
				continue
			}
			v, err := previewEvalNode(frame, effect.when.root)
			if err != nil {
				preview.Notes = appendPreviewNote(preview.Notes, "Есть условные эффекты, определяемые при выполнении")
				continue
			}
			if !v.flag {
				continue
			}
		}
		if effect.unique {
			count = 1
		}
		if effect.formula == nil || effect.formula.root == nil {
			continue
		}
		root := effect.formula.root
		sign := 1
		var addition *exprNode
		switch {
		case root.op == "+" && root.left != nil && root.left.op == "read" && root.left.address == "target":
			addition = root.right
		case root.op == "+" && root.right != nil && root.right.op == "read" && root.right.address == "target":
			addition = root.left
		case root.op == "-" && root.left != nil && root.left.op == "read" && root.left.address == "target":
			addition, sign = root.right, -1
		}
		if addition == nil || previewContainsDice(addition) || previewNeedsRollValue(addition) {
			preview.Notes = appendPreviewNote(preview.Notes, "Есть сложные эффекты, учитываемые при броске")
			continue
		}
		if len(preview.Parts)+count > 48 {
			preview.Notes = appendPreviewNote(preview.Notes, "Повторяющиеся эффекты показаны частично")
			count = max(0, 48-len(preview.Parts))
		}
		for i := 0; i < count; i++ {
			before := len(preview.Parts)
			appendPreviewExpression(preview, addition, frame, registry, sign)
			if len(preview.Parts) > before && addition.op == "literal" {
				name := "Эффект: " + effect.id
				if definition := registry.Effects[effect.id]; definition != nil {
					if label, ok := definition.values["name"].(string); ok && label != "" {
						name = label
					}
				}
				preview.Parts[len(preview.Parts)-1].Label = name
			}
		}
	}
}
