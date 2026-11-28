import {test} from 'node:test';
import assert from 'node:assert/strict';
import {humanTargetName,derivedDisplayName,simpleEffectStatus} from './ruleset-presentation.js';
const snapshot={statDefinitions:{strength:{name:'Сила',v04:{modifier:{name:'Модификатор силы'}}},hp:{name:'Здоровье'},unnamed:{name:''}},referenceNames:{'stat.external.current':'Мана'}};
test('human targets prefer display names, including references outside the sheet',()=>{
 assert.equal(humanTargetName('target.stat.strength.base',snapshot),'Сила');
 assert.equal(humanTargetName('stat.strength.modifier',snapshot),'Сила · Модификатор силы');
 assert.equal(humanTargetName('stat.hp.current',snapshot),'Здоровье');
 assert.equal(humanTargetName('target.stat.external.current',snapshot),'Мана');
 assert.equal(humanTargetName('stat.unnamed.base',snapshot),'stat.unnamed.base');
 assert.equal(derivedDisplayName(snapshot.statDefinitions.strength,'bonus'),'Бонус');
});
test('simple effect status omits technical stacking terminology',()=>{
 assert.equal(simpleEffectStatus({state:'inactive',status:'unique'}),'Не применяется');
 assert.equal(simpleEffectStatus({state:'conditional'}),'При определённых условиях');
});
