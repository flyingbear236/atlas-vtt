import {test} from 'node:test';
import {strict as assert} from 'node:assert';
import {actionDependencies} from './characters.js';

test('action dependencies distinguish defaults, missing values and errors',()=>{
  const action={rolls:[{modifierStat:'insight_bonus'},{modifierStat:'insight_bonus'},{modifierStat:'wisdom'}]};
  const definitions={insight_bonus:{id:'insight_bonus',name:'Проницательность',type:'integer',default:{type:'integer',value:0}},wisdom:{id:'wisdom',type:'number'}};
  const values={stats:{wisdom:{type:'number',value:12}},resources:{}};
  assert.deepEqual(actionDependencies(action,values,definitions).missing.map(v=>v.id),['insight_bonus']);
  assert.equal(actionDependencies(action,{stats:{...values.stats,insight_bonus:{type:'integer',value:0}}},definitions).missing.length,0);
  assert.equal(actionDependencies(action,{stats:{}},{wisdom:definitions.wisdom}).errors.length,1);
  assert.deepEqual(actionDependencies({rolls:[{modifierStat:'hp'}]},{stats:{}},{hp:{id:'hp',type:'string'}}).errors.length,1);
});
