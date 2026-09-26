package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

func sceneContentCommand(commandType string) bool {
	switch commandType {
	case "boundsUpdate",
		"floorCreate", "floorUpdate", "floorDelete",
		"layerCreate", "layerUpdate", "layerDelete",
		"elementCreate", "elementUpdate", "elementDelete", "elementPreview", "elementTransform", "elementFixRotation",
		"transitionCreate", "transitionUpdate", "transitionDelete",
		"assetRetention":
		return true
	}
	return false
}

func (s *Server) contentCommand(session *Session, peer *peer, command Command) {
	sceneID := command.SceneID
	if sceneID == "" {
		sceneID = peer.sceneID
	}
	persistence := commandPersistence(command.Type)
	reliable := persistence != persistenceRealtime
	stream := peer.member.ID + ":" + command.Client
	if session.Receipts == nil {
		session.Receipts = map[string]Receipt{}
	}
	previousReceipt := session.Receipts[stream]
	digest := ""
	if command.Seq > 0 {
		encoded, _ := json.Marshal(command)
		sum := sha256.Sum256(encoded)
		digest = hex.EncodeToString(sum[:])
		if !reliable || command.Client == "" || len(command.Client) > 80 {
			s.send(peer, map[string]string{"type": "fatal", "message": "Некорректный поток команд"})
			return
		}
		if command.Seq == previousReceipt.Seq {
			if previousReceipt.Digest != digest {
				s.send(peer, map[string]string{"type": "fatal", "message": "Номер команды уже занят другой командой. Откройте стол в новой вкладке"})
				return
			}
			s.send(peer, map[string]any{"type": "ack", "client": command.Client, "seq": command.Seq, "error": previousReceipt.Error, "revision": sceneRevision(session, sceneID), "sceneId": sceneID})
			return
		}
		if command.Seq < previousReceipt.Seq || (command.Seq != previousReceipt.Seq+1 && !receiptGapAllowed(command)) {
			s.send(peer, map[string]string{"type": "fatal", "message": "Нарушена последовательность команд. Откройте новую вкладку"})
			return
		}
	}

	scene := session.Scenes[sceneID]
	issue := ""
	if scene == nil {
		issue = "Сцена не найдена"
	} else if peer.member.Role != "gm" && !scene.Published {
		issue = "Сцена скрыта"
	}
	oldRevision, oldDirty := uint64(0), s.dirty
	if scene != nil {
		oldRevision = scene.Revision
	}
	changed := false
	structural := false
	assetRefsChanged := false
	var restore func()
	var oldElement, nextElement SceneElement
	var elementExisted, elementExists bool
	var oldToken, nextToken Token
	var tokenExisted, tokenExists bool
	var assetsBeforeMutation map[string]Asset

	if issue == "" && peer.member.Role != "gm" {
		issue = "Действие доступно ведущему"
	}
	if issue == "" {
		switch command.Type {
		case "elementFixRotation":
			issue = "Фиксация ротации отключена"
		case "boundsUpdate":
			if command.Bounds == nil || !validSceneBounds(*command.Bounds) {
				issue = "Некорректные границы сцены"
				break
			}
			old := scene.Bounds
			if old != *command.Bounds {
				scene.Bounds = *command.Bounds
				changed, structural = true, true
				restore = func() { scene.Bounds = old }
			}
		case "floorCreate":
			if len(scene.Floors) >= maxSceneFloors {
				issue = "Сейчас сцена поддерживает не больше двух этажей"
				break
			}
			name := strings.TrimSpace(command.Floor.Name)
			if name == "" || utf8.RuneCountInString(name) > 80 {
				issue = "Некорректное имя этажа"
				break
			}
			floor := Floor{ID: id(), Name: name, Order: command.Floor.Order, Opacity: 1}
			scene.Floors[floor.ID] = floor
			addFloorLayers(scene, floor.ID)
			changed, structural = true, true
			restore = func() {
				delete(scene.Floors, floor.ID)
				for layerID, layer := range scene.Layers {
					if layer.FloorID == floor.ID {
						delete(scene.Layers, layerID)
					}
				}
			}
		case "floorUpdate":
			floor, ok := scene.Floors[command.Floor.ID]
			if !ok {
				issue = "Этаж не найден"
				break
			}
			old := floor
			props := command.FloorProperties
			if props.Name != nil {
				name := strings.TrimSpace(*props.Name)
				if name == "" || utf8.RuneCountInString(name) > 80 {
					issue = "Некорректное имя этажа"
					break
				}
				floor.Name = name
			}
			if props.Order != nil {
				floor.Order = *props.Order
			}
			if props.Opacity != nil {
				if !validNumber(*props.Opacity) || *props.Opacity < 0 || *props.Opacity > 1 {
					issue = "Некорректная непрозрачность этажа"
					break
				}
				floor.Opacity = *props.Opacity
			}
			if props.OpacityWhenViewedFromBelow != nil {
				if !validNumber(*props.OpacityWhenViewedFromBelow) || *props.OpacityWhenViewedFromBelow < 0 || *props.OpacityWhenViewedFromBelow > 1 {
					issue = "Некорректная видимость этажа снизу"
					break
				}
				floor.OpacityWhenViewedFromBelow = *props.OpacityWhenViewedFromBelow
			}
			if floor != old {
				scene.Floors[floor.ID] = floor
				changed, structural = true, true
				restore = func() { scene.Floors[old.ID] = old }
			}
		case "floorDelete":
			floor, ok := scene.Floors[command.Floor.ID]
			if !ok || len(scene.Floors) <= 1 {
				issue = "Нельзя удалить этот этаж"
				break
			}
			for _, token := range scene.Tokens {
				if token.FloorID == floor.ID {
					issue = "Сначала перенесите токены с этажа"
					break
				}
			}
			for _, element := range scene.Elements {
				if element.FloorID == floor.ID {
					issue = "Сначала удалите или перенесите элементы этажа"
					break
				}
			}
			for _, transition := range scene.Transitions {
				if transition.EndpointA.FloorID == floor.ID || transition.EndpointB.FloorID == floor.ID {
					issue = "Сначала удалите переходы этажа"
					break
				}
			}
			if issue != "" {
				break
			}
			removedLayers := map[string]Layer{}
			for layerID, layer := range scene.Layers {
				if layer.FloorID == floor.ID {
					removedLayers[layerID] = layer
					delete(scene.Layers, layerID)
				}
			}
			delete(scene.Floors, floor.ID)
			changed, structural = true, true
			restore = func() {
				scene.Floors[floor.ID] = floor
				for layerID, layer := range removedLayers {
					scene.Layers[layerID] = layer
				}
			}
		case "layerCreate":
			name := strings.TrimSpace(command.Layer.Name)
			if scene.Floors[command.Layer.FloorID].ID == "" || name == "" || utf8.RuneCountInString(name) > 80 {
				issue = "Некорректный слой"
				break
			}
			layer := Layer{ID: id(), FloorID: command.Layer.FloorID, Name: name, Kind: layerKindVisual, Order: command.Layer.Order, Visible: true, Opacity: 1}
			scene.Layers[layer.ID] = layer
			changed, structural = true, true
			restore = func() { delete(scene.Layers, layer.ID) }
		case "layerUpdate":
			layer, ok := scene.Layers[command.Layer.ID]
			if !ok {
				issue = "Слой не найден"
				break
			}
			old := layer
			props := command.LayerProperties
			if props.Name != nil {
				name := strings.TrimSpace(*props.Name)
				if name == "" || utf8.RuneCountInString(name) > 80 {
					issue = "Некорректное имя слоя"
					break
				}
				layer.Name = name
			}
			if props.Order != nil {
				layer.Order = *props.Order
			}
			if props.Visible != nil && layer.Kind == layerKindVisual {
				layer.Visible = *props.Visible
			}
			if props.Locked != nil && layer.Kind == layerKindVisual {
				layer.Locked = *props.Locked
			}
			if props.Opacity != nil {
				if layer.Kind != layerKindVisual {
					issue = "Непрозрачность доступна только визуальному слою"
					break
				}
				if !validNumber(*props.Opacity) || *props.Opacity < 0 || *props.Opacity > 1 {
					issue = "Некорректная прозрачность"
					break
				}
				layer.Opacity = *props.Opacity
			}
			if props.WalkableBounds != nil {
				if layer.Kind != layerKindWalkable || !validWalkableBounds(*props.WalkableBounds) {
					issue = "Некорректная игровая область"
					break
				}
				bounds := *props.WalkableBounds
				layer.WalkableBounds = &bounds
			}
			if layer != old {
				scene.Layers[layer.ID] = layer
				changed, structural = true, true
				restore = func() { scene.Layers[old.ID] = old }
			}
		case "layerDelete":
			layer, ok := scene.Layers[command.Layer.ID]
			if !ok || layer.Kind != layerKindVisual {
				issue = "Нельзя удалить этот слой"
				break
			}
			for _, element := range scene.Elements {
				if element.LayerID == layer.ID {
					issue = "Сначала удалите или перенесите элементы слоя"
					break
				}
			}
			if issue != "" {
				break
			}
			delete(scene.Layers, layer.ID)
			changed, structural = true, true
			restore = func() { scene.Layers[layer.ID] = layer }
		case "elementCreate":
			if len(scene.Elements) >= maxSceneElements {
				issue = "Лимит элементов сцены достигнут"
				break
			}
			asset, ok := session.Assets[command.Element.AssetID]
			if !ok {
				issue = "Изображение не найдено"
				break
			}
			element := command.Element
			element.ID = id()
			if element.FloorID == "" {
				element.FloorID = firstFloorID(scene)
			}
			if element.LayerID == "" {
				element.LayerID = firstLayerID(scene, element.FloorID)
			}
			layer := scene.Layers[element.LayerID]
			if element.FloorID != layer.FloorID || layer.Kind != layerKindVisual {
				issue = "Элемент можно поместить только в визуальный слой своего этажа"
				break
			}
			if element.Transform.Width <= 0 {
				element.Transform.Width = float64(asset.Width)
			}
			if element.Transform.Height <= 0 {
				element.Transform.Height = float64(asset.Height)
			}
			if !validTransform(element.Transform) || element.Opacity < 0 || element.Opacity > 1 {
				issue = "Некорректный элемент"
				break
			}
			if element.Name == "" {
				element.Name = strings.TrimSuffix(asset.Filename, ".png")
			}
			scene.Elements[element.ID] = element
			oldElement, nextElement, elementExisted, elementExists = SceneElement{}, element, false, true
			changed, assetRefsChanged = true, true
			restore = func() { delete(scene.Elements, element.ID) }
		case "elementUpdate":
			element, ok := scene.Elements[command.Element.ID]
			if !ok {
				issue = "Элемент не найден"
				break
			}
			old := element
			props := command.ElementProperties
			if props.Name != nil {
				name := strings.TrimSpace(*props.Name)
				if utf8.RuneCountInString(name) > 120 {
					issue = "Слишком длинное имя элемента"
					break
				}
				element.Name = name
			}
			if props.FloorID != nil {
				element.FloorID = *props.FloorID
			}
			if props.LayerID != nil {
				element.LayerID = *props.LayerID
			}
			layer := scene.Layers[element.LayerID]
			if layer.ID == "" || layer.Kind != layerKindVisual || layer.FloorID != element.FloorID {
				issue = "Элемент можно поместить только в визуальный слой своего этажа"
				break
			}
			if props.ZOrder != nil {
				element.ZOrder = *props.ZOrder
			}
			if props.Visible != nil {
				element.Visible = *props.Visible
			}
			if props.Locked != nil {
				element.Locked = *props.Locked
			}
			if props.Opacity != nil {
				if !validNumber(*props.Opacity) || *props.Opacity < 0 || *props.Opacity > 1 {
					issue = "Некорректная прозрачность"
					break
				}
				element.Opacity = *props.Opacity
			}
			if element != old {
				scene.Elements[element.ID] = element
				oldElement, nextElement, elementExisted, elementExists = old, element, true, true
				changed = true
				restore = func() { scene.Elements[old.ID] = old }
			}
		case "elementPreview", "elementTransform":
			element, ok := scene.Elements[command.Element.ID]
			if !ok {
				issue = "Элемент не найден"
				break
			}
			if !validTransform(command.Element.Transform) {
				issue = "Некорректный transform"
				break
			}
			old := element
			element.Transform = command.Element.Transform
			if element != old {
				scene.Elements[element.ID] = element
				oldElement, nextElement, elementExisted, elementExists = old, element, true, true
				changed = true
				restore = func() { scene.Elements[old.ID] = old }
			}
		case "elementDelete":
			element, ok := scene.Elements[command.Element.ID]
			if !ok {
				issue = "Элемент не найден"
				break
			}
			delete(scene.Elements, element.ID)
			oldElement, elementExisted = element, true
			changed, assetRefsChanged = true, true
			restore = func() { scene.Elements[element.ID] = element }
		case "transitionCreate":
			transition := command.Transition
			transition.ID = id()
			if transition.Direction == "" {
				transition.Direction = "bidirectional"
			}
			if strings.TrimSpace(transition.Name) == "" {
				transition.Name = "Переход"
			}
			if !validTransition(scene, transition) {
				issue = "Некорректный переход"
				break
			}
			scene.Transitions[transition.ID] = transition
			changed, structural = true, true
			restore = func() { delete(scene.Transitions, transition.ID) }
		case "transitionUpdate":
			old, ok := scene.Transitions[command.Transition.ID]
			if !ok {
				issue = "Переход не найден"
				break
			}
			transition := command.Transition
			if strings.TrimSpace(transition.Name) == "" {
				transition.Name = old.Name
			}
			if transition.Direction == "" {
				transition.Direction = old.Direction
			}
			if !validTransition(scene, transition) {
				issue = "Некорректный переход"
				break
			}
			if transition != old {
				scene.Transitions[transition.ID] = transition
				changed, structural = true, true
				restore = func() { scene.Transitions[old.ID] = old }
			}
		case "transitionDelete":
			transition, ok := scene.Transitions[command.Transition.ID]
			if !ok {
				issue = "Переход не найден"
				break
			}
			delete(scene.Transitions, transition.ID)
			changed, structural = true, true
			restore = func() { scene.Transitions[transition.ID] = transition }
		case "assetRetention":
			asset, ok := session.Assets[command.AssetID]
			if !ok || (command.RetentionPolicy != assetKeep && command.RetentionPolicy != assetReclaimable) {
				issue = "Некорректная политика ассета"
				break
			}
			old := asset
			assetsBeforeMutation = make(map[string]Asset, len(session.Assets))
			for assetID, current := range session.Assets {
				assetsBeforeMutation[assetID] = current
			}
			asset.RetentionPolicy = command.RetentionPolicy
			session.Assets[asset.ID] = asset
			if asset != old {
				changed, assetRefsChanged = true, true
				restore = func() { session.Assets[old.ID] = old }
			}
		}
	}

	oldAssets := map[string]Asset(nil)
	if issue == "" && changed {
		if assetRefsChanged {
			oldAssets = assetsBeforeMutation
			if oldAssets == nil {
				oldAssets = make(map[string]Asset, len(session.Assets))
				for assetID, asset := range session.Assets {
					oldAssets[assetID] = asset
				}
			}
			refreshAssetOrphans(session, time.Now())
		}
		scene.Revision++
		s.dirty = true
		if elementExisted || elementExists {
			scene.applyElementRuntimeChange(oldElement, elementExisted, nextElement, elementExists)
		} else if tokenExisted || tokenExists {
			scene.applyTokenRuntimeChange(oldToken, tokenExisted, nextToken, tokenExists)
		} else if structural {
			scene.rebuildRuntime()
		}
	}
	if command.Seq > 0 {
		session.Receipts[stream] = Receipt{Seq: command.Seq, Error: issue, Digest: digest, Updated: time.Now().Unix()}
		if persistence == persistenceImmediate || issue != "" || changed {
			s.dirty = true
		}
	}
	immediateSave := persistence == persistenceImmediate || (persistence == persistenceCoalesced && issue != "" && command.Seq > 0)
	if immediateSave {
		if err := s.saveLocked(); err != nil {
			if restore != nil {
				restore()
			}
			if oldAssets != nil {
				session.Assets = oldAssets
			}
			if scene != nil {
				scene.Revision = oldRevision
				scene.rebuildRuntime()
			}
			s.dirty = oldDirty
			if previousReceipt.Seq == 0 {
				delete(session.Receipts, stream)
			} else {
				session.Receipts[stream] = previousReceipt
			}
			s.send(peer, map[string]any{"type": "saveError", "client": command.Client, "seq": command.Seq, "message": "Не удалось сохранить изменение сцены. Команда не подтверждена; будет повторена"})
			if scene != nil {
				s.send(peer, s.snapshotSceneForPeer(session, peer))
			}
			return
		}
	}
	if command.Seq > 0 && pruneReceipts(session, stream) && (persistence == persistenceImmediate || issue != "" || changed) {
		s.dirty = true
	}
	if issue == "" && changed {
		switch {
		case elementExisted || elementExists:
			s.publishElement(session, sceneID, oldElement, elementExisted, nextElement, elementExists, command.Type == "elementPreview")
		case tokenExisted || tokenExists:
			s.publish(session, sceneID, "upsert", oldToken, tokenExisted, nextToken)
		default:
			s.publishSceneSnapshot(session, sceneID)
		}
	} else if issue != "" && command.Seq == 0 {
		s.send(peer, map[string]string{"type": "error", "message": issue})
		if scene != nil {
			s.send(peer, s.snapshotSceneForPeer(session, peer))
		}
	}
	if command.Seq > 0 {
		s.send(peer, map[string]any{"type": "ack", "client": command.Client, "seq": command.Seq, "error": issue, "revision": sceneRevision(session, sceneID), "sceneId": sceneID})
	}
}

func sceneRevision(session *Session, sceneID string) uint64 {
	if scene := session.Scenes[sceneID]; scene != nil {
		return scene.Revision
	}
	return 0
}

func elementVisibleToPeer(peer *peer, scene *Scene, element SceneElement) bool {
	if _, visible := visibleFloorSet(scene, currentFloorForPeer(peer, scene))[element.FloorID]; !visible {
		return false
	}
	return peer.member.Role == "gm" || scene.ensureRuntime().elementPublic(element)
}

func elementLoadedForPeer(peer *peer, scene *Scene, element SceneElement) bool {
	return peer.region != nil && elementVisibleToPeer(peer, scene, element) && elementIntersectsRegion(element, *peer.region)
}

func (s *Server) publishElement(session *Session, sceneID string, old SceneElement, existed bool, next SceneElement, nextExists bool, compact bool) {
	for peer := range s.peers {
		if peer.session != session.ID || peer.sceneID != sceneID {
			continue
		}
		scene := session.Scenes[sceneID]
		if scene == nil {
			continue
		}
		oldLoaded := existed && elementLoadedForPeer(peer, scene, old)
		newLoaded := nextExists && elementLoadedForPeer(peer, scene, next)
		if !oldLoaded && !newLoaded {
			continue
		}
		message := map[string]any{"type": "elementUpsert", "sceneId": sceneID, "revision": scene.Revision, "id": next.ID}
		switch {
		case oldLoaded && !newLoaded:
			message["type"] = "elementDelete"
			message["id"] = old.ID
		case compact && oldLoaded && newLoaded:
			message["type"] = "elementTransform"
			message["transform"] = next.Transform
		case newLoaded:
			message["element"] = next
			if asset, ok := session.Assets[next.AssetID]; ok {
				message["asset"] = publicAsset(asset)
			}
		}
		peer.delivery++
		message["delivery"] = peer.delivery
		s.send(peer, message)
	}
}
