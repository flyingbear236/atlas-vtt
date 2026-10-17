import {test} from 'node:test';
import {strict as assert} from 'node:assert';
import {DiceAnimator,normalizeDiceCount,normalizeDiceGroups,cycleDiceSides,diceNotation} from './dice.js';

test('manual selector retains supported sides and count boundaries',()=>{
  assert.deepEqual([4,6,8,10,12,20].map(side=>cycleDiceSides(side)),[6,8,10,12,20,4]);
  assert.equal(normalizeDiceCount(100),100);
  assert.equal(normalizeDiceCount(101),null);
  assert.equal(normalizeDiceCount(0),null);
  assert.equal(normalizeDiceCount(2.5),null);
});

test('compound dice expressions are normalized and bounded',()=>{
  assert.deepEqual(normalizeDiceGroups('1d10 + 2d20'),[{count:1,sides:10},{count:2,sides:20}]);
  assert.equal(normalizeDiceGroups('1d7 + 2d20'),null);
  assert.equal(normalizeDiceGroups('60d10 + 41d20'),null);
  assert.equal(diceNotation({groups:[{count:1,sides:10},{count:2,sides:20}]}),'1d10 + 2d20');
});

test('a single renderer is prewarmed and reused for server outcomes',async()=>{
  let initialized=0,rendered=0,cleaned=0;const completed=[];
  const animator=new DiceAnimator({createRenderer:()=>({init:async()=>{initialized++},renderPredetermined:async()=>{rendered++},cleanup:async()=>{cleaned++}}),onFinished:event=>completed.push(event.id)});
  animator.setContext('scene','user');
  await Promise.all([animator.warmup(),animator.warmup()]);
  assert.equal(initialized,1);
  animator.enqueue({id:'roll-1',sceneId:'scene',userId:'user',count:1});
  // Allow queued microtasks to drain before verifying.
  for(let i=0;i<8&&completed.length<1;i++)await new Promise(resolve=>setImmediate(resolve));
  assert.equal(rendered,1);assert.deepEqual(completed,['roll-1']);
  await animator.cancel();assert.equal(cleaned,1);
});

test('off policy avoids renderer warmup',async()=>{
  let created=0;const animator=new DiceAnimator({createRenderer:()=>{created++;return {init:async()=>{},cleanup:async()=>{}}}});
  animator.setPolicy('off');animator.setContext('scene','user');await animator.warmup();
  assert.equal(created,0);
});

// Builder state and formula parser share a grammar; no DOM simulation needed.
test('manual formulas keep modifiers and synchronize counts',async()=>{
  const {parseDiceFormula,changeDiceCount}=await import('./dice.js');
  assert.deepEqual(parseDiceFormula('d20'),{groups:[{count:1,sides:20}],modifier:0});
  assert.deepEqual(parseDiceFormula('2d20 + 1d8 - 3'),{groups:[{count:2,sides:20},{count:1,sides:8}],modifier:-3});
  const changed=changeDiceCount(parseDiceFormula('2d20 + 5'),6,1);
  assert.equal(changed.expression,'2d20 + 1d6 + 5');
  const removed=changeDiceCount(parseDiceFormula('2d20 + 2d6'),20,-1);
  assert.equal(changeDiceCount(parseDiceFormula(removed.expression),20,-1).expression,'2d6');
  const duplicate=changeDiceCount(parseDiceFormula('1d8 + 2d8 + 3'),8,1);
  assert.equal(duplicate.groups.reduce((sum,group)=>sum+group.count,0),4);
});

test('invalid and incomplete manual formulas are rejected without evaluation',async()=>{
  const {parseDiceFormula}=await import('./dice.js');
  for(const bad of ['', '2d20 +', '2d20 - 1d6', '3d7', '3d6 + foo', '101d6', '1d20 * 3', '2d20 + 1d6 + ', '3', '2d20 1d6']){
    assert.ok(parseDiceFormula(bad).error,`${bad} was accepted`);
  }
  assert.equal(parseDiceFormula('3d6 + 2').modifier,2);
  assert.equal(parseDiceFormula('100d20 + 0').groups[0].count,100);
});
