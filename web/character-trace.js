import {catalogDefinitionFields,catalogDerivedFields} from './stat-catalog.js';
import {displayRulesetValue} from './ruleset-sheet.js';

const numberText=value=>displayRulesetValue(value);
const finiteInt=value=>typeof value==='number'&&Number.isSafeInteger(value);
const baseStatAddress=id=>`stat.${id}`;
export const traceSourceEditKey=source=>source.kind==='itemEffect'
 ?`itemEffect:${source.statId}:${source.itemId}:${source.effectId}:${source.target||''}`
 :source.kind==='statField'?`statField:${source.statId}:::${source.target}`
 :`${source.kind}:${source.statId}`;

export function buildStatTraceModel(snapshot,id){
 const definition=snapshot.statDefinitions?.[id];
 if(!definition||!snapshot.statTraces)return null;
 const raw=catalogDefinitionFields(definition),root=baseStatAddress(id),statTrace=snapshot.statTraces[root];
 const resource=snapshot.effective?.resources?.[id],stat=snapshot.effective?.stats?.[id];
 if(!resource&&!stat)return null;
 const initial=resource?resource.initial.value:stat.value;
 const current=resource?resource.current.value:stat.value;
 const base=statTrace?.initial??snapshot.statFields?.[id]?.base??initial;
 const defaultValue=raw.default??definition.default?.value;
 const baseFormula=defaultValue&&typeof defaultValue==='object'?(defaultValue.formula||defaultValue.derive||''):'';
 const sources=[];
 if(resource){
  sources.push({kind:'resourceCurrent',statId:id,label:'Текущее значение',value:current,description:'У персонажа',path:`${root}.current`,editable:definition.type==='integer'&&finiteInt(current)});
 }else if(!baseFormula){
  sources.push({kind:'statBase',statId:id,label:'Исходное значение персонажа',value:base,description:'Только для этого персонажа · переопределение default',path:`${root}.base`,editable:definition.type==='integer'&&finiteInt(base)});
 }
 const appendEffects=(trace,statId)=>{
  for(const effect of trace?.effects||[]){
   const item=effect.item;
   const rawBefore=Number(effect.before),rawAfter=Number(effect.after);
   const delta=Number.isFinite(rawBefore)&&Number.isFinite(rawAfter)?rawAfter-rawBefore:null;
   const itemName=item?(snapshot.itemNames?.[effect.sourceId]||snapshot.instance?.inventory?.find(row=>row.id===effect.sourceId)?.objectId||effect.sourceId):'У персонажа';
   sources.push({kind:'itemEffect',statId,target:`stat.${statId}`,itemId:item?effect.sourceId:'',effectId:effect.effectId,
    label:itemName,description:item?`Экземпляр ${effect.sourceId}`:'Непосредственный эффект персонажа',path:`effect.${effect.effectId}`,
    value:effect.editable?effect.value:delta,display:effect.editable?(effect.value>=0?`+${effect.value}`:String(effect.value)):numberText(effect.after),
    editable:!!(item&&effect.editable&&finiteInt(effect.value)),before:effect.before,after:effect.after,formula:effect.formula});
  }
 };
 // A resource's effects modify its computed maximum, not its current HP.
 // They are shown only in the maximum section, avoiding duplicate sources.
 if(!resource)appendEffects(statTrace,id);
 const derived=catalogDerivedFields(definition,snapshot.statFields?.[id]?.derived).map(field=>{
  const address=`${root}.${field.name}`;
  const calculation=snapshot.statTraces[address];
  const literal=raw[field.name];
  // A literal integer belongs to this character once overridden. Formula
  // results and effect intermediates have no independently editable source.
  const constant=Number.isSafeInteger(literal)&&typeof literal==='number'&&
   calculation?.initial!==undefined&&finiteInt(calculation.initial)?{
    kind:'statField',statId:id,target:address,
    label:'Исходное значение персонажа',description:`Только этот персонаж · в рулсете: ${literal}`,
    path:address,value:calculation.initial,editable:true
   }:null;
  const effects=(calculation?.effects||[]).map(effect=>({
   sourceId:effect.sourceId,effectId:effect.effectId,
   name:effect.item?(snapshot.itemNames?.[effect.sourceId]||effect.sourceId):'У персонажа',
   before:effect.before,after:effect.after,value:effect.value,
   editable:!!(effect.item&&effect.editable&&finiteInt(effect.value)),
   formula:effect.formula,item:!!effect.item
  }));
  return {name:field.label||field.name,field:field.name,type:field.type|| (typeof field.current==='number'?(Number.isInteger(field.current)?'integer':'number'):typeof field.current),path:address,value:field.current,formula:field.formula,constant,calculation,effects};
 });
 return {id,name:definition.name||raw.name||root,path:root,type:definition.type||raw.type||'integer',
  description:raw.description||'',resource:!!resource,current,maximum:resource?.initial.value,
  base,baseFormula,defaultValue,sources,derived,statTrace};
}

export class CharacterTracePanel{
 constructor({save}){
  this.save=save;this.snapshot=null;this.model=null;this.edits=new Map();this.openId='';
  this.host=document.createElement('aside');this.host.className='character-trace-shell';this.host.hidden=true;this.host.setAttribute('aria-label','Трассировка характеристики');document.body.append(this.host);
  this.onKey=event=>{if(event.key==='Escape'&&!this.host.hidden){event.preventDefault();this.close();}};
  document.addEventListener('keydown',this.onKey);
  this.onResize=()=>this.position();window.addEventListener('resize',this.onResize);
 }
 open(snapshot,id){
  if(!this.host.hidden&&this.edits.size){
   if(this.openId===id&&this.snapshot?.characterId===snapshot.characterId){this.position();return true;}
   if(!confirm('Несохранённые изменения источников будут потеряны. Открыть другую характеристику?'))return false;
  }
  const model=buildStatTraceModel(snapshot,id);if(!model)return false;this.snapshot=snapshot;this.model=model;this.openId=id;this.edits.clear();this.render();this.host.hidden=false;this.position();return true;
 }
 loading(snapshot,id){
  if(this.edits.size){if(this.openId===id&&this.snapshot?.characterId===snapshot.characterId)return false;if(!confirm('Несохранённые изменения источников будут потеряны. Открыть другую характеристику?'))return false;}
  this.snapshot=snapshot;this.openId=id;this.edits.clear();this.model=null;
  const close=this.elt('button','character-trace-close','×');close.onclick=()=>this.close();
  const card=this.elt('div','character-trace-card');card.append(this.elt('h2','',snapshot.statDefinitions?.[id]?.name||id));
  const value=snapshot.effective?.resources?.[id]?.current?.value??snapshot.effective?.stats?.[id]?.value;
  card.append(this.elt('strong','',numberText(value)));this.status=this.elt('p','','Загрузка расчёта…');card.append(this.status);this.host.replaceChildren(close,card);this.host.hidden=false;this.position();return true;
 }
 traceError(message){if(!this.model&&this.status?.isConnected)this.status.textContent=message;}
 close(force=false){
  if(!force&&this.edits.size&&!confirm('Изменения источников будут потеряны. Закрыть инспектор?'))return false;
  this.host.hidden=true;this.edits.clear();this.model=null;this.snapshot=null;this.openId='';this.host.replaceChildren();return true;
 }
 position(){if(this.host.hidden)return;const panel=document.querySelector('.inspector-frame')||document.querySelector('.inspector');const rect=panel?.getBoundingClientRect();const width=Math.min(440,window.innerWidth-36);this.host.style.width=`${width}px`;
  const left=rect?Math.max(18,Math.min(rect.left-width-12,window.innerWidth-width-18)):18;
  this.host.style.left=`${left}px`;this.host.style.top=`${Math.min(140,Math.max(24,window.innerHeight*0.12))}px`;
 }
 elt(tag,klass='',text){const el=document.createElement(tag);if(klass)el.className=klass;if(text!==undefined&&text!==null)el.textContent=String(text);return el;}
 formulaNode(formula){
  const wrap=this.elt('code','character-trace-formula');const src=String(formula??'');
  const pattern=/stat\.[\p{L}\p{N}_]+(?:\.[\p{L}\p{N}_]+)*|\bvalue\b|\btarget\b/gu;
  const refs=this.snapshot?.effective?.statValues||{};let start=0;
  for(const match of src.matchAll(pattern)){
   if(match.index>start)wrap.append(document.createTextNode(src.slice(start,match.index)));
   const key=match[0],val=key==='value'?this.model.current:refs[key];
   const ref=this.elt('abbr','character-trace-ref',key);const owner=key.startsWith('stat.')?key.slice(5):'';
   const d=this.snapshot?.statDefinitions?.[owner]||this.snapshot?.statDefinitions?.[owner?.replace(/\.[^.]*$/,'')];
   ref.title=key==='value'?`${this.model.name}: значение на момент расчёта ${numberText(val)}`:`${d?.name||key}${val!==undefined?' = '+numberText(val):''}`;
   wrap.append(ref);start=match.index+key.length;
  }
  if(start<src.length)wrap.append(document.createTextNode(src.slice(start)));
  return wrap;
 }
 evaluatedFormula(formula,answer){
  const values=this.snapshot?.effective?.statValues||{};
  const substituted=String(formula??'').replace(/stat\.[\p{L}\p{N}_]+(?:\.[\p{L}\p{N}_]+)*|\bvalue\b/gu,ref=>{
   const value=ref==='value'?this.model.current:values[ref];
   return typeof value==='number'?String(value):ref;
  });
  return `${substituted} = ${numberText(answer)}`;
 }
 sourceRow(source,order){
  const row=this.elt('div','character-trace-source');
  const value=this.elt('div','character-trace-value-box');
  const key=traceSourceEditKey(source);
  const saved=this.edits.get(key);const displayed=saved===undefined?source.value:saved;
  if(source.editable){
   const button=this.elt('button','character-trace-change',source.kind==='itemEffect'&&displayed>=0?`+${displayed}`:displayed);
   button.type='button';button.title='Изменить значение этого источника';
   button.onclick=()=>{const input=this.elt('input','character-trace-input');input.type='number';input.step='1';input.value=String(displayed);input.setAttribute('aria-label',`Изменить ${source.label}`);value.replaceChildren(input);input.focus();input.select();
    const finish=()=>{if(!input.isConnected)return;const next=Number(input.value);
     if(input.value.trim()!==''&&Number.isSafeInteger(next)){
      if(next===source.value)this.edits.delete(key);else this.edits.set(key,next);
     }
     this.render();
    };
    input.onchange=finish;input.onkeydown=e=>{if(e.key==='Enter'){e.preventDefault();finish();}};
   };value.append(button);
  }else value.append(this.elt('strong','',source.display??numberText(displayed)));
  const context=this.elt('div','character-trace-source-text');context.append(this.elt('strong','',source.label));
  if(source.description)context.append(this.elt('span','',source.description));
  if(source.path)context.append(this.elt('code','',source.path));
  if(source.kind==='itemEffect'&&source.before!==undefined&&source.after!==undefined){
   const transition=this.elt('span','character-trace-transition',`${numberText(source.before)} → ${numberText(source.after)}`);
   context.append(transition);
   if(source.formula)context.append(this.elt('code','character-trace-effect-formula',source.formula));
  }
  row.append(value,context);return row;
 }
 appendTraceSteps(section,effects,statId,target){
  for(const effect of effects){
   const item=!!effect.item,sourceId=effect.sourceId;
   const name=item?(this.snapshot?.itemNames?.[sourceId]||sourceId):'У персонажа';
   const editor={kind:'itemEffect',statId,itemId:item?sourceId:'',effectId:effect.effectId,
    label:name,description:item?`Экземпляр ${sourceId}`:'Прямой эффект персонажа',
    path:`effect.${effect.effectId}`,value:effect.value,
    before:effect.before,after:effect.after,formula:effect.formula,
    display:effect.editable?undefined:numberText(effect.after),
    editable:!!(item&&effect.editable),target};
   section.append(this.sourceRow(editor));
  }
 }
 render(){const m=this.model;if(!m)return;const outer=this.host;outer.replaceChildren();
  const close=this.elt('button','character-trace-close','×');close.type='button';close.title='Закрыть инспектор';close.setAttribute('aria-label','Закрыть инспектор');close.onclick=()=>this.close();outer.append(close);
  const body=this.elt('div','character-trace-card');outer.append(body);
  const head=this.elt('header','character-trace-header');head.append(this.elt('h2','',m.name),this.elt('small','',`${m.path} · ${m.type}`));if(m.description)head.append(this.elt('p','',m.description));body.append(head);
  const calc=this.elt('div','character-trace-calculation');
  calc.append(this.elt('span','character-trace-caption',this.edits.size?'Значение до сохранения':m.resource?'Ресурс':'Итоговое значение'));
  calc.append(this.elt('strong','',m.resource?`${numberText(m.current)} / ${numberText(m.maximum)}`:numberText(m.current)));
  if(this.edits.size){
   const baseChange=this.edits.get(`statBase:${m.id}`),currentChange=this.edits.get(`resourceCurrent:${m.id}`);
   if(baseChange!==undefined&&!m.statTrace?.effects?.length){
    calc.append(this.elt('small','character-trace-preview',`После сохранения: ${numberText(baseChange)}`));
   }else if(currentChange!==undefined){
    calc.append(this.elt('small','character-trace-preview',`После сохранения: ${numberText(currentChange)} / ${numberText(m.maximum)}`));
   }else{
    calc.append(this.elt('small','character-trace-preview','Итог пересчитается после сохранения изменений.'));
   }
  }
  // The resource's current value is independent of the formula computing
  // its maximum. Never present that maximum formula as the current HP formula.
  if(!m.resource){
   if(m.baseFormula){
    calc.append(this.formulaNode(m.baseFormula));
    calc.append(this.elt('small','character-trace-explanation','Значение вычисляется по формуле рулсета. Отдельного числа default для редактирования нет.'));
   }else if(m.statTrace?.effects?.length){
    calc.append(this.elt('small','character-trace-explanation',`До эффектов: ${numberText(m.statTrace.initial)}. Порядок преобразований показан ниже.`));
   }
  }
  body.append(calc);
  const sources=this.elt('section','character-trace-sources');
  for(const [i,source] of m.sources.entries())sources.append(this.sourceRow(source,i));
  if(m.sources.length)body.append(sources);
  // For resources the computed maximum is an actual resource value, not an
  // invented derived field (such as stat.hp.max).
  if(m.resource){
   const section=this.elt('section','character-trace-derived character-trace-resource-max');
   const header=this.elt('div','character-trace-derived-head'),label=this.elt('div');
   label.append(this.elt('strong','','Максимум ресурса'),this.elt('small','',m.path));
   header.append(label,this.elt('b','character-trace-derived-value',numberText(m.maximum)));section.append(header);
   if(m.baseFormula)section.append(this.formulaNode(m.baseFormula));
   else section.append(this.elt('small','character-trace-explanation',`Исходное значение: ${numberText(m.base)}`));
   if(m.statTrace?.effects?.length)this.appendTraceSteps(section,m.statTrace.effects,m.id,m.path);
   body.append(section);
  }
  if(m.derived.length){body.append(this.elt('hr','character-trace-divider'));for(const derived of m.derived){
   const sec=this.elt('section','character-trace-derived');const title=this.elt('div','character-trace-derived-head');const label=this.elt('div');label.append(this.elt('strong','',`${derived.name} · ${derived.type}`),this.elt('small','',derived.path));title.append(label,this.elt('b','character-trace-derived-value',numberText(derived.value)));sec.append(title);
   // The formula defines the value *before* effects. Showing "10 = 7" for a
   // constant 10 changed by effects is misleading, so show the intermediate
   // value and then every transformation in execution order.
   if(derived.constant){
    const source=this.elt('div','character-trace-sources character-trace-constant');
    source.append(this.sourceRow(derived.constant,0));sec.append(source);
   }else if(derived.formula!==undefined)sec.append(this.formulaNode(derived.formula));
   if(derived.effects.length){
    if(!derived.constant&&derived.calculation?.initial!==undefined)sec.append(this.elt('small','character-trace-explanation',`До эффектов: ${numberText(derived.calculation.initial)}`));
    this.appendTraceSteps(sec,derived.effects,m.id,derived.path);
   }else if(!derived.constant&&derived.formula!==undefined){
    sec.append(this.elt('small','character-trace-explanation','Результат формулы указан справа от названия.'));
   }
   body.append(sec);
  }}
  const bar=this.elt('footer','character-trace-actions');const status=this.elt('span','',this.edits.size?'Изменения требуют сохранения':'');bar.append(status);
  const cancel=this.elt('button','character-trace-cancel',this.edits.size?'Отмена':'Закрыть');cancel.onclick=()=>this.close();bar.append(cancel);
  const save=this.elt('button','character-trace-save','Сохранить');save.disabled=!this.edits.size;
  save.onclick=()=>{
   if(!this.edits.size||!this.snapshot)return;
   const changes=[...this.edits.entries()].map(([key,value])=>{const [kind,statId,itemId,effectId,target]=key.split(':');return {kind,statId,itemId,effectId,target,value};});
   if(this.save(this.snapshot,changes))this.close(true);
  };bar.append(save);body.append(bar);
  this.position();
 }
}
