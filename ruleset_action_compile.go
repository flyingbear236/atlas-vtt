package main

import (
	"fmt"
	"strings"
)

type compiledAction struct {
	selector                  string
	success                   *Expression
	direct, onSuccess, onFail actionConsequences
}

type actionConsequences struct {
	message *actionMessage
	apply   *actionApply
}

type actionApply struct {
	path    []string
	formula *Expression
}

type actionMessage struct {
	parts      []messagePart
	usesTarget bool
}

type messagePart struct {
	text string
	ref  *exprNode
}

func (c *expressionCompiler) compileActionPlans(definitions map[string]ActionDefinition) (map[string]compiledAction, error) {
	plans := map[string]compiledAction{}
	for _, id := range sortedDefinitionIDs(definitions) {
		def := definitions[id]
		if def.V04 == nil || !rulesetExecutable(def.V04.values) {
			continue
		}
		body := def.V04.values
		owner := "action." + id
		plan := compiledAction{success: c.programs[owner+".success"]}
		plan.selector, _ = body["target"].(string)
		outcomes, _ := body["on"].(map[string]any)
		pass, _ := outcomes["success"].(map[string]any)
		fail, _ := outcomes["fail"].(map[string]any)
		for _, block := range []struct {
			prefix string
			fields map[string]any
			dest   *actionConsequences
		}{
			{"", body, &plan.direct}, {"on.success.", pass, &plan.onSuccess}, {"on.fail.", fail, &plan.onFail},
		} {
			if raw, ok := block.fields["message"].(string); ok {
				msg, err := c.compileActionMessage(owner, plan.selector, raw)
				if err != nil {
					return nil, fmt.Errorf("%s.%smessage: %w", owner, block.prefix, err)
				}
				block.dest.message = msg
			}
			if fields, ok := block.fields["apply"].(map[string]any); ok {
				path, _ := parseRulesetPath(fields["path"].(string))
				block.dest.apply = &actionApply{path: path, formula: c.programs[owner+"."+block.prefix+"apply.formula"]}
			}
		}
		plans[id] = plan
	}
	return plans, nil
}

func (c *expressionCompiler) compileActionMessage(owner, selector, text string) (*actionMessage, error) {
	message := &actionMessage{}
	for {
		start := strings.IndexByte(text, '{')
		if start < 0 {
			message.parts = append(message.parts, messagePart{text: text})
			return message, nil
		}
		if len(message.parts)/2 >= maxExpressionSteps {
			return nil, fmt.Errorf("message reference limit exceeded")
		}
		message.parts = append(message.parts, messagePart{text: text[:start]})
		text = text[start+1:]
		end := messageReferenceEnd(text)
		if end < 0 {
			return nil, fmt.Errorf("unterminated message reference")
		}
		path, err := parseRulesetPath(strings.TrimSpace(text[:end]))
		if err != nil || len(path) == 0 {
			return nil, fmt.Errorf("invalid message reference %q", text[:end])
		}
		typ, err := c.resolve(expressionSite{kind: "action", owner: owner, selector: selector}, path, exprDynamic)
		if err != nil {
			return nil, err
		}
		address := rulesetPath(path)
		ref := &exprNode{op: "read", path: path, address: address, readType: typ}
		message.parts = append(message.parts, messagePart{ref: ref})
		if path[0] == "target" {
			message.usesTarget = true
		}
		if local := c.programs[owner+"."+address]; (path[0] == "roll" || path[0] == "var") && local != nil {
			message.usesTarget = message.usesTarget || local.UsesOptionalTarget
		}
		text = text[end+1:]
	}
}

// Quoted path segments can contain a literal closing brace.
func messageReferenceEnd(text string) int {
	var quote byte
	for i := 0; i < len(text); i++ {
		b := text[i]
		if quote != 0 {
			if b == '\\' && quote == '"' {
				i++
				continue
			}
			if b == quote {
				quote = 0
			}
		} else {
			switch b {
			case '}':
				return i
			case '\'', '"':
				quote = b
			}
		}
	}
	return -1
}

func (m actionMessage) render(frame *ExpressionFrame) (string, error) {
	var out strings.Builder
	for _, part := range m.parts {
		text := part.text
		if part.ref != nil {
			v, err := frame.eval(part.ref)
			if err != nil {
				return "", err
			}
			if v.typ == exprNumber {
				text = v.num.RatString()
			} else {
				text = fmt.Sprint(v.scalar())
			}
		}
		if len(text) > MaxDefinitionDocumentBytes-out.Len() {
			return "", fmt.Errorf("expanded message exceeds size limit")
		}
		out.WriteString(text)
	}
	return out.String(), nil
}
