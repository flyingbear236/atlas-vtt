export function orderedFloors(state){return Object.values(state?.floors||{}).sort((a,b)=>a.order-b.order||a.id.localeCompare(b.id));}
export function orderedLayers(state,floorId){return Object.values(state?.layers||{}).filter(layer=>layer.floorId===floorId).sort((a,b)=>a.order-b.order||a.id.localeCompare(b.id));}

// Keeps only render order. Element values are resolved from current state so
// transform previews do not invalidate the index or retain stale objects.
export class SceneRenderIndex {
  constructor(){this.layers=new Map();this.rebuilds=0;}
  clear(){this.layers.clear();}
  reset(){this.clear();}
  invalidateLayer(id){if(id)this.layers.delete(id);}
  elementChanged(previous,next){
    if(!previous||!next||previous.layerId!==next.layerId||previous.floorId!==next.floorId||previous.zOrder!==next.zOrder){
      this.invalidateLayer(previous?.layerId);this.invalidateLayer(next?.layerId);
    }
  }
  forLayer(state,layerId){
    let ids=this.layers.get(layerId);
    if(!ids){ids=Object.values(state?.elements||{}).filter(element=>element.layerId===layerId).sort((a,b)=>a.zOrder-b.zOrder||a.id.localeCompare(b.id)).map(element=>element.id);this.layers.set(layerId,ids);this.rebuilds++;}
    return ids;
  }
}

export function compositeFloors(state,currentFloorId){
  const floors=orderedFloors(state);if(!floors.length)return[];let index=floors.findIndex(f=>f.id===currentFloorId);if(index<0)index=0;
  if(index===0){const out=[{floor:floors[0],alpha:floors[0].opacity}];if(floors[1]){const alpha=floors[1].opacity*floors[1].opacityWhenViewedFromBelow;if(alpha>0)out.push({floor:floors[1],alpha});}return out;}
  const out=[];if(floors[0].opacity>0)out.push({floor:floors[0],alpha:floors[0].opacity});out.push({floor:floors[index],alpha:floors[index].opacity});return out;
}

export function inversePoint(transform,x,y){
  const cx=transform.x+transform.width/2,cy=transform.y+transform.height/2,angle=-transform.rotation*Math.PI/180,dx=x-cx,dy=y-cy;
  return {x:dx*Math.cos(angle)-dy*Math.sin(angle)+transform.width/2,y:dx*Math.sin(angle)+dy*Math.cos(angle)+transform.height/2};
}

function worldPoint(transform,x,y){
  const angle=transform.rotation*Math.PI/180,dx=x-transform.width/2,dy=y-transform.height/2,cx=transform.x+transform.width/2,cy=transform.y+transform.height/2;
  return {x:dx*Math.cos(angle)-dy*Math.sin(angle)+cx,y:dx*Math.sin(angle)+dy*Math.cos(angle)+cy};
}

export function hitElement(state,floorId,x,y,renderIndex){
  return elementsAtPoint(state,floorId,x,y,renderIndex)[0]||null;
}

export function elementsAtPoint(state,floorId,x,y,renderIndex){
  const result=[],layers=orderedLayers(state,floorId).filter(layer=>layer.kind==='visual'&&layer.visible&&layer.opacity>0);
  for(let layerIndex=layers.length-1;layerIndex>=0;layerIndex--){
    const layer=layers[layerIndex],elements=renderIndex?renderIndex.forLayer(state,layer.id):Object.values(state?.elements||{}).filter(element=>element.layerId===layer.id).sort((a,b)=>a.zOrder-b.zOrder||a.id.localeCompare(b.id));
    for(let elementIndex=elements.length-1;elementIndex>=0;elementIndex--){const element=renderIndex?state?.elements?.[elements[elementIndex]]:elements[elementIndex];if(!element||element.floorId!==floorId||!element.visible||element.opacity<=0)continue;const p=inversePoint(element.transform,x,y);if(p.x>=0&&p.y>=0&&p.x<=element.transform.width&&p.y<=element.transform.height)result.push(element);}
  }
  return result;
}

export function elementHandleAt(element,x,y,scale,allowRotation=true){
  if(!element)return null;const t=element.transform,r=9/scale;
  const handles=[['nw',0,0],['n',t.width/2,0],['ne',t.width,0],['e',t.width,t.height/2],['se',t.width,t.height],['s',t.width/2,t.height],['sw',0,t.height],['w',0,t.height/2]];if(allowRotation)handles.push(['rotate',t.width/2,-28/scale]);
  for(const [name,hx,hy]of handles){const p=worldPoint(t,hx,hy);if(Math.hypot(x-p.x,y-p.y)<=r)return name;}return null;
}

export function transformedFromDrag(start,handle,dx,dy,freeAspect=false){
  if(handle==='move')return {...start,x:start.x+dx,y:start.y+dy};
  if(handle==='rotate'){const cx=start.x+start.width/2,cy=start.y+start.height/2;return {...start,rotation:Math.atan2((start.pointerY+dy)-cy,(start.pointerX+dx)-cx)*180/Math.PI+90};}
  const angle=-start.rotation*Math.PI/180,localDX=dx*Math.cos(angle)-dy*Math.sin(angle),localDY=dx*Math.sin(angle)+dy*Math.cos(angle);let left=0,top=0,right=start.width,bottom=start.height;
  if(handle.includes('w'))left+=localDX;if(handle.includes('e'))right+=localDX;if(handle.includes('n'))top+=localDY;if(handle.includes('s'))bottom+=localDY;
  const corner=handle.length===2;if(corner&&!freeAspect){const ratio=start.width/start.height;let width=Math.max(8,right-left),height=Math.max(8,bottom-top);if(Math.abs(width-start.width)>Math.abs(height-start.height)*ratio)height=width/ratio;else width=height*ratio;if(handle.includes('w'))left=right-width;else right=left+width;if(handle.includes('n'))top=bottom-height;else bottom=top+height;}
  if(right-left<8){if(handle.includes('w'))left=right-8;else right=left+8;}if(bottom-top<8){if(handle.includes('n'))top=bottom-8;else bottom=top+8;}
  const oldCenter=worldPoint(start,start.width/2,start.height/2),newLocalCenter={x:(left+right)/2,y:(top+bottom)/2},newCenter=worldPoint(start,newLocalCenter.x,newLocalCenter.y);
  return {x:start.x+newCenter.x-oldCenter.x-(right-left-start.width)/2,y:start.y+newCenter.y-oldCenter.y-(bottom-top-start.height)/2,width:right-left,height:bottom-top,rotation:start.rotation};
}

function viewportPolygon(transform,view){return [[view.left,view.top],[view.right,view.top],[view.right,view.bottom],[view.left,view.bottom]].map(([x,y])=>inversePoint(transform,x,y));}
function polygonBounds(points){return {left:Math.min(...points.map(p=>p.x)),top:Math.min(...points.map(p=>p.y)),right:Math.max(...points.map(p=>p.x)),bottom:Math.max(...points.map(p=>p.y))};}
function rectIntersectsPolygon(left,top,right,bottom,points){
  const axes=[[1,0],[0,1]];for(let i=0;i<points.length;i++){const a=points[i],b=points[(i+1)%points.length];axes.push([-(b.y-a.y),b.x-a.x]);}
  for(const [ax,ay]of axes){let pmin=Infinity,pmax=-Infinity;for(const p of points){const value=p.x*ax+p.y*ay;pmin=Math.min(pmin,value);pmax=Math.max(pmax,value);}const values=[left*ax+top*ay,right*ax+top*ay,right*ax+bottom*ay,left*ax+bottom*ay],rmin=Math.min(...values),rmax=Math.max(...values);if(pmax<rmin||rmax<pmin)return false;}return true;
}

export function elementIntersectsView(element,view){const t=element?.transform;if(!t||!view)return false;return rectIntersectsPolygon(0,0,t.width,t.height,viewportPolygon(t,view));}

const tilePresenceCache=new WeakMap();
export function tileAvailable(asset,z,x,y,nx){
  if(!asset?.tilePresence)return true;let levels=tilePresenceCache.get(asset);if(!levels){levels=asset.tilePresence.split('.').map(encoded=>{const raw=atob(encoded),bytes=new Uint8Array(raw.length);for(let i=0;i<raw.length;i++)bytes[i]=raw.charCodeAt(i);return bytes;});tilePresenceCache.set(asset,levels);}const index=y*nx+x,bytes=levels[z];return !!bytes&&(bytes[index>>3]&(1<<(index&7)))!==0;
}

function drawTiled(ctx,element,asset,view,camera,dpr,requestImage){
  const t=element.transform,polygon=viewportPolygon(t,view),local=polygonBounds(polygon),sx=t.width/asset.width,sy=t.height/asset.height,screenScale=camera.scale*dpr*Math.max(sx,sy);
  const z=Math.max(0,Math.min(asset.levels-1,Math.floor(Math.log2(1/Math.max(screenScale,.000001))))),unit=512*2**z,nx=Math.ceil(asset.width/unit),ny=Math.ceil(asset.height/unit),tileW=unit*sx,tileH=unit*sy;
  const x0=Math.max(0,Math.floor(local.left/tileW)),y0=Math.max(0,Math.floor(local.top/tileH)),x1=Math.min(nx-1,Math.floor(local.right/tileW)),y1=Math.min(ny-1,Math.floor(local.bottom/tileH));
  const visible=new Set();for(let y=y0;y<=y1;y++)for(let x=x0;x<=x1;x++){const left=x*tileW,top=y*tileH,right=Math.min(t.width,left+tileW),bottom=Math.min(t.height,top+tileH);if(!rectIntersectsPolygon(left,top,right,bottom,polygon))continue;visible.add(`${x}:${y}`);if(!tileAvailable(asset,z,x,y,nx))continue;const bitmap=requestImage(`${asset.id}/${z}_${x}_${y}.png`,Math.max(1,512*screenScale*2**z));if(bitmap)ctx.drawImage(bitmap,left,top,right-left,bottom-top);}
  const prefetch=new Set();for(const key of visible){const [x,y]=key.split(':').map(Number);for(let dy=-1;dy<=1;dy++)for(let dx=-1;dx<=1;dx++){const px=x+dx,py=y+dy,next=`${px}:${py}`;if(px>=0&&py>=0&&px<nx&&py<ny&&!visible.has(next))prefetch.add(next);}}
  for(const key of prefetch){const [x,y]=key.split(':').map(Number);if(tileAvailable(asset,z,x,y,nx))requestImage(`${asset.id}/${z}_${x}_${y}.png`,Math.max(1,512*screenScale*2**z),'prefetch');}
}

function drawElement(ctx,element,asset,view,camera,dpr,requestImage){
  if(!elementIntersectsView(element,view))return;const t=element.transform;ctx.save();ctx.translate(t.x+t.width/2,t.y+t.height/2);ctx.rotate(t.rotation*Math.PI/180);ctx.translate(-t.width/2,-t.height/2);
  if(asset?.renderMode==='tiled')drawTiled(ctx,element,asset,view,camera,dpr,requestImage);
  else if(asset){const edge=Math.max(t.width,t.height)*camera.scale*dpr,bitmap=requestImage(`${asset.id}/image.png`,edge);if(bitmap)ctx.drawImage(bitmap,0,0,t.width,t.height);else{ctx.fillStyle='#33413d';ctx.fillRect(0,0,t.width,t.height);}}
  else{ctx.fillStyle='#422';ctx.fillRect(0,0,t.width,t.height);}ctx.restore();
}

function drawVisualLayer({ctx,state,layer,floorAlpha,view,camera,dpr,requestImage,renderIndex}){
  if(!layer.visible||layer.opacity<=0)return;const elements=renderIndex?renderIndex.forLayer(state,layer.id):Object.values(state.elements||{}).filter(element=>element.layerId===layer.id).sort((a,b)=>a.zOrder-b.zOrder||a.id.localeCompare(b.id));
  for(const entry of elements){const element=renderIndex?state.elements?.[entry]:entry;if(!element||element.floorId!==layer.floorId||!element.visible||element.opacity<=0)continue;ctx.globalAlpha=floorAlpha*layer.opacity*element.opacity;drawElement(ctx,element,state.assets?.[element.assetId],view,camera,dpr,requestImage);}
}

function drawSelection(ctx,element,camera,showRotation){
  if(!element)return;const t=element.transform,size=7/camera.scale,points=[[0,0],[t.width/2,0],[t.width,0],[t.width,t.height/2],[t.width,t.height],[t.width/2,t.height],[0,t.height],[0,t.height/2]];
  ctx.save();ctx.globalAlpha=1;ctx.translate(t.x+t.width/2,t.y+t.height/2);ctx.rotate(t.rotation*Math.PI/180);ctx.translate(-t.width/2,-t.height/2);ctx.strokeStyle='#edf8d5';ctx.lineWidth=2/camera.scale;ctx.strokeRect(0,0,t.width,t.height);if(showRotation){ctx.beginPath();ctx.moveTo(t.width/2,0);ctx.lineTo(t.width/2,-28/camera.scale);ctx.stroke();}ctx.fillStyle='#edf8d5';const handles=showRotation?[...points,[t.width/2,-28/camera.scale]]:points;for(const [x,y]of handles)ctx.fillRect(x-size/2,y-size/2,size,size);ctx.restore();
}

export function walkableBounds(state,floorId){return orderedLayers(state,floorId).find(layer=>layer.kind==='walkable')?.walkableBounds||(state?.currentFloorId===floorId?state.movementBounds:null)||null;}

export function drawSceneStack({ctx,state,currentFloorId,view,camera,dpr,requestImage,selectedElement,editor,drawTokenLayer,renderIndex,showRotationHandle=true}){
  for(const {floor,alpha}of compositeFloors(state,currentFloorId)){
    if(alpha<=0)continue;
    for(const layer of orderedLayers(state,floor.id)){
      if(layer.kind==='visual')drawVisualLayer({ctx,state,layer,floorAlpha:alpha,view,camera,dpr,requestImage,renderIndex});
      else if(layer.kind==='tokens')drawTokenLayer(floor.id,alpha);
    }
  }
  ctx.globalAlpha=1;if(editor){const selected=state.elements?.[selectedElement];if(selected?.floorId===currentFloorId)drawSelection(ctx,selected,camera,showRotationHandle);}
}

export function drawWalkableOverlay(ctx,state,currentFloorId,camera){
  const bounds=walkableBounds(state,currentFloorId);if(!bounds)return;ctx.save();ctx.globalAlpha=1;ctx.fillStyle='#65d6a414';ctx.strokeStyle='#65d6a4cc';ctx.lineWidth=2/camera.scale;ctx.setLineDash([10/camera.scale,7/camera.scale]);ctx.fillRect(bounds.x,bounds.y,bounds.width,bounds.height);ctx.strokeRect(bounds.x,bounds.y,bounds.width,bounds.height);ctx.restore();
}

export function transitionContains(endpoint,x,y){return !!endpoint&&Math.hypot(x-endpoint.position.x,y-endpoint.position.y)<=endpoint.radius;}

export function transitionHit(state,currentFloorId,x,y,scale,selectedId=''){
  const transitions=Object.values(state?.transitions||{}),handleRadius=10/scale;
  const ordered=selectedId?[...transitions.filter(t=>t.id===selectedId),...transitions.filter(t=>t.id!==selectedId)]:transitions;
  for(const transition of ordered){
    for(const key of ['endpointA','endpointB']){
      const endpoint=transition[key];if(endpoint.floorId!==currentFloorId)continue;
      const radiusHandle={x:endpoint.position.x+endpoint.radius,y:endpoint.position.y};
      if(transition.id===selectedId&&Math.hypot(x-radiusHandle.x,y-radiusHandle.y)<=handleRadius)return {transition,key,part:'radius'};
      if(transition.id===selectedId&&Math.hypot(x-endpoint.position.x,y-endpoint.position.y)<=handleRadius)return {transition,key,part:'center'};
      if(transitionContains(endpoint,x,y))return {transition,key,part:'zone'};
    }
  }
  return null;
}

function drawTransitionEndpoint(ctx,endpoint,label,camera,selected,ghost=false){
  const line=2/camera.scale,handle=6/camera.scale;ctx.save();ctx.globalAlpha=ghost ? .55 : 1;ctx.strokeStyle=selected?'#edf8d5':'#9ab48a';ctx.fillStyle=selected?'#c2d89b20':'#86a67512';ctx.lineWidth=line;ctx.setLineDash(selected?[]:[7/camera.scale,6/camera.scale]);ctx.beginPath();ctx.arc(endpoint.position.x,endpoint.position.y,endpoint.radius,0,Math.PI*2);ctx.fill();ctx.stroke();ctx.setLineDash([]);ctx.fillStyle=selected?'#edf8d5':'#9ab48a';ctx.beginPath();ctx.arc(endpoint.position.x,endpoint.position.y,handle,0,Math.PI*2);ctx.fill();ctx.font=`${12/camera.scale}px system-ui`;ctx.textAlign='center';ctx.textBaseline='middle';ctx.fillText(label,endpoint.position.x,endpoint.position.y-handle*2.3);if(selected){ctx.beginPath();ctx.arc(endpoint.position.x+endpoint.radius,endpoint.position.y,handle,0,Math.PI*2);ctx.fill();}ctx.restore();
}

export function drawTransitionOverlay(ctx,state,currentFloorId,camera,{selectedId='',draft=null,cursor=null}={}){
  const transitions=Object.values(state?.transitions||{});ctx.save();ctx.globalAlpha=1;
  for(const transition of transitions){
    const selected=transition.id===selectedId,a=transition.endpointA,b=transition.endpointB;
    if(selected&&(a.floorId===currentFloorId||b.floorId===currentFloorId)){ctx.save();ctx.strokeStyle='#b5c99a';ctx.fillStyle='#d8e8bd';ctx.lineWidth=1.5/camera.scale;ctx.setLineDash([7/camera.scale,6/camera.scale]);ctx.beginPath();ctx.moveTo(a.position.x,a.position.y);ctx.lineTo(b.position.x,b.position.y);ctx.stroke();ctx.setLineDash([]);ctx.font=`${15/camera.scale}px system-ui`;ctx.textAlign='center';ctx.textBaseline='middle';const mark=transition.direction==='AToB'?'→':transition.direction==='BToA'?'←':'↔';ctx.fillText(mark,(a.position.x+b.position.x)/2,(a.position.y+b.position.y)/2);ctx.restore();}
    if(a.floorId===currentFloorId)drawTransitionEndpoint(ctx,a,'A',camera,selected);else if(selected)drawTransitionEndpoint(ctx,a,'A',camera,false,true);
    if(b.floorId===currentFloorId)drawTransitionEndpoint(ctx,b,'B',camera,selected);else if(selected)drawTransitionEndpoint(ctx,b,'B',camera,false,true);
  }
  if(draft){
    const a=draft.endpointA,b=draft.endpointB,active=draft.phase==='editingA'?'endpointA':draft.phase==='editingB'?'endpointB':'';
    if(a){if(a.floorId===currentFloorId)drawTransitionEndpoint(ctx,a,'A',camera,true);if((draft.phase==='placingB'||draft.phase==='editingB')&&cursor){ctx.save();ctx.strokeStyle='#d8e8bd';ctx.lineWidth=1.5/camera.scale;ctx.setLineDash([7/camera.scale,6/camera.scale]);ctx.beginPath();ctx.moveTo(a.position.x,a.position.y);ctx.lineTo((b||cursor).position?.x??cursor.x,(b||cursor).position?.y??cursor.y);ctx.stroke();ctx.restore();}}
    if(b&&b.floorId===currentFloorId)drawTransitionEndpoint(ctx,b,'B',camera,true);
    if(active){const endpoint=draft[active];if(endpoint?.floorId===currentFloorId){const offset=endpoint.radius+18/camera.scale,size=7/camera.scale,y=endpoint.position.y-14/camera.scale,x=endpoint.position.x+offset;ctx.save();ctx.fillStyle='#cfe5a9';ctx.fillRect(x-size,y-size,2*size,2*size);ctx.fillStyle='#20301e';ctx.font=`${11/camera.scale}px system-ui`;ctx.textAlign='center';ctx.textBaseline='middle';ctx.fillText('✓',x,y);ctx.fillStyle='#e59b91';const cancelX=x+21/camera.scale;ctx.fillRect(cancelX-size,y-size,2*size,2*size);ctx.fillStyle='#35201f';ctx.fillText('×',cancelX,y);ctx.restore();}}
  }
  ctx.restore();
}
