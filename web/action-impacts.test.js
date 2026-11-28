import {test} from 'node:test';
import assert from 'node:assert/strict';
import {ActionImpacts,impactLabel,impactShake} from './action-impacts.js';

const tokens={a:{id:'a',characterInstanceId:'attacker',floorId:'f',x:0,y:0},b:{id:'b',characterInstanceId:'victim',floorId:'f',x:60,y:0}};
const hit={executionId:'hit',actorId:'attacker',targetId:'victim',changes:[{name:'Здоровье',delta:'-2',negative:true}]};
test('confirmed damage flies, shakes only the target, expires and deduplicates',()=>{
 const effects=new ActionImpacts();
 assert.equal(impactLabel(hit.changes[0]),'−2 Здоровье');
 assert.equal(effects.add(hit,tokens,0),true);
 assert.equal(effects.add(hit,tokens,0),false);
 effects.update(150);assert.equal(effects.offsets.size,0);
 effects.update(350);assert.notEqual(effects.offsets.get('b'),0);assert.equal(effects.offsets.has('a'),false);
 effects.update(1351);assert.equal(effects.active,false);assert.equal(effects.offsets.size,0);
});
test('gain has a positive label, no shake, and no projectile for unloaded actor',()=>{
 const effects=new ActionImpacts(),gain={...hit,executionId:'gain',changes:[{name:'Сила',delta:'2',negative:false}]};
 assert.equal(impactLabel(gain.changes[0]),'+2 Сила');assert.equal(impactShake(50,false),0);
 effects.add(gain,{b:tokens.b},0);assert.equal(effects.entries[0].flight,0);assert.equal(effects.entries[0].positive,true);
 effects.update(400);assert.equal(effects.offsets.size,0);effects.clear();assert.equal(effects.active,false);
});
test('effects and deduplication are bounded even during bursts',()=>{
 const effects=new ActionImpacts();for(let i=0;i<200;i++)effects.add({...hit,executionId:String(i)},tokens,0);
 assert.equal(effects.entries.length,32);assert.equal(effects.seen.size,128);
 assert.equal(effects.add({...hit,executionId:'absent',targetId:'invisible'},tokens,0),false);
});
