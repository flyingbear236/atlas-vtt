import {test} from 'node:test';
import {strict as assert} from 'node:assert';
import {DiceAnimator,normalizeDiceCount,cycleDiceSides} from './dice.js';

test('manual selector retains supported sides and count boundaries',()=>{
  assert.deepEqual([4,6,8,10,12,20].map(side=>cycleDiceSides(side)),[6,8,10,12,20,4]);
  assert.equal(normalizeDiceCount(100),100);
  assert.equal(normalizeDiceCount(101),null);
  assert.equal(normalizeDiceCount(0),null);
  assert.equal(normalizeDiceCount(2.5),null);
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
