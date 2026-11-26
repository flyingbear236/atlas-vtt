import {test} from 'node:test';
import assert from 'node:assert/strict';
import {ActionVisualGate,ObjectReactionGate} from './action-visual-gate.js';
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));

test('action damage stays hidden until its result or message is shown',async()=>{
  const gate=new ActionVisualGate({fallbackMs:100,afterPresentationMs:5});
  const changes=[];
  assert.equal(gate.defer('attack',()=>changes.push('HP reduced')),true);
  await sleep(15);assert.deepEqual(changes,[]);
  gate.present('attack');await sleep(20);
  assert.deepEqual(changes,['HP reduced']);gate.clear();
});
test('presentation can precede projected resource updates',async()=>{
  const gate=new ActionVisualGate({fallbackMs:100,afterPresentationMs:5});
  gate.present('attack');
  let applied=0;gate.defer('attack',()=>applied++);
  await sleep(20);assert.equal(applied,1);gate.clear();
});
test('missing result cannot hold resource values indefinitely',async()=>{
  const gate=new ActionVisualGate({fallbackMs:10,afterPresentationMs:2});
  let applied=0;gate.defer('attack',()=>applied++);
  await sleep(30);assert.equal(applied,1);gate.clear();
});
test('scene switch discards deferred visuals',async()=>{
  const gate=new ActionVisualGate({fallbackMs:15,afterPresentationMs:2});
  let applied=0;gate.defer('attack',()=>applied++);gate.clear();
  await sleep(25);assert.equal(applied,0);
});

test('cancel after presentation removes pending delayed update',async()=>{
  const gate=new ActionVisualGate({fallbackMs:50,afterPresentationMs:15});
  gate.present('attack');let applied=0;gate.defer('attack',()=>applied++);gate.clear();
  await sleep(30);assert.equal(applied,0);
});

 test('object reaction waits for real result presentation, even if receipt comes first',async()=>{
 const gate=new ObjectReactionGate({afterPresentationMs:5,expiryMs:1000});const seen=[];gate.defer('a',()=>seen.push('art'));await sleep(20);assert.deepEqual(seen,[]);gate.present('a');await sleep(20);assert.deepEqual(seen,['art']);gate.defer('a',()=>seen.push('duplicate'));await sleep(20);assert.deepEqual(seen,['art']);gate.clear();
 });
 test('object reaction receipt can follow the result',async()=>{
 const gate=new ObjectReactionGate({afterPresentationMs:5,expiryMs:1000});gate.present('a');let seen=0;gate.defer('a',()=>seen++);await sleep(20);assert.equal(seen,1);gate.clear();
 });
 test('expired or cancelled object reaction never reveals the image',async()=>{
 const gate=new ObjectReactionGate({afterPresentationMs:5,expiryMs:20});let seen=0;gate.defer('a',()=>seen++);await sleep(40);gate.present('a');await sleep(20);assert.equal(seen,0);gate.defer('b',()=>seen++);gate.present('b');gate.clear();await sleep(20);assert.equal(seen,0);
 });
