// Character-sheet presentation preferences. No mechanical character state is stored here.
export const LAYOUT_VERSION = 1;
const MAX_TABS = 32, MAX_SECTIONS = 128, MAX_ITEMS = 2048;
const builtin = [{id:'stats',name:'Статы'},{id:'actions',name:'Действия'}];
const keyFor = (scope) => `atlas-character-sheet:v1:${encodeURIComponent(String(scope).slice(0,256))}`;
const validItem = value => typeof value === 'string' && /^(stat|action):/.test(value) && value.length <= 150 && !/[\u0000-\u001f]/.test(value) && value.slice(value.indexOf(':')+1).trim().length > 0;
const cleanID = value => typeof value === 'string' && /^[^\s\u0000-\u001f]{1,180}$/.test(value) ? value : '';
const label = value => typeof value === 'string' && value.trim() ? value.trim().slice(0,120) : '';
const compare = (a,b) => a.localeCompare(b,'ru',{sensitivity:'base'}) || (a < b ? -1 : a > b ? 1 : 0);
const fresh = () => ({version:LAYOUT_VERSION,tabs:builtin.map(tab=>({...tab,sections:[]})),hidden:[]});
export function normalizeLayout(raw) {
 const layout=fresh();if(!raw||raw.version!==LAYOUT_VERSION||!Array.isArray(raw.tabs))return layout;
 const usedTabs=new Set(builtin.map(tab=>tab.id)),seenSections=new Set();
 for(const source of raw.tabs.slice(0,MAX_TABS)){
  if(!source||!cleanID(source.id))continue;
  let tab=layout.tabs.find(t=>t.id===source.id);
  if(!tab){if(usedTabs.has(source.id))continue;usedTabs.add(source.id);tab={id:source.id,name:label(source.name)||source.id,sections:[]};layout.tabs.push(tab);}
  for(const section of (Array.isArray(source.sections)?source.sections:[]).slice(0,MAX_SECTIONS)){
   if(!section||!cleanID(section.id)||seenSections.has(section.id))continue;
   seenSections.add(section.id);
   const items=[],local=new Set();
   for(const item of (Array.isArray(section.items)?section.items:[]).slice(0,MAX_ITEMS))if(validItem(item)&&!local.has(item)){local.add(item);items.push(item);}
   tab.sections.push({id:section.id,name:label(section.name)||section.id,view:section.view==='grid'?'grid':'list',collapsed:section.collapsed===true,items});
  }
 }
 // Saved tab ordering is respected; the built-in tabs remain present.
 const order=raw.tabs.map(tab=>tab?.id);layout.tabs.sort((a,b)=>order.indexOf(a.id)-order.indexOf(b.id));
 layout.hidden=[...new Set((Array.isArray(raw.hidden)?raw.hidden:[]).filter(item=>validItem(item)).slice(0,MAX_ITEMS))];
 const allocated=new Set();for(const tab of layout.tabs)for(const section of tab.sections)section.items=section.items.filter(item=>{if(allocated.has(item))return false;allocated.add(item);return true;});
 return layout;
}
export function loadLayout(scope){try{return normalizeLayout(JSON.parse(localStorage.getItem(keyFor(scope))));}catch{return fresh();}}
export function saveLayout(scope,layout){try{localStorage.setItem(keyFor(scope),JSON.stringify(normalizeLayout(layout)));return true;}catch{return false;}}
export function buildSheet(layout,entries){
 const copy=normalizeLayout(layout),exists=new Map(entries.map(entry=>[entry.key,entry]));const allocated=new Set();
 for(const tab of copy.tabs)for(const section of tab.sections)section.items=section.items.filter(key=>{if(!exists.has(key)||allocated.has(key))return false;allocated.add(key);return true;});
 const grouped=new Map();for(const entry of entries){if(allocated.has(entry.key)||copy.hidden.includes(entry.key))continue;const tab=entry.type==='action'?'actions':'stats';const name=label(entry.group)||(entry.type==='action'?'Прочие действия':'Характеристики');const groupKey=`${tab}\0${name}`;if(!grouped.has(groupKey))grouped.set(groupKey,{tab,name,items:[]});grouped.get(groupKey).items.push(entry);}
 for(const group of [...grouped.values()].sort((a,b)=>a.tab.localeCompare(b.tab)||compare(a.name,b.name))){const tab=copy.tabs.find(tab=>tab.id===group.tab),id=`auto:${group.tab}:${encodeURIComponent(group.name).slice(0,130)}`;let section=tab.sections.find(section=>section.id===id);if(!section){section={id,name:group.name,view:group.tab==='actions'?'grid':'grid',collapsed:false,items:[]};tab.sections.push(section);}group.items.sort((a,b)=>compare(a.name,b.name)||compare(a.key,b.key));section.items.push(...group.items.map(entry=>entry.key));}
 return copy;
}
export function moveEntry(layout,key,toTab,toSection,before=''){
 const copy=normalizeLayout(layout),target=copy.tabs.find(t=>t.id===toTab)?.sections.find(s=>s.id===toSection);if(!target)return copy;
 for(const tab of copy.tabs)for(const section of tab.sections)section.items=section.items.filter(item=>item!==key);
 copy.hidden=copy.hidden.filter(item=>item!==key);const index=target.items.indexOf(before);target.items.splice(index<0?target.items.length:index,0,key);return copy;
}
