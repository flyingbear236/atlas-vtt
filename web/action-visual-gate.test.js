import {test} from 'node:test';
import assert from 'node:assert/strict';
import {ActionVisualGate} from './action-visual-gate.js';
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
