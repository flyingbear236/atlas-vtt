package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxDefinitionOperationReceipts = 256
const maxDefinitionOperationKeyRunes = 80

const (
	definitionKindStat   = "stat"
	definitionKindAction = "action"
	definitionKindPreset = "preset"

	definitionOperationRulesetInstall = "rulesetInstall"
	definitionOperationCampaignImport = "campaignImport"
)

type DefinitionApplyResult struct {
	Kind             string             `json:"kind"`
	RegistryRevision uint64             `json:"registryRevision"`
	Diff             CampaignImportDiff `json:"diff,omitempty"`
	RulesetMetadata  *RulesetMetadata   `json:"rulesetMetadata,omitempty"`
}

type DefinitionOperationReceipt struct {
	Kind    string                `json:"kind"`
	Digest  string                `json:"digest"`
	Result  DefinitionApplyResult `json:"result"`
	Updated int64                 `json:"updated"`
}

type definitionRegistryResponse struct {
	RegistryRevision uint64            `json:"registryRevision"`
	Ruleset          RulesetSnapshot   `json:"ruleset"`
	Campaign         CampaignRegistry  `json:"campaign"`
	Effective        EffectiveRegistry `json:"effective"`
}

type definitionPreviewResponse struct {
	Digest           string                  `json:"digest"`
	RegistryRevision uint64                  `json:"registryRevision"`
	Diff             *CampaignImportDiff     `json:"diff,omitempty"`
	RulesetDiff      *RulesetReplacementDiff `json:"rulesetDiff,omitempty"`
	Ruleset          *RulesetSnapshot        `json:"ruleset,omitempty"`
}

func definitionCommandType(commandType string) bool {
	switch commandType {
	case "definitionCreate", "definitionUpdate", "definitionDuplicate", "definitionDelete":
		return true
	default:
		return false
	}
}

func sessionRegistries(session *Session) DefinitionRegistries {
	return DefinitionRegistries{Ruleset: session.Ruleset.Registry, Campaign: session.CampaignDefinitions}
}

func validateDefinitionState(session *Session, ruleset RulesetSnapshot, campaign CampaignRegistry) error {
	if err := ValidateRulesetSnapshot(ruleset); err != nil {
		return err
	}
	effective, err := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: ruleset.Registry, Campaign: campaign})
	if err != nil {
		return err
	}
	for id, preset := range campaign.Presets {
		if preset.AvatarAssetID != "" {
			if asset, exists := session.Assets[preset.AvatarAssetID]; !exists || asset.Kind != assetKindAvatar {
				return fmt.Errorf("campaign preset %q references unknown avatar asset %q", id, preset.AvatarAssetID)
			}
		}
	}
	for id, instance := range session.CharacterInstances {
		if err := ValidateCharacterInstance(instance, effective); err != nil {
			return fmt.Errorf("character instance %q: %w", id, err)
		}
	}
	return nil
}

func definitionReferences(session *Session, registry EffectiveRegistry, kind, definitionID string) []string {
	const maxReferences = 10
	references := make([]string, 0, maxReferences)
	add := func(reference string) {
		if len(references) < maxReferences && !slices.Contains(references, reference) {
			references = append(references, reference)
		}
	}
	switch kind {
	case definitionKindStat:
		for actionID, action := range registry.Actions {
			for _, roll := range action.Rolls {
				if roll.ModifierStat == definitionID {
					add("action:" + actionID)
				}
			}
		}
		for presetID, preset := range registry.Presets {
			if _, exists := preset.Stats[definitionID]; exists {
				add("preset:" + presetID)
			}
		}
		for instanceID, instance := range session.CharacterInstances {
			if _, exists := instance.StatOverrides[definitionID]; exists {
				add("character:" + instanceID)
			}
		}
	case definitionKindAction:
		for presetID, preset := range registry.Presets {
			if slices.Contains(preset.ActionIDs, definitionID) {
				add("preset:" + presetID)
			}
		}
		for instanceID, instance := range session.CharacterInstances {
			if slices.Contains(instance.AddedActionIDs, definitionID) || slices.Contains(instance.RemovedActionIDs, definitionID) {
				add("character:" + instanceID)
			}
		}
	case definitionKindPreset:
		for instanceID, instance := range session.CharacterInstances {
			if instance.PresetID == definitionID {
				add("character:" + instanceID)
			}
		}
	}
	sort.Strings(references)
	return references
}

func effectiveHasDefinition(registry EffectiveRegistry, kind, definitionID string) bool {
	switch kind {
	case definitionKindStat:
		_, ok := registry.Stats[definitionID]
		return ok
	case definitionKindAction:
		_, ok := registry.Actions[definitionID]
		return ok
	case definitionKindPreset:
		_, ok := registry.Presets[definitionID]
		return ok
	default:
		return false
	}
}

func definitionExistsInCampaign(registry CampaignRegistry, kind, definitionID string) bool {
	switch kind {
	case definitionKindStat:
		_, ok := registry.Stats[definitionID]
		return ok
	case definitionKindAction:
		_, ok := registry.Actions[definitionID]
		return ok
	case definitionKindPreset:
		_, ok := registry.Presets[definitionID]
		return ok
	default:
		return false
	}
}

func cloneEffectiveDefinitionIntoCampaign(candidate *CampaignRegistry, effective EffectiveRegistry, kind, sourceID, targetID string) error {
	switch kind {
	case definitionKindStat:
		definition, exists := effective.Stats[sourceID]
		if !exists {
			return errors.New("definition not found")
		}
		definition = cloneStatDefinition(definition)
		definition.ID = targetID
		candidate.Stats[targetID] = definition
	case definitionKindAction:
		definition, exists := effective.Actions[sourceID]
		if !exists {
			return errors.New("definition not found")
		}
		definition = cloneActionDefinition(definition)
		definition.ID = targetID
		candidate.Actions[targetID] = definition
	case definitionKindPreset:
		definition, exists := effective.Presets[sourceID]
		if !exists {
			return errors.New("definition not found")
		}
		definition = clonePresetDefinition(definition)
		definition.ID = targetID
		candidate.Presets[targetID] = definition
	default:
		return errors.New("unknown definition kind")
	}
	return nil
}

func setCommandDefinition(candidate *CampaignRegistry, registries DefinitionRegistries, command Command) error {
	switch command.DefinitionKind {
	case definitionKindStat:
		if command.StatDefinition == nil || command.StatDefinition.ID != command.DefinitionID {
			return errors.New("stat definition and id are required")
		}
		candidate.Stats[command.DefinitionID] = cloneStatDefinition(*command.StatDefinition)
	case definitionKindAction:
		if command.ActionDefinition == nil || command.ActionDefinition.ID != command.DefinitionID {
			return errors.New("action definition and id are required")
		}
		candidate.Actions[command.DefinitionID] = cloneActionDefinition(*command.ActionDefinition)
	case definitionKindPreset:
		if command.PresetDefinition == nil || command.PresetDefinition.ID != command.DefinitionID {
			return errors.New("preset definition and id are required")
		}
		inferred, err := WithInferredStatDefinitions(DefinitionRegistries{Ruleset: registries.Ruleset, Campaign: *candidate}, command.PresetDefinition.Stats)
		if err != nil {
			return err
		}
		*candidate = inferred
		candidate.Presets[command.DefinitionID] = clonePresetDefinition(*command.PresetDefinition)
	default:
		return errors.New("unknown definition kind")
	}
	return nil
}

func deleteCampaignDefinition(candidate *CampaignRegistry, kind, definitionID string) error {
	switch kind {
	case definitionKindStat:
		delete(candidate.Stats, definitionID)
	case definitionKindAction:
		delete(candidate.Actions, definitionID)
	case definitionKindPreset:
		delete(candidate.Presets, definitionID)
	default:
		return errors.New("unknown definition kind")
	}
	return nil
}

func applyDefinitionCommand(session *Session, command Command) (CampaignRegistry, bool, error) {
	if command.ExpectedRegistryRevision == nil || *command.ExpectedRegistryRevision != session.RegistryRevision {
		return CampaignRegistry{}, false, fmt.Errorf("stale registry revision: current is %d", session.RegistryRevision)
	}
	registries := sessionRegistries(session)
	currentEffective, err := MergeDefinitionRegistries(registries)
	if err != nil {
		return CampaignRegistry{}, false, err
	}
	candidate := cloneCampaignRegistry(session.CampaignDefinitions)
	switch command.Type {
	case "definitionCreate":
		if definitionExistsInCampaign(candidate, command.DefinitionKind, command.DefinitionID) {
			return CampaignRegistry{}, false, errors.New("campaign definition already exists")
		}
		if err := setCommandDefinition(&candidate, registries, command); err != nil {
			return CampaignRegistry{}, false, err
		}
	case "definitionUpdate":
		if !definitionExistsInCampaign(candidate, command.DefinitionKind, command.DefinitionID) {
			return CampaignRegistry{}, false, errors.New("campaign definition not found")
		}
		if command.DefinitionKind == definitionKindStat && command.StatDefinition != nil {
			old := currentEffective.Stats[command.DefinitionID]
			if old.ID != "" && old.Type != command.StatDefinition.Type {
				if references := definitionReferences(session, currentEffective, definitionKindStat, command.DefinitionID); len(references) > 0 {
					return CampaignRegistry{}, false, fmt.Errorf("cannot change type of referenced stat %q: %s", command.DefinitionID, strings.Join(references, ", "))
				}
			}
		}
		if err := setCommandDefinition(&candidate, registries, command); err != nil {
			return CampaignRegistry{}, false, err
		}
	case "definitionDuplicate":
		if command.DuplicateDefinitionID == "" || effectiveHasDefinition(currentEffective, command.DefinitionKind, command.DuplicateDefinitionID) {
			return CampaignRegistry{}, false, errors.New("duplicate target id is missing or already exists")
		}
		if err := cloneEffectiveDefinitionIntoCampaign(&candidate, currentEffective, command.DefinitionKind, command.DefinitionID, command.DuplicateDefinitionID); err != nil {
			return CampaignRegistry{}, false, err
		}
	case "definitionDelete":
		if !definitionExistsInCampaign(candidate, command.DefinitionKind, command.DefinitionID) {
			return CampaignRegistry{}, false, errors.New("campaign definition not found")
		}
		if err := deleteCampaignDefinition(&candidate, command.DefinitionKind, command.DefinitionID); err != nil {
			return CampaignRegistry{}, false, err
		}
		candidateEffective, mergeErr := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: session.Ruleset.Registry, Campaign: candidate})
		if mergeErr == nil && !effectiveHasDefinition(candidateEffective, command.DefinitionKind, command.DefinitionID) {
			if references := definitionReferences(session, currentEffective, command.DefinitionKind, command.DefinitionID); len(references) > 0 {
				return CampaignRegistry{}, false, fmt.Errorf("definition %q is referenced by %s", command.DefinitionID, strings.Join(references, ", "))
			}
		}
	default:
		return CampaignRegistry{}, false, errors.New("unknown definition command")
	}
	if err := validateDefinitionState(session, session.Ruleset, candidate); err != nil {
		return CampaignRegistry{}, false, err
	}
	changed := !campaignRegistriesEqual(session.CampaignDefinitions, candidate)
	return candidate, changed, nil
}

func campaignRegistriesEqual(left, right CampaignRegistry) bool {
	if len(left.Stats) != len(right.Stats) || len(left.Actions) != len(right.Actions) || len(left.Presets) != len(right.Presets) {
		return false
	}
	for id, definition := range left.Stats {
		if other, ok := right.Stats[id]; !ok || !equalStatDefinition(definition, other) {
			return false
		}
	}
	for id, definition := range left.Actions {
		if other, ok := right.Actions[id]; !ok || !equalActionDefinition(definition, other) {
			return false
		}
	}
	for id, definition := range left.Presets {
		if other, ok := right.Presets[id]; !ok || !equalPresetDefinition(definition, other) {
			return false
		}
	}
	return true
}

func (s *Server) definitionCommand(session *Session, peer *peer, command Command) {
	if session.Receipts == nil {
		session.Receipts = map[string]Receipt{}
	}
	stream := peer.member.ID + ":" + command.Client
	previousReceipt := session.Receipts[stream]
	encoded, _ := json.Marshal(command)
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	ack := func(receipt Receipt) {
		s.send(peer, map[string]any{"type": "ack", "client": command.Client, "seq": command.Seq, "error": receipt.Error, "registryRevision": session.RegistryRevision})
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

	oldCampaign := cloneCampaignRegistry(session.CampaignDefinitions)
	oldRevision, oldCharacterRevision, oldDirty := session.RegistryRevision, session.CharacterRevision, s.dirty
	var oldAssets map[string]Asset
	issue := ""
	changed := false
	if !memberIsGM(peer.member) {
		issue = "Действие доступно ведущему"
	} else {
		candidate, candidateChanged, err := applyDefinitionCommand(session, command)
		if err != nil {
			issue = err.Error()
		} else if candidateChanged {
			oldAssets = make(map[string]Asset, len(session.Assets))
			for assetID, asset := range session.Assets {
				oldAssets[assetID] = asset
			}
			session.CampaignDefinitions = candidate
			session.RegistryRevision++
			session.CharacterRevision++
			refreshAssetOrphans(session, time.Now())
			changed = true
		}
	}
	session.Receipts[stream] = Receipt{Seq: command.Seq, Error: issue, Digest: digest, Updated: time.Now().Unix()}
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		if changed {
			session.CampaignDefinitions = oldCampaign
		}
		if oldAssets != nil {
			session.Assets = oldAssets
		}
		session.RegistryRevision, session.CharacterRevision, s.dirty = oldRevision, oldCharacterRevision, oldDirty
		if previousReceipt.Seq == 0 {
			delete(session.Receipts, stream)
		} else {
			session.Receipts[stream] = previousReceipt
		}
		s.send(peer, map[string]any{"type": "saveError", "client": command.Client, "seq": command.Seq, "message": "Не удалось сохранить definitions. Команда не подтверждена; будет повторена"})
		return
	}
	if pruneReceipts(session, stream) {
		s.dirty = true
	}
	if issue == "" && changed {
		s.publishRegistryChanged(session)
		s.refreshCharacterWatches(session, nil, true)
	}
	ack(session.Receipts[stream])
}

func (s *Server) publishRegistryChanged(session *Session) {
	for peer := range s.peers {
		if peer.session == session.ID {
			s.send(peer, map[string]any{"type": "registryChanged", "registryRevision": session.RegistryRevision})
		}
	}
}

func definitionsDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readDefinitionDocument(w http.ResponseWriter, request *http.Request) ([]byte, bool) {
	request.Body = http.MaxBytesReader(w, request.Body, MaxDefinitionDocumentBytes)
	data, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(w, http.StatusRequestEntityTooLarge, "TOML превышает лимит 1 МиБ")
			return nil, false
		}
		fail(w, http.StatusBadRequest, "Не удалось прочитать TOML")
		return nil, false
	}
	return data, true
}

type rulesetUploadFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type rulesetUploadPayload struct {
	Files []rulesetUploadFile `json:"files"`
}

func readRulesetDocuments(w http.ResponseWriter, request *http.Request) (map[string]string, bool) {
	if strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		request.Body = http.MaxBytesReader(w, request.Body, 4*MaxRulesetPackageBytes+(1<<20))
		var payload rulesetUploadPayload
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				fail(w, http.StatusRequestEntityTooLarge, "Пакет ruleset превышает лимит")
				return nil, false
			}
			fail(w, http.StatusBadRequest, "Некорректный пакет ruleset")
			return nil, false
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			fail(w, http.StatusBadRequest, "Некорректный пакет ruleset")
			return nil, false
		}
		if len(payload.Files) == 0 || len(payload.Files) > MaxRulesetFiles {
			fail(w, http.StatusBadRequest, fmt.Sprintf("Пакет ruleset должен содержать от 1 до %d TOML-файлов", MaxRulesetFiles))
			return nil, false
		}
		files := make(map[string]string, len(payload.Files))
		totalBytes := 0
		for _, file := range payload.Files {
			if err := validateRulesetFileName(file.Name); err != nil {
				fail(w, http.StatusBadRequest, err.Error())
				return nil, false
			}
			if _, duplicate := files[file.Name]; duplicate {
				fail(w, http.StatusBadRequest, fmt.Sprintf("Файл %q передан дважды", file.Name))
				return nil, false
			}
			totalBytes += len(file.Content)
			if totalBytes > MaxRulesetPackageBytes {
				fail(w, http.StatusRequestEntityTooLarge, "Пакет ruleset превышает лимит 1 МиБ")
				return nil, false
			}
			files[file.Name] = file.Content
		}
		return files, true
	}

	data, ok := readDefinitionDocument(w, request)
	if !ok {
		return nil, false
	}
	name := request.URL.Query().Get("filename")
	if name == "" {
		name = request.URL.Query().Get("replaceFile")
	}
	if name == "" {
		name = "ruleset.toml"
	}
	if err := validateRulesetFileName(name); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return map[string]string{name: string(data)}, true
}

func rulesetDocumentsDigest(files map[string]string, replaceFile string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, "replaceFile")
	_, _ = hash.Write([]byte{0})
	_, _ = io.WriteString(hash, replaceFile)
	_, _ = hash.Write([]byte{0})
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		_, _ = io.WriteString(hash, name)
		_, _ = hash.Write([]byte{0})
		_, _ = io.WriteString(hash, files[name])
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func rulesetReplacementCandidate(session *Session, uploads map[string]string, replaceFile string) (RulesetSnapshot, error) {
	if replaceFile == "" {
		return CompileRulesetFiles(uploads)
	}
	if len(uploads) != 1 {
		return RulesetSnapshot{}, errors.New("для замены одного файла нужно передать ровно один TOML-файл")
	}
	content, exists := uploads[replaceFile]
	if !exists {
		return RulesetSnapshot{}, fmt.Errorf("имя загруженного файла должно совпадать с заменяемым %q", replaceFile)
	}
	return ReplaceRulesetFile(session.Ruleset, replaceFile, content)
}

func definitionOperationKeyValid(key string) bool {
	if key == "" || !utf8.ValidString(key) || utf8.RuneCountInString(key) > maxDefinitionOperationKeyRunes || strings.TrimSpace(key) != key {
		return false
	}
	for _, runeValue := range key {
		if unicode.IsControl(runeValue) {
			return false
		}
	}
	return true
}

func parseDefinitionApplyParameters(request *http.Request) (uint64, string, string, error) {
	revision, err := strconv.ParseUint(request.URL.Query().Get("expectedRevision"), 10, 64)
	if err != nil || revision == 0 {
		return 0, "", "", errors.New("expectedRevision is required")
	}
	digest := request.URL.Query().Get("digest")
	if len(digest) != sha256.Size*2 {
		return 0, "", "", errors.New("preview digest is required")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return 0, "", "", errors.New("preview digest is invalid")
	}
	key := request.URL.Query().Get("key")
	if !definitionOperationKeyValid(key) {
		return 0, "", "", errors.New("idempotency key is invalid")
	}
	return revision, digest, key, nil
}

func (s *Server) authorizeDefinitionsRequest(w http.ResponseWriter, request *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, member := s.auth(request)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return false
	}
	if s.stopping {
		fail(w, http.StatusServiceUnavailable, "Сервер останавливается")
		return false
	}
	return true
}

func (s *Server) readDefinitions(w http.ResponseWriter, request *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, member := s.auth(request)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	effective, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Registry повреждён")
		return
	}
	reply(w, definitionRegistryResponse{
		RegistryRevision: session.RegistryRevision,
		Ruleset:          cloneRulesetSnapshot(session.Ruleset),
		Campaign:         cloneCampaignRegistry(session.CampaignDefinitions),
		Effective:        effective,
	})
}

func writeDefinitionsTOML(w http.ResponseWriter, data []byte, filename string) {
	w.Header().Set("Content-Type", "application/toml; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) exportRulesetDefinitions(w http.ResponseWriter, request *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, member := s.auth(request)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	if !rulesetInstalled(session.Ruleset) {
		fail(w, http.StatusNotFound, "Ruleset не установлен")
		return
	}
	data, err := ExportRulesetSnapshotTOML(session.Ruleset)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeDefinitionsTOML(w, data, "ruleset.toml")
}

func (s *Server) exportCampaignDefinitions(w http.ResponseWriter, request *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, member := s.auth(request)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	data, err := ExportCampaignDefinitionsTOML(sessionRegistries(session))
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeDefinitionsTOML(w, data, "campaign-extensions.toml")
}

func rulesetInstalled(snapshot RulesetSnapshot) bool {
	return snapshot.Metadata != (RulesetMetadata{}) || len(snapshot.Registry.Stats) != 0 || len(snapshot.Registry.Actions) != 0 || len(snapshot.Registry.Presets) != 0
}

func (s *Server) previewRulesetInstall(w http.ResponseWriter, request *http.Request) {
	if !s.authorizeDefinitionsRequest(w, request) {
		return
	}
	uploads, ok := readRulesetDocuments(w, request)
	if !ok {
		return
	}
	replaceFile := request.URL.Query().Get("replaceFile")
	s.mu.Lock()
	defer s.mu.Unlock()
	session, member := s.auth(request)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	snapshot, err := rulesetReplacementCandidate(session, uploads, replaceFile)
	if err == nil {
		err = validateDefinitionState(session, snapshot, session.CampaignDefinitions)
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	copy := cloneRulesetSnapshot(snapshot)
	diff := DiffRulesetRegistries(session.Ruleset.Registry, snapshot.Registry)
	reply(w, definitionPreviewResponse{
		Digest: rulesetDocumentsDigest(uploads, replaceFile),
		RegistryRevision: session.RegistryRevision,
		RulesetDiff: &diff,
		Ruleset: &copy,
	})
}

func (s *Server) previewCampaignImport(w http.ResponseWriter, request *http.Request) {
	if !s.authorizeDefinitionsRequest(w, request) {
		return
	}
	data, ok := readDefinitionDocument(w, request)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, member := s.auth(request)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	preview, err := previewCampaignImportForSession(session, data)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	reply(w, definitionPreviewResponse{Digest: definitionsDigest(data), RegistryRevision: session.RegistryRevision, Diff: &preview.Diff})
}

func previewCampaignImportForSession(session *Session, data []byte) (CampaignImportPreview, error) {
	current, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil {
		return CampaignImportPreview{}, err
	}
	preview, err := PreviewCampaignDefinitionsImport(sessionRegistries(session), data)
	if err != nil {
		return CampaignImportPreview{}, err
	}
	for id, definition := range preview.Registry.Stats {
		if old, exists := current.Stats[id]; exists && old.Type != definition.Type {
			if references := definitionReferences(session, current, definitionKindStat, id); len(references) > 0 {
				return CampaignImportPreview{}, fmt.Errorf("cannot change type of referenced stat %q: %s", id, strings.Join(references, ", "))
			}
		}
	}
	if err := validateDefinitionState(session, session.Ruleset, preview.Registry); err != nil {
		return CampaignImportPreview{}, err
	}
	return preview, nil
}

func cloneDefinitionOperationReceipts(receipts map[string]DefinitionOperationReceipt) map[string]DefinitionOperationReceipt {
	result := make(map[string]DefinitionOperationReceipt, len(receipts))
	for key, receipt := range receipts {
		copy := receipt
		copy.Result.Diff = cloneCampaignImportDiff(receipt.Result.Diff)
		if receipt.Result.RulesetMetadata != nil {
			metadata := *receipt.Result.RulesetMetadata
			copy.Result.RulesetMetadata = &metadata
		}
		result[key] = copy
	}
	return result
}

func cloneCampaignImportDiff(diff CampaignImportDiff) CampaignImportDiff {
	return CampaignImportDiff{
		Stats:   DefinitionImportChanges{Added: append([]string(nil), diff.Stats.Added...), Updated: append([]string(nil), diff.Stats.Updated...)},
		Actions: DefinitionImportChanges{Added: append([]string(nil), diff.Actions.Added...), Updated: append([]string(nil), diff.Actions.Updated...)},
		Presets: DefinitionImportChanges{Added: append([]string(nil), diff.Presets.Added...), Updated: append([]string(nil), diff.Presets.Updated...)},
	}
}

func pruneDefinitionOperationReceipts(receipts map[string]DefinitionOperationReceipt, keep string) {
	if len(receipts) <= maxDefinitionOperationReceipts {
		return
	}
	type operationAge struct {
		key     string
		updated int64
	}
	items := make([]operationAge, 0, len(receipts))
	for key, receipt := range receipts {
		if key != keep {
			items = append(items, operationAge{key: key, updated: receipt.Updated})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].updated < items[j].updated })
	for index := 0; len(receipts) > maxDefinitionOperationReceipts && index < len(items); index++ {
		delete(receipts, items[index].key)
	}
}

func validateDefinitionOperationReceipts(receipts map[string]DefinitionOperationReceipt) error {
	if len(receipts) > maxDefinitionOperationReceipts {
		return errors.New("definition operation receipts exceed limit")
	}
	for key, receipt := range receipts {
		if !definitionOperationKeyValid(key) || len(receipt.Digest) != sha256.Size*2 || receipt.Result.RegistryRevision == 0 || receipt.Kind != receipt.Result.Kind {
			return errors.New("invalid definition operation receipt")
		}
		if _, err := hex.DecodeString(receipt.Digest); err != nil {
			return errors.New("invalid definition operation digest")
		}
		switch receipt.Kind {
		case definitionOperationRulesetInstall, definitionOperationCampaignImport:
		default:
			return errors.New("invalid definition operation kind")
		}
	}
	return nil
}

func (s *Server) applyRulesetInstall(w http.ResponseWriter, request *http.Request) {
	s.applyDefinitionDocument(w, request, definitionOperationRulesetInstall)
}

func (s *Server) applyCampaignImport(w http.ResponseWriter, request *http.Request) {
	s.applyDefinitionDocument(w, request, definitionOperationCampaignImport)
}

func (s *Server) applyDefinitionDocument(w http.ResponseWriter, request *http.Request, kind string) {
	if !s.authorizeDefinitionsRequest(w, request) {
		return
	}
	expectedRevision, expectedDigest, clientKey, err := parseDefinitionApplyParameters(request)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var data []byte
	var rulesetUploads map[string]string
	replaceFile := ""
	actualDigest := ""
	if kind == definitionOperationRulesetInstall {
		var ok bool
		rulesetUploads, ok = readRulesetDocuments(w, request)
		if !ok {
			return
		}
		replaceFile = request.URL.Query().Get("replaceFile")
		actualDigest = rulesetDocumentsDigest(rulesetUploads, replaceFile)
	} else {
		var ok bool
		data, ok = readDefinitionDocument(w, request)
		if !ok {
			return
		}
		actualDigest = definitionsDigest(data)
	}
	if actualDigest != expectedDigest {
		fail(w, http.StatusConflict, "Документ не совпадает с результатом проверки")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	session, member := s.auth(request)
	if !memberIsGM(member) {
		fail(w, http.StatusForbidden, "Нужны права ведущего")
		return
	}
	operationKey := clientKey
	if receipt, exists := session.DefinitionOperations[operationKey]; exists {
		if receipt.Kind != kind || receipt.Digest != actualDigest {
			fail(w, http.StatusConflict, "Ключ операции уже использован с другим документом")
			return
		}
		reply(w, receipt.Result)
		return
	}
	if expectedRevision != session.RegistryRevision {
		fail(w, http.StatusConflict, fmt.Sprintf("Устаревшая registry revision: текущая %d", session.RegistryRevision))
		return
	}

	oldRuleset := cloneRulesetSnapshot(session.Ruleset)
	oldCampaign := cloneCampaignRegistry(session.CampaignDefinitions)
	oldOperations := cloneDefinitionOperationReceipts(session.DefinitionOperations)
	var oldAssets map[string]Asset
	oldRevision, oldCharacterRevision, oldDirty := session.RegistryRevision, session.CharacterRevision, s.dirty
	result := DefinitionApplyResult{Kind: kind}
	switch kind {
	case definitionOperationRulesetInstall:
		snapshot, parseErr := rulesetReplacementCandidate(session, rulesetUploads, replaceFile)
		if parseErr == nil {
			parseErr = validateDefinitionState(session, snapshot, session.CampaignDefinitions)
		}
		if parseErr != nil {
			fail(w, http.StatusBadRequest, parseErr.Error())
			return
		}
		session.Ruleset = cloneRulesetSnapshot(snapshot)
		metadata := snapshot.Metadata
		result.RulesetMetadata = &metadata
	case definitionOperationCampaignImport:
		preview, previewErr := previewCampaignImportForSession(session, data)
		if previewErr != nil {
			fail(w, http.StatusBadRequest, previewErr.Error())
			return
		}
		oldAssets = make(map[string]Asset, len(session.Assets))
		for assetID, asset := range session.Assets {
			oldAssets[assetID] = asset
		}
		session.CampaignDefinitions = cloneCampaignRegistry(preview.Registry)
		refreshAssetOrphans(session, time.Now())
		result.Diff = cloneCampaignImportDiff(preview.Diff)
	default:
		fail(w, http.StatusBadRequest, "Неизвестная операция definitions")
		return
	}
	session.RegistryRevision++
	session.CharacterRevision++
	result.RegistryRevision = session.RegistryRevision
	session.DefinitionOperations[operationKey] = DefinitionOperationReceipt{Kind: kind, Digest: actualDigest, Result: result, Updated: time.Now().Unix()}
	pruneDefinitionOperationReceipts(session.DefinitionOperations, operationKey)
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		session.Ruleset = oldRuleset
		session.CampaignDefinitions = oldCampaign
		session.DefinitionOperations = oldOperations
		if oldAssets != nil {
			session.Assets = oldAssets
		}
		session.RegistryRevision, session.CharacterRevision, s.dirty = oldRevision, oldCharacterRevision, oldDirty
		fail(w, http.StatusInternalServerError, "Ошибка сохранения definitions")
		return
	}
	s.publishRegistryChanged(session)
	s.refreshCharacterWatches(session, nil, true)
	reply(w, result)
}
