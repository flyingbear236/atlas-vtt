import {humanTargetName} from './ruleset-presentation.js';
import {catalogActionRules,catalogActionSource,catalogDefinitionFields,catalogFullPath} from './stat-catalog.js';

// The catalogue and the floating inspector consume the same model and DOM.
export function actionReferenceModel(definition,group=null){
 const fields=catalogDefinitionFields(definition);
 return {
  name:definition?.name||fields.name||definition?.id||'Действие',
  id:catalogFullPath(definition?.id,'action'),
  description:fields.description||definition?.description||'',
  group:group?.id?{id:catalogFullPath(group.id,'action'),name:group.name||''}:null,
  source:catalogActionSource(definition),
  rules:catalogActionRules(definition)
 };
}

export function renderActionReference(definition,group=null,detailed=true,snapshot=null){
 const data=actionReferenceModel(definition,group);
 const root=document.createElement('div');root.className='action-reference-content';
 const heading=document.createElement('h3');heading.textContent=data.name;
 const path=document.createElement('p');path.className='action-reference-id';path.textContent=data.id;
 root.append(heading);if(detailed)root.append(path);
 const info=document.createElement('div');info.className='action-reference-info';root.append(info);
 if(data.group){
  const affiliation=document.createElement('div');affiliation.className='action-reference-group';
  affiliation.append(document.createTextNode('Группа действий'));
  const id=document.createElement('strong');id.textContent=data.group.id;if(detailed||!data.group.name)affiliation.append(id);
  if(data.group.name){const name=document.createElement('span');name.textContent=data.group.name;affiliation.append(name);}
  info.append(affiliation);
 }
 if(data.description){const description=document.createElement('p');description.className='action-reference-description';description.textContent=data.description;info.append(description);}
 if(!detailed){
  const targets=[...new Set(data.rules.filter(rule=>/(^|\.)apply(?:\.\d+)?\.path$/.test(rule.path)).map(rule=>humanTargetName(rule.value,snapshot)))];
  if(targets.length){const title=document.createElement('h4');title.textContent='Влияет на';info.append(title);for(const target of targets){const row=document.createElement('p');row.textContent=target;info.append(row);}}
  return root;
 }
 if(data.source){
  const title=document.createElement('h4');title.textContent='Полное определение действия';info.append(title);
  const pre=document.createElement('pre');pre.className='action-reference-source';
  const code=document.createElement('code');code.textContent=`[${data.id}]\n${data.source}`;pre.append(code);info.append(pre);
 }
 if(data.rules.length){
  const title=document.createElement('h4');title.textContent='Формулы и условия действия';info.append(title);
  for(const rule of data.rules){
   const row=document.createElement('div');row.className='action-reference-rule';
   const name=document.createElement('span');name.textContent=rule.path;
   const code=document.createElement('code');code.textContent=rule.value;
   row.append(name,code);info.append(row);
  }
 }
 return root;
}

export class ActionReferencePanel{
 constructor({detailed=()=>true}={}){
  this.detailed=detailed;this.openId='';this.characterId='';this.registryRevision=-1;
  this.host=document.createElement('aside');this.host.className='action-reference-shell';
  this.host.hidden=true;this.host.setAttribute('aria-label','Справочник действия');
  const close=document.createElement('button');close.type='button';close.className='action-reference-close';
  close.textContent='×';close.title='Закрыть справочник';close.setAttribute('aria-label','Закрыть справочник');
  close.onclick=()=>this.close();
  this.card=document.createElement('div');this.card.className='action-reference-card';
  this.host.append(close,this.card);document.body.append(this.host);
  document.addEventListener('keydown',event=>{if(event.key==='Escape'&&!this.host.hidden&&!document.querySelector('dialog[open]')){event.preventDefault();this.close();}});
  window.addEventListener('resize',()=>this.position());
 }
 open(snapshot,definition,group){
  if(!snapshot?.characterId||!definition?.id)return false;
  this.characterId=snapshot.characterId;this.openId=definition.id;this.registryRevision=snapshot.registryRevision;
  this.render(definition,group,snapshot);this.host.hidden=false;this.position();return true;
 }
 render(definition,group,snapshot){
  const note=document.createElement('div');note.className='action-reference-note';
  note.textContent=this.detailed()?'Справочник действия · данные из рулсета':'Справочник действия';
  this.card.replaceChildren(note,renderActionReference(definition,group,this.detailed(),snapshot));
 }
 refresh(snapshot,definition,group){
  if(this.host.hidden)return;
  if(!snapshot||snapshot.characterId!==this.characterId||!definition){this.close();return;}
  if(snapshot.registryRevision!==this.registryRevision){
   const scroll=this.card.scrollTop;this.registryRevision=snapshot.registryRevision;
   this.render(definition,group,snapshot);this.card.scrollTop=scroll;
  }
  this.position();
 }
 close(){this.host.hidden=true;this.openId='';this.characterId='';this.card.replaceChildren();}
 position(){
  if(this.host.hidden)return;
  const inspector=document.querySelector('.inspector');const rect=inspector?.getBoundingClientRect();
  const width=Math.min(this.maxWidth||555,Math.max(1,window.innerWidth-32));
  this.host.style.width=`${width}px`;
  const left=rect&&rect.left-width-12>=12?rect.left-width-12:Math.max(12,(window.innerWidth-width)/2);
  this.host.style.left=`${left}px`;
  this.host.style.top=`${Math.max(28,Math.min(95,window.innerHeight*0.1))}px`;
 }
}
