import {test} from 'node:test';
import {strict as assert} from 'node:assert';
import {canPatchCharacterSnapshot} from './character-live-update.js';
const base=()=>({characterId:'hero',registryRevision:4,permissions:{edit:true,manage:true},
 instance:{name:'Hero',presetId:'hero',persistent:true,inventory:[{id:'a',objectId:'ring',equipped:true}],effectIds:['power']},
 effective:{name:'Hero',avatarAssetId:'',artwork:{status:'missing'},stats:{str:{type:'integer',value:12}},resources:{hp:{initial:{type:'integer',value:20},current:{type:'integer',value:15}}},actionIds:['hit'],actionSources:[{actionId:'hit',sourceId:'a'}]},
 inactiveActionSources:[],itemNames:{a:'Ring'},statDefinitions:{str:{name:'Strength'}},actions:{hit:{name:'Hit'}},
 actionPreviews:[{actionId:'hit',sourceId:'a',parts:[{label:'STR',value:1}]}]});
test('HP/stat/derived values and previews allow incremental rendering',()=>{
 const a=base(),b=structuredClone(a);b.effective.stats.str.value=19;b.effective.resources.hp.current.value=8;b.actionPreviews[0].parts[0].value=4;
 assert.equal(canPatchCharacterSnapshot(a,b),true);
});
test('equipment, permissions, rules and action structure demand full rebuild',()=>{
 const a=base();
 for(const change of [b=>b.instance.inventory[0].equipped=false,b=>b.permissions.manage=false,b=>b.registryRevision=5,b=>b.effective.actionSources.push({actionId:'hit',sourceId:'b'}),b=>b.effective.stats.other={type:'integer',value:3},b=>b.instance.name='Other',b=>b.effective.stats.str.type='string',b=>b.effective.resources.hp.current.type='string',b=>b.instance.materialized={stats:{str:3}}]){
  const b=structuredClone(a);change(b);assert.equal(canPatchCharacterSnapshot(a,b),false);
 }
});


test('a materialized character can update numeric values without remounting the sheet',()=>{
 const a=base();a.instance.materialized={stats:{str:{type:'integer',value:12}},resourceInitial:{hp:{type:'integer',value:20}},actionIds:['hit']};
 const b=structuredClone(a);b.instance.materialized.stats.str.value=13;b.effective.stats.str.value=13;
 assert.equal(canPatchCharacterSnapshot(a,b),true);
});
