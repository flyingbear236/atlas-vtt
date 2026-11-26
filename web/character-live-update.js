// A narrow fast path for frequent character snapshots. Definitions and layout
// must remain structurally unchanged; otherwise the full renderer takes over.
const equal = (a,b) => a===b || JSON.stringify(a) === JSON.stringify(b);
const sameKeys = (a,b) => a===b || equal(Object.keys(a||{}).sort(),Object.keys(b||{}).sort());

export function canPatchCharacterSnapshot(previous, next) {
 if(!previous||!next||previous.characterId!==next.characterId||previous.registryRevision!==next.registryRevision)return false;
 if(!equal(previous.permissions,next.permissions)||!equal(previous.instance?.inventory,next.instance?.inventory))return false;
 if(!equal(previous.instance?.effectIds,next.instance?.effectIds))return false;
 if(previous.instance?.name!==next.instance?.name||previous.instance?.presetId!==next.instance?.presetId||previous.instance?.persistent!==next.instance?.persistent||!!previous.instance?.materialized!==!!next.instance?.materialized)return false;
 if(previous.effective?.description!==next.effective?.description||previous.effective?.name!==next.effective?.name||previous.effective?.avatarAssetId!==next.effective?.avatarAssetId||!equal(previous.effective?.artwork,next.effective?.artwork))return false;
 if(!sameKeys(previous.effective?.stats,next.effective?.stats)||!sameKeys(previous.effective?.resources,next.effective?.resources))return false;
 for(const [id,old] of Object.entries(previous.effective?.stats||{}))if(old?.type!==next.effective.stats[id]?.type)return false;
 for(const [id,old] of Object.entries(previous.effective?.resources||{}))if(old?.initial?.type!==next.effective.resources[id]?.initial?.type||old?.current?.type!==next.effective.resources[id]?.current?.type)return false;
 if(!equal(previous.effective?.actionIds,next.effective?.actionIds)||!equal(previous.effective?.actionSources,next.effective?.actionSources)||!equal(previous.inactiveActionSources,next.inactiveActionSources))return false;
 if(!equal(previous.itemNames,next.itemNames)||!sameKeys(previous.statDefinitions,next.statDefinitions)||!sameKeys(previous.actions,next.actions))return false;
 return true;
}
