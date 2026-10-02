package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func characterCommandType(commandType string) bool {
	switch commandType {
	case "characterCreate", "characterLink", "characterUnlink", "characterSetPersistent",
		"characterStatSet", "characterStatReset", "characterAvatarSet", "characterAvatarReset",
		"characterActionAdd", "characterActionRemove":
		return true
	default:
		return false
	}
}

type characterInstanceBackup struct {
	instance CharacterInstance
	existed  bool
}

type characterTokenMutation struct {
	scene       *Scene
	sceneID     string
	oldRevision uint64
	old         Token
	next        Token
}

type characterMutation struct {
	session              *Session
	oldCampaign          CampaignRegistry
	oldRegistryRevision  uint64
	oldCharacterRevision uint64
	campaignTouched      bool
	instances            map[string]characterInstanceBackup
	token                *characterTokenMutation
	oldAssets            map[string]Asset
	changedCharacters    map[string]bool
	registryChanged      bool
	resultCharacterID    string
}

func newCharacterMutation(session *Session) *characterMutation {
	return &characterMutation{
		session:              session,
		oldCampaign:          session.CampaignDefinitions,
		oldRegistryRevision:  session.RegistryRevision,
		oldCharacterRevision: session.CharacterRevision,
		instances:            map[string]characterInstanceBackup{},
		changedCharacters:    map[string]bool{},
	}
}

func (mutation *characterMutation) rememberInstance(characterID string) {
	if _, remembered := mutation.instances[characterID]; remembered {
		return
	}
	instance, existed := mutation.session.CharacterInstances[characterID]
	mutation.instances[characterID] = characterInstanceBackup{instance: cloneCharacterInstance(instance), existed: existed}
}

func (mutation *characterMutation) setInstance(instance CharacterInstance) {
	mutation.rememberInstance(instance.ID)
	mutation.session.CharacterInstances[instance.ID] = cloneCharacterInstance(instance)
	mutation.changedCharacters[instance.ID] = false
}

func (mutation *characterMutation) rememberAssets() {
	if mutation.oldAssets != nil {
		return
	}
	mutation.oldAssets = make(map[string]Asset, len(mutation.session.Assets))
	for assetID, asset := range mutation.session.Assets {
		mutation.oldAssets[assetID] = asset
	}
}

func (mutation *characterMutation) refreshAssets() {
	mutation.rememberAssets()
	refreshAssetOrphans(mutation.session, time.Now())
}

func (mutation *characterMutation) setCampaign(campaign CampaignRegistry) {
	if campaignRegistriesEqual(mutation.session.CampaignDefinitions, campaign) {
		return
	}
	mutation.campaignTouched = true
	mutation.session.CampaignDefinitions = campaign
	mutation.session.RegistryRevision++
	mutation.registryChanged = true
}

func (mutation *characterMutation) setToken(sceneID string, scene *Scene, old Token, next Token) {
	if tokensEqual(old, next) {
		return
	}
	if mutation.token == nil {
		mutation.token = &characterTokenMutation{scene: scene, sceneID: sceneID, oldRevision: scene.Revision, old: cloneToken(old)}
	}
	mutation.token.next = cloneToken(next)
	scene.Tokens[next.ID] = cloneToken(next)
	scene.Revision++
	scene.applyTokenRuntimeChange(old, true, next, true)
	updateCharacterReference(mutation.session, sceneID, old, true, next, true)
}

func (mutation *characterMutation) rollback() {
	if mutation.campaignTouched {
		mutation.session.CampaignDefinitions = mutation.oldCampaign
	}
	mutation.session.RegistryRevision = mutation.oldRegistryRevision
	mutation.session.CharacterRevision = mutation.oldCharacterRevision
	for characterID, backup := range mutation.instances {
		if backup.existed {
			mutation.session.CharacterInstances[characterID] = cloneCharacterInstance(backup.instance)
		} else {
			delete(mutation.session.CharacterInstances, characterID)
		}
	}
	if mutation.token != nil {
		mutation.token.scene.Tokens[mutation.token.old.ID] = cloneToken(mutation.token.old)
		mutation.token.scene.Revision = mutation.token.oldRevision
		mutation.token.scene.rebuildRuntime()
	}
	if mutation.oldAssets != nil {
		mutation.session.Assets = mutation.oldAssets
	}
	rebuildCharacterReferences(mutation.session)
}

func gcCharacterIfUnreferenced(session *Session, characterID string) (CharacterInstance, bool) {
	instance, exists := session.CharacterInstances[characterID]
	if !exists || instance.Persistent || len(session.characterReferences[characterID]) != 0 {
		return CharacterInstance{}, false
	}
	delete(session.CharacterInstances, characterID)
	return instance, true
}

func (mutation *characterMutation) gcCharacter(characterID string) {
	if characterID == "" {
		return
	}
	mutation.rememberInstance(characterID)
	instance, deleted := gcCharacterIfUnreferenced(mutation.session, characterID)
	if !deleted {
		return
	}
	mutation.changedCharacters[characterID] = true
	if instance.AvatarAssetID != nil && *instance.AvatarAssetID != "" {
		mutation.refreshAssets()
	}
}

func characterTargetToken(session *Session, sceneID, tokenID string) (*Scene, Token, error) {
	if sceneID == "" || tokenID == "" {
		return nil, Token{}, errors.New("scene and token are required")
	}
	scene := session.Scenes[sceneID]
	if scene == nil {
		return nil, Token{}, errors.New("scene not found")
	}
	token, exists := scene.Tokens[tokenID]
	if !exists {
		return nil, Token{}, errors.New("token not found")
	}
	return scene, cloneToken(token), nil
}

func nextCharacterID(session *Session) string {
	for {
		characterID := id()
		if _, exists := session.CharacterInstances[characterID]; !exists {
			return characterID
		}
	}
}

func applyCharacterCommand(session *Session, member *Member, sceneID string, command Command, mutation *characterMutation) error {
	admin := command.Type == "characterCreate" || command.Type == "characterLink" || command.Type == "characterUnlink" ||
		command.Type == "characterSetPersistent" || command.Type == "characterActionAdd" || command.Type == "characterActionRemove"
	if admin && !memberIsGM(member) {
		return errors.New("action is available to the GM")
	}

	registry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		return err
	}
	switch command.Type {
	case "characterCreate":
		scene, token, err := characterTargetToken(session, sceneID, command.TokenID)
		if err != nil {
			return err
		}
		var preset *CharacterPresetDefinition
		if command.PresetID != "" {
			definition, exists := registry.Presets[command.PresetID]
			if !exists {
				return fmt.Errorf("unknown preset %q", command.PresetID)
			}
			definition = clonePresetDefinition(definition)
			preset = &definition
		}
		characterID := nextCharacterID(session)
		instance := NewCharacterInstance(characterID, preset)
		if command.CharacterName != "" {
			instance.Name = command.CharacterName
		}
		if command.Persistent != nil {
			instance.Persistent = *command.Persistent
		}
		mutation.setInstance(instance)
		old := cloneToken(token)
		oldCharacterID := token.CharacterInstanceID
		token.CharacterInstanceID = characterID
		mutation.setToken(sceneID, scene, old, token)
		mutation.gcCharacter(oldCharacterID)
		mutation.resultCharacterID = characterID
	case "characterLink":
		if session.CharacterInstances[command.CharacterID].ID == "" {
			return errors.New("character not found")
		}
		scene, token, err := characterTargetToken(session, sceneID, command.TokenID)
		if err != nil {
			return err
		}
		old := cloneToken(token)
		oldCharacterID := token.CharacterInstanceID
		token.CharacterInstanceID = command.CharacterID
		mutation.setToken(sceneID, scene, old, token)
		mutation.gcCharacter(oldCharacterID)
		mutation.resultCharacterID = command.CharacterID
	case "characterUnlink":
		scene, token, err := characterTargetToken(session, sceneID, command.TokenID)
		if err != nil {
			return err
		}
		if token.CharacterInstanceID == "" {
			return errors.New("token has no character")
		}
		old := cloneToken(token)
		oldCharacterID := token.CharacterInstanceID
		token.CharacterInstanceID = ""
		mutation.setToken(sceneID, scene, old, token)
		mutation.gcCharacter(oldCharacterID)
		mutation.resultCharacterID = oldCharacterID
	case "characterSetPersistent":
		if command.Persistent == nil {
			return errors.New("persistent value is required")
		}
		instance, exists := session.CharacterInstances[command.CharacterID]
		if !exists {
			return errors.New("character not found")
		}
		if instance.Persistent != *command.Persistent {
			instance.Persistent = *command.Persistent
			mutation.setInstance(instance)
		}
		if !*command.Persistent {
			mutation.gcCharacter(command.CharacterID)
		}
		mutation.resultCharacterID = command.CharacterID
	case "characterStatSet":
		if command.StatValue == nil {
			return errors.New("stat value is required")
		}
		if !memberCanAccessCharacter(session, member, command.CharacterID) {
			return errors.New("character access denied")
		}
		instance := session.CharacterInstances[command.CharacterID]
		campaign, err := WithInferredStatDefinitions(sessionRegistries(session), map[string]StatValue{command.StatID: *command.StatValue})
		if err != nil {
			return err
		}
		mutation.setCampaign(campaign)
		instance = WithStatOverride(instance, command.StatID, *command.StatValue)
		mutation.setInstance(instance)
		mutation.resultCharacterID = command.CharacterID
	case "characterStatReset":
		if err := validateID(command.StatID); err != nil {
			return fmt.Errorf("stat id: %w", err)
		}
		if !memberCanAccessCharacter(session, member, command.CharacterID) {
			return errors.New("character access denied")
		}
		instance := session.CharacterInstances[command.CharacterID]
		if _, exists := instance.StatOverrides[command.StatID]; exists {
			mutation.setInstance(ResetStatOverride(instance, command.StatID))
		}
		mutation.resultCharacterID = command.CharacterID
	case "characterAvatarSet":
		if command.AvatarAssetID == nil {
			return errors.New("avatar asset id is required")
		}
		if !memberCanAccessCharacter(session, member, command.CharacterID) {
			return errors.New("character access denied")
		}
		if *command.AvatarAssetID != "" && (session.Assets[*command.AvatarAssetID].ID == "" || session.Assets[*command.AvatarAssetID].Kind != assetKindAvatar) {
			return errors.New("avatar asset not found")
		}
		instance := session.CharacterInstances[command.CharacterID]
		mutation.setInstance(WithAvatarOverride(instance, *command.AvatarAssetID))
		mutation.refreshAssets()
		mutation.resultCharacterID = command.CharacterID
	case "characterAvatarReset":
		if !memberCanAccessCharacter(session, member, command.CharacterID) {
			return errors.New("character access denied")
		}
		instance := session.CharacterInstances[command.CharacterID]
		if instance.AvatarAssetID != nil {
			mutation.setInstance(ResetAvatarOverride(instance))
			mutation.refreshAssets()
		}
		mutation.resultCharacterID = command.CharacterID
	case "characterActionAdd", "characterActionRemove":
		if _, exists := registry.Actions[command.ActionID]; !exists {
			return fmt.Errorf("unknown action %q", command.ActionID)
		}
		instance, exists := session.CharacterInstances[command.CharacterID]
		if !exists {
			return errors.New("character not found")
		}
		if command.Type == "characterActionAdd" {
			instance = WithActionAdded(instance, command.ActionID)
		} else {
			instance = WithActionRemoved(instance, command.ActionID)
		}
		mutation.setInstance(instance)
		mutation.resultCharacterID = command.CharacterID
	default:
		return errors.New("unknown character command")
	}

	if err := validateSessionCharacterState(session); err != nil {
		return err
	}
	return nil
}

func (s *Server) characterCommand(session *Session, peer *peer, command Command) {
	if session.Receipts == nil {
		session.Receipts = map[string]Receipt{}
	}
	stream := peer.member.ID + ":" + command.Client
	previousReceipt := session.Receipts[stream]
	encoded, _ := json.Marshal(command)
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	ack := func(receipt Receipt) {
		s.send(peer, map[string]any{"type": "ack", "client": command.Client, "seq": command.Seq, "error": receipt.Error, "characterId": receipt.CharacterID, "registryRevision": session.RegistryRevision})
	}
	if command.Seq == 0 || command.Client == "" || len(command.Client) > 80 {
		s.send(peer, map[string]string{"type": "fatal", "message": "Некорректный поток команд"})
		return
	}
	if command.Seq == previousReceipt.Seq {
		if previousReceipt.Digest != digest {
			s.send(peer, map[string]string{"type": "fatal", "message": "Номер команды уже занят другой командой. Откройте новую вкладку"})
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
	mutation := newCharacterMutation(session)
	sceneID := command.SceneID
	if sceneID == "" {
		sceneID = peer.sceneID
	}
	issue := ""
	if err := applyCharacterCommand(session, peer.member, sceneID, command, mutation); err != nil {
		issue = err.Error()
		mutation.rollback()
	}
	characterChanged := issue == "" && (len(mutation.changedCharacters) > 0 || mutation.token != nil || mutation.registryChanged)
	if characterChanged {
		session.CharacterRevision++
	}
	resultCharacterID := mutation.resultCharacterID
	if issue != "" {
		resultCharacterID = ""
	}
	receipt := Receipt{Seq: command.Seq, Error: issue, Digest: digest, Updated: time.Now().Unix(), CharacterID: resultCharacterID}
	session.Receipts[stream] = receipt
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		if issue == "" {
			mutation.rollback()
		}
		s.dirty = oldDirty
		if previousReceipt.Seq == 0 {
			delete(session.Receipts, stream)
		} else {
			session.Receipts[stream] = previousReceipt
		}
		s.send(peer, map[string]any{"type": "saveError", "client": command.Client, "seq": command.Seq, "message": "Не удалось сохранить character. Команда не подтверждена; будет повторена"})
		return
	}
	if pruneReceipts(session, stream) {
		s.dirty = true
	}
	if issue == "" {
		if mutation.registryChanged {
			s.publishRegistryChanged(session)
		}
		if mutation.token != nil {
			s.publish(session, mutation.token.sceneID, "upsert", mutation.token.old, true, mutation.token.next)
		}
		s.refreshCharacterWatches(session, mutation.changedCharacters, mutation.token != nil || mutation.registryChanged)
	}
	ack(session.Receipts[stream])
}
