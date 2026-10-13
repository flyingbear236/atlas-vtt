// Campaign-wide token resource indicators. The server remains the authority for the four slots.
export const RESOURCE_COLORS=['#D96D70','#EF963D','#69AD7E','#6499DF','#A485D6'];
export const isNumericResource=definition=>definition?.type==='integer'||definition?.type==='number';
const clone=items=>(items||[]).filter(item=>item.enabled).map(item=>({...item}));
export function arrangeResourceOverlays(items){
 const ordered=[];
 for(const position of ['top','bottom']){
  let slot=0;
  for(const item of items.filter(item=>item.position===position)){
   if(++slot>2)throw new Error('С каждой стороны допускается не больше двух индикаторов');
   ordered.push({...item,enabled:true,position,slot});
  }
 }
 if(ordered.length>4)throw new Error('Можно показать не больше четырёх индикаторов');
 return ordered;
}
export function resourceOverlayLabel(definition,resource){
 const kind=definition?.type;
 if(kind==='boolean')return resource?.current?.value?'Да':'Нет';
 if(kind==='string')return String(resource?.current?.value||'—');
 if(resource?.current&&resource?.initial)return `${resource.current.value} / ${resource.initial.value}`;
 return '14 / 20'; // Example value used only in the settings preview.
}
export function resourceOverlaySample(definition){
 if(definition?.type==='boolean')return {current:{value:true},initial:{value:false}};
 if(definition?.type==='string')return {current:{value:'Защитная'},initial:{value:'Обычная'}};
 return {current:{value:14},initial:{value:20}};
}
function make(tag,className='',text=''){
 const el=document.createElement(tag);if(className)el.className=className;if(text)el.textContent=text;return el;
}
function button(text,callback,className='subtle'){
 const el=make('button',className,text);el.type='button';el.onclick=callback;return el;
}

export class ResourceOverlayEditor {
 constructor(mount,{save,toast=()=>{}}){
  this.mount=mount;this.save=save;this.toast=toast;this.definitions={};this.items=[];this.original=[];this.selected='';this.saving=false;this.externalChange=false;this.message='';
  mount.classList.add('resource-config');
 }
 open(config,definitions){
  this.definitions=definitions||{};this.original=clone(config);this.items=arrangeResourceOverlays(clone(config).sort((a,b)=>a.position.localeCompare(b.position)||a.slot-b.slot));this.selected=this.items[0]?.statId||'';this.saving=false;this.externalChange=false;this.message='';this.render();
 }
 setDefinitions(definitions){this.definitions=definitions||{};this.render();}
 isDirty(){return JSON.stringify(arrangeResourceOverlays(this.items))!==JSON.stringify(arrangeResourceOverlays(clone(this.original).sort((a,b)=>a.position.localeCompare(b.position)||a.slot-b.slot)));}
 onRemote(config,definitions){
  this.definitions=definitions||this.definitions;
  if(!this.isDirty()&&!this.saving){this.open(config,this.definitions);return;}
  this.externalChange=true;this.render();
 }
 onSaved(error,config){
  this.saving=false;
  if(error){this.message=`Не удалось сохранить: ${error}`;this.render();return;}
  this.open(config||this.items,this.definitions);this.message='Изменения сохранены';this.render();
 }
 mutate(fn){if(this.saving)return;try{fn();this.items=arrangeResourceOverlays(this.items);this.message='';this.render();}catch(error){this.message=error.message;this.render();}}
 add(statId){this.mutate(()=>{
  if(!this.definitions[statId]||this.items.some(item=>item.statId===statId))return;
  const position=this.items.filter(item=>item.position==='bottom').length<2?'bottom':'top';
  if(this.items.filter(item=>item.position===position).length>=2)throw new Error('Все четыре позиции заняты');
  this.items.push({statId,enabled:true,position,slot:1,showName:true,color:RESOURCE_COLORS[2],mode:isNumericResource(this.definitions[statId])?'bar':'text'});this.selected=statId;
 });}
 update(statId,properties){this.mutate(()=>{
  const current=this.items.find(item=>item.statId===statId);if(!current)return;
  if(properties.position&&properties.position!==current.position&&this.items.filter(item=>item.position===properties.position).length>=2)throw new Error('На этой стороне уже два индикатора');
  Object.assign(current,properties);
 });}
 move(statId,delta){this.mutate(()=>{const index=this.items.findIndex(item=>item.statId===statId);if(index<0)return;const peer=this.items.filter(item=>item.position===this.items[index].position),offset=peer.findIndex(item=>item.statId===statId)+delta;if(offset<0||offset>=peer.length)return;const peerIndex=this.items.findIndex(item=>item.statId===peer[offset].statId);[this.items[index],this.items[peerIndex]]=[this.items[peerIndex],this.items[index]];});}
 submit(){if(this.saving||!this.isDirty())return;
  if(this.externalChange&&!confirm('Настройки были изменены в другом окне. Перезаписать их?'))return;
  for(const item of this.items){if(!this.definitions[item.statId]){this.message=`Определение характеристики ${item.statId} больше не доступно`;this.render();return;}}
  const payload=arrangeResourceOverlays(this.items);if(!this.save(payload)){this.message='Не удалось отправить настройки';this.render();return;}
  this.saving=true;this.message='Ожидание подтверждения сервера…';this.render();
 }
 render(){
  const m=this.mount;m.replaceChildren();const title=make('h3','','Индикаторы характеристик на карте'),help=make('p','muted','Общие правила для всех персонажей кампании. Индикатор появится у персонажа, если у него есть такая характеристика.');m.append(title,help);
  const layout=make('div','resource-config-layout'),preview=make('section','resource-config-preview'),controls=make('section','resource-config-controls');layout.append(preview,controls);m.append(layout);
  preview.append(make('strong','','Предпросмотр'),make('small','muted','Пример значений; на карте будут показаны реальные данные персонажа.'));
  for(const position of ['top','bottom']){
   if(position==='bottom'){
    const figure=make('div','resource-preview-figure'),avatar=make('div','resource-preview-avatar','◆');avatar.setAttribute('aria-hidden','true');figure.append(avatar,make('strong','','Волк'));preview.append(figure);
   }
   const group=make('div','resource-preview-group');for(const item of (position==='top'?this.items.filter(e=>e.position===position).reverse():this.items.filter(e=>e.position===position))){
    const def=this.definitions[item.statId]||{id:item.statId,name:item.statId},sample=resourceOverlaySample(def),row=button('',()=>{this.selected=item.statId;this.render()},'resource-preview-row');row.classList.toggle('selected',item.statId===this.selected);
    const heading=make('div','resource-preview-heading'),name=make('span','',def.name||item.statId);if(item.showName||!isNumericResource(def))heading.append(name);
    if(item.mode==='bar'&&isNumericResource(def)){
     row.append(heading);const background=make('div','resource-preview-bar'),fill=make('div','resource-preview-fill');fill.style.background=item.color;fill.style.width='70%';background.append(fill);row.append(background);
    }else{const value=make('span','resource-preview-value',resourceOverlayLabel(def,sample));value.style.color=item.color;heading.append(value);row.append(heading);}
    group.append(row);
   }
   if(!group.children.length)group.append(make('small','resource-preview-empty',position==='top'?'Нет индикаторов сверху':'Нет индикаторов снизу'));
   preview.append(group);
  }
  const head=make('div','resource-config-head');head.append(make('strong','','На токенах'),make('small','muted',`${this.items.length} из 4`));controls.append(head);
  const list=make('div','resource-config-list');controls.append(list);
  for(const item of this.items){const def=this.definitions[item.statId]||{name:item.statId};const row=make('div','resource-config-item');row.classList.toggle('selected',this.selected===item.statId);
   const pick=button('',()=>{this.selected=item.statId;this.render()},'resource-config-pick');const chip=make('span','resource-color-dot');chip.style.background=item.color;const text=make('span','resource-config-item-text');text.append(make('strong','',def.name||item.statId),make('small','muted',`${item.position==='top'?'Сверху':'Снизу'} · ${item.mode==='bar'?'Полоса':'Текст'}`));pick.append(chip,text);row.append(pick);
   row.append(button('×',()=>this.mutate(()=>{this.items=this.items.filter(e=>e.statId!==item.statId);if(this.selected===item.statId)this.selected=this.items[0]?.statId||'';}),'subtle resource-config-remove'));list.append(row);
  }
  const available=Object.values(this.definitions).filter(def=>def.kind==='resource'&&!this.items.some(item=>item.statId===def.id)).sort((a,b)=>(a.name||a.id).localeCompare(b.name||b.id));
  if(this.items.length<4&&available.length){const add=make('div','resource-config-add'),select=make('select'),option=make('option','','Выберите характеристику');option.value='';select.append(option);
   for(const def of available){const o=make('option','',def.name||def.id);o.value=def.id;select.append(o);}const addButton=button('+ Добавить',()=>{if(select.value)this.add(select.value)},'primary');addButton.disabled=this.saving;add.append(select,addButton);controls.append(add);
  }else if(!available.length&&!this.items.length)controls.append(make('p','muted','Нет характеристик с отслеживаемым значением. Добавьте такую характеристику в чарлисте.'));
  const selected=this.items.find(item=>item.statId===this.selected);
  if(selected){const def=this.definitions[selected.statId],editor=make('section','resource-config-editor');editor.append(make('strong','',def?.name||selected.statId));
   const positionLabel=make('label','','Положение'),side=make('div','resource-config-toggle');for(const [value,label] of [['top','↑ Сверху'],['bottom','↓ Снизу']]){const b=button(label,()=>this.update(selected.statId,{position:value}),'subtle');b.classList.toggle('active',selected.position===value);b.setAttribute('aria-pressed',String(selected.position===value));side.append(b);}positionLabel.append(side);editor.append(positionLabel);
   const peers=this.items.filter(e=>e.position===selected.position);if(peers.length>1){const order=make('div','resource-config-order');order.append(make('small','muted','Порядок от токена'));order.append(button('Ближе',()=>this.move(selected.statId,-1)),button('Дальше',()=>this.move(selected.statId,1)));editor.append(order);}
   if(isNumericResource(def)){const modeLabel=make('label','','Отображение'),modes=make('div','resource-config-toggle');for(const [value,label]of [['bar','Полоса'],['text','Число']]){const b=button(label,()=>this.update(selected.statId,{mode:value}),'subtle');b.classList.toggle('active',selected.mode===value);b.setAttribute('aria-pressed',String(selected.mode===value));modes.append(b);}modeLabel.append(modes);editor.append(modeLabel);}
   const colorLabel=make('label','','Цвет'),colors=make('div','resource-config-colors');for(const color of RESOURCE_COLORS){const b=button('',()=>this.update(selected.statId,{color}),'resource-config-color');b.style.background=color;b.title=color;b.setAttribute('aria-label',`Цвет ${color}`);b.classList.toggle('active',selected.color.toLowerCase()===color.toLowerCase());colors.append(b);}const custom=make('input');custom.type='color';custom.value=selected.color;custom.title='Свой цвет';custom.setAttribute('aria-label','Свой цвет');custom.onchange=()=>this.update(selected.statId,{color:custom.value});colors.append(custom);colorLabel.append(colors);editor.append(colorLabel);
   if(isNumericResource(def)){const showName=make('label','resource-config-checkbox');const check=make('input');check.type='checkbox';check.checked=selected.showName;check.onchange=()=>this.update(selected.statId,{showName:check.checked});showName.append(check,' Подписывать индикатор');editor.append(showName);}
   controls.append(editor);
  }
  if(this.externalChange)m.append(make('p','resource-config-warning','Настройки изменились в другом окне. Сохранение заменит новую конфигурацию.'));
  if(this.message)m.append(make('p','resource-config-message',this.message));
  const footer=make('div','resource-config-footer');const reset=button('Отменить изменения',()=>this.open(this.original,this.definitions));reset.disabled=!this.isDirty()||this.saving;const save=button(this.saving?'Сохраняется…':'Сохранить',()=>this.submit(),'primary');save.disabled=!this.isDirty()||this.saving;footer.append(reset,save);m.append(footer);
 }
}
