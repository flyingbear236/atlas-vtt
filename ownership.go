package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
)

type tokenJSON struct {
	ID                  string          `json:"id"`
	Name                string          `json:"name"`
	FloorID             string          `json:"floorId"`
	LayerID             string          `json:"layerId"`
	X                   float64         `json:"x"`
	Y                   float64         `json:"y"`
	Size                float64         `json:"size"`
	Rotation            float64         `json:"rotation"`
	Color               string          `json:"color"`
	Opacity             *float64        `json:"opacity,omitempty"`
	OwnerIDs            json.RawMessage `json:"ownerIds"`
	LegacyOwner         json.RawMessage `json:"owner,omitempty"`
	Hidden              bool            `json:"hidden"`
	Asset               string          `json:"asset"`
	CharacterInstanceID string          `json:"characterInstanceId,omitempty"`
}

func (token Token) MarshalJSON() ([]byte, error) {
	owners := token.OwnerIDs
	if owners == nil {
		owners = []string{}
	}
	ownerJSON, err := json.Marshal(owners)
	if err != nil {
		return nil, err
	}
	return json.Marshal(tokenJSON{
		ID: token.ID, Name: token.Name, FloorID: token.FloorID, LayerID: token.LayerID,
		X: token.X, Y: token.Y, Size: token.Size, Rotation: token.Rotation, Color: token.Color, Opacity: &token.Opacity,
		OwnerIDs: ownerJSON, Hidden: token.Hidden, Asset: token.Asset, CharacterInstanceID: token.CharacterInstanceID,
	})
}

func (token *Token) UnmarshalJSON(data []byte) error {
	if token == nil {
		return errors.New("token: nil receiver")
	}
	var wire tokenJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	owners := []string{}
	if wire.OwnerIDs != nil {
		if bytes.Equal(bytes.TrimSpace(wire.OwnerIDs), []byte("null")) {
			return errors.New("token ownerIds must be an array")
		}
		if err := json.Unmarshal(wire.OwnerIDs, &owners); err != nil {
			return errors.New("token ownerIds must be an array of strings")
		}
	} else if wire.LegacyOwner != nil {
		var owner string
		if err := json.Unmarshal(wire.LegacyOwner, &owner); err != nil {
			return errors.New("legacy token owner must be a string")
		}
		if owner != "" {
			owners = []string{owner}
		}
	}
	opacity := 1.0
	if wire.Opacity != nil {
		opacity = *wire.Opacity
	}
	*token = Token{
		ID: wire.ID, Name: wire.Name, FloorID: wire.FloorID, LayerID: wire.LayerID,
		X: wire.X, Y: wire.Y, Size: wire.Size, Rotation: wire.Rotation, Color: wire.Color, Opacity: opacity,
		OwnerIDs: owners, Hidden: wire.Hidden, Asset: wire.Asset, CharacterInstanceID: wire.CharacterInstanceID,
		legacyOwnerField: wire.LegacyOwner != nil, legacyOpacity: wire.Opacity == nil,
	}
	return nil
}

func normalizeOwnerIDs(ownerIDs []string) ([]string, bool) {
	normalized := make([]string, 0, len(ownerIDs))
	seen := make(map[string]struct{}, len(ownerIDs))
	changed := ownerIDs == nil
	for _, ownerID := range ownerIDs {
		if _, duplicate := seen[ownerID]; duplicate {
			changed = true
			continue
		}
		seen[ownerID] = struct{}{}
		normalized = append(normalized, ownerID)
	}
	return normalized, changed
}

func migrateTokens(scene *Scene) bool {
	changed := false
	for tokenID, token := range scene.Tokens {
		owners, normalized := normalizeOwnerIDs(token.OwnerIDs)
		if normalized || token.legacyOwnerField || token.legacyOpacity {
			changed = true
		}
		token.OwnerIDs = owners
		token.legacyOwnerField = false
		token.legacyOpacity = false
		scene.Tokens[tokenID] = token
	}
	return changed
}

func validateTokenOwners(token Token, members map[string]*Member) bool {
	seen := make(map[string]struct{}, len(token.OwnerIDs))
	for _, ownerID := range token.OwnerIDs {
		if ownerID == "" || members[ownerID] == nil {
			return false
		}
		if _, duplicate := seen[ownerID]; duplicate {
			return false
		}
		seen[ownerID] = struct{}{}
	}
	return true
}

func tokenOwnedBy(token Token, memberID string) bool {
	return memberID != "" && slices.Contains(token.OwnerIDs, memberID)
}

func memberCanControlToken(member *Member, token Token) bool {
	return memberIsGM(member) || (member != nil && !token.Hidden && tokenOwnedBy(token, member.ID))
}

func memberCanAccessCharacter(session *Session, member *Member, characterID string) bool {
	if session == nil || member == nil || session.CharacterInstances[characterID].ID == "" {
		return false
	}
	if memberIsGM(member) {
		return true
	}
	for reference := range session.characterReferences[characterID] {
		scene := session.Scenes[reference.SceneID]
		if scene == nil {
			continue
		}
		token, exists := scene.Tokens[reference.TokenID]
		if exists && scene.Published && !token.Hidden && tokenOwnedBy(token, member.ID) {
			return true
		}
	}
	return false
}

func cloneToken(token Token) Token {
	token.OwnerIDs = append([]string(nil), token.OwnerIDs...)
	return token
}

func tokensEqual(left, right Token) bool {
	return left.ID == right.ID && left.Name == right.Name && left.FloorID == right.FloorID && left.LayerID == right.LayerID &&
		left.X == right.X && left.Y == right.Y && left.Size == right.Size && left.Rotation == right.Rotation &&
		left.Color == right.Color && left.Opacity == right.Opacity && slices.Equal(left.OwnerIDs, right.OwnerIDs) && left.Hidden == right.Hidden &&
		left.Asset == right.Asset && left.CharacterInstanceID == right.CharacterInstanceID
}
