// Apply wire patches immutably; untouched branches retain their identity.
export function applyCharacterChanges(snapshot, changes) {
 if(!Array.isArray(changes)||changes.length>128)throw Error('Invalid character delta');
 const next={...snapshot},owned=new WeakSet([next]);
 const assign=(node,key,value)=>Object.defineProperty(node,key,{value,writable:true,enumerable:true,configurable:true});
 for(const op of changes){
  if(!Array.isArray(op.path)||!op.path.length||op.path.length>6||op.path.some(k=>typeof k!=='string'))throw Error('Invalid character path');
  if(!['instance','effective','preset','presetArtwork','statDefinitions','referenceNames','actions','permissions','registryRevision','actionSelectors','actionGroups','actionPreviews','inactiveActionSources','itemNames','itemDetails','directEffects','statFields','statTraces','traceAvailable'].includes(op.path[0]))throw Error('Invalid character field');
  let node=next;
  const validKey=(obj,key)=>!Array.isArray(obj)||(/^(0|[1-9]\d*)$/.test(key)&&Number(key)<obj.length);
  for(const key of op.path.slice(0,-1)){
   if(!validKey(node,key)||!Object.hasOwn(node,key)||!node[key]||typeof node[key]!=='object')throw Error('Invalid character branch');
   if(!owned.has(node[key])){const copy=Array.isArray(node[key])?node[key].slice():{...node[key]};owned.add(copy);assign(node,key,copy);}node=node[key];
  }
  const key=op.path.at(-1);if(!validKey(node,key)||Array.isArray(node)&&op.remove)throw Error('Invalid array patch');if(op.remove)delete node[key];else assign(node,key,op.value);
 }
 return next;
}
export class CharacterWatch {
 constructor(send,onChange=()=>{},onTrace=()=>{}){this.send=send;this.onChange=onChange;this.onTrace=onTrace;this.characterId='';this.request=0;this.revision=0;this.snapshot=null;this.pending=new Set();this.queue=[];this.traces=new Map();this.tracePending=new Map();}
 clearTracePending(){for(const timer of this.tracePending.values())clearTimeout(timer);this.tracePending.clear();}
 reset(){this.snapshot=null;this.revision=0;this.pending.clear();this.queue=[];this.traces.clear();this.clearTracePending();}
 watch(characterId){
  if(!characterId){this.unwatch();return;}
  if(this.characterId===characterId&&(this.snapshot||this.pending.has(this.request)))return;
  const previous=this.characterId;this.characterId=characterId;this.reset();this.reconnect();this.onChange(null,previous?'replaced':'pending',previous||characterId);
 }
 unwatch(){if(!this.characterId&&!this.snapshot&&!this.pending.size)return;const characterId=this.characterId,characterWatch=++this.request;this.characterId='';this.reset();this.send({type:'characterUnwatch',characterId,characterWatch});this.onChange(null,'unsubscribed',characterId);}
 disconnect(){this.reset();this.onChange(null,'disconnected',this.characterId);}
 reconnect(){if(!this.characterId)return;this.reset();const characterWatch=++this.request;this.pending.add(characterWatch);this.send({type:'characterWatch',characterId:this.characterId,characterWatch,characterDelta:true});}
 resync(){this.reconnect();this.onChange(null,'pending',this.characterId);}
 // Gate the whole ordered projection stream, not just the action that caused
 // a delta: later patches depend on every earlier sequence.
 receive(message,defer,release){
  if(!['characterSnapshot','characterDelta'].includes(message.type))return this.handle(message);
  if(message.characterWatch!==this.request||message.characterId!==this.characterId)return true;
  const entry={message,ready:false};this.queue.push(entry);
  const flush=()=>{if(message.characterWatch!==this.request)return;entry.ready=true;while(this.queue[0]?.ready){const item=this.queue.shift();this.handle(item.message);}};
  if(!message.executionId||!defer(message.executionId,flush))flush();
  // Bound pending visuals. Release the oldest gate rather than requesting a
  // snapshot that would disclose future action results.
  if(this.queue.length>128){const oldest=this.queue[0];if(oldest?.message.executionId&&release)release(oldest.message.executionId);else this.resync();}
  return true;
 }
 requestTrace(statId){const s=this.snapshot;if(!s?.traceAvailable||!s.characterSequence)return false;if(this.traces.has(statId)||this.tracePending.has(statId))return true;if(this.tracePending.size>=8)return false;const timer=setTimeout(()=>{if(this.tracePending.get(statId)!==timer)return;this.tracePending.delete(statId);this.onTrace({statId,characterId:s.characterId,characterSequence:s.characterSequence,error:'timeout'});},5000);this.tracePending.set(statId,timer);if(this.send({type:'characterStatTrace',characterId:s.characterId,characterWatch:this.request,characterSequence:s.characterSequence,statId})===false){clearTimeout(timer);this.tracePending.delete(statId);return false;}return true;}
 handle(message){
  if(!['characterSnapshot','characterDelta','characterWatchClosed','characterStatTrace'].includes(message.type))return false;
  if(message.characterWatch!==this.request||message.characterId!==this.characterId)return true;
  if(message.type==='characterStatTrace'){
   if(message.characterSequence!==this.snapshot?.characterSequence)return true;
   clearTimeout(this.tracePending.get(message.statId));this.tracePending.delete(message.statId);
   if(!message.error){this.traces.set(message.statId,message.statTraces||{});if(this.traces.size>16)this.traces.delete(this.traces.keys().next().value);}
   this.onTrace(message);return true;
  }
  this.pending.delete(message.characterWatch);
  if(message.type==='characterWatchClosed'){const characterId=this.characterId;this.characterId='';this.reset();this.onChange(null,message.reason||'closed',characterId);return true;}
  if(!Number.isSafeInteger(message.revision)||message.revision<this.revision)return true;
  if(message.type==='characterDelta'){
   if(this.snapshot&&message.characterSequence<=this.snapshot.characterSequence)return true;
   if(!this.snapshot||message.baseSequence!==this.snapshot.characterSequence||message.characterSequence!==message.baseSequence+1){this.resync();return true;}
   try{message={...applyCharacterChanges(this.snapshot,message.changes),type:'characterSnapshot',revision:message.revision,characterSequence:message.characterSequence,executionId:message.executionId};}catch{this.resync();return true;}
  }else if(message.characterSequence&&this.snapshot?.characterSequence>=message.characterSequence)return true;
  this.revision=message.revision;this.snapshot=message;this.traces.clear();this.clearTracePending();this.onChange(message,'snapshot');return true;
 }
}

export class CharacterCatalog {
  constructor(load,onChange=()=>{}){this.load=load;this.onChange=onChange;this.generation=0;this.maxItems=500;this.presets=[];this.roster=[];this.actions=[];this.next={presets:'',roster:'',actions:''};this.loading=new Set();}
  clear(){this.generation++;this.presets=[];this.roster=[];this.actions=[];this.next={presets:'',roster:'',actions:''};this.loading.clear();this.onChange();}
  async reload(){const generation=++this.generation;this.presets=[];this.roster=[];this.actions=[];this.next={presets:'',roster:'',actions:''};this.loading.clear();await Promise.all([this.#page('presets',generation),this.#page('roster',generation),this.#page('actions',generation)]);}
  more(kind){return this.#page(kind,this.generation);}
  async #page(kind,generation){
    if(generation!==this.generation||this.loading.has(kind))return;
    const cursor=this.next[kind];if(cursor===null)return;
    this.loading.add(kind);this.onChange();
    try{
      const page=await this.load(kind,cursor||'');if(generation!==this.generation)return;
      const field=kind==='presets'?'presets':kind==='actions'?'actions':'characters',target=kind==='presets'?'presets':kind==='actions'?'actions':'roster',known=new Set(this[target].map(item=>item.id));
      this[target].push(...(page[field]||[]).filter(item=>!known.has(item.id)));if(this[target].length>this.maxItems)this[target].splice(0,this[target].length-this.maxItems);this.next[kind]=page.nextCursor||null;
    }finally{if(generation===this.generation){this.loading.delete(kind);this.onChange();}}
  }
}

export function statValueFromInput(type,raw,checked=false){
  if(type==='boolean')return {type,value:!!checked};
  if(type==='integer'){
    if(!/^-?\d+$/.test(raw))throw new Error('Введите целое число');
    const value=Number(raw);if(!Number.isSafeInteger(value))throw new Error('Целое число вне безопасного диапазона');return {type,value};
  }
  if(type==='number'){
    const value=Number(raw);if(raw===''||!Number.isFinite(value))throw new Error('Введите конечное число');return {type,value};
  }
  if(type==='string')return {type,value:String(raw)};
  throw new Error('Неизвестный тип характеристики');
}

export function statInputValue(stat){return stat?.type==='boolean'?'':String(stat?.value??'');}

// Current roll specs have a single, direct numeric modifier dependency.
// Definitions are kept separate from instance values: a default is offered
// when adding a stat, but is not silently treated as an effective value.
export function actionDependencies(action,effective,definitions){
  const missing=[],errors=[],invalidIds=[],seen=new Set();
  for(const roll of action?.rolls||[]){
    const id=roll.modifierStat;
    if(!id||seen.has(id))continue;
    seen.add(id);
    const definition=definitions?.[id];
    if(!definition){invalidIds.push(id);errors.push(`Не найдено определение характеристики «${id}»`);continue;}
    if(!['integer','number'].includes(definition.type)){
      invalidIds.push(id);errors.push(`Характеристика «${definition.name||id}» должна быть числовой`);continue;
    }
    const value=effective?.stats?.[id]??effective?.resources?.[id]?.current;
    if(value===undefined)missing.push({id,definition});
    else if(!['integer','number'].includes(value.type)||!Number.isFinite(value.value)){invalidIds.push(id);errors.push(`Некорректное значение «${definition.name||id}»`);}
  }
  return {missing,errors,invalidIds};
}
