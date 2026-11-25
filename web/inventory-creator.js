// GM inventory authoring. Definitions are fetched only when the picker opens;
// the ordinary character watch remains small and only holds owned items.
export function inventoryGrantPayload(characterId, selected, count, equipped, draft=null) {
 if(!characterId||!Number.isSafeInteger(count)||count<1||count>20)throw new Error('Укажите количество от 1 до 20');
 if(draft){
  const name=String(draft.name||'').trim();
  if(!name)throw new Error('Укажите название предмета');
  if(name.length>120)throw new Error('Слишком длинное название');
  const properties={};
  for(const entry of draft.properties||[]){
   const key=String(entry.name||'').trim(),value=String(entry.value||'');
   if(!/^[A-Za-z][A-Za-z0-9_]{0,63}$/.test(key)||['id','name','description','actions','effects','equipped','object','effect_overrides'].includes(key))throw new Error(`Некорректное имя свойства: ${key}`);
   if(Object.hasOwn(properties,key))throw new Error(`Повторяющееся свойство: ${key}`);
   properties[key]=value;
  }
  return {type:'characterItemCreate',payload:{characterId,itemCount:count,equipped,objectDraft:{name,description:String(draft.description||''),properties,actions:draft.actions||[],effects:draft.effects||[]}}};
 }
 if(!selected)throw new Error('Выберите предмет');
 return {type:'characterItemAdd',payload:{characterId,objectId:selected,itemCount:count,equipped}};
}

const make=(tag,cls='',text='')=>{const el=document.createElement(tag);if(cls)el.className=cls;if(text)el.textContent=text;return el;};
const field=(title,input)=>{const label=make('label','item-create-field');label.append(make('span','',title),input);return label;};
const inp=(placeholder,type='text')=>{const el=make('input');el.type=type;el.placeholder=placeholder;return el;};

export function createInventoryCreator({loadCatalog,submit,toast}) {
 const dialog=make('dialog','character-item-create-dialog');
 dialog.setAttribute('aria-label','Выдать или создать предмет');
 document.body.append(dialog);
 let charId='',catalog=null,mode='existing',chosen='',query='';
 const header=make('div','item-create-header');
 const head=make('div');head.append(make('h2','','Добавить предмет'),make('small','','В инвентарь выбранного персонажа'));
 const close=make('button','subtle item-create-close','×');close.type='button';close.setAttribute('aria-label','Закрыть');close.onclick=()=>dialog.close();header.append(head,close);
 const tabs=make('div','item-create-tabs');
 for(const [id,label] of [['existing','Существующие'],['new','Новый']]){const b=make('button','subtle',label);b.type='button';b.dataset.mode=id;b.onclick=()=>{mode=id;sync();};tabs.append(b);}
 const content=make('div','item-create-content');
 const existing=make('div','item-create-existing');
 const search=inp('Поиск по названию или ID','search');search.setAttribute('aria-label','Поиск предметов');search.oninput=()=>{query=search.value.toLocaleLowerCase();renderObjects();};
 const split=make('div','item-create-split');const list=make('div','item-create-list');const preview=make('div','item-create-preview');split.append(list,preview);existing.append(search,split);
 const creation=make('div','item-create-new');
 const name=inp('Например, Эликсир силы');name.maxLength=120;
 const desc=make('textarea');desc.rows=3;desc.maxLength=4096;desc.placeholder='Описание предмета';
 creation.append(field('Название',name),field('Описание',desc));
 const attrsHead=make('div','item-create-section-title');attrsHead.append(make('strong','','Свойства'));
 const addAttr=make('button','subtle','+ Свойство');addAttr.type='button';attrsHead.append(addAttr);
 const attrs=make('div','item-create-attrs');
 const addProperty=(key='',value='')=>{
  if(attrs.children.length>=24)return;
  const row=make('div','item-create-attribute');
  const k=inp('damage');k.maxLength=64;k.value=key;k.setAttribute('aria-label','Имя свойства');
  const v=inp('1d8');v.maxLength=512;v.value=value;v.setAttribute('aria-label','Значение свойства');
  const del=make('button','subtle','×');del.type='button';del.setAttribute('aria-label','Удалить свойство');del.onclick=()=>row.remove();row.append(k,v,del);attrs.append(row);
 };addAttr.onclick=()=>addProperty();
 creation.append(attrsHead,attrs);
 const grants=make('div','item-create-grants');creation.append(grants);
 const footer=make('div','item-create-footer');
 const count=inp('1','number');count.value='1';count.min='1';count.max='20';count.step='1';
 const equip=make('input');equip.type='checkbox';
 const equipLabel=field('Сразу экипировать',equip);equipLabel.classList.add('item-create-check');
 const action=make('button','primary','Выдать персонажу');action.type='button';
 footer.append(field('Количество',count),equipLabel,action);
 dialog.append(header,tabs,content,footer);
 function renderObjects(){
  list.replaceChildren();preview.replaceChildren();
  const objects=(catalog?.objects||[]).filter(item=>`${item.name} ${item.id} ${item.description||''}`.toLocaleLowerCase().includes(query));
  for(const item of objects){const b=make('button','item-create-catalog-row');b.type='button';b.setAttribute('aria-pressed',String(chosen===item.id));b.append(make('strong','',item.name),make('small','',item.id));b.onclick=()=>{chosen=item.id;renderObjects();};list.append(b);}
  if(!objects.length)list.append(make('p','muted','Предметы не найдены'));
  const item=catalog?.objects?.find(item=>item.id===chosen);
  if(!item){preview.append(make('p','muted','Выберите определение предмета слева'));return;}
  preview.append(make('h3','',item.name),make('small','',`object.${item.id}`));
  if(item.description)preview.append(make('p','',item.description));
  if(item.properties?.length){preview.append(make('h4','','Свойства'));for(const row of item.properties.slice(0,20))preview.append(make('div','item-create-catalog-detail',`${row.path?.join('.')}: ${row.value}`));}
  const allActions=new Map((catalog.actions||[]).map(a=>[a.id,a.name]));const allEffects=new Map((catalog.effects||[]).map(a=>[a.id,a.name]));
  if(item.actions?.length){preview.append(make('h4','','Действия'));for(const id of item.actions)preview.append(make('div','item-create-catalog-detail',allActions.get(id)||id));}
  if(item.effects?.length){preview.append(make('h4','','Эффекты'));for(const id of item.effects)preview.append(make('div','item-create-catalog-detail',allEffects.get(id)||id));}
 }
 function addGrantPicker(label,entries,kind){
  const details=make('details','item-create-grant-picker');
  details.append(make('summary','',`${label} · ${entries.length} доступно`));
  const items=make('div','item-create-grant-items');
  const filter=inp('Найти по имени или ID','search');filter.setAttribute('aria-label',`Поиск: ${label}`);
  const matches=[];
  for(const entry of entries){
   const item=make('label','item-create-grant-row');const check=make('input');check.type='checkbox';check.dataset.kind=kind;check.value=entry.id;
   item.append(check,make('span','',`${entry.name||entry.id} · ${entry.id}`));items.append(item);matches.push({item,label:item.textContent.toLocaleLowerCase()});
  }
  filter.oninput=()=>{const q=filter.value.toLocaleLowerCase();for(const m of matches)m.item.hidden=!m.label.includes(q);};
  details.append(filter,items);return details;
 }
 function renderGrantPickers(){grants.replaceChildren();if(!catalog)return;grants.append(addGrantPicker('Действия',catalog.actions||[],'actions'),addGrantPicker('Эффекты',catalog.effects||[],'effects'));}
 function sync(){for(const tab of tabs.children)tab.setAttribute('aria-pressed',String(tab.dataset.mode===mode));content.replaceChildren(mode==='existing'?existing:creation);action.textContent=mode==='existing'?'Выдать персонажу':'Создать и выдать';}
 action.onclick=()=>{
  const props=[];for(const row of attrs.children)props.push({name:row.children[0].value,value:row.children[1].value});
  const grants={actions:[],effects:[]};for(const checkbox of creation.querySelectorAll('.item-create-grant-row input:checked'))grants[checkbox.dataset.kind].push(checkbox.value);
  try{
   const {type,payload}=inventoryGrantPayload(charId,mode==='existing'?chosen:'',Number(count.value),equip.checked,mode==='new'?{name:name.value,description:desc.value,properties:props,...grants}:null);
   if(submit(type,payload)){dialog.close();toast('Команда выдачи отправлена');}
  }catch(error){toast(error.message);}
 };
 return {open:async(characterId)=>{
   charId=characterId;mode='existing';query='';search.value='';chosen='';count.value='1';equip.checked=false;name.value='';desc.value='';attrs.replaceChildren();
   catalog=null;existing.replaceChildren(search,split);list.replaceChildren(make('p','muted','Загрузка каталога…'));preview.replaceChildren();grants.replaceChildren();sync();
   if(!dialog.open)dialog.showModal();
   try{const response=await loadCatalog();if(!dialog.open||charId!==characterId)return;catalog=response.objectCatalog||{objects:[],actions:[],effects:[]};chosen=catalog.objects?.[0]?.id||'';renderObjects();renderGrantPickers();}
   catch(error){list.replaceChildren(make('p','muted','Не удалось загрузить каталог'));toast(error.message);}
 },close:()=>dialog.close()};
}
