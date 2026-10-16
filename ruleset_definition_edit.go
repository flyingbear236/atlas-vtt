package main

// Legacy command names remain accepted, but share the unified update path.
func applyRulesetDefinitionCommand(session *Session, command Command) (RulesetSnapshot, bool, error) {
	return applyDefinitionCommand(session, command)
}
