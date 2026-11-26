import test from 'node:test';
import assert from 'node:assert/strict';
import {ActionTraceJournal,actionTraceSummary} from './action-trace-journal.js';

test('Trace journal ignores actions without private trace',()=>{
 assert.equal(actionTraceSummary({executionId:'one',actionId:'hit'}),null);
 assert.equal(actionTraceSummary({trace:{},actionId:'hit'}),null);
});
test('Trace journal uses authoritative action result and preserved source paths',()=>{
 const trace={rollFormula:'1d20 + stat.body.strength.bonus',reads:[{path:'stat.body.strength.bonus',value:'5'}]};
 const row=actionTraceSummary({executionId:'ex',sceneId:'scene',actionId:'hit',actionName:'Попадание',characterName:'Испытатель',roll:{total:21},trace});
 assert.equal(row.title,'Попадание');assert.equal(row.roll.total,21);assert.equal(row.trace.reads[0].path,'stat.body.strength.bonus');
});


test('GM history loads older records in bounded pages without losing the live stream',()=>{
 const sent=[];
 const journal=Object.create(ActionTraceJournal.prototype);
 Object.assign(journal,{enabled:true,sceneId:'scene',events:new Map(),selected:'',panel:{hidden:true},
  hasMore:false,historyCursor:0,loading:false,loadedPages:1,requestedCursor:0,
  sendPage:cmd=>{sent.push(cmd);return true;},render:()=>{}});
 const events=(from,to)=>Array.from({length:to-from+1},(_,i)=>({
  executionId:`event-${from+i}`,sceneId:'scene',actionRevision:from+i,actionId:'hit',trace:{rollFormula:'1d20'}
 }));
 assert.equal(journal.handle({type:'rollHistory',sceneId:'scene',actions:events(151,250),actionHistoryHasMore:true}),true);
 assert.equal(journal.events.size,100);
 assert.equal(journal.historyCursor,151);
 journal.loadMore();
 assert.deepEqual(sent[0],{type:'actionHistoryPage',sceneId:'scene',beforeActionRevision:151});
 journal.loadMore();assert.equal(sent.length,1,'concurrent pagination must be deduplicated');
 journal.handle({type:'actionTrace',event:{...events(251,251)[0]}});
 journal.handle({type:'actionHistoryPage',sceneId:'scene',beforeActionRevision:151,actions:events(51,150),actionHistoryHasMore:true});
 assert.equal(journal.events.size,200);
 assert.equal(journal.historyCursor,51);
 assert.ok(journal.events.has('event-251'),'live action must survive insertion of older pages');
 journal.loadMore();
 journal.handle({type:'actionHistoryPage',sceneId:'scene',beforeActionRevision:51,actions:events(1,50),actionHistoryHasMore:false});
 assert.equal(journal.events.size,250);assert.equal(journal.hasMore,false);
 assert.equal(sent.length,2);
 journal.loadMore();assert.equal(sent.length,2);
 journal.handle({type:'rollHistory',sceneId:'other',actions:[],actionHistoryHasMore:false});
 assert.equal(journal.events.size,250,'other scene must not change the journal');
});


test('a new live trace advances the older-history cursor after the display limit is reached',()=>{
 const sent=[];
 const journal=Object.create(ActionTraceJournal.prototype);
 Object.assign(journal,{enabled:true,sceneId:'s',events:new Map(),selected:'',panel:{hidden:true},
  hasMore:false,historyCursor:0,loading:false,loadedPages:1,requestedCursor:0,
  sendPage:cmd=>{sent.push(cmd);return true;},render:()=>{}});
 const event=i=>({executionId:`e${i}`,sceneId:'s',actionRevision:i,trace:{}});
 journal.handle({type:'rollHistory',sceneId:'s',actions:Array.from({length:100},(_,i)=>event(i+1)),actionHistoryHasMore:false});
 journal.handle({type:'actionTrace',event:event(101)});
 assert.equal(journal.events.size,100);
 assert.equal(journal.historyCursor,2);
 assert.equal(journal.hasMore,true);
 journal.loadMore();
 assert.equal(sent[0].beforeActionRevision,2);
});
