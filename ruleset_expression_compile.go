package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type expressionSite struct {
	kind, owner, field string
	source             string
	expected           exprType
	targets            []string
	writePath          string
}
type expressionCompiler struct {
	sites         map[string]expressionSite
	scalars       map[string]exprType
	aliases       map[string]string
	instanceTypes map[string]map[string]exprType
	programs      map[string]*Expression
	visiting      map[string]bool
	level         int
}

func (c *expressionCompiler) addFields(kind, owner string, path []string, raw any) {
	key := rulesetPath(path)
	address := owner + "." + key
	site := expressionSite{kind: kind, owner: owner, field: key, expected: exprDynamic}
	switch v := raw.(type) {
	case map[string]any:
		if formula, ok := v["formula"].(string); ok && len(v) == 1 {
			c.recordField(kind, key, exprDynamic)
			site.source = formula
			c.sites[address] = site
			return
		}
		for child, item := range v {
			c.addFields(kind, owner, append(path, child), item)
		}
	case []any:
		for i, item := range v {
			c.addFields(kind, owner, append(path, strconv.Itoa(i)), item)
		}
	case bool, string, int64:
		c.scalars[address] = literalType(v)
		c.recordField(kind, key, literalType(v))
	}
}

func (c *expressionCompiler) recordField(kind, key string, typ exprType) {
	fields := c.instanceTypes[kind]
	if fields == nil {
		return
	}
	if old, ok := fields[key]; ok && old != typ {
		typ = exprDynamic
	}
	fields[key] = typ
}

func (s *RulesetSnapshot) compileExpressions() error {
	s.expressions = nil
	if s.ExecutionReady() {
		return nil
	}
	c := expressionCompiler{
		sites: map[string]expressionSite{}, scalars: map[string]exprType{}, aliases: map[string]string{},
		instanceTypes: map[string]map[string]exprType{"object": {}, "character": {}},
		programs:      map[string]*Expression{}, visiting: map[string]bool{},
	}
	for kind, entities := range s.v04Document(nil) {
		for id, fields := range entities {
			owner := kind + "." + id
			for key, raw := range fields.values {
				if !slices.Contains(rulesetReservedFields[kind], key) || kind == "stat" && key == "default" {
					c.addFields(kind, owner, []string{key}, raw)
				}
			}
			add := func(field string, raw any, want exprType, targets ...string) {
				if source, ok := raw.(string); ok {
					c.sites[owner+"."+field] = expressionSite{kind: kind, owner: owner, field: field, source: source, expected: want, targets: targets}
				}
			}
			switch kind {
			case "stat":
				c.aliases[owner] = owner + ".default"
				c.aliases[owner+".base"] = owner + ".default"
				stat := s.Registry.Stats[id]
				if stat.IsResource() {
					c.aliases[owner+".current"] = owner + ".default"
				}
				if site, ok := c.sites[owner+".default"]; ok {
					switch stat.Type {
					case StatTypeInteger:
						site.expected = exprNumber
					case StatTypeString:
						site.expected = exprText
					case StatTypeBoolean:
						site.expected = exprBool
					}
					c.sites[owner+".default"] = site
				}
			case "effect":
				targets, _ := rulesetStrings(fields.values["target"], "target", true)
				for i, ref := range targets {
					parts, _ := parseRulesetPath(ref)
					targets[i] = rulesetPath(parts)
				}
				add("formula", fields.values["formula"], exprDynamic, targets...)
				add("when", fields.values["when"], exprBool)
			case "action":
				add("roll", fields.values["roll"], exprNumber)
				add("success", fields.values["success"], exprBool)
				vars, _ := fields.values["var"].(map[string]any)
				for name, raw := range vars {
					add(rulesetPath([]string{"var", name}), raw, exprDynamic)
				}
				for _, block := range []string{"", "on.success.", "on.fail."} {
					body := fields.values
					if block != "" {
						outcomes, _ := body["on"].(map[string]any)
						body, _ = outcomes[strings.Split(block, ".")[1]].(map[string]any)
					}
					if apply, ok := body["apply"].(map[string]any); ok {
						path, _ := apply["path"].(string)
						parts, _ := parseRulesetPath(path)
						var targets []string
						if len(parts) > 2 && parts[1] == "stat" {
							targets = []string{rulesetPath(parts[1:])}
						}
						add(block+"apply.formula", apply["formula"], exprDynamic, targets...)
						key := owner + "." + block + "apply.formula"
						site := c.sites[key]
						site.writePath = rulesetPath(parts)
						c.sites[key] = site
					}
				}
			}
		}
	}
	for _, key := range sortedDefinitionIDs(c.sites) {
		if _, err := c.compile(key); err != nil {
			return err
		}
	}
	s.expressions = c.programs
	return nil
}

func (c *expressionCompiler) addressType(address string) (exprType, error) {
	if alias, ok := c.aliases[address]; ok {
		address = alias
	}
	if t, ok := c.scalars[address]; ok {
		return t, nil
	}
	if _, ok := c.sites[address]; ok {
		program, err := c.compile(address)
		if err != nil {
			return "", err
		}
		return program.Type, nil
	}
	return "", fmt.Errorf("unknown scalar address %q", address)
}
func (c *expressionCompiler) compile(key string) (*Expression, error) {
	if program := c.programs[key]; program != nil {
		return program, nil
	}
	if c.visiting[key] {
		return nil, fmt.Errorf("cyclic expression dependency at %s", key)
	}
	if c.level >= maxExpressionDepth {
		return nil, fmt.Errorf("expression dependency depth exceeded at %s", key)
	}
	c.visiting[key] = true
	c.level++
	defer func() { delete(c.visiting, key); c.level-- }()
	site := c.sites[key]
	random := site.kind == "action" && (site.field == "roll" || site.field == "success" || site.writePath != "" || strings.HasPrefix(site.field, "var."))
	root, err := parseExpression(site.source)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	wants := []exprType{site.expected}
	if site.writePath != "" && len(site.targets) == 0 {
		parts, err := parseRulesetPath(site.writePath)
		if err != nil {
			return nil, err
		}
		want, err := c.resolve(site, parts, exprDynamic)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		wants[0] = want
	}
	if len(site.targets) > 0 {
		wants = nil
		for _, target := range site.targets {
			t, err := c.addressType(target)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			if !slices.Contains(wants, t) {
				wants = append(wants, t)
			}
		}
	}
	var program *Expression
	for _, want := range wants {
		resolve := func(parts []string) (exprType, error) { return c.resolve(site, parts, want) }
		p, err := checkExpression(root, resolve, random)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if !exprFits(p.Type, want) {
			return nil, fmt.Errorf("%s: expected %s, got %s", key, want, p.Type)
		}
		if want != exprDynamic {
			p.Type = want
		}
		p.integer = p.Type == exprNumber && (site.kind == "stat" && site.field == "default" || len(site.targets) > 0 || site.kind == "action" && site.field == "roll")
		// Bind context aliases to the addresses they actually depend on.
		if slices.Contains(p.Dependencies, "value") {
			ref := site.writePath
			if site.kind == "stat" {
				ref = site.owner
			}
			if ref != "" && !slices.Contains(p.Dependencies, ref) {
				p.Dependencies = append(p.Dependencies, ref)
			}
		}
		if site.kind == "effect" && slices.Contains(p.Dependencies, "target") {
			seen := make(map[string]bool, len(p.Dependencies))
			for _, ref := range p.Dependencies {
				seen[ref] = true
			}
			for _, ref := range site.targets {
				if !seen[ref] {
					seen[ref] = true
					p.Dependencies = append(p.Dependencies, ref)
				}
			}
		}
		for _, dep := range p.Dependencies {
			if strings.HasPrefix(dep, "target.") || strings.HasPrefix(dep, "action.target.") {
				p.UsesOptionalTarget = true
			}
			if site.kind == "action" && (dep == "roll" || strings.HasPrefix(dep, "var.")) {
				local := c.programs[site.owner+"."+dep]
				p.UsesOptionalTarget = p.UsesOptionalTarget || local.UsesOptionalTarget
				p.UsesRandom = p.UsesRandom || local.UsesRandom
			}
		}
		program = p
	}
	if len(wants) > 1 {
		program.Type = exprDynamic
		program.integer = false
		program.matchTarget = true
	}
	c.programs[key] = program
	return program, nil
}
func (c *expressionCompiler) resolve(site expressionSite, p []string, targetType exprType) (exprType, error) {
	address := rulesetPath(p)
	switch p[0] {
	case "stat":
		return c.addressType(address)
	case "roll", "var":
		if site.kind == "action" && (p[0] == "roll" && len(p) == 1 || p[0] == "var" && len(p) == 2) {
			if _, exists := c.sites[site.owner+"."+address]; exists {
				return c.addressType(site.owner + "." + address)
			}
		}
	case "value":
		if len(p) == 1 {
			if site.kind == "stat" && site.field != "default" {
				return c.addressType(site.owner)
			}
			if site.writePath != "" {
				return targetType, nil
			}
		}
	case "target":
		if site.kind == "effect" && site.field == "formula" {
			if len(p) == 1 {
				return targetType, nil
			}
			break
		}
		if site.kind == "stat" {
			break
		}
		return c.instanceField("target", p[1:])
	case "action":
		if site.kind == "effect" && len(p) > 2 && p[1] == "target" {
			return c.instanceField("target", p[2:])
		}
	case "actor":
		return c.instanceField("character", p[1:])
	case "source":
		if site.kind != "stat" && len(p) > 1 {
			return c.instanceField("object", p[1:])
		}
	}
	return "", fmt.Errorf("unknown or unavailable reference %q", address)
}
func (c *expressionCompiler) instanceField(kind string, path []string) (exprType, error) {
	if len(path) == 0 {
		return "", fmt.Errorf("expected a scalar instance field")
	}
	if path[0] == "stat" && kind != "object" {
		return c.addressType(rulesetPath(path))
	}
	if len(path) == 1 && (path[0] == "name" || path[0] == "id") {
		return exprText, nil
	}
	key := rulesetPath(path)
	if kind == "target" {
		a, b := c.instanceTypes["object"][key], c.instanceTypes["character"][key]
		if a != "" && (b == "" || a == b) {
			return a, nil
		}
		if b != "" && a == "" {
			return b, nil
		}
	} else if typ, ok := c.instanceTypes[kind][key]; ok {
		return typ, nil
	}
	// Custom instance data is open-ended. Its concrete type is checked on read/use.
	return exprDynamic, nil
}

func (s RulesetSnapshot) actionExpressionFrame(id string, bindings map[string]any, rng DiceRNG) *ExpressionFrame {
	frame := &ExpressionFrame{Bindings: bindings, RNG: rng, Locals: map[string]*Expression{}}
	owner := "action." + id + "."
	if roll := s.expressions[owner+"roll"]; roll != nil {
		frame.Locals["roll"] = roll
	}
	if action := s.Registry.Actions[id]; action.V04 != nil {
		vars, _ := action.V04.values["var"].(map[string]any)
		for name := range vars {
			key := rulesetPath([]string{"var", name})
			frame.Locals[key] = s.expressions[owner+key]
		}
	}
	return frame
}
