import test from 'node:test';
import assert from 'node:assert/strict';
import {CharacterWatch,applyCharacterChanges} from './characters.js';

function setup(){
 const sent=[],changes=[],traces=[];
 const watch=new CharacterWatch(m=>sent.push(m),(s)=>changes.push(s),m=>traces.push(m));
 watch.watch('hero');
 const base={characterId:'hero',characterWatch:watch.request,revision:1,characterSequence:1,type:'characterSnapshot',traceAvailable:true,effective:{stats:{hp:{value:10}}},actions:{attack:{name:'Attack'}},instance:{inventory:[{id:'one'}]}};
 watch.handle(base);
 const delta=(seq,value,executionId)=>({type:'characterDelta',characterId:'hero',characterWatch:base.characterWatch,revision:seq,baseSequence:seq-1,characterSequence:seq,executionId,changes:[{path:['effective','stats','hp','value'],value}]});
 return {watch,sent,changes,traces,base,delta};
}
test('delta preserves immutable unchanged branches and supports array entries and removals',()=>{
 const {base}=setup();
 const next=applyCharacterChanges(base,[{path:['effective','stats','hp','value'],value:8},{path:['instance','inventory','0'],value:{id:'two'}},{path:['effective','stats','new'],value:null}]);
 assert.equal(base.effective.stats.hp.value,10);assert.equal(next.effective.stats.hp.value,8);assert.equal(next.instance.inventory[0].id,'two');assert.equal(base.instance.inventory[0].id,'one');assert.equal(next.actions,base.actions);assert.equal(next.effective.stats.new,null);
 assert.equal(Object.hasOwn(applyCharacterChanges(next,[{path:['effective','stats','new'],remove:true}]).effective.stats,'new'),false);
 assert.throws(()=>applyCharacterChanges(base,[{path:['effective','__proto__','polluted'],value:true}]));assert.equal({}.polluted,undefined);
});
test('animation gate preserves delta order including ungated later updates',()=>{
 const {watch,delta,sent}=setup();const gates=new Map();const defer=(id,cb)=>{gates.set(id,cb);return true;};
 watch.receive(delta(2,8,'first'),defer);watch.receive(delta(3,6,'second'),defer);watch.receive(delta(4,5),defer);
 gates.get('second')();assert.equal(watch.snapshot.characterSequence,1);gates.get('first')();assert.equal(watch.snapshot.effective.stats.hp.value,5);assert.equal(watch.snapshot.characterSequence,4);assert.equal(sent.length,1);
 watch.handle(delta(4,99));assert.equal(watch.snapshot.effective.stats.hp.value,5);
});
test('missing base triggers new watch and late old messages cannot overwrite it',()=>{
 const {watch,delta,sent,base}=setup();watch.handle(delta(3,6));assert.equal(watch.snapshot,null);assert.equal(sent.length,2);assert.equal(sent[1].characterDelta,true);watch.handle(delta(2,8));assert.equal(watch.snapshot,null);
 watch.handle({...base,characterWatch:watch.request,revision:3});assert.equal(watch.snapshot.characterSequence,1);
});
test('trace request deduplicates, caches and rejects older projection responses',()=>{
 const {watch,delta,sent,traces,base}=setup();assert.equal(watch.requestTrace('hp'),true);watch.requestTrace('hp');assert.equal(sent.length,2);
 const result={type:'characterStatTrace',characterId:'hero',characterWatch:base.characterWatch,characterSequence:1,statId:'hp',statTraces:{'stat.hp':{value:10}}};
 watch.handle(result);assert.equal(traces.length,1);assert.equal(watch.traces.has('hp'),true);watch.requestTrace('hp');assert.equal(sent.length,2);
 watch.handle(delta(2,8));assert.equal(watch.traces.size,0);watch.handle(result);assert.equal(traces.length,1);watch.requestTrace('hp');assert.equal(sent.at(-1).characterSequence,2);watch.disconnect();assert.equal(watch.tracePending.size,0);
});
test('revoked watch discards gated updates and trace state',()=>{
 const {watch,delta,base}=setup();let release;watch.receive(delta(2,8,'attack'),(_,cb)=>{release=cb;return true;});watch.requestTrace('hp');watch.handle({type:'characterWatchClosed',characterId:'hero',characterWatch:base.characterWatch,reason:'accessRevoked'});release();assert.equal(watch.snapshot,null);assert.equal(watch.queue.length,0);assert.equal(watch.tracePending.size,0);
});
