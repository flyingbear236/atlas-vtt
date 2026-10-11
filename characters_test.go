package main

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func baseCharacterRegistries() DefinitionRegistries {
	return DefinitionRegistries{
		Ruleset: RulesetRegistry{
			Stats: map[string]StatDefinition{
				"hp":       {ID: "hp", Name: "HP", Type: StatTypeInteger},
				"accuracy": {ID: "accuracy", Name: "Accuracy", Type: StatTypeNumber},
			},
			Actions: map[string]ActionDefinition{
				"bite": {ID: "bite", Name: "Bite", Tags: []string{"attack"}},
				"dash": {ID: "dash", Name: "Dash"},
			},
			Presets: map[string]CharacterPresetDefinition{
				"wolf": {
					ID:        "wolf",
					Name:      "Wolf",
					Kind:      "monster",
					Stats:     map[string]StatValue{"hp": IntegerStatValue(11)},
					ActionIDs: []string{"bite", "bite", "dash"},
				},
			},
		},
		Campaign: CampaignRegistry{
			Stats:   map[string]StatDefinition{},
			Actions: map[string]ActionDefinition{},
			Presets: map[string]CharacterPresetDefinition{},
		},
	}
}

func TestMergeDefinitionRegistriesPrecedenceAndNamespaces(t *testing.T) {
	registries := baseCharacterRegistries()
	registries.Campaign.Stats["hp"] = StatDefinition{ID: "hp", Name: "Hit points", Type: StatTypeInteger}
	registries.Campaign.Stats["воля.духа"] = StatDefinition{ID: "воля.духа", Name: "Воля духа", Type: StatTypeBoolean}
	registries.Campaign.Actions["wolf"] = ActionDefinition{ID: "wolf", Name: "Wolf action"}
	registries.Campaign.Presets["wolf"] = CharacterPresetDefinition{ID: "wolf", Name: "Red wolf", Kind: "monster"}

	merged, err := MergeDefinitionRegistries(registries)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Stats["hp"].Name != "Hit points" || merged.Actions["wolf"].Name != "Wolf action" || merged.Presets["wolf"].Name != "Red wolf" {
		t.Fatalf("unexpected merged registry: %#v", merged)
	}
	if _, ok := merged.Stats["воля.духа"]; !ok {
		t.Fatal("Unicode stat ID was not preserved")
	}

	merged.Actions["bite"] = ActionDefinition{}
	if registries.Ruleset.Actions["bite"].ID != "bite" {
		t.Fatal("merge mutated ruleset")
	}
}

func TestMergeRejectsRulesetStatTypeConflict(t *testing.T) {
	registries := baseCharacterRegistries()
	registries.Campaign.Stats["hp"] = StatDefinition{ID: "hp", Name: "HP", Type: StatTypeBoolean}
	if _, err := MergeDefinitionRegistries(registries); err == nil {
		t.Fatal("expected stat type conflict")
	}
}

func TestStatValueNumericBoundariesAndJSON(t *testing.T) {
	for _, value := range []int64{-MaxSafeInteger, MaxSafeInteger} {
		stat := IntegerStatValue(value)
		if err := ValidateStatValue(stat); err != nil {
			t.Fatalf("boundary %d rejected: %v", value, err)
		}
		encoded, err := json.Marshal(stat)
		if err != nil {
			t.Fatal(err)
		}
		var decoded StatValue
		if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.Type() != StatTypeInteger || decoded.Integer() != value {
			t.Fatalf("integer round trip failed: %s %#v %v", encoded, decoded, err)
		}
	}
	for _, value := range []StatValue{
		IntegerStatValue(MaxSafeInteger + 1),
		IntegerStatValue(-MaxSafeInteger - 1),
		NumberStatValue(math.Inf(1)),
		NumberStatValue(math.NaN()),
	} {
		if err := ValidateStatValue(value); err == nil {
			t.Fatalf("invalid boundary accepted: %#v", value)
		}
	}
	var mathematicalInteger StatValue
	if err := json.Unmarshal([]byte("12.0"), &mathematicalInteger); err != nil || mathematicalInteger.Type() != StatTypeInteger || mathematicalInteger.Integer() != 12 {
		t.Fatalf("12.0 must infer integer: %#v, %v", mathematicalInteger, err)
	}
	var fractional StatValue
	if err := json.Unmarshal([]byte("12.5"), &fractional); err != nil || fractional.Type() != StatTypeNumber || fractional.Number() != 12.5 {
		t.Fatalf("12.5 must infer number: %#v, %v", fractional, err)
	}
	var unsafeInteger StatValue
	if err := json.Unmarshal([]byte("9007199254740992.0"), &unsafeInteger); err == nil {
		t.Fatal("unsafe mathematical integer was accepted as number")
	}
}

func TestDefaultsAreOnlyAppliedWhenStatIsExplicitlyAdded(t *testing.T) {
	defaultHP := IntegerStatValue(10)
	definition := StatDefinition{ID: "hp", Name: "HP", Type: StatTypeInteger, Default: &defaultHP}
	preset := CharacterPresetDefinition{ID: "blank", Name: "Blank"}
	instance := NewCharacterInstance("one", &preset)
	effective := EffectiveCharacterState(instance, &preset)
	if _, exists := effective.Stats["hp"]; exists {
		t.Fatal("default materialized into effective stats")
	}
	value, err := ResolveAddedStatValue(definition, nil)
	if err != nil || value.Integer() != 10 {
		t.Fatalf("explicit add did not use default: %#v %v", value, err)
	}
}

func TestEffectiveStatsOverridesResetAndPresetChanges(t *testing.T) {
	preset := CharacterPresetDefinition{ID: "wolf", Name: "Wolf", Stats: map[string]StatValue{
		"hp": IntegerStatValue(11), "zero": IntegerStatValue(5), "ready": BooleanStatValue(true), "note": StringStatValue("preset"),
	}, ActionIDs: []string{"bite", "dash"}}
	instance := NewCharacterInstance("wolf-1", &preset)
	if instance.Materialized != nil || instance.Name != "Wolf" {
		t.Fatal("new preset character should be linked and keep preset name")
	}
	instance = MaterializeCharacter(instance, &preset)
	instance = WithStatOverride(instance, "hp", IntegerStatValue(4))
	instance = WithStatOverride(instance, "zero", IntegerStatValue(0))
	instance = WithStatOverride(instance, "ready", BooleanStatValue(false))
	instance = WithStatOverride(instance, "note", StringStatValue(""))
	preset.Stats["hp"] = IntegerStatValue(20)
	preset.Stats["armor"] = IntegerStatValue(12)
	preset.ActionIDs = []string{"dash"}
	effective := EffectiveCharacterState(instance, &preset)
	if effective.Stats["hp"].Integer() != 4 || len(effective.Stats) != 4 ||
		effective.Stats["zero"].Integer() != 0 || effective.Stats["ready"].Boolean() ||
		effective.Stats["note"].String() != "" || !reflect.DeepEqual(effective.ActionIDs, []string{"bite", "dash"}) {
		t.Fatalf("materialization failed to freeze all fields: %#v", effective)
	}
	reset := ResetStatOverride(instance, "hp")
	if _, ok := EffectiveCharacterState(reset, &preset).Stats["hp"]; ok {
		t.Fatal("reset accidentally restored inheritance")
	}
	if instance.Materialized.Stats["hp"].Integer() != 4 {
		t.Fatal("reset mutated its input")
	}
}
func TestEffectiveActionsStableDeduplicatedAndRemoved(t *testing.T) {
	preset := CharacterPresetDefinition{ID: "wolf", Name: "Wolf", ActionIDs: []string{"bite", "dash", "bite", "hide"}}
	instance := NewCharacterInstance("one", &preset)
	instance = MaterializeCharacter(instance, &preset)
	instance = WithActionAdded(instance, "roar")
	instance = WithActionRemoved(instance, "dash")
	instance = WithActionRemoved(instance, "roar")
	if got := EffectiveCharacterState(instance, &preset).ActionIDs; !reflect.DeepEqual(got, []string{"bite", "hide"}) {
		t.Fatalf("actions = %#v", got)
	}
	if !reflect.DeepEqual(preset.ActionIDs, []string{"bite", "dash", "bite", "hide"}) {
		t.Fatal("materialization mutated preset")
	}
	added := WithActionAdded(instance, "roar")
	if !reflect.DeepEqual(added.Materialized.ActionIDs, []string{"bite", "hide", "roar"}) {
		t.Fatalf("add action failed: %#v", added)
	}
	removed := WithActionRemoved(added, "hide")
	if !reflect.DeepEqual(removed.Materialized.ActionIDs, []string{"bite", "roar"}) {
		t.Fatalf("remove action failed: %#v", removed)
	}
}
func TestNewInstancesDoNotShareMutableState(t *testing.T) {
	preset := CharacterPresetDefinition{ID: "wolf", Name: "Wolf", Stats: map[string]StatValue{"hp": IntegerStatValue(11)}, ActionIDs: []string{"bite"}}
	one := NewCharacterInstance("one", &preset)
	two := NewCharacterInstance("two", &preset)
	one = MaterializeCharacter(one, &preset)
	one.Materialized.Stats["hp"] = IntegerStatValue(3)
	one.Materialized.ActionIDs = append(one.Materialized.ActionIDs, "dash")
	if two.Materialized != nil || preset.Stats["hp"].Integer() != 11 || len(preset.ActionIDs) != 1 {
		t.Fatal("instances or preset share mutable state")
	}
	standalone := NewCharacterInstance("independent", nil)
	if standalone.Materialized == nil || len(standalone.Materialized.Stats) != 0 || standalone.PresetID != "" {
		t.Fatalf("standalone must begin materialized and empty: %#v", standalone)
	}
}
func TestInferUnknownStatsIsAtomicAndRejectsTypeConflict(t *testing.T) {
	registries := baseCharacterRegistries()
	campaign, err := WithInferredStatDefinitions(registries, map[string]StatValue{
		"цвет":  StringStatValue("рыжий"),
		"alive": BooleanStatValue(false),
	})
	if err != nil || campaign.Stats["цвет"].Type != StatTypeString || campaign.Stats["alive"].Type != StatTypeBoolean {
		t.Fatalf("inference failed: %#v %v", campaign, err)
	}
	if len(registries.Campaign.Stats) != 0 {
		t.Fatal("inference mutated input campaign")
	}
	if _, err := WithInferredStatDefinitions(registries, map[string]StatValue{"hp": BooleanStatValue(true)}); err == nil {
		t.Fatal("expected existing stat type conflict")
	}
	registries.Campaign.Presets["fox"] = CharacterPresetDefinition{ID: "fox", Name: "Fox", Stats: map[string]StatValue{"цвет": StringStatValue("рыжий")}}
	if _, err := WithInferredStatDefinitions(registries, map[string]StatValue{"цвет": StringStatValue("рыжий")}); err != nil {
		t.Fatalf("atomic inference plus preset validation failed: %v", err)
	}
}

func TestValidationChecksReferencesAndReturnsIndependentLookup(t *testing.T) {
	registries := baseCharacterRegistries()
	registries.Campaign.Actions["strike"] = ActionDefinition{ID: "strike", Name: "Strike", Rolls: []RollSpec{{
		ID: "attack", Name: "Attack", Count: 1, Sides: 20, ModifierStat: "accuracy",
	}}}
	merged, err := MergeDefinitionRegistries(registries)
	if err != nil {
		t.Fatal(err)
	}
	action, ok := merged.LookupAction("strike")
	if !ok {
		t.Fatal("action lookup failed")
	}
	action.Rolls[0].Sides = 2
	if merged.Actions["strike"].Rolls[0].Sides != 20 {
		t.Fatal("lookup leaked mutable registry slice")
	}
	registries.Campaign.Actions["bad"] = ActionDefinition{ID: "bad", Name: "Bad", Rolls: []RollSpec{{ID: "x", Name: "X", Count: 1, Sides: 20, ModifierStat: "missing"}}}
	if _, err := MergeDefinitionRegistries(registries); err == nil {
		t.Fatal("expected unresolved reference error")
	}
}
