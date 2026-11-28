package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	return validateActionJournal(session)
}

func validateRollEvent(event RollEvent) error {
	if event.ID == "" || event.SceneID == "" || event.UserID == "" || event.AuthorName == "" || event.Timestamp.IsZero() {
		return errors.New("roll event is missing server metadata")
	}
	groups := event.Groups
	if len(groups) == 0 {
		if err := validateDiceParameters(event.Count, event.Sides); err != nil {
			return err
		}
		groups = []DiceGroup{{Count: event.Count, Sides: event.Sides, Results: event.Results}}
	} else if event.Count != 0 || event.Sides != 0 {
		return errors.New("grouped roll contains legacy dice fields")
	}
	if err := validateDiceGroups(groups); err != nil {
		return err
	}
	diceTotal := 0
	flattened := make([]int, 0, len(event.Results))
	for _, group := range groups {
		if len(group.Results) != group.Count {
			return errors.New("roll group result count does not match dice count")
		}
		for _, result := range group.Results {
			if result < 1 || result > group.Sides {
				return fmt.Errorf("roll result %d is outside [1,%d]", result, group.Sides)
			}
			flattened = append(flattened, result)
			diceTotal += result
		}
	}
	if !slices.Equal(flattened, event.Results) {
		return errors.New("roll results do not match grouped results")
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
	if event.CharacterInstanceID == "" && event.CharacterName != "" {
		return errors.New("roll character metadata is incomplete")
	}
	return nil
}

func cloneRollEvent(event RollEvent) RollEvent {
	event.Results = append([]int(nil), event.Results...)
	event.Groups = append([]DiceGroup(nil), event.Groups...)
	for index := range event.Groups {
		event.Groups[index].Results = append([]int(nil), event.Groups[index].Results...)
	}
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
	var payload any = command
	if command.Type == "actionExecute" {
		payload = actionCommandWire{Type: command.Type, Client: command.Client, Seq: command.Seq, SceneID: command.SceneID, Action: command.Action, ActionGeneration: command.ActionGeneration}
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	if command.invalidActionDigest != "" {
		digest = command.invalidActionDigest
	}
	ack := func(receipt Receipt, replayed bool) {
		response := map[string]any{
			"type": "ack", "client": command.Client, "seq": command.Seq, "error": receipt.Error,
			"sceneId": command.SceneID, "rollRevision": session.RollRevision,
		}
		if receipt.RollEvent != nil {
			response["sceneId"] = receipt.RollEvent.SceneID
			response["rollEvent"] = cloneRollEvent(*receipt.RollEvent)
			response["replayed"] = true
		}
		if command.Type == "actionExecute" {
			response["actionGeneration"] = session.ActionGeneration
			if receipt.Error != "" && !memberIsGM(peer.member) {
				response["error"] = "action rejected"
			}
		}
		if receipt.Action != nil {
			response["replayed"] = replayed
			response["actionRevision"] = session.ActionRevision
			response["characterRevision"] = session.CharacterRevision
			a := audienceForAction(session, peer)
			e := receipt.Action
			if public, ok := a.project(e); ok {
				response["action"] = public
				if e.ObjectResponse != nil && e.UserID == peer.member.ID {
					name := "Объект"
					if e.Target != nil {
						if scene := session.Scenes[e.SceneID]; scene != nil {
							if element := scene.Elements[e.Target.ElementID]; element.Name != "" {
								name = element.Name
							}
						}
					}
					reaction := map[string]any{"type": "objectReaction", "sceneId": e.SceneID, "executionId": e.ExecutionID, "text": e.ObjectResponse.Text, "name": name}
					if e.ObjectResponse.ImageAssetID != "" {
						if media := objectMediaPayload(session, peer.member, e.ObjectResponse.ImageAssetID); media != nil {
							reaction["media"] = media
						}
					}
					response["objectReaction"] = reaction
				}
				if a.seesCharacter(e.CharacterID, true) && (e.Target == nil || e.Target.ElementID != "" && a.seesHistoricalTarget(*e.Target) || a.seesCharacter(e.Target.CharacterID, true)) {
					response["details"] = e.Details
				}
			}
		}
		s.send(peer, response)
	}
	if command.Seq == 0 || command.Client == "" || len(command.Client) > 80 {
		s.send(peer, map[string]string{"type": "fatal", "message": "Некорректный поток команд"})
		return
	}
	minGeneration, knownStream := session.ActionStreams[stream]
	if !knownStream {
		minGeneration = session.ActionGeneration
	}
	expired := command.Type == "actionExecute" && (command.ActionGeneration < minGeneration || command.ActionGeneration > session.ActionGeneration)
	if command.Seq == previousReceipt.Seq && !expired {
		if previousReceipt.Digest != digest {
			s.send(peer, map[string]string{"type": "fatal", "message": "Номер команды уже занят другой командой. Откройте стол в новой вкладке"})
			return
		}
		ack(previousReceipt, true)
		return
	}
	if command.Type == "actionExecute" && (expired || command.Seq < previousReceipt.Seq) {
		for i := len(session.ActionHistory) - 1; i >= 0; i-- {
			e := &session.ActionHistory[i]
			if e.Stream == stream && e.Seq == command.Seq && e.Digest == digest {
				ack(Receipt{Action: e}, true)
				return
			}
		}
	}
	if expired {
		s.send(peer, map[string]any{"type": "fatal", "message": "Поколение потока команд устарело. Откройте новую вкладку", "actionGeneration": session.ActionGeneration})
		return
	}
	if command.Seq < previousReceipt.Seq || command.Seq != previousReceipt.Seq+1 {
		s.send(peer, map[string]string{"type": "fatal", "message": "Нарушена последовательность команд. Откройте новую вкладку"})
		return
	}

	// An asynchronous rotation still owns its sequence until its receipt is
	// committed. It must not be able to overwrite an action's newer receipt.
	if command.Type == "actionExecute" {
		for _, pending := range s.rotationJobs {
			if pending.sessionID == session.ID && pending.memberID == peer.member.ID && pending.client == command.Client {
				s.send(peer, map[string]any{"type": "fatal", "message": "Поток занят незавершённой командой"})
				return
			}
		}
	}
	oldDirty := s.dirty
	oldRevision := session.RollRevision
	oldHistory := session.RollHistory
	var event *RollEvent
	var action *actionCommit
	issue := ""
	sceneID := peer.sceneID
	if sceneID == "" || command.SceneID != "" && command.SceneID != sceneID {
		issue = "Бросок требует подписки на указанную сцену"
	} else if command.Type == "actionExecute" {
		var err error
		action, err = s.prepareAction(session, peer, command, stream, digest)
		if err != nil {
			issue = err.Error()
		} else {
			action.apply(session)
		}
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
	if action != nil {
		receipt.Action = &action.entry
	}
	newActionStream := command.Type == "actionExecute" && !knownStream
	if newActionStream {
		if session.ActionStreams == nil {
			session.ActionStreams = make(map[string]uint64)
		}
		session.ActionStreams[stream] = session.ActionGeneration
	}
	session.Receipts[stream] = receipt
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		if action != nil {
			action.rollback(session)
		}
		if newActionStream {
			delete(session.ActionStreams, stream)
		}
		session.RollRevision = oldRevision
		session.RollHistory = oldHistory
		if previousReceipt.Seq == 0 {
			delete(session.Receipts, stream)
		} else {
			session.Receipts[stream] = previousReceipt
		}
		s.dirty = oldDirty
		s.send(peer, map[string]any{"type": "saveError", "client": command.Client, "seq": command.Seq, "message": "Не удалось сохранить команду. Можно повторить отправку"})
		return
	}
	if pruneReceipts(session, stream) {
		s.dirty = true
	}
	if event != nil {
		s.publishRoll(session, *event)
	}
	if action != nil {
		s.publishAction(session, action)
	}
	ack(session.Receipts[stream], false)
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
	actions, hasMore := actionHistoryPageForPeer(session, peer, 0)
	s.send(peer, map[string]any{
		"type": "rollHistory", "sceneId": scene.ID, "rollRevision": session.RollRevision,
		"replayed": true, "events": rollHistoryForScene(session, scene.ID),
		"actionRevision": session.ActionRevision, "actionGeneration": session.ActionGeneration,
		"actions": actions, "actionHistoryHasMore": hasMore,
	})
}

func (s *Server) sendActionTraceHistory(session *Session, peer *peer) {
	if !peerCanReadTrace(peer) || !sceneVisible(session.Scenes[peer.sceneID], peer.member) {
		return
	}
	actions, hasMore := actionHistoryPageForPeer(session, peer, 0)
	s.send(peer, map[string]any{"type": "actionTraceHistory", "sceneId": peer.sceneID, "actions": actions, "actionHistoryHasMore": hasMore})
}

// Older action history is requested only when the user scrolls the GM debug
// journal. The cursor is an exclusive revision, never a client-visible offset.
func (s *Server) sendActionHistoryPage(session *Session, peer *peer, beforeRevision uint64) {
	if beforeRevision == 0 || !peerCanReadTrace(peer) || !sceneVisible(session.Scenes[peer.sceneID], peer.member) {
		return
	}
	actions, hasMore := actionHistoryPageForPeer(session, peer, beforeRevision)
	s.send(peer, map[string]any{
		"type": "actionHistoryPage", "sceneId": peer.sceneID,
		"beforeActionRevision": beforeRevision, "actions": actions,
		"actionHistoryHasMore": hasMore,
	})
}
