import {test} from 'node:test';
import {strict as assert} from 'node:assert';
import {effectCounts,effectMatches,effectKey,effectStatus,effectTargetName} from './character-effects.js';
const effects=[
 {name:'Сила',id:'effect.ring_strength',sourceName:'Кольцо',sourceId:'a',sourceKind:'item',targets:['stat.strength'],status:'Активен',state:'active',value:5},
 {name:'Фокус',id:'effect.ring_focus',sourceName:'Кольцо',sourceId:'b',sourceKind:'item',targets:['stat.focus'],status:'Не складывается (unique)',state:'inactive',value:3},
 {name:'Стойка',id:'effect.open_guard',sourceName:'У персонажа',sourceId:'hero',sourceKind:'character',targets:['action.weapon.hit.roll'],status:'Условие проверяется при расчёте',state:'conditional'}
];
test('single-list counts distinguish active, conditional and inactive sources',()=>{
 assert.deepEqual(effectCounts(effects),{active:1,conditional:1,inactive:1});
 assert.equal(effectStatus(effects[1]),'inactive');
});
test('filters and search find individual item instances and direct effects',()=>{
 assert.deepEqual(effects.filter(e=>effectMatches(e,'','active')).map(e=>e.id),['effect.ring_strength','effect.open_guard']);
 assert.deepEqual(effects.filter(e=>effectMatches(e,'','inactive')).map(e=>e.id),['effect.ring_focus']);
 assert.deepEqual(effects.filter(e=>effectMatches(e,'stat.focus','all')).map(e=>e.id),['effect.ring_focus']);
 assert.deepEqual(effects.filter(e=>effectMatches(e,'КОЛЬЦО','all')).length,2);
 assert.deepEqual(effects.filter(e=>effectMatches(e,'hero','all')).length,1);
 assert.notEqual(effectKey(effects[0]),effectKey({...effects[0],sourceId:'b'}));
});

test('unequipped snapshot entries cannot be shown in active filters, even with legacy state',()=>{
 const removed={...effects[0],status:'Снят',state:'active'};
 assert.equal(effectStatus(removed),'inactive');
 assert.equal(effectMatches(removed,'','active'),false);
 assert.equal(effectMatches(removed,'','inactive'),true);
 assert.deepEqual(effectCounts([removed,effects[2]]),{active:0,conditional:1,inactive:1});
});


test('target names resolve from character definitions, including derived fields',()=>{
 const snapshot={
  statDefinitions:{'body.strength':{name:'Сила'},focus:{name:'Фокус'},signal:{name:'Сигнал'}},
  actions:{'weapon.strike.hit':{name:'Попадание'}}
 };
 assert.equal(effectTargetName('stat.body.strength',snapshot),'Сила');
 assert.equal(effectTargetName('stat.focus',snapshot),'Фокус');
 assert.equal(effectTargetName('stat.signal.gain',snapshot),'Сигнал · gain');
 assert.equal(effectTargetName('action.weapon.strike.hit.roll',snapshot),'Попадание · roll');
 assert.equal(effectTargetName('stat.unknown',snapshot),'stat.unknown');
 assert.equal(effectTargetName('action.weapon.strike.hit.roll',null),'action.weapon.strike.hit.roll');
 assert.equal(effectTargetName('stat.focus.extra',{statDefinitions:{foc:{name:'Другой'}}}),'stat.focus.extra');
});
