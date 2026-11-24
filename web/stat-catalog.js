// RulesetFields is serialized by Go as a TOML string, rather than a JSON object.
// Read the definition-level subset needed for the inspector without loading a
// general TOML parser on every catalogue interaction.
function splitToml(value, separator=',') {
 const result=[];let start=0,depth=0,quoted='',escaped=false;
 for(let i=0;i<value.length;i++){
  const char=value[i];
  if(quoted){if(escaped)escaped=false;else if(char==='\\'&&quoted==='"')escaped=true;else if(char===quoted)quoted='';continue;}
  if(char==='"'||char==="'"){quoted=char;continue;}
  if(char==='{'||char==='[')depth++;
  else if(char==='}'||char===']')depth--;
  else if(char===separator&&depth===0){result.push(value.slice(start,i).trim());start=i+1;}
 }
 result.push(value.slice(start).trim());return result;
}
function splitAssignment(line){
 let quote='',escaped=false;
 for(let i=0;i<line.length;i++){
  const char=line[i];
  if(quote){if(escaped)escaped=false;else if(char==='\\'&&quote==='"')escaped=true;else if(char===quote)quote='';continue;}
  if(char==='"'||char==="'"){quote=char;continue;}
  if(char==='=')return [line.slice(0,i).trim(),line.slice(i+1).trim()];
 }
 return null;
}
function tomlPath(raw){return splitToml(raw,'.').map(key=>key.startsWith('"')&&key.endsWith('"')?parseTomlValue(key):key.startsWith("'")&&key.endsWith("'")?key.slice(1,-1):key).filter(Boolean);}
function parseTomlValue(source){
 const value=source.trim();
 if(value.startsWith('"')&&value.endsWith('"')){try{return JSON.parse(value);}catch{return value.slice(1,-1);}}
 if(value.startsWith("'")&&value.endsWith("'"))return value.slice(1,-1);
 if(value==='true')return true;if(value==='false')return false;
 if(/^[+-]?(?:\d[\d_]*)(?:\.[\d_]+)?(?:[eE][+-]?\d+)?$/.test(value))return Number(value.replaceAll('_',''));
 if(value.startsWith('{')&&value.endsWith('}')){
  const table={};for(const entry of splitToml(value.slice(1,-1))){if(!entry)continue;const pair=splitAssignment(entry);if(pair)assignToml(table,tomlPath(pair[0]),parseTomlValue(pair[1]));}return table;
 }
 if(value.startsWith('[')&&value.endsWith(']'))return splitToml(value.slice(1,-1)).filter(Boolean).map(parseTomlValue);
 return value;
}
function assignToml(target,path,value){
 if(!path.length||path.some(part=>['__proto__','prototype','constructor'].includes(part)))return;let node=target;
 for(const part of path.slice(0,-1)){
  if(!node[part]||typeof node[part]!=='object'||Array.isArray(node[part]))node[part]={};
  node=node[part];
 }
 node[path.at(-1)]=value;
}
export function catalogDefinitionFields(definition){
 const raw=definition?.v04;
 if(raw&&typeof raw==='object'&&!Array.isArray(raw))return raw;
 if(typeof raw!=='string'||raw.length>200000)return {};
 const result={};let section=[];
 for(const source of raw.split(/\r?\n/)){
  const line=source.trim();if(!line||line.startsWith('#'))continue;
  if(line.startsWith('[')&&line.endsWith(']')){section=tomlPath(line.slice(1,-1));continue;}
  const pair=splitAssignment(line);if(!pair)continue;
  assignToml(result,[...section,...tomlPath(pair[0])],parseTomlValue(pair[1]));
 }
 return result;
}

// Discover dependencies only from expressions that are actually present in
// the exposed definitions; never guess a missing ruleset field.
export function statDependencies(id, definitions) {
 const definition = definitions?.[id];
 if (!definition) return [];
 const formulas = [];
 const visit = (value, depth = 0) => {
  if (depth > 8 || value == null) return;
  if (Array.isArray(value)) { for (const item of value) visit(item, depth + 1); return; }
  if (typeof value !== 'object') return;
  for (const [key, part] of Object.entries(value)) {
   if ((key === 'formula' || key === 'derive') && typeof part === 'string') formulas.push(part);
   else if (part && typeof part === 'object') visit(part, depth + 1);
  }
 };
 visit(catalogDefinitionFields(definition));
 const result = new Set();
 const ids=Object.keys(definitions||{}).filter(other=>other!==id).sort((a,b)=>b.length-a.length);
 for(const formula of formulas){
  let offset=0,at;
  while((at=formula.indexOf('stat.',offset))!==-1){
   offset=at+5;
   const before=at?formula[at-1]:'';
   if(before&&/[\p{L}\p{N}_.]/u.test(before))continue;
   for(const other of ids){
    if(!formula.startsWith(other,offset))continue;
    const after=formula[offset+other.length]||'';
    if(after&&/[\p{L}\p{N}_]/u.test(after))continue;
    result.add(other);break;
   }
  }
 }
 return [...result];
}

export function collectStatDependencies(id, definitions, assignedIds) {
 const assigned = new Set(assignedIds), needed = [], missing = [], visited = new Set();
 const visit = (statId, chain) => {
  if (visited.has(statId)) return;
  visited.add(statId);
  for (const dependency of statDependencies(statId, definitions)) {
   if (chain.includes(dependency)) continue;
   if (!assigned.has(dependency)) {
    if (definitions[dependency]) { visit(dependency, [...chain, dependency]); if (!needed.includes(dependency)) needed.push(dependency); }
    else if (!missing.includes(dependency)) missing.push(dependency);
   }
  }
 };
 visit(id, [id]);
 return { needed, missing };
}


// Definitions use IDs without the `stat.`/`action.` namespace prefix in snapshots.
export function catalogFullPath(id, kind='stat') {
 const prefix=kind==='action'?'action':'stat';
 return String(id||'').startsWith(prefix+'.')?String(id):`${prefix}.${id||''}`;
}

export function catalogParentPath(id) {
 const parts=String(id||'').replace(/^stat\./,'').split('.');
 return parts.length>1?'stat.'+parts.slice(0,-1).join('.'):'';
}

const statMetadata=new Set(['name','description','type','kind','group','resource','default','id','v04','modifiers','effects']);
export function catalogDerivedFields(definition, currentFields=[]) {
 const live=new Map((currentFields||[]).map(field=>[field.name,field.value]));
 const results=[];
 for(const [name,raw] of Object.entries(catalogDefinitionFields(definition))) {
  if(statMetadata.has(name))continue;
  // Ignore non-derived metadata; derived entries are literals or formula nodes.
  const isFormula=raw&&typeof raw==='object'&&!Array.isArray(raw)&&(typeof raw.formula==='string'||typeof raw.derive==='string');
  if(!isFormula&&typeof raw!=='number'&&typeof raw!=='boolean'&&typeof raw!=='string')continue;
  const expression=isFormula?(raw.formula||raw.derive):String(raw);
  const field={name,formula:expression,current:live.get(name)};
  if(isFormula&&typeof raw.name==='string')field.label=raw.name;
  if(isFormula&&typeof raw.type==='string')field.type=raw.type;
  results.push(field);
  live.delete(name);
 }
 // Some older definitions expose only the evaluated fields in the snapshot.
 for(const [name,current] of live)results.push({name,current});
 return results;
}

export function catalogActionRules(definition) {
 const result=[];
 const visit=(value,path='',depth=0)=>{
  if(result.length>=40||depth>5||value==null)return;
  if(typeof value!=='object') {result.push({path,value:String(value)});return;}
  if(Array.isArray(value)){for(const item of value)visit(item,path,depth+1);return;}
  for(const [key,item] of Object.entries(value)){
   if(!path&&['name','description','id','group','type'].includes(key))continue;
   visit(item,path?path+'.'+key:key,depth+1);
  }
 };
 visit(catalogDefinitionFields(definition));
 return result;
}


// A metadata-only action with descendant actions is a group, not an
// executable catalogue choice. Keep executable parents if they have rules.
const actionGroupMetadata = new Set(['name','description','group','id','type']);
const isActionGroupOnly = definition => Object.keys(catalogDefinitionFields(definition)).every(key => actionGroupMetadata.has(key));
export function catalogActionEntries(definitions) {
 const actions=Object.values(definitions||{}).filter(def=>def?.id&&def.v04);
 const byId=new Map(actions.map(def=>[def.id,def]));
 const childCount=new Map();
 for(const def of actions){
  const parts=def.id.split('.');
  for(let length=1;length<parts.length;length++){
   const parent=parts.slice(0,length).join('.');
   childCount.set(parent,(childCount.get(parent)||0)+1);
  }
 }
 return actions.filter(def=>!(childCount.get(def.id)&&isActionGroupOnly(def))).map(def=>{
  let group=null;
  const parts=def.id.split('.');
  for(let length=parts.length-1;length>0;length--){
   const id=parts.slice(0,length).join('.'),parent=byId.get(id);
   if(parent&&isActionGroupOnly(parent)){
    group={id,name:parent.name||catalogDefinitionFields(parent).name||''};break;
   }
   if(!group&&length===1&&childCount.get(id)>1)group={id,name:''};
  }
  return {def,group};
 });
}

// v0.4 definitions are serialized as TOML by the server. Display the complete
// serialized action, then keep the existing individual fields underneath.
export function catalogActionSource(definition) {
 if(typeof definition?.v04==='string')return definition.v04.trim();
 if(definition?.v04&&typeof definition.v04==='object')return JSON.stringify(definition.v04,null,2);
 return '';
}
