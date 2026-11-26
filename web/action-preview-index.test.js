import {test} from 'node:test';
import {strict as assert} from 'node:assert';
import {actionFormulaPreviews,actionPreviewForSource} from './action-preview-index.js';

test('action previews index multiple item instances independently',()=>{
 const snapshot={actionPreviews:[
  {actionId:'hit',sourceId:'ring-a',formula:'A'},
  {actionId:'hit',sourceId:'ring-b',formula:'B'},
  {actionId:'hit',formula:'default'},
  {actionId:'heal',formula:'heal'}
 ]};
 assert.deepEqual(actionFormulaPreviews(snapshot,'hit').map(p=>p.formula),['A','B','default']);
 assert.equal(actionPreviewForSource(snapshot,'hit','ring-b').formula,'B');
 assert.equal(actionPreviewForSource(snapshot,'hit','').formula,'default');
 assert.equal(actionPreviewForSource(snapshot,'hit','missing'),undefined);
 assert.deepEqual(actionFormulaPreviews(snapshot,'missing'),[]);
 snapshot.actionPreviews=[{actionId:'hit',formula:'changed'}];
 assert.equal(actionPreviewForSource({...snapshot},'hit').formula,'changed');
});

test('large index lookup returns stable references and does not merge sources',()=>{
 const rows=Array.from({length:1500},(_,i)=>({actionId:i%2?'hit':'damage',sourceId:`item-${i}`,formula:`${i}`}));
 const snapshot={actionPreviews:rows};
 for(let i=0;i<rows.length;i+=53)assert.equal(actionPreviewForSource(snapshot,rows[i].actionId,rows[i].sourceId),rows[i]);
 assert.equal(actionFormulaPreviews(snapshot,'hit').length,750);
 assert.equal(actionFormulaPreviews(snapshot,'hit'),actionFormulaPreviews(snapshot,'hit'));
});
