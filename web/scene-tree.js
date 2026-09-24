import {orderedFloors,orderedLayers} from './scene-content.js';

const button=(text,title,action)=>{const node=document.createElement('button');node.type='button';node.className='tree-action';node.textContent=text;node.title=title;node.onclick=event=>{event.stopPropagation();action();};return node;};

export class SceneTreeRuntime{
  constructor(elements,{queue,selectElement,selectTransition,beginTransition,setFloor,getFloor}){
    this.elements=elements;this.queue=queue;this.selectElement=selectElement;this.selectTransition=selectTransition;this.beginTransition=beginTransition;this.setFloor=setFloor;this.getFloor=getFloor;this.state=null;
    elements.addFloor.onclick=()=>this.createFloor();elements.addLayer.onclick=()=>this.createLayer();elements.boundsSave.onclick=()=>this.saveBounds();elements.addTransition.onclick=()=>this.beginTransition();
  }

  update(state,selectedElement,selectedTransition=''){
    this.state=state;const floors=orderedFloors(state);let current=this.getFloor();if(!floors.some(f=>f.id===current))current=floors[0]?.id||'';
    this.elements.boundsWidth.value=Math.round(state.scene.bounds.width);this.elements.boundsHeight.value=Math.round(state.scene.bounds.height);this.elements.tree.replaceChildren();
    if(state.you.role==='gm')for(const floor of [...floors].reverse())this.elements.tree.append(this.floorNode(floor,current,selectedElement));
    if(state.you.role==='gm'&&Object.keys(state.transitions||{}).length)this.elements.tree.append(this.transitionSection(selectedTransition));
    const gm=state.you.role==='gm';for(const control of this.elements.controls)control.hidden=!gm;this.elements.addFloor.disabled=floors.length>=2;this.elements.addFloor.title=floors.length>=2?'Сейчас поддерживаются только два этажа':'Добавить этаж';
  }

  saveBounds(){const width=Number(this.elements.boundsWidth.value),height=Number(this.elements.boundsHeight.value);if(!(width>0&&height>0))return;const outside=[...Object.values(this.state.elements||{}),...Object.values(this.state.tokens||{})].filter(item=>{const t=item.transform||{x:item.x,y:item.y,width:item.size,height:item.size};return t.x<0||t.y<0||t.x+t.width>width||t.y+t.height>height;}).length;if(outside&&!confirm(`За новыми границами останутся ${outside} элементов. Продолжить?`))return;this.queue('boundsUpdate',{bounds:{width,height}});}
  createFloor(){if(orderedFloors(this.state).length>=2){alert('Сейчас сцена поддерживает не больше двух этажей');return;}const name=prompt('Название этажа','Новый этаж')?.trim();if(!name)return;const order=Math.max(-1,...orderedFloors(this.state).map(f=>f.order))+1;this.queue('floorCreate',{floor:{name,order}});}
  createLayer(){const floorId=this.getFloor();if(!floorId)return;const name=prompt('Название слоя','Новый слой')?.trim();if(!name)return;const order=Math.max(-1,...orderedLayers(this.state,floorId).filter(layer=>layer.kind!=='walkable').map(layer=>layer.order))+1;this.queue('layerCreate',{layer:{floorId,name,order}});}
  transitionSection(selectedTransition){
    const section=document.createElement('section');section.className='tree-transitions';const head=document.createElement('div');head.className='tree-row tree-transition-heading';const label=document.createElement('strong');label.textContent='Переходы';head.append(label);section.append(head);
    const items=Object.values(this.state.transitions||{}).sort((a,b)=>(a.name||'').localeCompare(b.name||'')||a.id.localeCompare(b.id));
    items.forEach((transition,index)=>{const row=document.createElement('div');row.className='tree-row tree-transition-row'+(transition.id===selectedTransition?' selected':'');row.dataset.transitionId=transition.id;const name=document.createElement('span');name.textContent=transition.name||`Переход #${index+1}`;row.append(name);row.onclick=()=>{const floorId=transition.endpointA?.floorId;if(floorId&&floorId!==this.getFloor())this.setFloor(floorId);this.selectTransition(transition.id);};section.append(row);});return section;
  }

  floorNode(floor,current,selectedElement){
    const section=document.createElement('section');section.className='tree-floor'+(floor.id===current?' active':'');section.dataset.floorId=floor.id;
    const head=document.createElement('div');head.className='tree-row tree-floor-row';const label=document.createElement('strong');label.textContent=floor.name;head.append(label);head.onclick=()=>this.setFloor(floor.id);
    head.append(button(`${Math.round(floor.opacity*100)}%`,'Непрозрачность этажа',()=>this.updateFloorAlpha(floor,'opacity','Непрозрачность этажа, %')),button(`↑${Math.round(floor.opacityWhenViewedFromBelow*100)}%`,'Видимость снизу',()=>this.updateFloorAlpha(floor,'opacityWhenViewedFromBelow','Непрозрачность при просмотре снизу, %')),button('✎','Переименовать',()=>{const name=prompt('Название этажа',floor.name)?.trim();if(name)this.queue('floorUpdate',{floor:{id:floor.id},floorProperties:{name}});}),button('↑','Поднять',()=>this.shiftFloor(floor,1)),button('↓','Опустить',()=>this.shiftFloor(floor,-1)),button('×','Удалить',()=>{if(confirm(`Удалить этаж «${floor.name}»?`))this.queue('floorDelete',{floor:{id:floor.id}});}));
    section.append(head);for(const layer of [...orderedLayers(this.state,floor.id)].reverse())section.append(this.layerNode(layer,selectedElement));return section;
  }

  updateFloorAlpha(floor,field,title){const value=prompt(title,String(Math.round(floor[field]*100)));if(value===null)return;const opacity=Number(value)/100;if(Number.isFinite(opacity)&&opacity>=0&&opacity<=1)this.queue('floorUpdate',{floor:{id:floor.id},floorProperties:{[field]:opacity}});}

  layerNode(layer,selectedElement){
    const section=document.createElement('div');section.className=`tree-layer tree-layer-${layer.kind}`;section.dataset.layerId=layer.id;
    if(layer.kind==='visual'){section.ondragover=event=>event.preventDefault();section.ondrop=event=>{event.preventDefault();const id=event.dataTransfer.getData('text/atlas-element');if(id)this.moveElementToLayer(id,layer);};}
    const head=document.createElement('div');head.className='tree-row tree-layer-row';const label=document.createElement('span');label.textContent=layer.name;head.append(label);
    if(layer.kind==='visual'){
      head.prepend(button(layer.visible?'◉':'○','Видимость',()=>this.queue('layerUpdate',{layer:{id:layer.id},layerProperties:{visible:!layer.visible}})));
      head.append(button(layer.locked?'🔒':'🔓','Блокировка',()=>this.queue('layerUpdate',{layer:{id:layer.id},layerProperties:{locked:!layer.locked}})),button(`${Math.round(layer.opacity*100)}%`,'Непрозрачность слоя',()=>{const value=prompt('Непрозрачность слоя, %',String(Math.round(layer.opacity*100)));if(value===null)return;const opacity=Number(value)/100;if(Number.isFinite(opacity)&&opacity>=0&&opacity<=1)this.queue('layerUpdate',{layer:{id:layer.id},layerProperties:{opacity}});}),button('✎','Переименовать',()=>this.renameLayer(layer)),button('↑','Поднять',()=>this.shiftLayer(layer,1)),button('↓','Опустить',()=>this.shiftLayer(layer,-1)),button('×','Удалить',()=>{if(confirm(`Удалить слой «${layer.name}»?`))this.queue('layerDelete',{layer:{id:layer.id}});}));
    }else if(layer.kind==='tokens'){
      label.textContent=`◆ ${layer.name}`;head.append(button('✎','Переименовать',()=>this.renameLayer(layer)),button('↑','Поднять',()=>this.shiftLayer(layer,1)),button('↓','Опустить',()=>this.shiftLayer(layer,-1)));
    }else{
      label.textContent=`▧ ${layer.name}`;head.append(button('Рамка','Изменить игровую область',()=>this.editWalkable(layer)));
    }
    section.append(head);if(layer.kind==='visual'){const elements=Object.values(this.state.elements||{}).filter(element=>element.layerId===layer.id).sort((a,b)=>b.zOrder-a.zOrder||a.id.localeCompare(b.id));for(const element of elements)section.append(this.elementNode(element,selectedElement));}return section;
  }

  renameLayer(layer){const name=prompt('Название слоя',layer.name)?.trim();if(name)this.queue('layerUpdate',{layer:{id:layer.id},layerProperties:{name}});}
  editWalkable(layer){const current=layer.walkableBounds||{x:0,y:0,width:this.state.scene.bounds.width,height:this.state.scene.bounds.height},x=Number(prompt('X игровой области',String(current.x))),y=Number(prompt('Y игровой области',String(current.y))),width=Number(prompt('Ширина игровой области',String(current.width))),height=Number(prompt('Высота игровой области',String(current.height)));if([x,y,width,height].every(Number.isFinite)&&width>0&&height>0)this.queue('layerUpdate',{layer:{id:layer.id},layerProperties:{walkableBounds:{x,y,width,height}}});}

  elementNode(element,selectedElement){
    const row=document.createElement('div');row.className='tree-row tree-element-row'+(element.id===selectedElement?' selected':'');row.draggable=true;row.dataset.elementId=element.id;row.onclick=()=>{this.setFloor(element.floorId);this.selectElement(element.id);};row.ondragstart=event=>{event.stopPropagation();event.dataTransfer.setData('text/atlas-element',element.id);};row.ondragover=event=>event.preventDefault();row.ondrop=event=>{event.preventDefault();event.stopPropagation();const id=event.dataTransfer.getData('text/atlas-element');if(id&&id!==element.id)this.dropElement(id,element);};
    const name=document.createElement('span');name.textContent=element.name||this.state.assets?.[element.assetId]?.filename||'Изображение';row.append(name,button('↑','На один уровень вперёд',()=>this.shiftElement(element,1)),button('↓','На один уровень назад',()=>this.shiftElement(element,-1)),button('⇈','На передний план',()=>this.moveElementZ(element,true)),button('⇊','На задний план',()=>this.moveElementZ(element,false)));return row;
  }

  moveElementToLayer(id,layer){const element=this.state.elements?.[id];if(!element||layer.kind!=='visual')return;const zOrder=Math.max(-1,...Object.values(this.state.elements).filter(item=>item.layerId===layer.id).map(item=>item.zOrder))+1;this.queue('elementUpdate',{element:{id},elementProperties:{floorId:layer.floorId,layerId:layer.id,zOrder}});this.setFloor(layer.floorId);}
  dropElement(id,target){const element=this.state.elements?.[id];if(!element)return;if(element.layerId!==target.layerId){this.queue('elementUpdate',{element:{id},elementProperties:{floorId:target.floorId,layerId:target.layerId,zOrder:target.zOrder+1}});this.setFloor(target.floorId);return;}this.queue('elementUpdate',{element:{id},elementProperties:{zOrder:target.zOrder}});this.queue('elementUpdate',{element:{id:target.id},elementProperties:{zOrder:element.zOrder}});}
  shiftElement(element,direction){const list=Object.values(this.state.elements).filter(item=>item.layerId===element.layerId).sort((a,b)=>a.zOrder-b.zOrder||a.id.localeCompare(b.id)),index=list.findIndex(item=>item.id===element.id),other=list[index+direction];if(!other)return;this.queue('elementUpdate',{element:{id:element.id},elementProperties:{zOrder:other.zOrder}});this.queue('elementUpdate',{element:{id:other.id},elementProperties:{zOrder:element.zOrder}});}
  moveElementZ(element,front){const values=Object.values(this.state.elements).filter(item=>item.layerId===element.layerId).map(item=>item.zOrder),zOrder=front?Math.max(...values)+1:Math.min(...values)-1;this.queue('elementUpdate',{element:{id:element.id},elementProperties:{zOrder}});}
  shiftFloor(floor,direction){const list=orderedFloors(this.state),index=list.findIndex(item=>item.id===floor.id),other=list[index+direction];if(!other)return;this.queue('floorUpdate',{floor:{id:floor.id},floorProperties:{order:other.order}});this.queue('floorUpdate',{floor:{id:other.id},floorProperties:{order:floor.order}});}
  shiftLayer(layer,direction){const list=orderedLayers(this.state,layer.floorId).filter(item=>item.kind!=='walkable'),index=list.findIndex(item=>item.id===layer.id),other=list[index+direction];if(!other)return;this.queue('layerUpdate',{layer:{id:layer.id},layerProperties:{order:other.order}});this.queue('layerUpdate',{layer:{id:other.id},layerProperties:{order:layer.order}});}
}
