package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

const (
	resourceOverlayTop    = "top"
	resourceOverlayBottom = "bottom"
	resourceOverlayText   = "text"
	resourceOverlayBar    = "bar"
	maxResourceOverlays   = 4
)

var resourceOverlayColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type ResourceOverlayConfig struct {
	StatID   string `json:"statId"`
	Enabled  bool   `json:"enabled"`
	Position string `json:"position"`
	Slot     int    `json:"slot"`
	ShowName bool   `json:"showName"`
	Color    string `json:"color"`
	Mode     string `json:"mode"`
}

type CharacterDisplay struct {
	ID        string                       `json:"id"`
	Resources map[string]EffectiveResource `json:"resources"`
}

func cloneResourceOverlayConfig(config []ResourceOverlayConfig) []ResourceOverlayConfig {
	return append([]ResourceOverlayConfig{}, config...)
}

func validateResourceOverlayConfig(config []ResourceOverlayConfig, registry EffectiveRegistry) error {
	if len(config) > maxResourceOverlays {
		return fmt.Errorf("resource overlays exceed limit %d", maxResourceOverlays)
	}
	slots := map[string]string{}
	stats := map[string]bool{}
	enabled := 0
	for _, item := range config {
		if !item.Enabled {
			continue
		}
		enabled++
		definition, exists := registry.Stats[item.StatID]
		if !exists || !definition.IsResource() {
			return fmt.Errorf("stat %q is not a resource", item.StatID)
		}
		if stats[item.StatID] {
			return fmt.Errorf("resource %q is configured more than once", item.StatID)
		}
		stats[item.StatID] = true
		if item.Position != resourceOverlayTop && item.Position != resourceOverlayBottom {
			return fmt.Errorf("resource %q has invalid position", item.StatID)
		}
		if item.Slot != 1 && item.Slot != 2 {
			return fmt.Errorf("resource %q has invalid slot", item.StatID)
		}
		key := fmt.Sprintf("%s/%d", item.Position, item.Slot)
		if prior := slots[key]; prior != "" {
			return fmt.Errorf("resources %q and %q use the same slot", prior, item.StatID)
		}
		slots[key] = item.StatID
		if !resourceOverlayColorPattern.MatchString(item.Color) {
			return fmt.Errorf("resource %q has invalid color", item.StatID)
		}
		switch definition.Type {
		case StatTypeNumber, StatTypeInteger:
			if item.Mode != resourceOverlayText && item.Mode != resourceOverlayBar {
				return fmt.Errorf("resource %q has invalid display mode", item.StatID)
			}
		case StatTypeBoolean, StatTypeString:
			if item.Mode != resourceOverlayText {
				return fmt.Errorf("resource %q supports text mode only", item.StatID)
			}
		default:
			return fmt.Errorf("resource %q has unsupported type", item.StatID)
		}
	}
	if enabled > maxResourceOverlays {
		return fmt.Errorf("resource overlays exceed limit %d", maxResourceOverlays)
	}
	return nil
}

func resourceOverlayDefinitions(registry EffectiveRegistry, config []ResourceOverlayConfig, all bool) map[string]StatDefinition {
	configured := map[string]bool{}
	for _, item := range config {
		if item.Enabled {
			configured[item.StatID] = true
		}
	}
	definitions := map[string]StatDefinition{}
	for statID, definition := range registry.Stats {
		if definition.IsResource() && (all || configured[statID]) {
			definitions[statID] = cloneStatDefinition(definition)
		}
	}
	return definitions
}

func characterDisplayForConfig(session *Session, registry EffectiveRegistry, characterID string) (CharacterDisplay, bool) {
	instance, exists := session.CharacterInstances[characterID]
	if !exists {
		return CharacterDisplay{}, false
	}
	display := CharacterDisplay{ID: characterID, Resources: map[string]EffectiveResource{}}
	var preset CharacterPresetDefinition
	if instance.Materialized == nil && instance.PresetID != "" {
		preset = registry.Presets[instance.PresetID]
	}
	for _, item := range session.ResourceOverlays {
		if !item.Enabled {
			continue
		}
		var initial StatValue
		var present bool
		if instance.Materialized != nil {
			initial, present = instance.Materialized.ResourceInitial[item.StatID]
		} else {
			initial, present = preset.Stats[item.StatID]
		}
		if !present {
			continue
		}
		current := initial
		if value, overridden := instance.ResourceCurrent[item.StatID]; overridden {
			current = value
		}
		display.Resources[item.StatID] = EffectiveResource{Current: current, Initial: initial}
	}
	return display, true
}

func characterDisplaysForTokens(session *Session, registry EffectiveRegistry, tokens map[string]Token) map[string]CharacterDisplay {
	displays := map[string]CharacterDisplay{}
	if len(session.ResourceOverlays) == 0 {
		return displays
	}
	for _, token := range tokens {
		characterID := token.CharacterInstanceID
		if characterID == "" {
			continue
		}
		if _, exists := displays[characterID]; exists {
			continue
		}
		if display, exists := characterDisplayForConfig(session, registry, characterID); exists {
			displays[characterID] = display
		}
	}
	return displays
}

func resourceOverlayPayload(session *Session, member *Member, registry EffectiveRegistry) map[string]any {
	return map[string]any{
		"resourceOverlays":           cloneResourceOverlayConfig(session.ResourceOverlays),
		"resourceOverlayDefinitions": resourceOverlayDefinitions(registry, session.ResourceOverlays, memberIsGM(member)),
	}
}

func (s *Server) resourceOverlayCommand(session *Session, peer *peer, command Command) {
	if command.Seq == 0 || command.Client == "" || len(command.Client) > 80 {
		s.send(peer, map[string]string{"type": "fatal", "message": "Некорректный поток команд"})
		return
	}
	if session.Receipts == nil {
		session.Receipts = map[string]Receipt{}
	}
	stream := peer.member.ID + ":" + command.Client
	previous := session.Receipts[stream]
	encoded, _ := json.Marshal(command)
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	if command.Seq == previous.Seq {
		if previous.Digest != digest {
			s.send(peer, map[string]string{"type": "fatal", "message": "Номер команды уже занят другой командой. Откройте новую вкладку"})
			return
		}
		s.send(peer, map[string]any{"type": "ack", "client": command.Client, "seq": command.Seq, "error": previous.Error, "revision": session.CampaignRevision})
		return
	}
	if command.Seq < previous.Seq || command.Seq != previous.Seq+1 {
		s.send(peer, map[string]string{"type": "fatal", "message": "Нарушена последовательность команд. Откройте новую вкладку"})
		return
	}
	issue := ""
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if !memberIsGM(peer.member) {
		issue = "Действие доступно ведущему"
	} else if err != nil {
		issue = err.Error()
	} else if err := validateResourceOverlayConfig(command.ResourceOverlays, registry); err != nil {
		issue = err.Error()
	}
	oldConfig := cloneResourceOverlayConfig(session.ResourceOverlays)
	oldRevision, oldDirty := session.CampaignRevision, s.dirty
	if issue == "" {
		session.ResourceOverlays = cloneResourceOverlayConfig(command.ResourceOverlays)
		session.CampaignRevision++
	}
	session.Receipts[stream] = Receipt{Seq: command.Seq, Error: issue, Digest: digest, Updated: time.Now().Unix()}
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		session.ResourceOverlays = oldConfig
		session.CampaignRevision, s.dirty = oldRevision, oldDirty
		if previous.Seq == 0 {
			delete(session.Receipts, stream)
		} else {
			session.Receipts[stream] = previous
		}
		s.send(peer, map[string]any{"type": "saveError", "client": command.Client, "seq": command.Seq, "message": "Не удалось сохранить настройки ресурсов"})
		return
	}
	if pruneReceipts(session, stream) {
		s.dirty = true
	}
	if issue == "" {
		s.publishCampaign(session)
		s.publishResourceOverlayConfig(session)
	}
	s.send(peer, map[string]any{"type": "ack", "client": command.Client, "seq": command.Seq, "error": issue, "revision": session.CampaignRevision})
}

func (s *Server) publishResourceOverlayConfig(session *Session) {
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		return
	}
	for peer := range s.peers {
		if peer.session != session.ID {
			continue
		}
		message := resourceOverlayPayload(session, peer.member, registry)
		message["type"] = "resourceOverlayConfigChanged"
		if scene := session.Scenes[peer.sceneID]; scene != nil && peer.region != nil {
			tokens := map[string]Token{}
			for _, token := range scene.ensureRuntime().query(*peer.region) {
				if tokenLoadedForPeer(peer, scene, token) {
					tokens[token.ID] = token
				}
			}
			message["characterDisplays"] = characterDisplaysForTokens(session, registry, tokens)
		}
		s.send(peer, message)
	}
}

func (s *Server) publishCharacterDisplayChanged(session *Session, changed map[string]bool) {
	if len(session.ResourceOverlays) == 0 || len(changed) == 0 {
		return
	}
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		return
	}
	for peer := range s.peers {
		if peer.session != session.ID || peer.sceneID == "" || peer.region == nil {
			continue
		}
		scene := session.Scenes[peer.sceneID]
		if scene == nil {
			continue
		}
		for characterID, deleted := range changed {
			if deleted {
				continue
			}
			visible := false
			for reference := range session.characterReferences[characterID] {
				if reference.SceneID != peer.sceneID {
					continue
				}
				if token, exists := scene.Tokens[reference.TokenID]; exists && tokenLoadedForPeer(peer, scene, token) {
					visible = true
					break
				}
			}
			if !visible {
				continue
			}
			if display, exists := characterDisplayForConfig(session, registry, characterID); exists {
				s.send(peer, map[string]any{"type": "characterDisplayChanged", "sceneId": peer.sceneID, "characterId": characterID, "display": display})
			}
		}
	}
}

func validateSessionResourceOverlays(session *Session) error {
	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		return err
	}
	if err := validateResourceOverlayConfig(session.ResourceOverlays, registry); err != nil {
		return fmt.Errorf("resource overlay config: %w", err)
	}
	return nil
}
