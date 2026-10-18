import {orderedFloors,orderedLayers} from './scene-content.js';

const isGM=member=>!!(member?.gm||member?.role==='gm');
const glyphs={
  eye:'<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12Z"/><circle cx="12" cy="12" r="3"/>',
  'eye-off':'<path d="M3 3l18 18M10.6 5.1C16.5 4 20.4 9.7 22 12c-.7 1.2-1.8 2.5-3.1 3.6M6.1 6.1C4.1 7.4 2.7 9.6 2 12c2.4 4.2 6 7 10 7 1.5 0 2.8-.3 4-1"/>',
  lock:'<rect x="5" y="10" width="14" height="11" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/>',
  unlock:'<rect x="5" y="10" width="14" height="11" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0"/>',
  trash:'<path d="M3 6h18M8 6V4h8v2m3 0-1 15H6L5 6M10 10v7m4-7v7"/>',
  more:'<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>',
  pencil:'<path d="m15 5 4 4M4 20l4.5-.8L20 7.7 16.3 4 4.8 15.5 4 20Z"/>',
  image:'<rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8" cy="8" r="1.5"/><path d="m3 16 5-5 4 4 3-3 6 6"/>',
  users:'<circle cx="9" cy="8" r="3"/><path d="M3 20v-2a6 6 0 0 1 12 0v2M16 5a3 3 0 0 1 0 6m2 4a5 5 0 0 1 3 5"/>',
  chevron:'<path d="m9 6 6 6-6 6"/>',
  plus:'<path d="M12 5v14M5 12h14"/>',
  minus:'<path d="M5 12h14"/>',
  move:'<path d="m8 4 4-3 4 3M12 1v22m-4-3 4 3 4-3M4 8l-3 4 3 4M1 12h22m-3-4 3 4-3 4"/>',
  rectangle:'<rect x="3" y="5" width="18" height="14" rx="1"/>',
  polygon:'<path d="m4 10 7-7 9 6-3 12H7Z"/>',
  target:'<circle cx="12" cy="12" r="8"/><path d="M12 1v5m0 12v5M1 12h5m12 0h5"/>',
  layers:'<path d="m12 2 10 5-10 5L2 7Zm-10 10 10 5 10-5M2 17l10 5 10-5"/>',
  link:'<path d="M10 13a5 5 0 0 0 7 .4l3-3a5 5 0 0 0-7-7l-2 2m3 5a5 5 0 0 0-7-.4l-3 3a5 5 0 0 0 7 7l2-2"/>'
};
const icon=name=>`<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${glyphs[name]}</svg>`;
function action(name,title,onClick){const b=document.createElement('button');b.type='button';b.className='tree-action';b.innerHTML=icon(name);b.title=title;b.setAttribute('aria-label',title);b.onclick=e=>{e.stopPropagation();onClick();};return b;}
const text=(tag,className,value)=>{const el=document.createElement(tag);el.className=className;el.textContent=value;return el;};

export class SceneTreeRuntime{
  constructor(elements,{queue,selectElement,selectTransition,selectToken,beginTransition,setFloor,getFloor,focusElement,updateTransition,setWalkableTool,getWalkableTool,setRenderBoundsTool,getRenderBoundsTool}){
    Object.assign(this,{elements,queue,selectElement,selectTransition,selectToken:selectToken||(()=>{}),beginTransition,setFloor,getFloor,focusElement,updateTransition:updateTransition||(()=>false)});
    this.setWalkableTool=setWalkableTool||(()=>{});this.getWalkableTool=getWalkableTool||(()=> '');this.setRenderBoundsTool=setRenderBoundsTool||(()=>{});this.getRenderBoundsTool=getRenderBoundsTool||(()=> '');
    this.state=null;this.structureKey='';this.rebuilds=0;this.elementRows=new Map();this.transitionRows=new Map();this.floorSections=new Map();this.selectedElement='';this.selectedTransition='';this.currentFloor='';
    this.expandedFloors=new Set();this.expandedTokens=new Set();this.collapsedLayers=new Set();this.expandedAreas=new Set();this.initializedScene='';this.drag=null;this.dragTarget=null;this.hoverTimer=null;this.scrollFrame=0;this.contextMenu=null;
    elements.addFloor.onclick=()=>this.createFloor();elements.addLayer.onclick=()=>this.createLayer();elements.addTransition.onclick=()=>this.beginTransition();
    this.onMove=e=>this.moveDrag(e);this.onUp=e=>this.endDrag(e);this.onCancel=()=>this.cancelDrag();this.onWheel=e=>{if(!this.drag?.started)return;e.preventDefault();e.stopPropagation();this.elements.tree.scrollTop+=e.deltaY*(e.deltaMode===1?16:e.deltaMode===2?this.elements.tree.clientHeight:1);this.markDrop();};
    document.addEventListener('pointermove',this.onMove);document.addEventListener('pointerup',this.onUp);document.addEventListener('pointercancel',this.onCancel);
    document.addEventListener('wheel',this.onWheel,{capture:true,passive:false});window.addEventListener('blur',this.onCancel);
    document.addEventListener('keydown',e=>{if(e.key==='Escape'){this.cancelDrag();this.closeMenu();}});
    document.addEventListener('pointerdown',e=>{if(this.contextMenu&&!this.contextMenu.contains(e.target))this.closeMenu();});
  }
  update(state,selectedElement,selectedTransition='',editor=isGM(state.you)){
    const gm=!!editor,catalog=gm?(state.elementCatalog||{}):state.elements||{};
    this.state={...state,elements:catalog};const floors=orderedFloors(this.state);let current=this.getFloor();if(!floors.some(f=>f.id===current))current=floors[0]?.id||'';
    if(state.scene.id!==this.initializedScene){this.initializedScene=state.scene.id;this.expandedFloors=new Set(current?[current]:[]);this.expandedTokens.clear();this.collapsedLayers.clear();this.expandedAreas.clear();this.cancelDrag();}
    for(const control of this.elements.controls)control.hidden=!gm;
    this.elements.addFloor.hidden=floors.length>=2;this.elements.addFloor.disabled=floors.length>=2;
    const floorStructure=floors.map(f=>[f.id,f.name,f.order,f.opacity,f.opacityWhenViewedFromBelow,f.walkableMode,f.showWalkableToPlayers,f.geometryRevision,f.walkableComponents?.length,!!f.renderBounds]);
    const tokens=gm?Object.values(state.tokenCatalog||state.tokens||{}):[];
    const tokenStructure=tokens.map(t=>[t.id,t.name,t.floorId]);
    const structureKey=JSON.stringify([state.scene.id,state._catalogVersion||0,floorStructure,state.layers,state.transitions,tokenStructure,gm,this.getWalkableTool(),this.getRenderBoundsTool()]);
    if(structureKey!==this.structureKey){this.structureKey=structureKey;this.rebuilds++;this.rebuild(current,selectedElement,selectedTransition,gm);return;}
    if(this.selectedElement!==(selectedElement||'')){this.elementRows.get(this.selectedElement)?.classList.remove('selected');this.elementRows.get(selectedElement)?.classList.add('selected');this.selectedElement=selectedElement||'';}
    if(this.selectedTransition!==(selectedTransition||'')){this.transitionRows.get(this.selectedTransition)?.classList.remove('selected');this.transitionRows.get(selectedTransition)?.classList.add('selected');this.selectedTransition=selectedTransition||'';}
    if(this.currentFloor!==current){this.floorSections.get(this.currentFloor)?.classList.remove('active');this.floorSections.get(current)?.classList.add('active');this.currentFloor=current;}
  }
  rebuild(current=this.getFloor(),selectedElement=this.selectedElement,selectedTransition=this.selectedTransition,gm=true){
    const container=this.elements.tree,scroll=container.scrollTop;
    this.elementRows.clear();this.transitionRows.clear();this.floorSections.clear();container.replaceChildren();
    if(gm){for(const floor of [...orderedFloors(this.state)].reverse())container.append(this.floorNode(floor,current,selectedElement));if(orderedFloors(this.state).length<2){const add=action('plus','Создать второй этаж',()=>this.createFloor());add.classList.add('tree-add-floor');add.append(' Создать второй этаж');container.append(add);}if(Object.keys(this.state.transitions||{}).length)container.append(this.transitionSection(selectedTransition));}
    container.scrollTop=scroll;this.selectedElement=selectedElement||'';this.selectedTransition=selectedTransition||'';this.currentFloor=current;
  }
  createFloor(){if(orderedFloors(this.state).length>=2)return;const name=prompt('Название этажа','Новый этаж')?.trim();if(!name)return;const order=Math.max(-1,...orderedFloors(this.state).map(f=>f.order))+1;this.queue('floorCreate',{floor:{name,order}});}
  createLayer(floorId=this.getFloor()){if(!floorId)return;const name=prompt('Название слоя','Новый слой')?.trim();if(!name)return;const order=Math.max(-1,...orderedLayers(this.state,floorId).map(l=>l.order))+1;this.queue('layerCreate',{layer:{floorId,name,order}});}
  rename(label,name,save){const input=document.createElement('input');input.className='tree-rename';input.maxLength=80;input.value=name;label.replaceWith(input);input.focus();input.select();let done=false;const finish=ok=>{if(done)return;done=true;const next=input.value.trim();input.replaceWith(label);if(ok&&next&&next!==name)save(next);};input.addEventListener('keydown',e=>{if(e.key==='Enter')finish(true);if(e.key==='Escape')finish(false);});input.addEventListener('blur',()=>finish(true));}
  toggle(set,id){if(set.has(id))set.delete(id);else set.add(id);this.rebuild();}
  menu(items,x,y,opacity=null){this.closeMenu();const el=document.createElement('div');el.className='scene-context-menu';el.setAttribute('role','menu');
    if(opacity){const item=opacity.kind==='image'?this.state.elements?.[opacity.id]:this.state.layers?.[opacity.id];if(item){const label=document.createElement('label');label.textContent='Непрозрачность';const output=document.createElement('output');output.textContent=`${Math.round(item.opacity*100)}%`;const slider=document.createElement('input');slider.type='range';slider.min='0';slider.max='100';slider.value=String(Math.round(item.opacity*100));slider.oninput=()=>{output.textContent=`${slider.value}%`;};slider.onchange=()=>{const value=Number(slider.value)/100;if(opacity.kind==='image')this.elementChange(opacity.id,{opacity:value});else this.queue('layerUpdate',{layer:{id:opacity.id},layerProperties:{opacity:value}});};label.append(output,slider);el.append(label);}}
    for(const item of items){if(item===null){el.append(document.createElement('hr'));continue;}const b=document.createElement('button');b.type='button';b.textContent=item[0];b.disabled=!!item[2];b.onclick=()=>{this.closeMenu();item[1]();};el.append(b);}document.body.append(el);this.contextMenu=el;const width=el.offsetWidth,height=el.offsetHeight;el.style.left=`${Math.max(5,Math.min(x,innerWidth-width-5))}px`;el.style.top=`${Math.max(5,Math.min(y,innerHeight-height-5))}px`;}
  closeMenu(){this.contextMenu?.remove();this.contextMenu=null;}
  rowContext(row,items,opacity=null){row.oncontextmenu=e=>{e.preventDefault();this.menu(items,e.clientX,e.clientY,opacity);};row.append(action('more','Действия',()=>{const r=row.getBoundingClientRect();this.menu(items,r.right-8,r.bottom,opacity);}));}
  transitionSection(selected){const section=document.createElement('section');section.className='tree-transitions';const head=text('div','tree-row tree-transition-heading','');head.append(iconNode('link'),text('strong','', 'Переходы'));section.append(head);
    Object.values(this.state.transitions||{}).sort((a,b)=>(a.name||'').localeCompare(b.name||'')||a.id.localeCompare(b.id)).forEach((t,n)=>{const row=text('div','tree-row tree-transition-row'+(t.id===selected?' selected':''),t.name||`Переход #${n+1}`);row.onclick=()=>this.selectTransition(t.id);row.oncontextmenu=e=>{e.preventDefault();this.selectTransition(t.id);this.showTransitionMenu(t.id,e.clientX,e.clientY);};this.transitionRows.set(t.id,row);section.append(row);});return section;
  }
  transitionMenuItems(transition,endpointKey=''){
    const items=[['Переименовать',()=>{const answer=prompt('Название перехода',transition.name??'');if(answer!==null)this.updateTransition(transition.id,{name:answer.trim().slice(0,120)});}],
      [transition.showName?'Скрыть названия':'Показать названия',()=>this.updateTransition(transition.id,{showName:!transition.showName})],
      [transition.showToPlayers?'Скрыть от игроков':'Показать игрокам',()=>this.updateTransition(transition.id,{showToPlayers:!transition.showToPlayers})],null];
    const endpoints=endpointKey?[endpointKey]:['endpointA','endpointB'];
    for(const key of endpoints){const endpoint=transition[key],label=key==='endpointA'?'A':'B';for(const floor of orderedFloors(this.state)){if(floor.id===endpoint?.floorId)continue;items.push([`Точку ${label} → ${floor.name}`,()=>this.updateTransition(transition.id,{[key]:{...endpoint,floorId:floor.id,position:{...endpoint.position}}})]);}}
    items.push(null,['Удалить',()=>{if(confirm(`Удалить переход «${transition.name||'Переход'}»?`)){this.queue('transitionDelete',{transition:{id:transition.id}});this.selectTransition('');}}]);return items;
  }
  showTransitionMenu(id,x,y,endpointKey=''){const transition=this.state?.transitions?.[id];if(transition)this.menu(this.transitionMenuItems(transition,endpointKey),x,y);}
  floorNode(floor,current,selectedElement){const section=document.createElement('section');section.className='tree-floor'+(floor.id===current?' active':'');section.dataset.floorId=floor.id;this.floorSections.set(floor.id,section);
    const head=text('div','tree-row tree-floor-row','');head.dataset.dropKind='floor';head.dataset.floorId=floor.id;
    const expanded=this.expandedFloors.has(floor.id),chevron=action('chevron',expanded?'Свернуть этаж':'Раскрыть этаж',()=>this.toggle(this.expandedFloors,floor.id));chevron.classList.toggle('expanded',expanded);head.append(chevron);
    const label=text('strong','tree-item-name',floor.name);label.onclick=()=>this.setFloor(floor.id);label.ondblclick=()=>this.rename(label,floor.name,name=>this.queue('floorUpdate',{floor:{id:floor.id},floorProperties:{name}}));head.append(label);
    head.append(action('pencil','Переименовать этаж',()=>this.rename(label,floor.name,name=>this.queue('floorUpdate',{floor:{id:floor.id},floorProperties:{name}}))));
    const vis=document.createElement('details');vis.className='tree-floor-visibility';const summary=document.createElement('summary');summary.textContent='Настроить видимость';vis.append(summary);
    for(const [field,title,description] of [['opacity','Сверху','Непрозрачность текущего этажа при просмотре с него'],['opacityWhenViewedFromBelow','Снизу','Непрозрачность этого этажа при просмотре нижнего']]){
      const item=document.createElement('label');item.textContent=title;item.title=description;const value=text('output','',`${Math.round((floor[field]??1)*100)}%`),slider=document.createElement('input');slider.type='range';slider.min='0';slider.max='100';slider.value=String(Math.round((floor[field]??1)*100));slider.oninput=()=>{value.textContent=`${slider.value}%`;};slider.onchange=()=>this.queue('floorUpdate',{floor:{id:floor.id},floorProperties:{[field]:Number(slider.value)/100}});item.append(value,slider,text('small','',description));vis.append(item);
    }vis.onclick=e=>e.stopPropagation();head.append(vis);
    this.rowContext(head,[['Переименовать',()=>this.rename(label,floor.name,name=>this.queue('floorUpdate',{floor:{id:floor.id},floorProperties:{name}}))],['Поднять этаж',()=>this.shiftFloor(floor,1)],['Опустить этаж',()=>this.shiftFloor(floor,-1)],null,['Удалить этаж',()=>{if(confirm(`Удалить этаж «${floor.name}»?`))this.queue('floorDelete',{floor:{id:floor.id}});}]]);
    section.append(head);
    if(expanded){const content=text('div','tree-floor-content',''),layers=[...orderedLayers(this.state,floor.id)].reverse();for(let index=0;index<=layers.length;index++){content.append(this.layerSlot(floor.id,index));if(index<layers.length)content.append(this.layerNode(layers[index],selectedElement,index));}content.append(this.areasNode(floor));section.append(content);}return section;
  }
  layerSlot(floorId,index){const slot=text('div','tree-layer-drop-slot','');slot.dataset.dropKind='layer-slot';slot.dataset.floorId=floorId;slot.dataset.layerIndex=String(index);slot.setAttribute('aria-hidden','true');return slot;}
  areasNode(floor){const details=document.createElement('details');details.className='tree-areas';details.open=this.expandedAreas.has(floor.id);details.ontoggle=()=>{if(details.open)this.expandedAreas.add(floor.id);else this.expandedAreas.delete(floor.id);};const summary=document.createElement('summary');summary.textContent='Границы отрисовки и движения';details.append(summary);
    const bounds=text('div','tree-areas-section','');bounds.append(text('strong','',`Границы отрисовки · ${floor.renderBounds?'заданы':'нет'}`),text('p','','За их пределами изображение сцены не отображается.'));
    const tools=text('div','tree-areas-tools',''),current=this.getRenderBoundsTool();for(const [key,name,tip] of [['rect','rectangle','Прямоугольник'],['polygon','polygon','Многоугольник'],['edit','pencil','Редактировать']]){const btn=action(name,tip,()=>this.setRenderBoundsTool(current===key?'':key));btn.classList.toggle('active',key===current);if(key==='edit')btn.disabled=!floor.renderBounds;tools.append(btn);}
    const clear=action('trash','Удалить границу отрисовки',()=>this.clearRenderBounds(floor));clear.disabled=!floor.renderBounds;tools.append(clear);bounds.append(tools);details.append(bounds);
    const movement=text('div','tree-areas-section','');movement.append(text('strong','',`Границы движения · ${floor.walkableComponents?.length||0} областей`),text('p','','Проходимые области определяют, где персонажам разрешено перемещаться.'));
    const mode=this.check('Включить границы движения',floor.walkableMode==='restricted',()=>this.editWalkable(floor)),players=this.check('Показывать границы движения игрокам',!!floor.showWalkableToPlayers,()=>this.togglePlayerWalkable(floor));movement.append(mode,players);
    const movetools=text('div','tree-areas-tools',''),walk=this.getWalkableTool();for(const [key,name,tip] of [['add','plus','Добавить проходимую область'],['subtract','minus','Вычесть проходимую область'],['move','move','Переместить область']]){const btn=action(name,tip,()=>this.setWalkableTool(walk===key?'':key));btn.classList.toggle('active',key===walk);movetools.append(btn);}movement.append(movetools);details.append(movement);return details;
  }
  check(title,checked,fn){const label=document.createElement('label');label.className='tree-check';const input=document.createElement('input');input.type='checkbox';input.checked=checked;input.onchange=fn;label.append(input,document.createTextNode(title));return label;}
  clearRenderBounds(floor){if(floor.renderBounds&&confirm('Удалить границы отрисовки этого этажа?'))this.queue('clearRenderBounds',{floorId:floor.id,expectedGeometryRevision:floor.geometryRevision});}
  editWalkable(floor){this.queue('setWalkableMode',{floorId:floor.id,expectedGeometryRevision:floor.geometryRevision,walkableMode:floor.walkableMode==='restricted'?'unrestricted':'restricted'});}
  togglePlayerWalkable(floor){this.queue('floorUpdate',{floor:{id:floor.id},floorProperties:{showWalkableToPlayers:!floor.showWalkableToPlayers}});}
  layerNode(layer,selected,index){const section=text('div',`tree-layer tree-layer-${layer.kind}`,'');section.dataset.layerId=layer.id;
    const head=text('div','tree-row tree-layer-row','');head.dataset.dropKind='layer';head.dataset.layerId=layer.id;head.dataset.floorId=layer.floorId;head.dataset.layerIndex=String(index);head.onpointerdown=e=>{if(e.button===0&&!e.target.closest('button,input,summary,details')){e.preventDefault();this.startDrag(e,'layer',layer.id);}};
    const expanded=layer.kind==='visual'?!this.collapsedLayers.has(layer.id):this.expandedTokens.has(layer.id),collapse=action('chevron',expanded?'Свернуть слой':'Раскрыть слой',()=>this.toggleLayer(layer));collapse.classList.toggle('expanded',expanded);head.append(collapse);
    if(layer.kind==='visual')head.append(action(layer.visible?'eye':'eye-off',layer.visible?'Скрыть слой':'Показать слой',()=>this.queue('layerUpdate',{layer:{id:layer.id},layerProperties:{visible:!layer.visible}})));
    head.append(iconNode(layer.kind==='tokens'?'users':'layers'));
    const displayName=layer.kind==='tokens'&&layer.name==='Токены'?'Персонажи':layer.name;const label=text('span','tree-item-name',displayName);label.ondblclick=()=>this.rename(label,displayName,name=>this.renameLayer(layer,name));head.append(label);
    if(layer.kind==='visual')head.append(action(layer.locked?'lock':'unlock',layer.locked?'Разблокировать слой':'Заблокировать слой',()=>this.queue('layerUpdate',{layer:{id:layer.id},layerProperties:{locked:!layer.locked}})));
    const menus=[['Переименовать',()=>this.rename(label,displayName,name=>this.renameLayer(layer,name))],['На самый верх',()=>this.moveLayerZ(layer,true)],['В самый низ',()=>this.moveLayerZ(layer,false)]];
    if(layer.kind==='visual')menus.unshift(['Показать / скрыть',()=>this.queue('layerUpdate',{layer:{id:layer.id},layerProperties:{visible:!layer.visible}})],['Заблокировать / разблокировать',()=>this.queue('layerUpdate',{layer:{id:layer.id},layerProperties:{locked:!layer.locked}})]);
    if(layer.kind==='visual')menus.push(null,['Удалить слой',()=>{if(confirm(`Удалить слой «${layer.name}»?`))this.queue('layerDelete',{layer:{id:layer.id}});}]);
    this.rowContext(head,menus,layer.kind==='visual'?{kind:'layer',id:layer.id}:null);section.append(head);
    if(layer.kind==='visual'&&expanded){
      const elements=Object.values(this.state.elements||{}).filter(e=>e.layerId===layer.id).sort((a,b)=>b.zOrder-a.zOrder||a.id.localeCompare(b.id));
      for(const el of elements)section.append(this.elementNode(el,selected));
    }else if(layer.kind==='tokens'&&expanded){
      const characters=Object.values(this.state.tokenCatalog||this.state.tokens||{}).filter(t=>t.floorId===layer.floorId).sort((a,b)=>(a.name||'').localeCompare(b.name||''));
      for(const character of characters){const row=text('div','tree-row tree-person-row','');row.dataset.tokenId=character.id;row.dataset.dropKind='token';row.dataset.floorId=layer.floorId;row.append(iconNode('users'),text('span','tree-item-name',character.name||'Персонаж'));row.onclick=()=>this.selectToken(character.id);row.onpointerdown=e=>{if(e.button===0&&!e.target.closest('button')){e.preventDefault();this.startDrag(e,'token',character.id);}};section.append(row);}
    }return section;
  }
  toggleLayer(layer){if(layer.kind==='visual')this.toggle(this.collapsedLayers,layer.id);else this.toggle(this.expandedTokens,layer.id);}
  renameLayer(layer,name){if(name)this.queue('layerUpdate',{layer:{id:layer.id},layerProperties:{name}});}
  elementNode(el,selected){const row=text('div','tree-row tree-element-row'+(el.id===selected?' selected':''),'');row.dataset.dropKind='image';row.dataset.elementId=el.id;row.dataset.floorId=el.floorId;this.elementRows.set(el.id,row);
    row.append(iconNode('image'));
    const name=text('span','tree-item-name',el.name||'Изображение');row.append(name);row.onclick=()=>{this.setFloor(el.floorId,true);this.selectElement(el.id);};row.onpointerdown=e=>{if(e.button===0&&!e.target.closest('button')){e.preventDefault();this.startDrag(e,'image',el.id);}};
    row.append(action(el.visible?'eye':'eye-off',el.visible?'Скрыть изображение':'Показать изображение',()=>this.elementChange(el.id,{visible:!el.visible})));
    row.append(action(el.locked?'lock':'unlock',el.locked?'Разблокировать изображение':'Заблокировать изображение',()=>this.elementChange(el.id,{locked:!el.locked})));
    this.rowContext(row,this.imageMenuItems(el),{kind:'image',id:el.id});return row;
  }
  imageMenuItems(el){return [['Показать / скрыть',()=>this.elementChange(el.id,{visible:!el.visible})],['Заблокировать / разблокировать',()=>this.elementChange(el.id,{locked:!el.locked})],null,['Дублировать',()=>this.queue('elementCreate',{element:{...el,id:'',name:`${el.name} копия`,transform:{...el.transform,x:el.transform.x+30,y:el.transform.y+30}}})],['Центрировать',()=>this.focusElement(el)],['На один уровень вперёд',()=>this.shiftElement(el,1)],['На один уровень назад',()=>this.shiftElement(el,-1)],['На передний план',()=>this.moveElementZ(el,true)],['На задний план',()=>this.moveElementZ(el,false)],null,['Удалить',()=>{if(confirm(`Удалить изображение «${el.name}»?`))this.queue('elementDelete',{element:{id:el.id}});}]];}
  showElementMenu(id,x,y){const el=this.state?.elements?.[id];if(el)this.menu(this.imageMenuItems(el),x,y,{kind:'image',id:el.id});}
  elementChange(id,properties){this.queue('elementUpdate',{element:{id},elementProperties:properties});}
  updateAlpha(kind,item){const input=prompt('Непрозрачность, %',Math.round(item.opacity*100));if(input===null)return;const value=Number(input);if(!input.trim()||!Number.isFinite(value)||value<0||value>100)return;if(kind==='layer')this.queue('layerUpdate',{layer:{id:item.id},layerProperties:{opacity:value/100}});else this.elementChange(item.id,{opacity:value/100});}
  startDrag(e,kind,id){this.cancelDrag();const label=kind==='image'?this.state.elements?.[id]?.name:kind==='token'?(this.state.tokenCatalog||this.state.tokens)?.[id]?.name:this.state.layers?.[id]?.name;if(!label)return;this.drag={kind,id,x:e.clientX,y:e.clientY,startX:e.clientX,startY:e.clientY,started:false,ghost:null};}
  moveDrag(e){const d=this.drag;if(!d)return;d.x=e.clientX;d.y=e.clientY;if(!d.started){if(Math.hypot(d.x-d.startX,d.y-d.startY)<4)return;d.started=true;d.ghost=text('div','scene-drag-ghost',d.kind==='token'?'♟ '+(this.state.tokenCatalog||this.state.tokens)?.[d.id]?.name: d.kind==='image'?this.state.elements?.[d.id]?.name:this.state.layers?.[d.id]?.name);document.body.append(d.ghost);if(d.kind==='layer')this.elements.tree.classList.add('tree-layer-dragging');this.scrollDrag();}
    d.ghost.style.transform=`translate(${d.x+15}px,${d.y+12}px)`;this.markDrop();}
  markDrop(){const d=this.drag;if(!d?.started)return;const el=document.elementFromPoint(d.x,d.y),row=el?.closest('[data-drop-kind]');const kind=row?.dataset.dropKind;
    let target=null;if(row&&this.elements.tree.contains(row)){
      if(d.kind==='image'&&(['floor','image'].includes(kind)||kind==='layer'&&this.state.layers[row.dataset.layerId]?.kind==='visual'))target={kind,id:kind==='floor'?row.dataset.floorId:kind==='image'?row.dataset.elementId:row.dataset.layerId,row,after:kind==='image'&&d.y>row.getBoundingClientRect().top+row.offsetHeight/2};
      else if(d.kind==='layer'&&kind==='layer-slot'&&row.dataset.floorId===this.state.layers[d.id]?.floorId)target={kind,floorId:row.dataset.floorId,index:Number(row.dataset.layerIndex),row};
      else if(d.kind==='layer'&&kind==='layer'&&row.dataset.floorId===this.state.layers[d.id]?.floorId){const after=d.y>row.getBoundingClientRect().top+row.offsetHeight/2,index=Number(row.dataset.layerIndex)+(after?1:0),slot=row.closest('.tree-floor-content')?.querySelector(`.tree-layer-drop-slot[data-layer-index="${index}"]`);if(slot)target={kind:'layer-slot',floorId:row.dataset.floorId,index,row:slot};}
      else if(d.kind==='token'&&(kind==='floor'||kind==='layer'&&this.state.layers[row.dataset.layerId]?.kind==='tokens'||kind==='token'))target={kind,id:kind==='floor'?row.dataset.floorId:kind==='token'?row.dataset.floorId:row.dataset.layerId,row};
    }
    this.elements.tree.querySelectorAll('.tree-drop-target,.tree-drop-after,.tree-layer-drop-active').forEach(el=>el.classList.remove('tree-drop-target','tree-drop-after','tree-layer-drop-active'));
    if(target){target.row.classList.add(target.kind==='layer-slot'?'tree-layer-drop-active':'tree-drop-target');if(target.after)target.row.classList.add('tree-drop-after');}
    this.dragTarget=target;
    const floorId=row?.dataset.floorId;
    if(floorId&&floorId!==this.hoverFloor){clearTimeout(this.hoverTimer);this.hoverFloor=floorId;if(!this.expandedFloors.has(floorId))this.hoverTimer=setTimeout(()=>{if(!this.drag)return;this.expandedFloors.add(floorId);this.rebuild();requestAnimationFrame(()=>this.markDrop());},400);}else if(!floorId){clearTimeout(this.hoverTimer);this.hoverFloor='';}
  }
  scrollDrag(){if(!this.drag?.started)return;const box=this.elements.tree.getBoundingClientRect(),d=this.drag;
    if(d.x>=box.left-40&&d.x<=box.right+40){const zone=45;let speed=0;if(d.y<box.top+zone)speed=-Math.min(24,Math.max(0,(box.top+zone-d.y)*.45));else if(d.y>box.bottom-zone)speed=Math.min(24,Math.max(0,(d.y-(box.bottom-zone))*.45));if(speed){const previous=this.elements.tree.scrollTop;this.elements.tree.scrollTop+=speed;if(this.elements.tree.scrollTop!==previous)this.markDrop();}}
    this.scrollFrame=requestAnimationFrame(()=>this.scrollDrag());
  }
  endDrag(){const d=this.drag,t=this.dragTarget;if(!d)return;this.cancelDrag();if(!d.started||!t||t.id===d.id)return;
    if(d.kind==='image'){
      if(t.kind==='image'){const other=this.state.elements[t.id];if(!other)return;this.dropElement(d.id,other,t.after);}
      else if(t.kind==='layer')this.moveElementToLayer(d.id,this.state.layers[t.id]);
      else if(t.kind==='floor'){const visuals=orderedLayers(this.state,t.id).filter(l=>l.kind==='visual');if(visuals.length===1)this.moveElementToLayer(d.id,visuals[0]);}
    }else if(d.kind==='token'){
      const floorId=t.kind==='floor'||t.kind==='token'?t.row.dataset.floorId:this.state.layers[t.id]?.floorId;
      const token=(this.state.tokenCatalog||this.state.tokens||{})[d.id];if(token&&floorId&&floorId!==token.floorId)this.queue('properties',{token:{id:d.id},properties:{floorId}});
    }else if(d.kind==='layer'&&t.kind==='layer-slot'){this.reorderLayerAt(d.id,t.floorId,t.index);}
  }
  cancelDrag(){clearTimeout(this.hoverTimer);this.hoverTimer=null;this.hoverFloor='';cancelAnimationFrame(this.scrollFrame);this.scrollFrame=0;this.drag?.ghost?.remove();this.drag=null;this.dragTarget=null;this.elements.tree.classList.remove('tree-layer-dragging');this.elements.tree.querySelectorAll('.tree-drop-target,.tree-drop-after,.tree-layer-drop-active').forEach(el=>el.classList.remove('tree-drop-target','tree-drop-after','tree-layer-drop-active'));}
  moveElementToLayer(id,layer){const element=this.state.elements?.[id];if(!element||layer?.kind!=='visual')return;const zOrder=Math.max(-1,...Object.values(this.state.elements).filter(e=>e.layerId===layer.id).map(e=>e.zOrder))+1;this.elementChange(id,{floorId:layer.floorId,layerId:layer.id,zOrder});this.setFloor(layer.floorId);}
  dropElement(id,target,after=false){const element=this.state.elements?.[id];if(!element||id===target.id)return;const sorted=Object.values(this.state.elements).filter(e=>e.layerId===target.layerId&&e.id!==id).sort((a,b)=>b.zOrder-a.zOrder),index=sorted.findIndex(e=>e.id===target.id)+(after?1:0),ahead=sorted[index-1],behind=sorted[index];const zOrder=ahead&&behind?(ahead.zOrder+behind.zOrder)/2:ahead?ahead.zOrder-1:behind?behind.zOrder+1:0;this.elementChange(id,{floorId:target.floorId,layerId:target.layerId,zOrder});this.setFloor(target.floorId);}
  reorderLayerAt(id,floorId,slotIndex){const layer=this.state.layers[id];if(!layer||layer.floorId!==floorId)return;const displayed=[...orderedLayers(this.state,floorId)].reverse(),sourceIndex=displayed.findIndex(item=>item.id===id);if(sourceIndex<0)return;const without=displayed.filter(item=>item.id!==id),index=Math.max(0,Math.min(without.length,slotIndex-(slotIndex>sourceIndex?1:0)));without.splice(index,0,layer);if(without.every((item,n)=>item.id===displayed[n].id))return;for(let n=0;n<without.length;n++){const order=without.length-n;if(order!==without[n].order)this.queue('layerUpdate',{layer:{id:without[n].id},layerProperties:{order}});}}
  shiftFloor(floor,direction){const list=orderedFloors(this.state),index=list.findIndex(f=>f.id===floor.id),other=list[index+direction];if(!other)return;this.queue('floorUpdate',{floor:{id:floor.id},floorProperties:{order:other.order}});this.queue('floorUpdate',{floor:{id:other.id},floorProperties:{order:floor.order}});}
  moveLayerZ(layer,front){const list=orderedLayers(this.state,layer.floorId),newOrder=front?Math.max(...list.map(l=>l.order))+1:Math.min(...list.map(l=>l.order))-1;this.queue('layerUpdate',{layer:{id:layer.id},layerProperties:{order:newOrder}});}
  shiftElement(element,direction){const list=Object.values(this.state.elements).filter(e=>e.layerId===element.layerId).sort((a,b)=>a.zOrder-b.zOrder||a.id.localeCompare(b.id)),index=list.findIndex(e=>e.id===element.id),other=list[index+direction];if(!other)return;this.elementChange(element.id,{zOrder:other.zOrder});this.elementChange(other.id,{zOrder:element.zOrder});}
  moveElementZ(element,front){const list=Object.values(this.state.elements).filter(e=>e.layerId===element.layerId),zOrder=front?Math.max(...list.map(e=>e.zOrder))+1:Math.min(...list.map(e=>e.zOrder))-1;this.elementChange(element.id,{zOrder});}
}
function iconNode(name){const el=document.createElement('span');el.className='tree-symbol';el.innerHTML=icon(name);return el;}
