package main

import (
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"
)

// RollRequest contains both request fields and server-owned fields which an
// untrusted transport might try to supply. Execute rejects every server-owned
// field instead of silently ignoring it.
type RollRequest struct {
	Count               *int        `json:"count,omitempty"`
	Sides               *int        `json:"sides,omitempty"`
	Groups              []DiceGroup `json:"groups,omitempty"`
	ManualModifier      *float64    `json:"manualModifier,omitempty"`
	CharacterInstanceID string      `json:"characterInstanceId,omitempty"`
	ActionID            string      `json:"actionId,omitempty"`
	RollSpecID          string      `json:"rollSpecId,omitempty"`

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

type DiceGroup struct {
	Count   int   `json:"count"`
	Sides   int   `json:"sides"`
	Results []int `json:"results,omitempty"`
}

type RollEvent struct {
	ID                  string      `json:"id"`
	SceneID             string      `json:"sceneId"`
	UserID              string      `json:"userId"`
	AuthorName          string      `json:"authorName"`
	CharacterInstanceID string      `json:"characterInstanceId,omitempty"`
	CharacterName       string      `json:"characterName,omitempty"`
	Count               int         `json:"count"`
	Sides               int         `json:"sides"`
	Results             []int       `json:"results"`
	Groups              []DiceGroup `json:"groups,omitempty"`
	Modifier            float64     `json:"modifier"`
	Total               float64     `json:"total"`
	Timestamp           time.Time   `json:"timestamp"`
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
	count         int
	sides         int
	modifier      float64
	groups        []DiceGroup
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
	groups := resolved.groups
	if len(groups) == 0 {
		groups = []DiceGroup{{Count: resolved.count, Sides: resolved.sides}}
	}
	if err := validateDiceGroups(groups); err != nil {
		return RollEvent{}, err
	}
	if err := validateRollNumber("modifier", resolved.modifier); err != nil {
		return RollEvent{}, err
	}
	minimum, maximum := resolved.modifier, resolved.modifier
	for _, group := range groups {
		minimum += float64(group.Count)
		maximum += float64(group.Count * group.Sides)
	}
	if err := validateRollNumber("minimum total", minimum); err != nil {
		return RollEvent{}, err
	}
	if err := validateRollNumber("maximum total", maximum); err != nil {
		return RollEvent{}, err
	}

	results := make([]int, 0)
	diceTotal := 0
	rolledGroups := make([]DiceGroup, len(groups))
	for groupIndex, group := range groups {
		rolledGroups[groupIndex] = DiceGroup{Count: group.Count, Sides: group.Sides, Results: make([]int, group.Count)}
		for index := range rolledGroups[groupIndex].Results {
			value, rollErr := service.rng.Intn(group.Sides)
			if rollErr != nil {
				return RollEvent{}, fmt.Errorf("dice RNG: %w", rollErr)
			}
			if value < 0 || value >= group.Sides {
				return RollEvent{}, fmt.Errorf("dice RNG returned %d outside [0,%d)", value, group.Sides)
			}
			rolledGroups[groupIndex].Results[index] = value + 1
			results = append(results, value+1)
			diceTotal += value + 1
		}
	}
	total := float64(diceTotal) + resolved.modifier
	if err := validateRollNumber("total", total); err != nil {
		return RollEvent{}, err
	}

	event := RollEvent{
		ID: service.newEventID(), SceneID: sceneID, UserID: author.ID, AuthorName: author.Name,
		CharacterInstanceID: resolved.characterID, CharacterName: resolved.characterName,
		Count: resolved.count, Sides: resolved.sides, Results: results,
		Modifier: resolved.modifier, Total: total, Timestamp: service.now().UTC(),
	}
	if len(rolledGroups) == 1 {
		event.Count, event.Sides = rolledGroups[0].Count, rolledGroups[0].Sides
	} else {
		event.Count, event.Sides, event.Groups = 0, 0, rolledGroups
	}
	return event, nil
}

func rejectServerOwnedRollFields(request RollRequest) error {
	if request.ID != "" || request.UserID != "" || request.AuthorName != "" || request.CharacterName != "" ||
		request.ActionName != "" || request.RollSpecName != "" || request.Results != nil || request.Modifier != nil ||
		request.Total != nil || request.Timestamp != nil {
		return errors.New("roll request contains server-owned fields")
	}
	for _, group := range request.Groups {
		if group.Results != nil {
			return errors.New("roll request contains server-owned group results")
		}
	}
	return nil
}

func resolveRoll(session *Session, scene *Scene, member *Member, request RollRequest) (resolvedRoll, error) {
	if request.ActionID != "" || request.RollSpecID != "" {
		return resolvedRoll{}, errors.New(v04RollCommandIssue)
	}
	if len(request.Groups) > 0 {
		if request.Count != nil || request.Sides != nil {
			return resolvedRoll{}, errors.New("manual roll must use either groups or count and sides")
		}
		if err := validateDiceGroups(request.Groups); err != nil {
			return resolvedRoll{}, err
		}
	} else if request.Count == nil || request.Sides == nil {
		return resolvedRoll{}, errors.New("manual roll requires count and sides")
	}

	resolved := resolvedRoll{groups: request.Groups}
	if request.ManualModifier != nil {
		if err := validateRollNumber("manual modifier", *request.ManualModifier); err != nil {
			return resolvedRoll{}, err
		}
		resolved.modifier = *request.ManualModifier
	}
	if len(request.Groups) == 0 {
		resolved.count, resolved.sides = *request.Count, *request.Sides
	}
	if request.CharacterInstanceID != "" {
		instance, exists := session.CharacterInstances[request.CharacterInstanceID]
		if !exists || !characterHasControllableToken(session, scene, member, request.CharacterInstanceID) {
			return resolvedRoll{}, errors.New("character has no accessible linked token in scene")
		}
		resolved.characterID = instance.ID
		resolved.characterName = instance.Name
	}
	return resolved, nil
}

func rollRegistry(session *Session) (EffectiveRegistry, error) {
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		return EffectiveRegistry{}, fmt.Errorf("definition registry: %w", err)
	}
	return registry, nil
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

func validateDiceParameters(count, sides int) error {
	if count < 1 || count > MaxDicePerRoll {
		return fmt.Errorf("dice count must be between 1 and %d", MaxDicePerRoll)
	}
	if !validDiceSides(sides) {
		return fmt.Errorf("unsupported die with %d sides", sides)
	}
	return nil
}

func validateDiceGroups(groups []DiceGroup) error {
	if len(groups) == 0 || len(groups) > 16 {
		return errors.New("dice roll must contain between 1 and 16 groups")
	}
	total := 0
	for _, group := range groups {
		if err := validateDiceParameters(group.Count, group.Sides); err != nil {
			return err
		}
		total += group.Count
		if total > MaxDicePerRoll {
			return fmt.Errorf("total dice count must not exceed %d", MaxDicePerRoll)
		}
	}
	return nil
}

func validateRollNumber(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > float64(MaxSafeInteger) {
		return fmt.Errorf("%s is outside the finite precisely representable range", name)
	}
	return nil
}
