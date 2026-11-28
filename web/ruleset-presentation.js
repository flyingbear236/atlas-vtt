import {catalogDefinitionFields} from './stat-catalog.js';

// A named definition takes precedence over its address. Technical suffixes
// are retained only in detailed mode or when no display name is supplied.
const fieldNames={base:'Исходное значение',current:'Текущее значение',bonus:'Бонус',roll:'Бросок',default:'Исходное значение'};
const fieldCache=new WeakMap();
function definitionFields(def){if(!def||typeof def!=='object')return {};if(!fieldCache.has(def))fieldCache.set(def,catalogDefinitionFields(def));return fieldCache.get(def);}
export const derivedDisplayName=(definition,field)=>definitionFields(definition)?.[field]?.name||(Object.hasOwn(fieldNames,field)?fieldNames[field]:field);
export function humanTargetName(target,snapshot){
 const raw=String(target||'');
 const path=raw.replace(/^(?:actor|target)\./,'');
 for(const key of [raw,path])if(Object.hasOwn(snapshot?.referenceNames||{},key))return snapshot.referenceNames[key];
 const kind=path.startsWith('stat.')?'stat':path.startsWith('action.')?'action':null;
 if(!kind)return raw;
 const defs=kind==='stat'?snapshot?.statDefinitions:snapshot?.actions;
 const rest=path.slice(kind.length+1);
 for(let id=rest;id;){
  const def=defs?.[id];
  if(def){
   const name=String(def.name||definitionFields(def).name||'').trim();
   if(!name)return path;
   const suffix=rest.slice(id.length+1);
   if(!suffix||suffix==='base'||suffix==='current'||suffix==='default')return name;
   return `${name} · ${derivedDisplayName(def,suffix)}`;
  }
  const dot=id.lastIndexOf('.');if(dot<0)break;id=id.slice(0,dot);
 }
 return path;
}
export function simpleEffectStatus(effect){
 if(effect?.state==='inactive'||/снят|unique|не складывается/i.test(effect?.status||''))return /снят/i.test(effect?.status||'')?'Снят':'Не применяется';
 if(effect?.state==='conditional'||/условие|только для/i.test(effect?.status||''))return 'При определённых условиях';
 return 'Активен';
}
