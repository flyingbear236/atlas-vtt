import test from 'node:test';
import assert from 'node:assert/strict';
import {actionTraceSummary} from './action-trace-journal.js';

test('Trace journal ignores actions without private trace',()=>{
 assert.equal(actionTraceSummary({executionId:'one',actionId:'hit'}),null);
 assert.equal(actionTraceSummary({trace:{},actionId:'hit'}),null);
});
test('Trace journal uses authoritative action result and preserved source paths',()=>{
 const trace={rollFormula:'1d20 + stat.body.strength.bonus',reads:[{path:'stat.body.strength.bonus',value:'5'}]};
 const row=actionTraceSummary({executionId:'ex',sceneId:'scene',actionId:'hit',actionName:'Попадание',characterName:'Испытатель',roll:{total:21},trace});
 assert.equal(row.title,'Попадание');assert.equal(row.roll.total,21);assert.equal(row.trace.reads[0].path,'stat.body.strength.bonus');
});
