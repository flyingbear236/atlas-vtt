import {ActionReferencePanel} from './action-reference.js';

// Both the inventory drawer and the character-wide effects tab use the same
// server-provided entries. Never infer an active bonus from the TOML formula.
// Item effects are already present in itemDetails. Compose the character-wide
// list once per immutable snapshot to avoid duplicating them on the wire.
const combinedEffects = new WeakMap();
export function characterEffectsFromSnapshot(snapshot){
 if(!snapshot)return [];
 if(Array.isArray(snapshot.characterEffects))return snapshot.characterEffects;
 let effects=combinedEffects.get(snapshot);
 if(effects)return effects;
 effects=[];
 for(const item of snapshot.instance?.inventory||[])effects.push(...(snapshot.itemDetails?.[item.id]?.effects||[]));
 effects.push(...(snapshot.directEffects||[]));
 combinedEffects.set(snapshot,effects);
 return effects;
}
export function effectStatus(effect){
 // Older snapshots can have state="active" with a later "Снят" status.
 if(/снят|unique|не складывается/i.test(effect?.status||''))return 'inactive';
 if(['active','conditional','inactive'].includes(effect?.state))return effect.state;
 if(/условие|только для/i.test(effect?.status||''))return 'conditional';
 return 'active';
}
export function effectCounts(effects=[]){
 const counts={active:0,conditional:0,inactive:0};
 for(const effect of effects)counts[effectStatus(effect)]++;
 return counts;
}
export function effectMatches(effect,query='',filter='all'){
 const state=effectStatus(effect);
 if(filter==='active'&&state==='inactive'||filter==='inactive'&&state!=='inactive')return false;
 const needle=query.trim().toLocaleLowerCase();
 if(!needle)return true;
 return [effect.name,effect.id,effect.sourceName,effect.sourceId,effect.status,effect.formula,...(effect.targets||[])].join(' ').toLocaleLowerCase().includes(needle);
}
export function effectKey(effect){return `${effect.sourceKind}:${effect.sourceId}:${effect.id}`;}
// Use the same names that the character sheet already receives. Match the
// longest definition path so derived targets still resolve to their parent.
export function effectTargetName(target,snapshot){
 const path=String(target||'');
 const kind=path.startsWith('stat.')?'stat':path.startsWith('action.')?'action':null;
 if(!kind)return path;
 const definitions=kind==='stat'?snapshot?.statDefinitions:snapshot?.actions;
 if(!definitions)return path;
 const rest=path.slice(kind.length+1);
 for(let id=rest;id;){
  const name=definitions[id]?.name;
  if(typeof name==='string'&&name.trim()&&name!==id){
   const suffix=rest.slice(id.length).replace(/^\./,'');
   return suffix?`${name} · ${suffix}`:name;
  }
  const lastDot=id.lastIndexOf('.');
  if(lastDot<0)break;
  id=id.slice(0,lastDot);
 }
 return path;
}
const node=(tag,cls='',value)=>{const el=document.createElement(tag);if(cls)el.className=cls;if(value!==undefined)el.textContent=String(value);return el;};
const effectValue=effect=>effect.value===undefined||effect.value===null?'ƒ':`${Number(effect.value)>0?'+':''}${effect.value}`;

export function renderEffectCard(effect,onInspect,snapshot){
 const button=node('button','character-effect-card');button.type='button';button.title=`Подробнее об эффекте ${effect.name||effect.id}`;
 const head=node('span','character-effect-card-head');head.append(node('strong','character-effect-name',effect.name||effect.id),node('b','character-effect-value',effectValue(effect)));
 const id=node('span','character-effect-id',effect.id);
 const info=node('span','character-effect-card-meta');info.append(node('span',effectStatus(effect)==='inactive'?'character-effect-status is-inactive':'character-effect-status',effect.status||'Активен'));
 button.append(head,id,info);
 if(effect.targets?.length){const target=node('span','character-effect-target');target.title=effect.targets.join(' · ');target.append(node('span','character-effect-field-label','Меняет: '),document.createTextNode(effect.targets.map(path=>effectTargetName(path,snapshot)).join(' · ')));button.append(target);}
 if(effect.formula){const formula=node('code','character-effect-formula');formula.append(node('span','character-effect-field-label','Формула: '),document.createTextNode(effect.formula));button.append(formula);}
 if(effect.condition)button.append(node('span','character-effect-when',`Условие: ${effect.condition}`));
 if(effectStatus(effect)==='inactive')button.classList.add('is-inactive');
 button.onclick=()=>onInspect(effect);return button;
}
export function renderEffectRow(effect,onInspect,snapshot){
 const button=node('button','character-effect-row');button.type='button';button.title='Посмотреть источник и формулу эффекта';
 const text=node('span','character-effect-row-info');text.append(node('strong','character-effect-name',effect.name||effect.id));
 text.append(node('span','character-effect-origin',effect.sourceKind==='character'?'У персонажа':`${effect.sourceName||'Предмет'} · ${effect.sourceId}`));
 const targets=node('span','character-effect-target');targets.title=effect.targets?.join(' · ')||effect.id;targets.append(node('span','character-effect-field-label','Меняет: '),document.createTextNode(effect.targets?.length?effect.targets.map(path=>effectTargetName(path,snapshot)).join(' · '):effect.id));text.append(targets);
 const end=node('span','character-effect-row-right');end.append(node('b','character-effect-value',effectValue(effect)),node('span','character-effect-status',effect.status||'Активен'));
 button.append(text,end);
 if(effectStatus(effect)==='inactive')button.classList.add('is-inactive');
 button.onclick=()=>onInspect(effect);return button;
}

export class EffectReferencePanel extends ActionReferencePanel{
 constructor({openItem}){
  super();this.openItem=openItem;this.key='';this.snapshotRevision=-1;
  this.host.setAttribute('aria-label','Инспектор эффекта');this.host.classList.add('character-effect-inspector');
 }
 open(snapshot,effect){
  if(!snapshot?.characterId||!effect)return false;
  this.characterId=snapshot.characterId;this.openId=effect.id;this.key=effectKey(effect);this.snapshotRevision=snapshot.revision;
  this.renderEffect(effect,snapshot);this.host.hidden=false;this.position();return true;
 }
 renderEffect(effect,snapshot){
  const root=node('div','character-effect-reference');
  root.append(node('h3','',effect.name||effect.id),node('p','character-effect-id',effect.id));
  const block=(heading)=>{const wrapper=node('section','character-effect-reference-block');wrapper.append(node('h4','',heading));root.append(wrapper);return wrapper;};
  const line=(parent,label,value)=>{const row=node('div','character-effect-reference-line');row.append(node('span','',label),node('strong','',value));parent.append(row);};
  const state=block('Состояние');
  line(state,'Статус',effect.status||'Активен');
  line(state,'Источник',effect.sourceName||'У персонажа');
  if(effect.sourceKind==='item')line(state,'Экземпляр',effect.sourceId);
  if(effect.stack)line(state,'Наложение',effect.stack);
  if(effect.scope)line(state,'Область действия',effect.scope);
  const targets=block('На что влияет');
  for(const target of effect.targets||[]){
   const name=effectTargetName(target,snapshot);
   if(name===target){line(targets,'Цель',target);continue;}
   const row=node('div','character-effect-reference-line');
   const value=node('strong','character-effect-reference-target');
   value.append(node('span','',name),node('code','',target));
   row.append(node('span','','Цель'),value);targets.append(row);
  }
  const formula=block('Формула из рулсета');
  if(effect.formula)formula.append(node('code','character-effect-reference-formula',effect.formula));
  if(effect.condition)formula.append(node('p','character-effect-reference-condition',`Условие: ${effect.condition}`));
  if(effect.value!==undefined&&effect.value!==null)formula.append(node('p','character-effect-reference-condition',`Бонус источника: ${effectValue(effect)}`));
  if(effect.sourceKind==='item'){
   const open=node('button','subtle character-effect-source-link',`Открыть предмет «${effect.sourceName||effect.sourceId}»`);
   open.type='button';open.onclick=()=>{this.close();this.openItem(effect.sourceId);};root.append(open);
  }
  root.append(node('p','character-effect-reference-note','Результаты применения и пропуска при выполнении действий доступны в журнале трассировки.'));
  this.card.replaceChildren(root);
 }
 refresh(snapshot){
  if(this.host.hidden)return;
  if(!snapshot||snapshot.characterId!==this.characterId){this.close();return;}
  const effect=characterEffectsFromSnapshot(snapshot).find(entry=>effectKey(entry)===this.key);
  if(!effect){this.close();return;}
  if(this.snapshotRevision!==snapshot.revision){const scroll=this.card.scrollTop;this.renderEffect(effect,snapshot);this.card.scrollTop=scroll;this.snapshotRevision=snapshot.revision;}
  this.position();
 }
 close(){super.close();this.key='';}
}
