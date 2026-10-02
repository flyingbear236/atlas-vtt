package main

import "errors"

type characterPermissions struct {
	Edit   bool `json:"edit"`
	Manage bool `json:"manage"`
}

type characterSnapshot struct {
	Type            string                      `json:"type"`
	CharacterWatch  uint64                      `json:"characterWatch"`
	CharacterID     string                      `json:"characterId"`
	Revision        uint64                      `json:"revision"`
	Instance        CharacterInstance           `json:"instance"`
	Preset          *CharacterPresetDefinition  `json:"preset,omitempty"`
	Effective       EffectiveCharacter          `json:"effective"`
	StatDefinitions map[string]StatDefinition   `json:"statDefinitions"`
	Actions         map[string]ActionDefinition `json:"actions"`
	Permissions     characterPermissions        `json:"permissions"`
}

func characterSnapshotForMember(session *Session, member *Member, characterID string, watch uint64) (characterSnapshot, error) {
	if !memberCanAccessCharacter(session, member, characterID) {
		return characterSnapshot{}, errors.New("character access denied")
	}
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		return characterSnapshot{}, err
	}
	instance := cloneCharacterInstance(session.CharacterInstances[characterID])
	var preset *CharacterPresetDefinition
	if instance.PresetID != "" {
		definition := clonePresetDefinition(registry.Presets[instance.PresetID])
		preset = &definition
	}
	effective := EffectiveCharacterState(instance, preset)
	stats := make(map[string]StatDefinition, len(effective.Stats))
	for statID := range effective.Stats {
		if definition, ok := registry.Stats[statID]; ok {
			stats[statID] = cloneStatDefinition(definition)
		}
	}
	actions := make(map[string]ActionDefinition, len(effective.ActionIDs))
	for _, actionID := range effective.ActionIDs {
		if definition, ok := registry.Actions[actionID]; ok {
			actions[actionID] = cloneActionDefinition(definition)
			for _, roll := range definition.Rolls {
				if stat, exists := registry.Stats[roll.ModifierStat]; roll.ModifierStat != "" && exists {
					stats[roll.ModifierStat] = cloneStatDefinition(stat)
				}
			}
		}
	}
	return characterSnapshot{
		Type: "characterSnapshot", CharacterWatch: watch, CharacterID: characterID,
		Revision: session.CharacterRevision, Instance: instance, Preset: preset, Effective: effective,
		StatDefinitions: stats, Actions: actions,
		Permissions: characterPermissions{Edit: true, Manage: memberIsGM(member)},
	}, nil
}

func (s *Server) closeCharacterWatch(peer *peer, revision uint64, reason string) {
	if peer.characterID == "" {
		return
	}
	characterID, watch := peer.characterID, peer.characterWatch
	peer.characterID = ""
	s.send(peer, map[string]any{
		"type": "characterWatchClosed", "characterWatch": watch, "characterId": characterID,
		"revision": revision, "reason": reason,
	})
}

func (s *Server) watchCharacter(session *Session, peer *peer, characterID string, watch uint64) {
	if watch == 0 || watch < peer.characterWatch {
		return
	}
	peer.characterWatch = watch
	peer.characterID = characterID
	snapshot, err := characterSnapshotForMember(session, peer.member, characterID, watch)
	if err != nil {
		s.closeCharacterWatch(peer, session.CharacterRevision, "accessRevoked")
		return
	}
	s.send(peer, snapshot)
}

func (s *Server) unwatchCharacter(peer *peer, characterID string, watch uint64) {
	if watch == 0 || watch < peer.characterWatch {
		return
	}
	peer.characterWatch = watch
	if characterID == "" || peer.characterID == characterID {
		peer.characterID = ""
	}
}

// refreshCharacterWatches runs only after a successful durable mutation. Each
// peer owns one watch slot, so access revocation and fan-out are bounded by the
// number of connected peers and never depend on viewport delivery.
func (s *Server) refreshCharacterWatches(session *Session, changed map[string]bool, all bool) {
	for peer := range s.peers {
		if peer.session != session.ID || peer.characterID == "" {
			continue
		}
		characterID := peer.characterID
		if !memberCanAccessCharacter(session, peer.member, characterID) {
			s.closeCharacterWatch(peer, session.CharacterRevision, "accessRevoked")
			continue
		}
		if !all {
			if _, ok := changed[characterID]; !ok {
				continue
			}
		}
		snapshot, err := characterSnapshotForMember(session, peer.member, characterID, peer.characterWatch)
		if err != nil {
			s.closeCharacterWatch(peer, session.CharacterRevision, "unavailable")
			continue
		}
		s.send(peer, snapshot)
	}
}
