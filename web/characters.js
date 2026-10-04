export class CharacterWatch {
  constructor(send,onChange=()=>{}){this.send=send;this.onChange=onChange;this.characterId='';this.request=0;this.revision=0;this.snapshot=null;this.pending=new Set();}
  watch(characterId){
    if(!characterId){this.unwatch();return;}
    if(this.characterId===characterId&&(this.snapshot||this.pending.has(this.request)))return;
    const previous=this.characterId;this.characterId=characterId;this.snapshot=null;this.revision=0;this.pending.clear();
    const characterWatch=++this.request;this.pending.add(characterWatch);
    this.send({type:'characterWatch',characterId,characterWatch});this.onChange(null,previous?'replaced':'pending',previous||characterId);
  }
  unwatch(){
    if(!this.characterId&&!this.snapshot&&!this.pending.size)return;
    const characterId=this.characterId,characterWatch=++this.request;
    this.characterId='';this.snapshot=null;this.revision=0;this.pending.clear();
    this.send({type:'characterUnwatch',characterId,characterWatch});this.onChange(null,'unsubscribed',characterId);
  }
  disconnect(){this.snapshot=null;this.revision=0;this.pending.clear();this.onChange(null,'disconnected',this.characterId);}
  reconnect(){
    if(!this.characterId)return;
    const characterWatch=++this.request;this.pending.clear();this.pending.add(characterWatch);
    this.send({type:'characterWatch',characterId:this.characterId,characterWatch});
  }
  handle(message){
    if(message.type!=='characterSnapshot'&&message.type!=='characterWatchClosed')return false;
    if(message.characterWatch!==this.request||message.characterId!==this.characterId)return true;
    this.pending.delete(message.characterWatch);
    if(message.type==='characterWatchClosed'){
      const characterId=this.characterId;this.characterId='';this.snapshot=null;this.revision=0;this.pending.clear();this.onChange(null,message.reason||'closed',characterId);return true;
    }
    if(!Number.isSafeInteger(message.revision)||message.revision<this.revision)return true;
    this.revision=message.revision;this.snapshot=message;this.onChange(message,'snapshot');return true;
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
