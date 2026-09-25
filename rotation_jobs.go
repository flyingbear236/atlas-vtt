package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type rotationJob struct {
	key        string
	sessionID  string
	sceneID    string
	elementID  string
	memberID   string
	client     string
	seq        uint64
	digest     string
	recipe     rotationRecipe
	recipeHash string
	source     Asset
	waiters    map[*peer]bool
}

func rotationJobKey(sessionID, memberID, client string, seq uint64) string {
	return fmt.Sprintf("%s:%s:%s:%d", sessionID, memberID, client, seq)
}

func rotationCommandDigest(command Command) string {
	encoded, _ := json.Marshal(command)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (s *Server) sendRotationJob(job *rotationJob, status, message string) {
	for waiter := range job.waiters {
		s.send(waiter, map[string]any{"type": "job", "operation": "elementFixRotation", "status": status, "sceneId": job.sceneID, "elementId": job.elementID, "client": job.client, "seq": job.seq, "message": message})
	}
}

func (s *Server) sendRotationAck(job *rotationJob, session *Session, issue string) {
	for waiter := range job.waiters {
		s.send(waiter, map[string]any{"type": "ack", "client": job.client, "seq": job.seq, "error": issue, "revision": sceneRevision(session, job.sceneID), "sceneId": job.sceneID})
	}
}

func (s *Server) persistRotationReceipt(session *Session, job *rotationJob, unused *Asset, issue string) bool {
	stream := job.memberID + ":" + job.client
	previous, previousExists := session.Receipts[stream]
	oldDirty := s.dirty
	var oldAssets map[string]Asset
	if unused != nil && unused.ID != "" {
		oldAssets = make(map[string]Asset, len(session.Assets))
		for assetID, existing := range session.Assets {
			oldAssets[assetID] = existing
		}
		session.Assets[unused.ID] = *unused
		refreshAssetOrphans(session, time.Now())
	}
	session.Receipts[stream] = Receipt{Seq: job.seq, Error: issue, Digest: job.digest, Updated: time.Now().Unix()}
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		if oldAssets != nil {
			session.Assets = oldAssets
		}
		if previousExists {
			session.Receipts[stream] = previous
		} else {
			delete(session.Receipts, stream)
		}
		s.dirty = oldDirty
		for waiter := range job.waiters {
			s.send(waiter, map[string]any{"type": "saveError", "client": job.client, "seq": job.seq, "message": "Не удалось сохранить результат обработки. Команда не подтверждена; будет повторена"})
		}
		return false
	}
	if pruneReceipts(session, stream) {
		s.dirty = true
	}
	status := "error"
	if issue == "" {
		status = "completed"
	}
	s.sendRotationJob(job, status, issue)
	s.sendRotationAck(job, session, issue)
	return true
}

func (s *Server) rejectRotationCommand(session *Session, p *peer, command Command, digest, issue string) {
	job := &rotationJob{sessionID: session.ID, sceneID: command.SceneID, elementID: command.Element.ID, memberID: p.member.ID, client: command.Client, seq: command.Seq, digest: digest, waiters: map[*peer]bool{p: true}}
	s.persistRotationReceipt(session, job, nil, issue)
}

func (s *Server) rotationCommand(session *Session, p *peer, command Command) {
	sceneID := command.SceneID
	if sceneID == "" {
		sceneID = p.sceneID
	}
	command.SceneID = sceneID
	stream := p.member.ID + ":" + command.Client
	if session.Receipts == nil {
		session.Receipts = map[string]Receipt{}
	}
	previous := session.Receipts[stream]
	if command.Seq == 0 || command.Client == "" || len(command.Client) > 80 {
		s.send(p, map[string]string{"type": "fatal", "message": "Некорректный поток команд"})
		return
	}
	digest := rotationCommandDigest(command)
	if command.Seq == previous.Seq {
		if previous.Digest != digest {
			s.send(p, map[string]string{"type": "fatal", "message": "Номер команды уже занят другой командой. Откройте стол в новой вкладке"})
			return
		}
		s.send(p, map[string]any{"type": "ack", "client": command.Client, "seq": command.Seq, "error": previous.Error, "revision": sceneRevision(session, sceneID), "sceneId": sceneID})
		return
	}
	if command.Seq < previous.Seq || command.Seq != previous.Seq+1 {
		s.send(p, map[string]string{"type": "fatal", "message": "Нарушена последовательность команд. Откройте новую вкладку"})
		return
	}
	key := rotationJobKey(session.ID, p.member.ID, command.Client, command.Seq)
	if pending := s.rotationJobs[key]; pending != nil {
		if pending.digest != digest {
			s.send(p, map[string]string{"type": "fatal", "message": "Номер команды уже занят другой командой. Откройте стол в новой вкладке"})
			return
		}
		pending.waiters[p] = true
		s.send(p, map[string]any{"type": "job", "operation": "elementFixRotation", "status": "processing", "sceneId": sceneID, "elementId": pending.elementID, "client": command.Client, "seq": command.Seq, "message": "Фиксация ротации выполняется"})
		return
	}
	if p.member.Role != "gm" {
		s.rejectRotationCommand(session, p, command, digest, "Действие доступно ведущему")
		return
	}
	scene := session.Scenes[sceneID]
	if scene == nil {
		s.rejectRotationCommand(session, p, command, digest, "Сцена не найдена")
		return
	}
	element, ok := scene.Elements[command.Element.ID]
	if !ok {
		s.rejectRotationCommand(session, p, command, digest, "Элемент не найден")
		return
	}
	if normalizedRotation(element.Transform.Rotation) == 0 {
		s.rejectRotationCommand(session, p, command, digest, "")
		return
	}
	source, ok := session.Assets[element.AssetID]
	if !ok || !isSceneRasterKind(source.Kind) {
		s.rejectRotationCommand(session, p, command, digest, "Исходное изображение не найдено")
		return
	}
	plan, err := planFixedRotation(source, element.Transform)
	if err != nil {
		s.rejectRotationCommand(session, p, command, digest, err.Error())
		return
	}
	recipe, recipeHash := fixedRotationRecipe(source, element.Transform, plan)
	select {
	case s.imageJobs <- struct{}{}:
	default:
		s.rejectRotationCommand(session, p, command, digest, "Сервер уже обрабатывает другое изображение")
		return
	}
	job := &rotationJob{key: key, sessionID: session.ID, sceneID: sceneID, elementID: element.ID, memberID: p.member.ID, client: command.Client, seq: command.Seq, digest: digest, recipe: recipe, recipeHash: recipeHash, source: source, waiters: map[*peer]bool{p: true}}
	s.rotationJobs[key] = job
	s.sendRotationJob(job, "processing", "Фиксация ротации выполняется")
	s.jobWG.Add(1)
	go s.runRotationJob(job)
}

func (s *Server) runRotationJob(job *rotationJob) {
	defer s.jobWG.Done()
	defer func() { <-s.imageJobs }()
	asset, processErr := fixedRotationPreparer(s.jobContext, s.root, job.source, job.recipe, job.recipeHash)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rotationJobs, job.key)
	if s.stopping {
		return
	}
	session := s.sessions[job.sessionID]
	if session == nil {
		return
	}
	if processErr != nil {
		s.persistRotationReceipt(session, job, nil, processErr.Error())
		return
	}
	member := session.Members[job.memberID]
	if member == nil || member.Role != "gm" {
		s.persistRotationReceipt(session, job, &asset, "Права ведущего изменились во время обработки")
		return
	}
	scene := session.Scenes[job.sceneID]
	if scene == nil {
		s.persistRotationReceipt(session, job, &asset, "Сцена удалена во время обработки")
		return
	}
	current, ok := scene.Elements[job.elementID]
	if !ok {
		s.persistRotationReceipt(session, job, &asset, "Элемент удалён во время обработки")
		return
	}
	currentSource, ok := session.Assets[current.AssetID]
	if !ok {
		s.persistRotationReceipt(session, job, &asset, "Исходное изображение изменилось")
		return
	}
	currentPlan, err := planFixedRotation(currentSource, current.Transform)
	if err != nil {
		s.persistRotationReceipt(session, job, &asset, "Transform элемента изменился во время обработки")
		return
	}
	_, currentHash := fixedRotationRecipe(currentSource, current.Transform, currentPlan)
	if currentHash != job.recipeHash {
		s.persistRotationReceipt(session, job, &asset, "Элемент изменился во время обработки; результат не применён")
		return
	}
	if _, err = os.Stat(filepath.Join(s.root, "assets", asset.ID, "meta.json")); err != nil {
		s.persistRotationReceipt(session, job, nil, "Подготовленный asset не опубликован")
		return
	}
	oldRevision, oldDirty := scene.Revision, s.dirty
	oldElement := current
	oldAssets := make(map[string]Asset, len(session.Assets))
	for assetID, existing := range session.Assets {
		oldAssets[assetID] = existing
	}
	stream := job.memberID + ":" + job.client
	previousReceipt, previousReceiptExists := session.Receipts[stream]
	session.Assets[asset.ID] = asset
	current.AssetID = asset.ID
	current.Transform = Transform{X: job.recipe.Plan.WorldX, Y: job.recipe.Plan.WorldY, Width: job.recipe.Plan.WorldWidth, Height: job.recipe.Plan.WorldHeight}
	scene.Elements[current.ID] = current
	refreshAssetOrphans(session, time.Now())
	scene.Revision++
	session.Receipts[stream] = Receipt{Seq: job.seq, Digest: job.digest, Updated: time.Now().Unix()}
	s.dirty = true
	if err = s.saveLocked(); err != nil {
		session.Assets = oldAssets
		scene.Elements[oldElement.ID] = oldElement
		scene.Revision = oldRevision
		scene.rebuildRuntime()
		if previousReceiptExists {
			session.Receipts[stream] = previousReceipt
		} else {
			delete(session.Receipts, stream)
		}
		s.dirty = oldDirty
		for waiter := range job.waiters {
			s.send(waiter, map[string]any{"type": "saveError", "client": job.client, "seq": job.seq, "message": "Не удалось сохранить результат обработки. Команда не подтверждена; будет повторена"})
		}
		return
	}
	if pruneReceipts(session, stream) {
		s.dirty = true
	}
	scene.applyElementRuntimeChange(oldElement, true, current, true)
	s.publishElement(session, scene.ID, oldElement, true, current, true, false)
	s.sendRotationJob(job, "completed", "Ротация зафиксирована")
	s.sendRotationAck(job, session, "")
}
