package main

import "fmt"

// prepareRulesetCharacters calculates all character changes on a detached copy.
// No session state is changed during preview or before the durable commit.
func prepareRulesetCharacters(session *Session, next RulesetSnapshot, campaign CampaignRegistry) (map[string]CharacterInstance, error) {
	oldRegistry, err := MergeDefinitionRegistries(sessionRegistries(session))
	if err != nil { return nil, err }
	nextRegistry, err := MergeDefinitionRegistries(DefinitionRegistries{Ruleset: next.Registry, Campaign: campaign})
	if err != nil { return nil, err }

	characters := make(map[string]CharacterInstance, len(session.CharacterInstances))
	for id, source := range session.CharacterInstances {
		instance := cloneCharacterInstance(source)
		if instance.PresetID != "" {
			if _, available := nextRegistry.Presets[instance.PresetID]; !available {
				preset, oldAvailable := oldRegistry.Presets[instance.PresetID]
				if instance.Materialized == nil {
					if !oldAvailable { return nil, fmt.Errorf("linked character %q has missing preset %q", id, instance.PresetID) }
					instance = MaterializeCharacter(instance, &preset)
				}
				if oldAvailable && instance.AvatarAssetID == nil {
					assetID := preset.AvatarAssetID
					if preset.Artwork != "" { assetID = session.Ruleset.Media[preset.Artwork] }
					if assetID != "" && isCharacterArtworkAsset(session.Assets[assetID]) {
						instance.AvatarAssetID = &assetID
					}
				}
			}
		}
		if err := ValidateCharacterInstance(instance, nextRegistry); err != nil {
			return nil, fmt.Errorf("character %q: %w", id, err)
		}
		characters[id] = instance
	}
	return characters, nil
}
