package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Only identifiers of server-side instances cross the command boundary.
type actionCommandWire struct {
	ActionGeneration uint64                  `json:"actionGeneration,omitempty"`
	Type             string                  `json:"type"`
	Client           string                  `json:"client"`
	Seq              uint64                  `json:"seq"`
	SceneID          string                  `json:"sceneId,omitempty"`
	Action           *ActionExecutionRequest `json:"action"`
}

// Action journal records and receipts are server-only. Project them for the
// subscriber both on live delivery and on replay after permissions change.
type actionJournalEntry struct {
	ActionEvent
	UserID        string                `json:"userId"`
	Details       json.RawMessage       `json:"details"`
	Stream        string                `json:"stream"`
	Seq           uint64                `json:"seq"`
	Digest        string                `json:"digest"`
	Target        *ActionInstanceRef    `json:"target,omitempty"`
	GMOnly        bool                  `json:"gmOnly,omitempty"`
	MessageAccess []actionMessageAccess `json:"messageAccess,omitempty"`
}

type ActionEvent struct {
	ExecutionID string               `json:"executionId"`
	SceneID     string               `json:"sceneId"`
	CharacterID string               `json:"characterId"`
	ActionID    string               `json:"actionId"`
	Revision    uint64               `json:"actionRevision"`
	Timestamp   time.Time            `json:"timestamp"`
	Roll        *ActionExecutionRoll `json:"roll,omitempty"`
	Success     *bool                `json:"success,omitempty"`
	Messages    []string             `json:"messages,omitempty"`
}

type actionMessageAccess struct {
	Actor  bool `json:"actor,omitempty"`
	Target bool `json:"target,omitempty"`
}

func messageAccess(result ActionExecutionResult) []actionMessageAccess {
	var access []actionMessageAccess
	for _, message := range result.messageTemplates {
		var permission actionMessageAccess
		for _, part := range message.parts {
			if part.ref == nil {
				continue
			}
			switch part.ref.path[0] {
			case "roll": // The action's announced roll.
			case "target", "action":
				permission.Target = true
			case "var":
				permission.Actor = true
				permission.Target = result.Target != nil
			default:
				permission.Actor = true
			}
		}
		access = append(access, permission)
	}
	return access
}

type actionAudience struct {
	session *Session
	peer    *peer
	scene   *Scene
	floors  map[string]float64
}

func audienceForAction(session *Session, p *peer) actionAudience {
	a := actionAudience{session: session, peer: p, scene: session.Scenes[p.sceneID]}
	if a.scene != nil && peerUsesPlayerProjection(p) {
		a.floors = visibleFloorSet(a.scene, currentFloorForPeer(p, a.scene))
	}
	return a
}

func (a actionAudience) seesCharacter(characterID string, control bool) bool {
	if !sceneVisible(a.scene, a.peer.member) || a.peer.playerPreview && !a.scene.Published {
		return false
	}
	for ref := range a.session.characterReferences[characterID] {
		if ref.SceneID != a.scene.ID {
			continue
		}
		t, ok := a.scene.Tokens[ref.TokenID]
		if !ok || t.CharacterInstanceID != characterID || !tokenVisibleToPeer(a.peer, t) {
			continue
		}
		if a.floors != nil {
			if _, ok := a.floors[tokenFloorID(a.scene, t)]; !ok {
				continue
			}
		}
		if !control || !a.peer.playerPreview && memberCanControlToken(a.peer.member, t) {
			return true
		}
	}
	return false
}

func (a actionAudience) project(e *actionJournalEntry) (ActionEvent, bool) {
	if a.scene == nil || e.SceneID != a.scene.ID {
		return ActionEvent{}, false
	}
	if !peerUsesPlayerProjection(a.peer) {
		return e.ActionEvent, true
	}
	if e.GMOnly || !a.seesCharacter(e.CharacterID, false) || e.Target != nil && !a.seesCharacter(e.Target.CharacterID, e.Target.ItemID != "") {
		return ActionEvent{}, false
	}
	out := e.ActionEvent
	out.Messages = nil
	for i, message := range e.Messages {
		access := e.MessageAccess[i]
		if access.Actor && !a.seesCharacter(e.CharacterID, true) || access.Target && (e.Target == nil || !a.seesCharacter(e.Target.CharacterID, true)) {
			continue
		}
		out.Messages = append(out.Messages, message)
	}
	return out, true
}

func actionHistoryForPeer(session *Session, p *peer) []ActionEvent {
	out := make([]ActionEvent, 0)
	if len(session.ActionHistory) == 0 {
		return out
	}
	a := audienceForAction(session, p)
	for i := range session.ActionHistory {
		if event, ok := a.project(&session.ActionHistory[i]); ok {
			out = append(out, event)
		}
	}
	return out
}

// Executor replacements are detached, validated drafts. Keep the original
// instance values until the atomic session rename succeeds.
type actionCommit struct {
	entry           actionJournalEntry
	plan            *ActionChangePlan
	previous        map[string]CharacterInstance
	changed         map[string]bool
	previousHistory []actionJournalEntry
}

// Bound both retained history and the last result retained for each command stream.
const maxActionOutputBytes = 32 << 10

type actionDetails struct {
	Source    *ActionInstanceRef     `json:"source,omitempty"`
	Target    *ActionInstanceRef     `json:"target,omitempty"`
	Changes   []ActionChange         `json:"changes,omitempty"`
	Variables map[string]any         `json:"variables,omitempty"`
	Skipped   []ActionExecutionIssue `json:"skipped,omitempty"`
}

func (s *Server) prepareAction(session *Session, p *peer, c Command, stream, digest string) (*actionCommit, error) {
	if c.invalidActionDigest != "" || c.Action == nil || c.SceneID != p.sceneID || p.playerPreview {
		return nil, errors.New("invalid action command")
	}

	req := *c.Action
	a := audienceForAction(session, p)
	if !a.seesCharacter(req.CharacterID, true) || req.Target != nil && !a.seesCharacter(req.Target.CharacterID, req.Target.ItemID != "") {
		return nil, errors.New("actor or target unavailable")
	}
	result, err := ExecuteRulesetAction(session, p.sceneID, p.member, req, s.rollService.rng)
	if err != nil {
		if !memberIsGM(p.member) {
			return nil, errors.New("action unavailable or calculation failed")
		}
		return nil, err
	}
	if plan := result.Plan; plan != nil {
		if plan.SessionID != session.ID || plan.SceneID != a.scene.ID || plan.SceneRevision != a.scene.Revision || plan.CampaignRevision != session.CampaignRevision || plan.RegistryRevision != session.RegistryRevision || plan.CharacterRevision != session.CharacterRevision {
			return nil, errors.New("stale action plan")
		}
		for _, change := range plan.Changes {
			if !canApplyActionChange(a, result.Target, change) {
				return nil, errors.New("action write denied")
			}
		}
	}
	details := actionDetails{Source: result.Source, Target: result.Target, Variables: result.Variables, Skipped: result.Skipped}
	if result.Plan != nil {
		details.Changes = result.Plan.Changes
	}
	// Keep the exact JSON scalars (including int64 variables) across restart,
	// without retaining the executor's character drafts in memory.
	encoded, err := json.Marshal(details)
	if err != nil {
		return nil, err
	}
	size := len(encoded)
	for _, message := range result.Messages {
		size += len(message)
	}
	if size > maxActionOutputBytes {
		return nil, errors.New("action output exceeds journal limit")
	}
	entry := actionJournalEntry{ActionEvent: ActionEvent{
		ExecutionID: id(), SceneID: p.sceneID, CharacterID: req.CharacterID, ActionID: req.ActionID, Timestamp: s.rollService.now().UTC(),
		Revision: session.ActionRevision + 1, Roll: result.Roll, Success: result.Success, Messages: result.Messages,
	}, UserID: p.member.ID, Details: encoded, Stream: stream, Seq: c.Seq, Digest: digest, Target: result.Target, MessageAccess: messageAccess(result)}
	player := &Member{}
	entry.GMOnly = !a.scene.Published || !actionCharacterVisible(session, a.scene, player, req.CharacterID) || result.Target != nil && !actionCharacterVisible(session, a.scene, player, result.Target.CharacterID)
	return &actionCommit{entry: entry, plan: result.Plan}, nil
}

func canApplyActionChange(a actionAudience, target *ActionInstanceRef, change ActionChange) bool {
	if memberIsGM(a.peer.member) {
		return true
	}
	path, err := parseRulesetPath(change.Field)
	if err != nil {
		return false
	}
	if change.Instance.ItemID == "" && len(path) >= 3 && path[0] == "stat" {
		stat := a.session.Ruleset.Registry.Stats[rulesetPath(path[1:len(path)-1])]
		if stat.IsResource() {
			// Direct changes of resource initial values require GM privileges.
			if path[len(path)-1] == "base" {
				return false
			}
			if path[len(path)-1] == "current" && target != nil && change.Instance == *target {
				return a.seesCharacter(target.CharacterID, false)
			}
		}
	}
	return a.seesCharacter(change.Instance.CharacterID, true)
}

func (m *actionCommit) apply(session *Session) {
	if m.plan != nil {
		m.previous = make(map[string]CharacterInstance, len(m.plan.replacements))
		m.changed = make(map[string]bool, len(m.plan.replacements))
		for id, draft := range m.plan.replacements {
			m.previous[id] = session.CharacterInstances[id]
			session.CharacterInstances[id] = draft
			m.changed[id] = false
		}
		session.CharacterRevision++
	}
	if m.entry.Roll != nil {
		session.RollRevision++
	}
	m.previousHistory = session.ActionHistory
	if len(session.ActionHistory) == MaxRollHistory {
		session.ActionHistory = slices.Clone(session.ActionHistory[1:])
	}
	session.ActionHistory = append(session.ActionHistory, m.entry)
	session.ActionRevision++

}

func (m *actionCommit) rollback(session *Session) {
	for id, original := range m.previous {
		session.CharacterInstances[id] = original
	}
	if m.plan != nil {
		session.CharacterRevision--
	}
	session.ActionHistory = m.previousHistory
	session.ActionRevision--

}

func (s *Server) publishAction(session *Session, m *actionCommit) {
	for p := range s.peers {
		if p.session != session.ID || p.sceneID != m.entry.SceneID {
			continue
		}
		event, ok := audienceForAction(session, p).project(&m.entry)
		if !ok {
			continue
		}
		messages := event.Messages
		event.Messages = nil
		if event.Roll != nil {
			s.send(p, map[string]any{"type": "actionRoll", "executionId": event.ExecutionID, "rollRevision": session.RollRevision, "event": event})
		}
		for i, text := range messages {
			s.send(p, map[string]any{"type": "actionMessage", "executionId": event.ExecutionID, "sceneId": event.SceneID, "actionRevision": event.Revision, "index": i, "message": text})
		}
	}
	if len(m.changed) > 0 {
		s.publishCharacterDisplayChanged(session, m.changed, m.entry.ExecutionID)
		s.refreshCharacterWatches(session, m.changed, false, m.entry.ExecutionID)
	}
}

func validateActionJournal(session *Session) error {
	if len(session.ActionHistory) > MaxRollHistory {
		return errors.New("invalid action journal size")
	}
	for stream, minimum := range session.ActionStreams {
		if minimum > session.ActionGeneration || session.Receipts[stream].Seq == 0 {
			return errors.New("missing action stream receipt")
		}
	}
	validate := func(e *actionJournalEntry) error {
		if e.ExecutionID == "" || e.UserID == "" || e.CharacterID == "" || e.ActionID == "" || e.SceneID == "" || e.Timestamp.IsZero() ||
			e.Revision == 0 || e.Revision > session.ActionRevision || !strings.HasPrefix(e.Stream, e.UserID+":") || e.Seq == 0 || len(e.Digest) != 64 ||
			len(e.Messages) != len(e.MessageAccess) || len(e.Details) == 0 {
			return fmt.Errorf("invalid action journal entry %q", e.ExecutionID)
		}
		size := len(e.Details)
		for _, message := range e.Messages {
			size += len(message)
		}
		if size > maxActionOutputBytes {
			return errors.New("oversized action journal entry")
		}
		if e.Roll != nil {
			dice := 0
			for _, group := range e.Roll.Dice {
				if group.Count != len(group.Results) || group.Count < 1 || group.Count > MaxDicePerRoll-dice || group.Sides < 1 || group.Sides > maxExpressionSides {
					return errors.New("invalid action dice group")
				}
				dice += group.Count
				for _, v := range group.Results {
					if v < 1 || v > group.Sides {
						return errors.New("invalid action die result")
					}
				}
			}
		}
		return nil
	}
	if uint64(len(session.ActionHistory)) != min(uint64(MaxRollHistory), session.ActionRevision) {
		return errors.New("action history and revision disagree")
	}
	ids := make(map[string]bool, len(session.ActionHistory))
	for i := range session.ActionHistory {
		e := &session.ActionHistory[i]
		if ids[e.ExecutionID] || e.Revision != session.ActionRevision-uint64(len(session.ActionHistory)-1-i) {
			return errors.New("action journal out of sequence or duplicate execution")
		}
		ids[e.ExecutionID] = true
		if err := validate(e); err != nil {
			return err
		}
	}
	for stream, receipt := range session.Receipts {
		if e := receipt.Action; e != nil {
			_, known := session.ActionStreams[stream]
			if !known || receipt.Error != "" || receipt.RollEvent != nil || e.Stream != stream || e.Seq != receipt.Seq || e.Digest != receipt.Digest {
				return errors.New("inconsistent action receipt")
			}
			if err := validate(e); err != nil {
				return err
			}
		}
	}
	return nil
}
