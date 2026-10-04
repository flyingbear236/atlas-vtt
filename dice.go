package main

import (
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"time"
)

// RollRequest contains both request fields and server-owned fields which an
// untrusted transport might try to supply. Execute rejects every server-owned
// field instead of silently ignoring it.
type RollRequest struct {
	Count               *int   `json:"count,omitempty"`
	Sides               *int   `json:"sides,omitempty"`
	CharacterInstanceID string `json:"characterInstanceId,omitempty"`
	ActionID            string `json:"actionId,omitempty"`
	RollSpecID          string `json:"rollSpecId,omitempty"`

	ID            string     `json:"id,omitempty"`
	UserID        string     `json:"userId,omitempty"`
	AuthorName    string     `json:"authorName,omitempty"`
	CharacterName string     `json:"characterName,omitempty"`
	ActionName    string     `json:"actionName,omitempty"`
	RollSpecName  string     `json:"rollSpecName,omitempty"`
	Results       []int      `json:"results,omitempty"`
	Modifier      *float64   `json:"modifier,omitempty"`
	Total         *float64   `json:"total,omitempty"`
	Timestamp     *time.Time `json:"timestamp,omitempty"`
}

type RollEvent struct {
	ID                  string    `json:"id"`
	SceneID             string    `json:"sceneId"`
	UserID              string    `json:"userId"`
	AuthorName          string    `json:"authorName"`
	CharacterInstanceID string    `json:"characterInstanceId,omitempty"`
	CharacterName       string    `json:"characterName,omitempty"`
	ActionID            string    `json:"actionId,omitempty"`
	ActionName          string    `json:"actionName,omitempty"`
	RollSpecID          string    `json:"rollSpecId,omitempty"`
	RollSpecName        string    `json:"rollSpecName,omitempty"`
	Count               int       `json:"count"`
	Sides               int       `json:"sides"`
	Results             []int     `json:"results"`
	Modifier            float64   `json:"modifier"`
	Total               float64   `json:"total"`
	Timestamp           time.Time `json:"timestamp"`
}

// DiceRNG returns a uniformly selected value in [0, upperBound). Keeping the
// small interface independent from RollService makes deterministic and error
// paths cheap to test.
type DiceRNG interface {
	Intn(upperBound int) (int, error)
}

type CryptoDiceRNG struct{}

func (CryptoDiceRNG) Intn(upperBound int) (int, error) {
	if upperBound <= 0 {
		return 0, errors.New("dice RNG upper bound must be positive")
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(upperBound)))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}

type RollService struct {
	rng        DiceRNG
	now        func() time.Time
	newEventID func() string
}

func NewRollService(rng DiceRNG) *RollService {
	if rng == nil {
		rng = CryptoDiceRNG{}
	}
	return &RollService{rng: rng, now: time.Now, newEventID: id}
}

type resolvedRoll struct {
	characterID   string
	characterName string
	actionID      string
	actionName    string
	rollSpecID    string
	rollSpecName  string
	count         int
	sides         int
	modifier      float64
}

func (service *RollService) Execute(session *Session, sceneID string, member *Member, request RollRequest) (RollEvent, error) {
	if service == nil || service.rng == nil || service.now == nil || service.newEventID == nil {
		return RollEvent{}, errors.New("roll service is not initialized")
	}
	if err := rejectServerOwnedRollFields(request); err != nil {
		return RollEvent{}, err
	}
	if session == nil || member == nil {
		return RollEvent{}, errors.New("roll requires a session member")
	}
	author := session.Members[member.ID]
	if author == nil {
		return RollEvent{}, errors.New("roll author is not a session member")
	}
	scene := session.Scenes[sceneID]
	if scene == nil {
		return RollEvent{}, fmt.Errorf("unknown scene %q", sceneID)
	}
	if !memberIsGM(author) && !scene.Published {
		return RollEvent{}, errors.New("scene is not available")
	}

	resolved, err := resolveRoll(session, scene, author, request)
	if err != nil {
		return RollEvent{}, err
	}
	if err := validateDiceParameters(resolved.count, resolved.sides); err != nil {
		return RollEvent{}, err
	}
	if err := validateRollNumber("modifier", resolved.modifier); err != nil {
		return RollEvent{}, err
	}
	if err := validateRollNumber("minimum total", resolved.modifier+float64(resolved.count)); err != nil {
		return RollEvent{}, err
	}
	if err := validateRollNumber("maximum total", resolved.modifier+float64(resolved.count*resolved.sides)); err != nil {
		return RollEvent{}, err
	}

	results := make([]int, resolved.count)
	diceTotal := 0
	for index := range results {
		value, rollErr := service.rng.Intn(resolved.sides)
		if rollErr != nil {
			return RollEvent{}, fmt.Errorf("dice RNG: %w", rollErr)
		}
		if value < 0 || value >= resolved.sides {
			return RollEvent{}, fmt.Errorf("dice RNG returned %d outside [0,%d)", value, resolved.sides)
		}
		results[index] = value + 1
		diceTotal += value + 1
	}
	total := float64(diceTotal) + resolved.modifier
	if err := validateRollNumber("total", total); err != nil {
		return RollEvent{}, err
	}

	return RollEvent{
		ID: service.newEventID(), SceneID: sceneID, UserID: author.ID, AuthorName: author.Name,
		CharacterInstanceID: resolved.characterID, CharacterName: resolved.characterName,
		ActionID: resolved.actionID, ActionName: resolved.actionName,
		RollSpecID: resolved.rollSpecID, RollSpecName: resolved.rollSpecName,
		Count: resolved.count, Sides: resolved.sides, Results: results,
		Modifier: resolved.modifier, Total: total, Timestamp: service.now().UTC(),
	}, nil
}

func rejectServerOwnedRollFields(request RollRequest) error {
	if request.ID != "" || request.UserID != "" || request.AuthorName != "" || request.CharacterName != "" ||
		request.ActionName != "" || request.RollSpecName != "" || request.Results != nil || request.Modifier != nil ||
		request.Total != nil || request.Timestamp != nil {
		return errors.New("roll request contains server-owned fields")
	}
	return nil
}

func resolveRoll(session *Session, scene *Scene, member *Member, request RollRequest) (resolvedRoll, error) {
	actionMode := request.ActionID != "" || request.RollSpecID != ""
	if !actionMode {
		if request.Count == nil || request.Sides == nil {
			return resolvedRoll{}, errors.New("manual roll requires count and sides")
		}
		resolved := resolvedRoll{count: *request.Count, sides: *request.Sides}
		if request.CharacterInstanceID != "" {
			registry, err := rollRegistry(session)
			if err != nil {
				return resolvedRoll{}, err
			}
			character, err := resolveRollCharacter(session, scene, member, registry, request.CharacterInstanceID)
			if err != nil {
				return resolvedRoll{}, err
			}
			resolved.characterID = character.ID
			resolved.characterName = character.Name
		}
		return resolved, nil
	}

	if request.CharacterInstanceID == "" || request.ActionID == "" || request.RollSpecID == "" {
		return resolvedRoll{}, errors.New("action roll requires character, action, and roll spec ids")
	}
	if request.Count != nil || request.Sides != nil {
		return resolvedRoll{}, errors.New("action roll count and sides are server-owned")
	}
	registry, err := rollRegistry(session)
	if err != nil {
		return resolvedRoll{}, err
	}
	character, err := resolveRollCharacter(session, scene, member, registry, request.CharacterInstanceID)
	if err != nil {
		return resolvedRoll{}, err
	}
	if !slices.Contains(character.ActionIDs, request.ActionID) {
		return resolvedRoll{}, fmt.Errorf("character %q does not have action %q", character.ID, request.ActionID)
	}
	action, exists := registry.Actions[request.ActionID]
	if !exists {
		return resolvedRoll{}, fmt.Errorf("unknown action %q", request.ActionID)
	}
	var spec *RollSpec
	for index := range action.Rolls {
		if action.Rolls[index].ID == request.RollSpecID {
			spec = &action.Rolls[index]
			break
		}
	}
	if spec == nil {
		return resolvedRoll{}, fmt.Errorf("action %q has no roll spec %q", action.ID, request.RollSpecID)
	}
	modifier, err := resolveRollModifier(character, *spec)
	if err != nil {
		return resolvedRoll{}, err
	}
	return resolvedRoll{
		characterID: character.ID, characterName: character.Name,
		actionID: action.ID, actionName: action.Name, rollSpecID: spec.ID, rollSpecName: spec.Name,
		count: spec.Count, sides: spec.Sides, modifier: modifier,
	}, nil
}

func rollRegistry(session *Session) (EffectiveRegistry, error) {
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		return EffectiveRegistry{}, fmt.Errorf("definition registry: %w", err)
	}
	return registry, nil
}

func resolveRollCharacter(session *Session, scene *Scene, member *Member, registry EffectiveRegistry, characterID string) (EffectiveCharacter, error) {
	instance, exists := session.CharacterInstances[characterID]
	if !exists {
		return EffectiveCharacter{}, fmt.Errorf("unknown character %q", characterID)
	}
	if !characterHasControllableToken(session, scene, member, characterID) {
		return EffectiveCharacter{}, fmt.Errorf("character %q has no accessible linked token in scene %q", characterID, scene.ID)
	}
	var preset *CharacterPresetDefinition
	if instance.PresetID != "" {
		value, ok := registry.Presets[instance.PresetID]
		if !ok {
			return EffectiveCharacter{}, fmt.Errorf("character %q references unknown preset %q", characterID, instance.PresetID)
		}
		preset = &value
	}
	return EffectiveCharacterState(instance, preset), nil
}

func characterHasControllableToken(session *Session, scene *Scene, member *Member, characterID string) bool {
	for reference := range session.characterReferences[characterID] {
		if reference.SceneID != scene.ID {
			continue
		}
		token, exists := scene.Tokens[reference.TokenID]
		if exists && token.CharacterInstanceID == characterID && memberCanControlToken(member, token) {
			return true
		}
	}
	return false
}

func resolveRollModifier(character EffectiveCharacter, spec RollSpec) (float64, error) {
	modifier := spec.ModifierFixed
	if err := validateRollNumber("fixed modifier", modifier); err != nil {
		return 0, err
	}
	if spec.ModifierStat == "" {
		return modifier, nil
	}
	value, exists := character.Stats[spec.ModifierStat]
	if !exists {
		return 0, fmt.Errorf("character %q has no value for modifier stat %q", character.ID, spec.ModifierStat)
	}
	var statModifier float64
	switch value.Type() {
	case StatTypeNumber:
		statModifier = value.Number()
	case StatTypeInteger:
		statModifier = float64(value.Integer())
	default:
		return 0, fmt.Errorf("modifier stat %q is not numeric", spec.ModifierStat)
	}
	if err := validateRollNumber("stat modifier", statModifier); err != nil {
		return 0, err
	}
	modifier += statModifier
	if err := validateRollNumber("combined modifier", modifier); err != nil {
		return 0, err
	}
	return modifier, nil
}

func validateDiceParameters(count, sides int) error {
	if count < 1 || count > MaxDicePerRoll {
		return fmt.Errorf("dice count must be between 1 and %d", MaxDicePerRoll)
	}
	if !validDiceSides(sides) {
		return fmt.Errorf("unsupported die with %d sides", sides)
	}
	return nil
}

func validateRollNumber(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > float64(MaxSafeInteger) {
		return fmt.Errorf("%s is outside the finite precisely representable range", name)
	}
	return nil
}
