package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const MaxRollHistory = 500

func initializeSessionDiceState(session *Session) bool {
	changed := false
	if session.RollHistory == nil {
		session.RollHistory = []RollEvent{}
		changed = true
	}
	if len(session.RollHistory) > MaxRollHistory {
		session.RollHistory = append([]RollEvent(nil), session.RollHistory[len(session.RollHistory)-MaxRollHistory:]...)
		changed = true
	}
	return changed
}

func validateSessionDiceState(session *Session) error {
	if session == nil {
		return errors.New("session is nil")
	}
	if len(session.RollHistory) > MaxRollHistory {
		return fmt.Errorf("roll history exceeds limit %d", MaxRollHistory)
	}
	if len(session.RollHistory) > 0 && session.RollRevision == 0 {
		return errors.New("roll revision must be positive when history is not empty")
	}
	eventIDs := make(map[string]struct{}, len(session.RollHistory))
	for index, event := range session.RollHistory {
		if err := validateRollEvent(event); err != nil {
			return fmt.Errorf("roll history event %d: %w", index, err)
		}
		if _, duplicate := eventIDs[event.ID]; duplicate {
			return fmt.Errorf("duplicate roll event id %q", event.ID)
		}
		eventIDs[event.ID] = struct{}{}
	}
	for stream, receipt := range session.Receipts {
		if receipt.RollEvent == nil {
			continue
		}
		if receipt.Error != "" {
			return fmt.Errorf("receipt %q has both roll event and error", stream)
		}
		if err := validateRollEvent(*receipt.RollEvent); err != nil {
			return fmt.Errorf("receipt %q roll event: %w", stream, err)
		}
	}
	return nil
}

func validateRollEvent(event RollEvent) error {
	if event.ID == "" || event.SceneID == "" || event.UserID == "" || event.AuthorName == "" || event.Timestamp.IsZero() {
		return errors.New("roll event is missing server metadata")
	}
	if err := validateDiceParameters(event.Count, event.Sides); err != nil {
		return err
	}
	if len(event.Results) != event.Count {
		return errors.New("roll result count does not match dice count")
	}
	diceTotal := 0
	for _, result := range event.Results {
		if result < 1 || result > event.Sides {
			return fmt.Errorf("roll result %d is outside [1,%d]", result, event.Sides)
		}
		diceTotal += result
	}
	if err := validateRollNumber("modifier", event.Modifier); err != nil {
		return err
	}
	if err := validateRollNumber("total", event.Total); err != nil {
		return err
	}
	if event.Total != float64(diceTotal)+event.Modifier {
		return errors.New("roll total does not match results and modifier")
	}
	if (event.ActionID == "") != (event.RollSpecID == "") || (event.ActionID == "") != (event.ActionName == "") || (event.RollSpecID == "") != (event.RollSpecName == "") {
		return errors.New("roll action metadata is incomplete")
	}
	if event.CharacterInstanceID == "" && (event.CharacterName != "" || event.ActionID != "") {
		return errors.New("roll character metadata is incomplete")
	}
	return nil
}

func cloneRollEvent(event RollEvent) RollEvent {
	event.Results = append([]int(nil), event.Results...)
	return event
}

func appendRollHistory(session *Session, event RollEvent) {
	if len(session.RollHistory) >= MaxRollHistory {
		kept := make([]RollEvent, 0, MaxRollHistory)
		kept = append(kept, session.RollHistory[len(session.RollHistory)-MaxRollHistory+1:]...)
		session.RollHistory = kept
	}
	session.RollHistory = append(session.RollHistory, cloneRollEvent(event))
}

func (s *Server) diceCommand(session *Session, peer *peer, command Command) {
	if session.Receipts == nil {
		session.Receipts = map[string]Receipt{}
	}
	stream := peer.member.ID + ":" + command.Client
	previousReceipt := session.Receipts[stream]
	encoded, _ := json.Marshal(command)
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	ack := func(receipt Receipt) {
		response := map[string]any{
			"type": "ack", "client": command.Client, "seq": command.Seq, "error": receipt.Error,
			"sceneId": command.SceneID, "rollRevision": session.RollRevision,
		}
		if receipt.RollEvent != nil {
			response["sceneId"] = receipt.RollEvent.SceneID
			response["rollEvent"] = cloneRollEvent(*receipt.RollEvent)
			response["replayed"] = true
		}
		s.send(peer, response)
	}
	if command.Seq == 0 || command.Client == "" || len(command.Client) > 80 {
		s.send(peer, map[string]string{"type": "fatal", "message": "Некорректный поток команд"})
		return
	}
	if command.Seq == previousReceipt.Seq {
		if previousReceipt.Digest != digest {
			s.send(peer, map[string]string{"type": "fatal", "message": "Номер команды уже занят другой командой. Откройте стол в новой вкладке"})
			return
		}
		ack(previousReceipt)
		return
	}
	if command.Seq < previousReceipt.Seq || command.Seq != previousReceipt.Seq+1 {
		s.send(peer, map[string]string{"type": "fatal", "message": "Нарушена последовательность команд. Откройте новую вкладку"})
		return
	}

	oldDirty := s.dirty
	oldRevision := session.RollRevision
	oldHistory := session.RollHistory
	var event *RollEvent
	issue := ""
	sceneID := peer.sceneID
	if sceneID == "" || command.SceneID != "" && command.SceneID != sceneID {
		issue = "Бросок требует подписки на указанную сцену"
	} else if command.Roll == nil {
		issue = "Параметры броска не заданы"
	} else {
		rolled, err := s.rollService.Execute(session, sceneID, peer.member, *command.Roll)
		if err != nil {
			issue = err.Error()
		} else {
			rolled = cloneRollEvent(rolled)
			event = &rolled
			session.RollRevision++
			appendRollHistory(session, rolled)
		}
	}

	receipt := Receipt{Seq: command.Seq, Error: issue, Digest: digest, Updated: time.Now().Unix()}
	if event != nil {
		copy := cloneRollEvent(*event)
		receipt.RollEvent = &copy
	}
	session.Receipts[stream] = receipt
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		session.RollRevision = oldRevision
		session.RollHistory = oldHistory
		if previousReceipt.Seq == 0 {
			delete(session.Receipts, stream)
		} else {
			session.Receipts[stream] = previousReceipt
		}
		s.dirty = oldDirty
		s.send(peer, map[string]any{"type": "saveError", "client": command.Client, "seq": command.Seq, "message": "Не удалось сохранить бросок. Команда не подтверждена; будет повторена"})
		return
	}
	if pruneReceipts(session, stream) {
		s.dirty = true
	}
	if event != nil {
		s.publishRoll(session, *event)
	}
	ack(session.Receipts[stream])
}

func (s *Server) publishRoll(session *Session, event RollEvent) {
	scene := session.Scenes[event.SceneID]
	if scene == nil {
		return
	}
	for peer := range s.peers {
		if peer.session != session.ID || peer.sceneID != event.SceneID || !sceneVisible(scene, peer.member) {
			continue
		}
		s.send(peer, map[string]any{
			"type": "rollEvent", "sceneId": event.SceneID, "rollRevision": session.RollRevision,
			"replayed": false, "event": cloneRollEvent(event),
		})
	}
}

func rollHistoryForScene(session *Session, sceneID string) []RollEvent {
	events := make([]RollEvent, 0)
	for _, event := range session.RollHistory {
		if event.SceneID == sceneID {
			events = append(events, cloneRollEvent(event))
		}
	}
	return events
}

func (s *Server) sendRollHistory(session *Session, peer *peer) {
	scene := session.Scenes[peer.sceneID]
	if !sceneVisible(scene, peer.member) {
		return
	}
	s.send(peer, map[string]any{
		"type": "rollHistory", "sceneId": scene.ID, "rollRevision": session.RollRevision,
		"replayed": true, "events": rollHistoryForScene(session, scene.ID),
	})
}
