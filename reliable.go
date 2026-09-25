package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// One outstanding reliable command per browser stream. Business receipts are
// persisted before ACK; coalesced transform receipts persist on the next flush.
type Receipt struct {
	Seq     uint64 `json:"seq"`
	Error   string `json:"error,omitempty"`
	Digest  string `json:"digest"`
	Updated int64  `json:"updated,omitempty"`
}

const maxReceiptsPerSession = 2048

const persistenceFlushInterval = 5 * time.Second
const storageDegradedMessage = "Сервер временно не может сохранять изменения на диск. Работа продолжается, но при аварийном завершении процесса несохранённые изменения могут быть потеряны"

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
	Name    *string  `json:"name,omitempty"`
	Size    *float64 `json:"size,omitempty"`
	Color   *string  `json:"color,omitempty"`
	Owner   *string  `json:"owner,omitempty"`
	Hidden  *bool    `json:"hidden,omitempty"`
	Asset   *string  `json:"asset,omitempty"`
	FloorID *string  `json:"floorId,omitempty"`
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
	Name           *string         `json:"name,omitempty"`
	Order          *int            `json:"order,omitempty"`
	Visible        *bool           `json:"visible,omitempty"`
	Locked         *bool           `json:"locked,omitempty"`
	Opacity        *float64        `json:"opacity,omitempty"`
	WalkableBounds *WalkableBounds `json:"walkableBounds,omitempty"`
}

type FloorProperties struct {
	Name                       *string  `json:"name,omitempty"`
	Order                      *int     `json:"order,omitempty"`
	Opacity                    *float64 `json:"opacity,omitempty"`
	OpacityWhenViewedFromBelow *float64 `json:"opacityWhenViewedFromBelow,omitempty"`
}
type Command struct {
	Type              string            `json:"type"`
	Token             Token             `json:"token"`
	Properties        Properties        `json:"properties"`
	Client            string            `json:"client"`
	Seq               uint64            `json:"seq"`
	After             uint64            `json:"after"`
	SceneID           string            `json:"sceneId,omitempty"`
	SceneName         string            `json:"sceneName,omitempty"`
	Published         *bool             `json:"published,omitempty"`
	Region            *SceneRegion      `json:"region,omitempty"`
	ViewFloorID       string            `json:"viewFloorId,omitempty"`
	ActiveTokenID     string            `json:"activeTokenId,omitempty"`
	Focus             bool              `json:"focus,omitempty"`
	Bounds            *SceneBounds      `json:"bounds,omitempty"`
	Floor             Floor             `json:"floor,omitempty"`
	FloorProperties   FloorProperties   `json:"floorProperties,omitempty"`
	Layer             Layer             `json:"layer,omitempty"`
	LayerProperties   LayerProperties   `json:"layerProperties,omitempty"`
	Element           SceneElement      `json:"element,omitempty"`
	ElementProperties ElementProperties `json:"elementProperties,omitempty"`
	Transition        Transition        `json:"transition,omitempty"`
	AssetID           string            `json:"assetId,omitempty"`
	RetentionPolicy   string            `json:"retentionPolicy,omitempty"`
}

func (s *Server) command(ss *Session, p *peer, c Command) {
	if c.Type == "elementFixRotation" {
		s.rotationCommand(ss, p, c)
		return
	}
	if c.Type == "sceneCreate" || c.Type == "sceneUpdate" || c.Type == "sceneDelete" {
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
		s.send(p, map[string]any{"type": "ack", "client": c.Client, "seq": c.Seq, "error": r.Error, "revision": revision, "sceneId": sceneID})
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
	} else if p.member.Role != "gm" && !scene.Published {
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
	t := old
	gm := p.member.Role == "gm"
	kind := "upsert"
	var issue string
	switch c.Type {
	case "move", "final":
		if c.Type == "move" {
			kind = "move"
		}
		if !exists || (!gm && (old.Owner != p.member.ID || old.Hidden)) {
			issue = "Нет права перемещать этот токен"
			break
		}
		if !gm && old.FloorID != currentFloorForPeer(p, scene) {
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
		if !gm && !positionInWalkable(scene, old.FloorID, c.Token.X, c.Token.Y) {
			issue = "Токен нельзя переместить за границы игровой области"
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
			if v.Owner != nil {
				t.Owner = *v.Owner
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
		if !validNumber(t.X) || !validNumber(t.Y) || !validNumber(t.Size) || !validNumber(t.Rotation) || t.Size < 16 || t.Size > 1024 || utf8.RuneCountInString(t.Name) > 80 || len(t.Color) > 32 || scene.Floors[t.FloorID].ID == "" || tokenLayer.Kind != layerKindTokens || tokenLayer.FloorID != t.FloorID {
			issue = "Некорректные свойства токена"
			break
		}
		if t.Owner != "" && ss.Members[t.Owner] == nil {
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
	revision, dirty := scene.Revision, s.dirty
	changed := false
	var assetsBeforeReferences map[string]Asset
	if issue == "" {
		if kind == "delete" {
			delete(scene.Tokens, t.ID)
			changed = true
		} else if !exists || t != old {
			scene.Tokens[t.ID] = t
			changed = true
		}
		if changed {
			scene.Revision++
			s.dirty = true
			scene.applyTokenRuntimeChange(old, exists, t, kind != "delete")
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
			scene.rebuildRuntime()
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
	} else if issue != "" && c.Seq == 0 {
		s.send(p, map[string]string{"type": "error", "message": issue})
		s.send(p, s.snapshotSceneForPeer(ss, p))
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

	oldRevision, oldDirty := ss.CampaignRevision, s.dirty
	var oldAssets map[string]Asset
	var issue, changedSceneID string
	if p.member.Role != "gm" {
		issue = "Действие доступно ведущему"
	}
	var restore func()
	switch {
	case issue != "":
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
		}
		changedSceneID = scene.ID
	case c.Type == "sceneDelete":
		scene := ss.Scenes[c.SceneID]
		if scene == nil {
			issue = "Нельзя удалить эту сцену"
			break
		}
		delete(ss.Scenes, c.SceneID)
		oldAssets = make(map[string]Asset, len(ss.Assets))
		for assetID, asset := range ss.Assets {
			oldAssets[assetID] = asset
		}
		refreshAssetOrphans(ss, time.Now())
		restore = func() { ss.Scenes[c.SceneID] = scene }
		changedSceneID = c.SceneID
	}
	if issue == "" {
		ss.CampaignRevision++
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
		ss.CampaignRevision, s.dirty = oldRevision, oldDirty
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
			if c.Type == "sceneDelete" || (c.Type == "sceneUpdate" && !ss.Scenes[changedSceneID].Published && peer.member.Role != "gm") {
				peer.sceneID = ""
			}
		}
		s.publishCampaign(ss)
	} else if c.Seq == 0 {
		s.send(p, map[string]string{"type": "error", "message": issue})
	}
	if c.Seq > 0 {
		s.send(p, map[string]any{"type": "ack", "client": c.Client, "seq": c.Seq, "error": issue, "revision": ss.CampaignRevision})
	}
}

func tokenVisibleToPeer(p *peer, token Token) bool {
	return p.member.Role == "gm" || !token.Hidden
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

func (s *Server) publish(ss *Session, sceneID, kind string, old Token, existed bool, t Token) {
	for p := range s.peers {
		if p.session != ss.ID || p.sceneID != sceneID {
			continue
		}
		scene := ss.Scenes[sceneID]
		if scene == nil {
			continue
		}

		previousFloor, previousActive := p.floorID, p.activeTokenID
		p.floorID = currentFloorForPeer(p, scene)
		if p.member.Role != "gm" && previousFloor != "" && previousFloor != p.floorID {
			s.send(p, s.snapshotSceneForPeer(ss, p))
			continue
		}
		if p.member.Role != "gm" && previousActive != p.activeTokenID {
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
