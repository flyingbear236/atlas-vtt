package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// One outstanding reliable command per browser stream. Business receipts are
// persisted before ACK; coalesced transform receipts persist on the next flush.
type Receipt struct {
	Seq         uint64     `json:"seq"`
	Error       string     `json:"error,omitempty"`
	Digest      string     `json:"digest"`
	Updated     int64      `json:"updated,omitempty"`
	CharacterID string     `json:"characterId,omitempty"`
	RollEvent   *RollEvent `json:"rollEvent,omitempty"`
}

const maxReceiptsPerSession = 2048

const persistenceFlushInterval = 5 * time.Second
const storageDegradedMessage = "Сервер временно не может сохранять изменения на диск. Работа продолжается, но при аварийном завершении процесса несохранённые изменения могут быть потеряны"
const movementBlockedErrorCode = "movementBlocked"
const previewContextChangedErrorCode = "previewContextChanged"
const previewContextChangedIssue = "Контекст Player View изменился"
const movementBlockedIssue = "Токен нельзя переместить за границы игровой области"

type persistenceClass uint8

const (
	persistenceImmediate persistenceClass = iota
	persistenceRealtime
	persistenceCoalesced
)

// Position/transform commands are classified here rather than in Token
// storage. Future movable Scene Objects and Layers can use the same lifecycle.
func commandPersistence(commandType string) persistenceClass {
	switch commandType {
	case "move":
		return persistenceRealtime
	case "elementPreview":
		return persistenceRealtime
	case "final", "elementTransform":
		return persistenceCoalesced
	default:
		return persistenceImmediate
	}
}

func receiptGapAllowed(c Command) bool {
	// Coalesced positions deliberately tolerate sequence gaps after an unclean
	// server stop: ACKed transforms may have been accepted in RAM but not yet
	// reached the periodic persistence flush.
	return commandPersistence(c.Type) == persistenceCoalesced
}

func pruneReceipts(ss *Session, keep string) bool {
	if len(ss.Receipts) <= maxReceiptsPerSession {
		return false
	}
	type receiptAge struct {
		key     string
		updated int64
	}
	items := make([]receiptAge, 0, len(ss.Receipts))
	for key, receipt := range ss.Receipts {
		if key != keep {
			items = append(items, receiptAge{key: key, updated: receipt.Updated})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].updated < items[j].updated })
	remove := len(ss.Receipts) - maxReceiptsPerSession
	for i := 0; i < remove && i < len(items); i++ {
		delete(ss.Receipts, items[i].key)
	}
	return remove > 0
}

type Properties struct {
	Name     *string   `json:"name,omitempty"`
	Size     *float64  `json:"size,omitempty"`
	Color    *string   `json:"color,omitempty"`
	Opacity  *float64  `json:"opacity,omitempty"`
	OwnerIDs *[]string `json:"ownerIds,omitempty"`
	Hidden   *bool     `json:"hidden,omitempty"`
	Asset    *string   `json:"asset,omitempty"`
	FloorID  *string   `json:"floorId,omitempty"`
}

type ElementProperties struct {
	Name    *string  `json:"name,omitempty"`
	FloorID *string  `json:"floorId,omitempty"`
	LayerID *string  `json:"layerId,omitempty"`
	ZOrder  *int     `json:"zOrder,omitempty"`
	Visible *bool    `json:"visible,omitempty"`
	Locked  *bool    `json:"locked,omitempty"`
	Opacity *float64 `json:"opacity,omitempty"`
}

type LayerProperties struct {
	Name    *string  `json:"name,omitempty"`
	Order   *int     `json:"order,omitempty"`
	Visible *bool    `json:"visible,omitempty"`
	Locked  *bool    `json:"locked,omitempty"`
	Opacity *float64 `json:"opacity,omitempty"`
}

type FloorProperties struct {
	Name                       *string  `json:"name,omitempty"`
	Order                      *int     `json:"order,omitempty"`
	Opacity                    *float64 `json:"opacity,omitempty"`
	OpacityWhenViewedFromBelow *float64 `json:"opacityWhenViewedFromBelow,omitempty"`
	ShowWalkableToPlayers      *bool    `json:"showWalkableToPlayers,omitempty"`
}
type Command struct {
	Type                     string                     `json:"type"`
	Token                    Token                      `json:"token"`
	Properties               Properties                 `json:"properties"`
	Client                   string                     `json:"client"`
	Seq                      uint64                     `json:"seq"`
	After                    uint64                     `json:"after"`
	SceneID                  string                     `json:"sceneId,omitempty"`
	SceneName                string                     `json:"sceneName,omitempty"`
	Published                *bool                      `json:"published,omitempty"`
	Region                   *SceneRegion               `json:"region,omitempty"`
	ViewFloorID              string                     `json:"viewFloorId,omitempty"`
	ActiveTokenID            string                     `json:"activeTokenId,omitempty"`
	Focus                    bool                       `json:"focus,omitempty"`
	PreviewEnabled           *bool                      `json:"enabled,omitempty"`
	PreviewRevision          uint64                     `json:"previewRevision,omitempty"`
	Bounds                   *SceneBounds               `json:"bounds,omitempty"`
	Floor                    Floor                      `json:"floor,omitempty"`
	FloorProperties          FloorProperties            `json:"floorProperties,omitempty"`
	Layer                    Layer                      `json:"layer,omitempty"`
	LayerProperties          LayerProperties            `json:"layerProperties,omitempty"`
	Element                  SceneElement               `json:"element,omitempty"`
	ElementProperties        ElementProperties          `json:"elementProperties,omitempty"`
	Transition               Transition                 `json:"transition,omitempty"`
	AssetID                  string                     `json:"assetId,omitempty"`
	RetentionPolicy          string                     `json:"retentionPolicy,omitempty"`
	MemberID                 string                     `json:"memberId,omitempty"`
	GM                       *bool                      `json:"gm,omitempty"`
	FloorID                  string                     `json:"floorId,omitempty"`
	ComponentID              string                     `json:"componentId,omitempty"`
	WalkableBounds           *WalkableBounds            `json:"walkableBounds,omitempty"`
	RenderBounds             *Polygon                   `json:"renderBounds,omitempty"`
	WalkableMode             string                     `json:"walkableMode,omitempty"`
	DeltaX                   float64                    `json:"deltaX,omitempty"`
	DeltaY                   float64                    `json:"deltaY,omitempty"`
	ExpectedGeometryRevision *uint64                    `json:"expectedGeometryRevision,omitempty"`
	DefinitionKind           string                     `json:"definitionKind,omitempty"`
	DefinitionID             string                     `json:"definitionId,omitempty"`
	DuplicateDefinitionID    string                     `json:"duplicateDefinitionId,omitempty"`
	StatDefinition           *StatDefinition            `json:"statDefinition,omitempty"`
	ActionDefinition         *ActionDefinition          `json:"actionDefinition,omitempty"`
	PresetDefinition         *CharacterPresetDefinition `json:"presetDefinition,omitempty"`
	ExpectedRegistryRevision *uint64                    `json:"expectedRegistryRevision,omitempty"`
	CharacterID              string                     `json:"characterId,omitempty"`
	CharacterWatch           uint64                     `json:"characterWatch,omitempty"`
	CharacterName            string                     `json:"characterName,omitempty"`
	PresetID                 string                     `json:"presetId,omitempty"`
	TokenID                  string                     `json:"tokenId,omitempty"`
	StatID                   string                     `json:"statId,omitempty"`
	StatValue                *StatValue                 `json:"statValue,omitempty"`
	ActionID                 string                     `json:"actionId,omitempty"`
	Persistent               *bool                      `json:"persistent,omitempty"`
	AvatarAssetID            *string                    `json:"avatarAssetId,omitempty"`
	Roll                     *RollRequest               `json:"roll,omitempty"`
	wireBytes                int
}

func (command *Command) UnmarshalJSON(data []byte) error {
	type commandWire Command
	var decoded commandWire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*command = Command(decoded)
	command.wireBytes = len(data)
	return nil
}

func (s *Server) command(ss *Session, p *peer, c Command) {
	if c.Type == "roll" {
		s.diceCommand(ss, p, c)
		return
	}
	if characterCommandType(c.Type) {
		s.characterCommand(ss, p, c)
		return
	}
	if definitionCommandType(c.Type) {
		s.definitionCommand(ss, p, c)
		return
	}
	if c.Type == "sceneCreate" || c.Type == "sceneUpdate" || c.Type == "sceneDelete" || c.Type == "memberUpdate" {
		s.sceneCommand(ss, p, c)
		return
	}
	if sceneContentCommand(c.Type) {
		s.contentCommand(ss, p, c)
		return
	}
	sceneID := c.SceneID
	if sceneID == "" {
		sceneID = p.sceneID
	}
	persistence := commandPersistence(c.Type)
	reliable := persistence != persistenceRealtime
	stream := p.member.ID + ":" + c.Client
	if ss.Receipts == nil {
		ss.Receipts = map[string]Receipt{}
	}
	receipt := ss.Receipts[stream]
	digest := ""
	if c.Seq > 0 {
		encoded, _ := json.Marshal(c)
		sum := sha256.Sum256(encoded)
		digest = hex.EncodeToString(sum[:])
	}
	if c.Type == "move" && c.Client != "" && c.After < receipt.Seq {
		return
	}
	ack := func(r Receipt) {
		var revision uint64
		if current := ss.Scenes[sceneID]; current != nil {
			revision = current.Revision
		}
		response := map[string]any{"type": "ack", "client": c.Client, "seq": c.Seq, "error": r.Error, "revision": revision, "sceneId": sceneID}
		if r.Error == movementBlockedIssue {
			response["errorCode"] = movementBlockedErrorCode
		} else if r.Error == previewContextChangedIssue {
			response["errorCode"] = previewContextChangedErrorCode
			response["previewRevision"] = p.previewRevision
		}
		s.send(p, response)
	}
	if c.Seq > 0 {
		if !reliable || c.Client == "" || len(c.Client) > 80 {
			s.send(p, map[string]string{"type": "fatal", "message": "Некорректный поток команд"})
			return
		}
		if c.Seq == receipt.Seq {
			if receipt.Digest != digest {
				s.send(p, map[string]string{"type": "fatal", "message": "Номер команды уже занят другой командой. Откройте стол в новой вкладке"})
				return
			}
			ack(receipt)
			return
		}
		if c.Seq < receipt.Seq || (c.Seq != receipt.Seq+1 && !receiptGapAllowed(c)) {
			s.send(p, map[string]string{"type": "fatal", "message": "Нарушена последовательность команд. Откройте новую вкладку"})
			return
		}
	}
	scene := ss.Scenes[sceneID]
	accessIssue := ""
	if scene == nil {
		accessIssue = "Сцена не найдена"
	} else if !memberIsGM(p.member) && !scene.Published {
		accessIssue = "Сцена скрыта"
	}
	if accessIssue != "" {
		if c.Seq == 0 {
			s.send(p, map[string]string{"type": "error", "message": accessIssue})
			return
		}
		oldDirty := s.dirty
		ss.Receipts[stream] = Receipt{Seq: c.Seq, Error: accessIssue, Digest: digest, Updated: time.Now().Unix()}
		s.dirty = true
		if err := s.saveLocked(); err != nil {
			if receipt.Seq == 0 {
				delete(ss.Receipts, stream)
			} else {
				ss.Receipts[stream] = receipt
			}
			s.dirty = oldDirty
			s.send(p, map[string]any{"type": "saveError", "client": c.Client, "seq": c.Seq, "message": "Не удалось записать отказ команды на диск. Команда не подтверждена; будет повторена"})
			return
		}
		if pruneReceipts(ss, stream) {
			s.dirty = true
		}
		ack(ss.Receipts[stream])
		return
	}
	old, exists := scene.Tokens[c.Token.ID]
	old = cloneToken(old)
	t := cloneToken(old)
	gm := memberIsGM(p.member)
	kind := "upsert"
	var issue string
	switch c.Type {
	case "move", "final":
		if c.Type == "move" {
			kind = "move"
		}
		if !previewContextMatches(p, c) {
			issue = previewContextChangedIssue
			break
		}
		if !exists || !memberCanControlToken(p.member, old) {
			issue = "Нет права перемещать этот токен"
			break
		}
		if peerUsesPlayerProjection(p) && old.FloorID != currentFloorForPeer(p, scene) {
			issue = "Токен находится на другом этаже"
			break
		}
		if !validNumber(c.Token.X) || !validNumber(c.Token.Y) {
			issue = "Некорректные координаты"
			break
		}
		if c.Token.FloorID != "" && c.Token.FloorID != old.FloorID {
			// A transition can arrive while the pointer still has one queued move/final
			// from the source floor. Treat that stale drag event as an idempotent no-op:
			// it must neither move the teleported token nor surface a spurious error.
			break
		}
		if peerUsesPlayerProjection(p) && !CanMoveTokenSegment(scene, old.FloorID, ScenePoint{X: old.X, Y: old.Y}, ScenePoint{X: c.Token.X, Y: c.Token.Y}) {
			issue = movementBlockedIssue
			break
		}
		t.X = c.Token.X
		t.Y = c.Token.Y
		if destination, ok := transitionForMove(scene, old, ScenePoint{X: t.X, Y: t.Y}); ok {
			t.FloorID = destination.FloorID
			t.LayerID = layerIDByKind(scene, t.FloorID, layerKindTokens)
			t.X, t.Y = destination.Position.X, destination.Position.Y
			kind = "upsert"
		}
	case "properties", "create":
		if !gm {
			issue = "Действие доступно ведущему"
			break
		}
		if c.Type == "create" {
			if len(scene.Tokens) >= maxSceneTokens {
				issue = "Лимит объектов сцены достигнут"
				break
			}
			t = c.Token
			t.ID = id()
			if t.legacyOwnerField {
				t.OwnerIDs = []string{}
			}
			t.OwnerIDs, _ = normalizeOwnerIDs(t.OwnerIDs)
			t.legacyOwnerField = false
			// Character assignment has its own validated commands in a later
			// stage; generic token creation must not smuggle in a relationship.
			t.CharacterInstanceID = ""
			if t.FloorID == "" {
				t.FloorID = firstFloorID(scene)
			}
			t.LayerID = layerIDByKind(scene, t.FloorID, layerKindTokens)
		} else if !exists {
			issue = "Токен не найден"
			break
		}
		if c.Type == "properties" {
			v := c.Properties
			if v.Name != nil {
				t.Name = *v.Name
			}
			if v.Size != nil {
				t.Size = *v.Size
			}
			if v.Color != nil {
				t.Color = *v.Color
			}
			if v.Opacity != nil {
				t.Opacity = *v.Opacity
			}
			if v.OwnerIDs != nil {
				t.OwnerIDs, _ = normalizeOwnerIDs(*v.OwnerIDs)
			}
			if v.Hidden != nil {
				t.Hidden = *v.Hidden
			}
			if v.Asset != nil {
				t.Asset = *v.Asset
			}
			if v.FloorID != nil {
				t.FloorID = *v.FloorID
				t.LayerID = layerIDByKind(scene, t.FloorID, layerKindTokens)
			}
		}
		tokenLayer := scene.Layers[t.LayerID]
		if !validNumber(t.X) || !validNumber(t.Y) || !validNumber(t.Size) || !validNumber(t.Rotation) || !validNumber(t.Opacity) || t.Opacity < 0 || t.Opacity > 1 || t.Size < 16 || t.Size > 1024 || utf8.RuneCountInString(t.Name) > 80 || len(t.Color) > 32 || scene.Floors[t.FloorID].ID == "" || tokenLayer.Kind != layerKindTokens || tokenLayer.FloorID != t.FloorID {
			issue = "Некорректные свойства токена"
			break
		}
		if !validateTokenOwners(t, ss.Members) {
			issue = "Игрок не найден"
			break
		}
		if t.Asset != "" && ss.Assets[t.Asset].ID == "" {
			issue = "Изображение не найдено"
			break
		}
	case "delete":
		if !gm {
			issue = "Действие доступно ведущему"
			break
		}
		if !exists {
			issue = "Токен не найден"
			break
		}
		kind = "delete"
	default:
		issue = "Неизвестное событие"
	}
	revision, characterRevision, dirty := scene.Revision, ss.CharacterRevision, s.dirty
	changed := false
	characterWatchChanged := false
	var assetsBeforeReferences map[string]Asset
	var collectedCharacter CharacterInstance
	collectedCharacterExists := false
	if issue == "" {
		if kind == "delete" {
			delete(scene.Tokens, t.ID)
			changed = true
		} else if !exists || !tokensEqual(t, old) {
			scene.Tokens[t.ID] = t
			changed = true
		}
		if changed {
			scene.Revision++
			s.dirty = true
			scene.applyTokenRuntimeChange(old, exists, t, kind != "delete")
			updateCharacterReference(ss, sceneID, old, exists, t, kind != "delete")
			characterWatchChanged = old.CharacterInstanceID != "" && (kind == "delete" || old.Hidden != t.Hidden || !slices.Equal(old.OwnerIDs, t.OwnerIDs))
			if characterWatchChanged {
				ss.CharacterRevision++
			}
			if kind == "delete" && old.CharacterInstanceID != "" {
				collectedCharacter, collectedCharacterExists = gcCharacterIfUnreferenced(ss, old.CharacterInstanceID)
			}
			if c.Type == "create" || c.Type == "delete" || old.Asset != t.Asset {
				assetsBeforeReferences = make(map[string]Asset, len(ss.Assets))
				for assetID, asset := range ss.Assets {
					assetsBeforeReferences[assetID] = asset
				}
				refreshAssetOrphans(ss, time.Now())
			}
		}
	}
	if c.Seq > 0 {
		ss.Receipts[stream] = Receipt{Seq: c.Seq, Error: issue, Digest: digest, Updated: time.Now().Unix()}
		if persistence == persistenceImmediate || issue != "" || changed {
			s.dirty = true
		}
	}
	immediateSave := persistence == persistenceImmediate || (persistence == persistenceCoalesced && issue != "" && c.Seq > 0)
	if immediateSave {
		if err := s.saveLocked(); err != nil {
			if issue == "" && changed {
				if collectedCharacterExists {
					ss.CharacterInstances[collectedCharacter.ID] = collectedCharacter
				}
				if c.Type == "create" {
					delete(scene.Tokens, t.ID)
				} else if exists {
					scene.Tokens[old.ID] = old
				} else {
					delete(scene.Tokens, t.ID)
				}
			}
			if assetsBeforeReferences != nil {
				ss.Assets = assetsBeforeReferences
			}
			scene.Revision = revision
			ss.CharacterRevision = characterRevision
			scene.rebuildRuntime()
			rebuildCharacterReferences(ss)
			s.dirty = dirty
			if receipt.Seq == 0 {
				delete(ss.Receipts, stream)
			} else {
				ss.Receipts[stream] = receipt
			}
			s.send(p, map[string]any{"type": "saveError", "client": c.Client, "seq": c.Seq, "message": "Не удалось записать изменения на диск. Команда не подтверждена; будет повторена"})
			s.send(p, s.snapshotSceneForPeer(ss, p))
			return
		}
	}
	if c.Seq > 0 && pruneReceipts(ss, stream) {
		// A no-op coalesced receipt may remain intentionally volatile. Pruning it
		// must not turn a no-op transform into a disk write.
		if persistence == persistenceImmediate || issue != "" || changed {
			s.dirty = true
		}
	}
	if issue == "" && changed {
		s.publish(ss, sceneID, kind, old, exists, t)
		if characterWatchChanged {
			s.refreshCharacterWatches(ss, nil, true)
		}
	} else if issue != "" && c.Seq == 0 {
		if issue == movementBlockedIssue && exists {
			// Preview rejections are expected while a pointer presses against a
			// boundary. Return only the authoritative position: a full snapshot on
			// every pointer move is both disruptive and unnecessarily expensive.
			s.send(p, map[string]any{"type": "error", "operation": "move", "errorCode": movementBlockedErrorCode, "sceneId": sceneID, "id": old.ID, "floorId": old.FloorID, "x": old.X, "y": old.Y})
		} else if issue == previewContextChangedIssue {
			s.send(p, map[string]any{"type": "error", "operation": c.Type, "errorCode": previewContextChangedErrorCode, "message": issue, "previewRevision": p.previewRevision})
			s.send(p, s.snapshotSceneForPeer(ss, p))
		} else {
			s.send(p, map[string]string{"type": "error", "message": issue})
			s.send(p, s.snapshotSceneForPeer(ss, p))
		}
	}
	if c.Seq > 0 {
		ack(ss.Receipts[stream])
	}
}

func (s *Server) sceneCommand(ss *Session, p *peer, c Command) {
	stream := p.member.ID + ":" + c.Client
	if ss.Receipts == nil {
		ss.Receipts = map[string]Receipt{}
	}
	previousReceipt := ss.Receipts[stream]
	encoded, _ := json.Marshal(c)
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	if c.Seq > 0 {
		if c.Client == "" || len(c.Client) > 80 {
			s.send(p, map[string]string{"type": "fatal", "message": "Некорректный поток команд"})
			return
		}
		if c.Seq == previousReceipt.Seq {
			if previousReceipt.Digest != digest {
				s.send(p, map[string]string{"type": "fatal", "message": "Номер команды уже занят другой командой. Откройте стол в новой вкладке"})
				return
			}
			s.send(p, map[string]any{"type": "ack", "client": c.Client, "seq": c.Seq, "error": previousReceipt.Error, "revision": ss.CampaignRevision})
			return
		}
		if c.Seq < previousReceipt.Seq || (c.Seq != previousReceipt.Seq+1 && !receiptGapAllowed(c)) {
			s.send(p, map[string]string{"type": "fatal", "message": "Нарушена последовательность команд. Откройте новую вкладку"})
			return
		}
	}

	oldRevision, oldCharacterRevision, oldDirty := ss.CampaignRevision, ss.CharacterRevision, s.dirty
	var oldAssets map[string]Asset
	var collectedCharacters map[string]CharacterInstance
	var issue, changedSceneID, changedMemberID string
	characterAccessChanged := false
	if !memberIsGM(p.member) {
		issue = "Действие доступно ведущему"
	}
	var restore func()
	switch {
	case issue != "":
	case c.Type == "memberUpdate":
		member := ss.Members[c.MemberID]
		if member == nil || c.GM == nil {
			issue = "Участник не найден"
			break
		}
		if memberIsGM(member) && !*c.GM {
			gmCount := 0
			for _, candidate := range ss.Members {
				if memberIsGM(candidate) {
					gmCount++
				}
			}
			if gmCount <= 1 {
				issue = "В кампании должен остаться хотя бы один ведущий"
				break
			}
		}
		oldRole, oldGM := member.Role, member.GM
		member.GM = *c.GM
		if member.GM {
			member.Role = "gm"
		} else {
			member.Role = "player"
		}
		changedMemberID = member.ID
		characterAccessChanged = oldRole != member.Role || oldGM != member.GM
		restore = func() { member.Role, member.GM = oldRole, oldGM }
	case c.Type == "sceneCreate":
		name := strings.TrimSpace(c.SceneName)
		if name == "" || utf8.RuneCountInString(name) > 80 {
			issue = "Некорректное имя сцены"
			break
		}
		changedSceneID = id()
		ss.Scenes[changedSceneID] = newScene(changedSceneID, name)
		ss.Scenes[changedSceneID].rebuildRuntime()
		restore = func() { delete(ss.Scenes, changedSceneID) }
	case c.Type == "sceneUpdate":
		scene := ss.Scenes[c.SceneID]
		if scene == nil {
			issue = "Сцена не найдена"
			break
		}
		name := strings.TrimSpace(c.SceneName)
		if c.SceneName != "" && (name == "" || utf8.RuneCountInString(name) > 80) {
			issue = "Некорректное имя сцены"
			break
		}
		oldName, oldPublished := scene.Name, scene.Published
		restore = func() { scene.Name, scene.Published = oldName, oldPublished }
		if c.SceneName != "" {
			scene.Name = name
		}
		if c.Published != nil {
			scene.Published = *c.Published
			characterAccessChanged = oldPublished != scene.Published
		}
		changedSceneID = scene.ID
	case c.Type == "sceneDelete":
		scene := ss.Scenes[c.SceneID]
		if scene == nil {
			issue = "Нельзя удалить эту сцену"
			break
		}
		gcCandidates := map[string]struct{}{}
		for _, token := range scene.Tokens {
			if token.CharacterInstanceID != "" {
				gcCandidates[token.CharacterInstanceID] = struct{}{}
			}
		}
		delete(ss.Scenes, c.SceneID)
		for _, token := range scene.Tokens {
			updateCharacterReference(ss, scene.ID, token, true, Token{}, false)
		}
		collectedCharacters = map[string]CharacterInstance{}
		for characterID := range gcCandidates {
			if instance, collected := gcCharacterIfUnreferenced(ss, characterID); collected {
				collectedCharacters[characterID] = instance
			}
		}
		oldAssets = make(map[string]Asset, len(ss.Assets))
		for assetID, asset := range ss.Assets {
			oldAssets[assetID] = asset
		}
		refreshAssetOrphans(ss, time.Now())
		restore = func() {
			ss.Scenes[c.SceneID] = scene
			for characterID, instance := range collectedCharacters {
				ss.CharacterInstances[characterID] = instance
			}
			rebuildCharacterReferences(ss)
		}
		changedSceneID = c.SceneID
		characterAccessChanged = true
	}
	if issue == "" {
		ss.CampaignRevision++
		if characterAccessChanged {
			ss.CharacterRevision++
		}
		s.dirty = true
	}
	if c.Seq > 0 {
		ss.Receipts[stream] = Receipt{Seq: c.Seq, Error: issue, Digest: digest, Updated: time.Now().Unix()}
		s.dirty = true
	}
	if err := s.saveLocked(); err != nil {
		if restore != nil {
			restore()
		}
		if oldAssets != nil {
			ss.Assets = oldAssets
		}
		ss.CampaignRevision, ss.CharacterRevision, s.dirty = oldRevision, oldCharacterRevision, oldDirty
		if previousReceipt.Seq == 0 {
			delete(ss.Receipts, stream)
		} else {
			ss.Receipts[stream] = previousReceipt
		}
		s.send(p, map[string]any{"type": "saveError", "client": c.Client, "seq": c.Seq, "message": "Не удалось сохранить сцену. Команда не подтверждена; будет повторена"})
		return
	}
	if c.Seq > 0 && pruneReceipts(ss, stream) {
		s.dirty = true
	}
	if issue == "" {
		for peer := range s.peers {
			if peer.session != ss.ID || peer.sceneID != changedSceneID {
				continue
			}
			if c.Type == "sceneDelete" || (c.Type == "sceneUpdate" && !ss.Scenes[changedSceneID].Published && !memberIsGM(peer.member)) {
				peer.sceneID = ""
			}
		}
		s.publishCampaign(ss)
		if characterAccessChanged {
			s.refreshCharacterWatches(ss, nil, true)
		}
		if changedMemberID != "" {
			for peer := range s.peers {
				if peer.session != ss.ID || peer.sceneID == "" {
					continue
				}
				scene := ss.Scenes[peer.sceneID]
				if scene == nil {
					peer.sceneID = ""
					continue
				}
				if peer.member.ID == changedMemberID {
					if !memberIsGM(peer.member) && !scene.Published {
						peer.sceneID = ""
						continue
					}
					// A newly promoted GM needs the one-time catalog-bearing snapshot.
					peer.region = nil
				}
				s.send(peer, s.snapshotSceneForPeer(ss, peer))
			}
		}
	} else if c.Seq == 0 {
		s.send(p, map[string]string{"type": "error", "message": issue})
	}
	if c.Seq > 0 {
		s.send(p, map[string]any{"type": "ack", "client": c.Client, "seq": c.Seq, "error": issue, "revision": ss.CampaignRevision})
	}
}

func tokenVisibleToPeer(p *peer, token Token) bool {
	return !peerUsesPlayerProjection(p) || !token.Hidden
}

func tokenLoadedForPeer(p *peer, scene *Scene, token Token) bool {
	if !tokenVisibleToPeer(p, token) {
		return false
	}
	if _, visible := visibleFloorSet(scene, currentFloorForPeer(p, scene))[tokenFloorID(scene, token)]; !visible {
		return false
	}
	return p.region != nil && tokenIntersectsRegion(token, *p.region)
}

func (s *Server) publishTokenLocator(p *peer, sceneID, kind string, old Token, existed bool, token Token, revision uint64) {
	oldListed := existed && tokenHasLocatorForPeer(old, p)
	newListed := kind != "delete" && tokenHasLocatorForPeer(token, p)
	if !oldListed && !newListed {
		return
	}
	message := map[string]any{"sceneId": sceneID, "revision": revision, "id": token.ID}
	switch {
	case !newListed:
		message["type"] = "tokenLocatorDelete"
	case kind == "move" && oldListed:
		message["type"] = "tokenLocatorMove"
		message["x"] = token.X
		message["y"] = token.Y
	default:
		message["type"] = "tokenLocatorUpsert"
		message["locator"] = tokenLocator(token)
	}
	s.send(p, message)
}

func tokenHasLocatorForPeer(token Token, peer *peer) bool {
	if peer == nil || peer.member == nil {
		return false
	}
	if memberIsGM(peer.member) {
		return peer.playerPreview
	}
	return tokenHasLocator(token, peer.member)
}

func (s *Server) publish(ss *Session, sceneID, kind string, old Token, existed bool, t Token) {
	for p := range s.peers {
		if p.session != ss.ID || p.sceneID != sceneID {
			continue
		}
		scene := ss.Scenes[sceneID]
		if scene == nil {
			continue
		}
		s.publishTokenLocator(p, sceneID, kind, old, existed, t, scene.Revision)

		previousFloor, previousActive := p.floorID, p.activeTokenID
		p.floorID = currentFloorForPeer(p, scene)
		if peerUsesPlayerProjection(p) && previousFloor != "" && previousFloor != p.floorID {
			s.send(p, s.snapshotSceneForPeer(ss, p))
			continue
		}
		if peerUsesPlayerProjection(p) && previousActive != p.activeTokenID {
			s.send(p, s.snapshotSceneForPeer(ss, p))
		}
		oldLoaded := existed && tokenLoadedForPeer(p, scene, old)
		newLoaded := kind != "delete" && tokenLoadedForPeer(p, scene, t)
		if !oldLoaded && !newLoaded {
			continue
		}

		v := map[string]any{"type": kind, "sceneId": sceneID, "revision": scene.Revision, "id": t.ID}
		switch {
		case oldLoaded && !newLoaded:
			v["type"] = "delete"
		case newLoaded:
			// Entering a loaded region or changing visibility needs a full token.
			// Compact move is safe only while both old/new state are already loaded.
			if kind == "move" && oldLoaded {
				v["x"] = t.X
				v["y"] = t.Y
			} else {
				v["type"] = "upsert"
				v["token"] = t
				if t.Asset != "" {
					if asset, ok := ss.Assets[t.Asset]; ok {
						v["asset"] = publicAsset(asset)
					}
				}
			}
		}
		p.delivery++
		v["delivery"] = p.delivery
		s.send(p, v)
	}
}

// Reject all further mutations before taking the final snapshot, including WS
// connections (http.Server.Shutdown does not own hijacked connections).

func (s *Server) flushPersistence() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.saveLocked()
	if err != nil {
		if !s.storageDegraded {
			s.storageDegraded = true
			for p := range s.peers {
				s.send(p, map[string]string{"type": "storageError", "message": storageDegradedMessage})
			}
		}
		return err
	}
	if s.storageDegraded {
		s.storageDegraded = false
		for p := range s.peers {
			s.send(p, map[string]string{"type": "storageRecovered"})
		}
	}
	return nil
}

func (s *Server) stop() error {
	s.mu.Lock()
	s.stopping = true
	s.jobCancel()
	s.mu.Unlock()
	s.jobWG.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.saveLocked()
	for p := range s.peers {
		p.conn.Close()
	}
	return err
}
func (s *Server) serve(ctx context.Context, listener net.Listener) error {
	h := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() { done <- h.Serve(listener) }()
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			saveErr := s.stop()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			shutdownErr := h.Shutdown(shutdownCtx)
			cancel()
			if shutdownErr != nil {
				h.Close()
			}
			return errors.Join(saveErr, shutdownErr)
		case err := <-done:
			return errors.Join(err, s.stop())
		case <-ticker.C:
			_ = s.flushPersistence()
		}
	}
}
