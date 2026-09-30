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
export function renderBoundsAABB(polygon){const points=polygon?.outer;if(!points?.length)return null;const bounds=polygonBounds(points);return {...bounds,width:bounds.right-bounds.left,height:bounds.bottom-bounds.top};}
export function floorCameraBounds(state,floorId){
  const renderBounds=renderBoundsAABB(state?.floors?.[floorId]?.renderBounds);if(renderBounds)return renderBounds;
  const catalog=Object.values(state?.elementCatalog||{}),elements=catalog.length?catalog:Object.values(state?.elements||{}),points=[];
  for(const element of elements){if(element.floorId!==floorId||!element.transform)continue;const t=element.transform;for(const [x,y]of [[0,0],[t.width,0],[t.width,t.height],[0,t.height]])points.push(worldPoint(t,x,y));}
  for(const token of Object.values(state?.tokens||{})){if(token.floorId!==floorId)continue;const radius=(token.size||0)/2;points.push({x:token.x-radius,y:token.y-radius},{x:token.x+radius,y:token.y+radius});}
  if(!points.length)return null;const bounds=polygonBounds(points);return {...bounds,width:Math.max(1,bounds.right-bounds.left),height:Math.max(1,bounds.bottom-bounds.top)};
}
function rectIntersectsPolygon(left,top,right,bottom,points){
  const axes=[[1,0],[0,1]];for(let i=0;i<points.length;i++){const a=points[i],b=points[(i+1)%points.length];axes.push([-(b.y-a.y),b.x-a.x]);}
  for(const [ax,ay]of axes){let pmin=Infinity,pmax=-Infinity;for(const p of points){const value=p.x*ax+p.y*ay;pmin=Math.min(pmin,value);pmax=Math.max(pmax,value);}const values=[left*ax+top*ay,right*ax+top*ay,right*ax+bottom*ay,left*ax+bottom*ay],rmin=Math.min(...values),rmax=Math.max(...values);if(pmax<rmin||rmax<pmin)return false;}return true;
}

function rectIntersectsSimplePolygon(left,top,right,bottom,points,bounds=polygonBounds(points)){
  if(right<bounds.left||left>bounds.right||bottom<bounds.top||top>bounds.bottom)return false;
  for(const point of points)if(point.x>=left&&point.x<=right&&point.y>=top&&point.y<=bottom)return true;
  const corners=[{x:left,y:top},{x:right,y:top},{x:right,y:bottom},{x:left,y:bottom}];if(corners.some(point=>ringContains(points,point.x,point.y)))return true;
  for(let i=0;i<points.length;i++)for(let j=0;j<corners.length;j++)if(renderBoundsSegmentsIntersect(points[i],points[(i+1)%points.length],corners[j],corners[(j+1)%corners.length]))return true;
  return false;
}

export class FloorRenderBoundsResourceCache{
  constructor(limit=8192,elementLimit=512){this.limit=limit;this.elementLimit=elementLimit;this.sceneId='';this.revisions=new Map();this.floors=new Map();this.elements=new Map();this.classifications=new Map();this.hits=0;this.misses=0;}
  clear(){this.sceneId='';this.revisions.clear();this.floors.clear();this.elements.clear();this.classifications.clear();}
  invalidateFloor(floorId){this.revisions.delete(floorId);this.floors.delete(floorId);for(const [key,value]of this.elements)if(value.floorId===floorId)this.elements.delete(key);for(const [key,value]of this.classifications)if(value.floorId===floorId)this.classifications.delete(key);}
  syncState(state){
    const sceneId=state?.scene?.id||'';if(sceneId!==this.sceneId){this.clear();this.sceneId=sceneId;}const floors=state?.floors||{},live=new Set(Object.keys(floors));
    for(const floorId of this.revisions.keys())if(!live.has(floorId))this.invalidateFloor(floorId);for(const floor of Object.values(floors))this.sync(state,floor);
  }
  sync(state,floor){
    const sceneId=state?.scene?.id||'';if(sceneId!==this.sceneId){this.clear();this.sceneId=sceneId;}
    const previous=this.revisions.get(floor.id),revision=floor.geometryRevision;if(previous===revision)return;if(previous!==undefined)this.invalidateFloor(floor.id);this.revisions.set(floor.id,revision);
  }
  floorBounds(state,floor){
    this.sync(state,floor);let cached=this.floors.get(floor.id);if(cached)return cached;const points=floor.renderBounds?.outer||[];cached={points,bounds:points.length?polygonBounds(points):null};this.floors.set(floor.id,cached);return cached;
  }
  localBounds(state,floor,element){
    this.sync(state,floor);const t=element.transform,signature=`${floor.geometryRevision}:${t.x}:${t.y}:${t.width}:${t.height}:${t.rotation}`,key=`${floor.id}:${element.id}`,cached=this.elements.get(key);if(cached?.signature===signature){this.elements.delete(key);this.elements.set(key,cached);return cached;}
    const points=(floor.renderBounds?.outer||[]).map(point=>inversePoint(t,point.x,point.y)),entry={floorId:floor.id,signature,points,bounds:points.length?polygonBounds(points):null};this.elements.delete(key);this.elements.set(key,entry);while(this.elements.size>this.elementLimit)this.elements.delete(this.elements.keys().next().value);return entry;
  }
  intersectsRect(state,floor,element,rectKey,left,top,right,bottom){
    if(!floor?.renderBounds)return true;const local=this.localBounds(state,floor,element),key=`${floor.id}:${element.id}:${local.signature}:${rectKey}`,cached=this.classifications.get(key);if(cached){this.hits++;this.classifications.delete(key);this.classifications.set(key,cached);return cached.result;}
    this.misses++;const result=!!local.bounds&&rectIntersectsSimplePolygon(left,top,right,bottom,local.points,local.bounds);this.classifications.set(key,{floorId:floor.id,result});while(this.classifications.size>this.limit)this.classifications.delete(this.classifications.keys().next().value);return result;
  }
  intersectsCircle(state,floor,x,y,radius){
    if(!floor?.renderBounds)return true;const geometry=this.floorBounds(state,floor),bounds=geometry.bounds;if(!bounds||x+radius<bounds.left||x-radius>bounds.right||y+radius<bounds.top||y-radius>bounds.bottom)return false;if(ringContains(geometry.points,x,y))return true;for(let i=0;i<geometry.points.length;i++)if(renderBoundsEdgeDistance({x,y},geometry.points[i],geometry.points[(i+1)%geometry.points.length])<=radius)return true;return false;
  }
}

export function elementIntersectsView(element,view){const t=element?.transform;if(!t||!view)return false;return rectIntersectsPolygon(0,0,t.width,t.height,viewportPolygon(t,view));}

const tilePresenceCache=new WeakMap();
export function tileAvailable(asset,z,x,y,nx){
  if(!asset?.tilePresence)return true;let levels=tilePresenceCache.get(asset);if(!levels){levels=asset.tilePresence.split('.').map(encoded=>{const raw=atob(encoded),bytes=new Uint8Array(raw.length);for(let i=0;i<raw.length;i++)bytes[i]=raw.charCodeAt(i);return bytes;});tilePresenceCache.set(asset,levels);}const index=y*nx+x,bytes=levels[z];return !!bytes&&(bytes[index>>3]&(1<<(index&7)))!==0;
}

export function tiledFallbackCandidates(asset,z,x,y){
  const unit=512*2**z,left=x*unit,top=y*unit,right=Math.min(asset.width,left+unit),bottom=Math.min(asset.height,top+unit),out=[];
  for(let fallbackZ=z+1;fallbackZ<asset.levels;fallbackZ++){
    const scale=2**fallbackZ,levelWidth=Math.ceil(asset.width/scale),levelHeight=Math.ceil(asset.height/scale),fallbackX=Math.floor(left/(512*scale)),fallbackY=Math.floor(top/(512*scale)),nx=Math.ceil(levelWidth/512);
    if(!tileAvailable(asset,fallbackZ,fallbackX,fallbackY,nx))continue;
    const nativeWidth=Math.min(512,levelWidth-fallbackX*512),nativeHeight=Math.min(512,levelHeight-fallbackY*512);
    out.push({path:`${asset.id}/${fallbackZ}_${fallbackX}_${fallbackY}.png`,nativeWidth,nativeHeight,cropX:left/scale-fallbackX*512,cropY:top/scale-fallbackY*512,cropWidth:(right-left)/scale,cropHeight:(bottom-top)/scale});
  }
  return out;
}

function drawTiledFallback(ctx,asset,z,x,y,left,top,width,height,peekImage){
  if(!peekImage)return false;
  for(const candidate of tiledFallbackCandidates(asset,z,x,y)){
    const bitmap=peekImage(candidate.path);if(!bitmap)continue;
    const scaleX=bitmap.width/candidate.nativeWidth,scaleY=bitmap.height/candidate.nativeHeight;
    ctx.drawImage(bitmap,candidate.cropX*scaleX,candidate.cropY*scaleY,candidate.cropWidth*scaleX,candidate.cropHeight*scaleY,left,top,width,height);return true;
  }
  return false;
}

function drawTiled(ctx,element,asset,view,camera,dpr,requestImage,peekImage,state,floor,resourceBounds){
  const t=element.transform,polygon=viewportPolygon(t,view),local=polygonBounds(polygon),sx=t.width/asset.width,sy=t.height/asset.height,screenScale=camera.scale*dpr*Math.max(sx,sy);
  const z=Math.max(0,Math.min(asset.levels-1,Math.floor(Math.log2(1/Math.max(screenScale,.000001))))),unit=512*2**z,nx=Math.ceil(asset.width/unit),ny=Math.ceil(asset.height/unit),tileW=unit*sx,tileH=unit*sy;
  const x0=Math.max(0,Math.floor(local.left/tileW)),y0=Math.max(0,Math.floor(local.top/tileH)),x1=Math.min(nx-1,Math.floor(local.right/tileW)),y1=Math.min(ny-1,Math.floor(local.bottom/tileH));
  const visible=new Set();for(let y=y0;y<=y1;y++)for(let x=x0;x<=x1;x++){const left=x*tileW,top=y*tileH,right=Math.min(t.width,left+tileW),bottom=Math.min(t.height,top+tileH),key=`${x}:${y}`;if(!rectIntersectsPolygon(left,top,right,bottom,polygon)||resourceBounds&&!resourceBounds.intersectsRect(state,floor,element,`tile:${z}:${key}`,left,top,right,bottom))continue;visible.add(key);if(!tileAvailable(asset,z,x,y,nx))continue;const bitmap=requestImage(`${asset.id}/${z}_${x}_${y}.png`,Math.max(1,512*screenScale*2**z));if(bitmap)ctx.drawImage(bitmap,left,top,right-left,bottom-top);else drawTiledFallback(ctx,asset,z,x,y,left,top,right-left,bottom-top,peekImage);}
  const prefetch=new Set();for(const key of visible){const [x,y]=key.split(':').map(Number);for(let dy=-1;dy<=1;dy++)for(let dx=-1;dx<=1;dx++){const px=x+dx,py=y+dy,next=`${px}:${py}`;if(px>=0&&py>=0&&px<nx&&py<ny&&!visible.has(next))prefetch.add(next);}}
  for(const key of prefetch){const [x,y]=key.split(':').map(Number),left=x*tileW,top=y*tileH,right=Math.min(t.width,left+tileW),bottom=Math.min(t.height,top+tileH);if(resourceBounds&&!resourceBounds.intersectsRect(state,floor,element,`tile:${z}:${key}`,left,top,right,bottom))continue;if(tileAvailable(asset,z,x,y,nx))requestImage(`${asset.id}/${z}_${x}_${y}.png`,Math.max(1,512*screenScale*2**z),'prefetch');}
}

function drawElement(ctx,element,asset,view,camera,dpr,requestImage,peekImage,state,floor,resourceBounds){
  if(!elementIntersectsView(element,view))return;const t=element.transform;if(resourceBounds&&!resourceBounds.intersectsRect(state,floor,element,'element',0,0,t.width,t.height))return;ctx.save();ctx.translate(t.x+t.width/2,t.y+t.height/2);ctx.rotate(t.rotation*Math.PI/180);ctx.translate(-t.width/2,-t.height/2);
  if(asset?.renderMode==='tiled')drawTiled(ctx,element,asset,view,camera,dpr,requestImage,peekImage,state,floor,resourceBounds);
  else if(asset){const edge=Math.max(t.width,t.height)*camera.scale*dpr,bitmap=requestImage(`${asset.id}/image.png`,edge);if(bitmap)ctx.drawImage(bitmap,0,0,t.width,t.height);else{ctx.fillStyle='#33413d';ctx.fillRect(0,0,t.width,t.height);}}
  else{ctx.fillStyle='#422';ctx.fillRect(0,0,t.width,t.height);}ctx.restore();
}

function drawVisualLayer({ctx,state,layer,floor,floorAlpha,view,camera,dpr,requestImage,peekImage,renderIndex,resourceBounds}){
  if(!layer.visible||layer.opacity<=0)return;const elements=renderIndex?renderIndex.forLayer(state,layer.id):Object.values(state.elements||{}).filter(element=>element.layerId===layer.id).sort((a,b)=>a.zOrder-b.zOrder||a.id.localeCompare(b.id));
  for(const entry of elements){const element=renderIndex?state.elements?.[entry]:entry;if(!element||element.floorId!==layer.floorId||!element.visible||element.opacity<=0)continue;ctx.globalAlpha=floorAlpha*layer.opacity*element.opacity;drawElement(ctx,element,state.assets?.[element.assetId],view,camera,dpr,requestImage,peekImage,state,floor,resourceBounds);}
}

function drawSelection(ctx,element,camera,showRotation){
  if(!element)return;const t=element.transform,size=7/camera.scale,points=[[0,0],[t.width/2,0],[t.width,0],[t.width,t.height/2],[t.width,t.height],[t.width/2,t.height],[0,t.height],[0,t.height/2]];
  ctx.save();ctx.globalAlpha=1;ctx.translate(t.x+t.width/2,t.y+t.height/2);ctx.rotate(t.rotation*Math.PI/180);ctx.translate(-t.width/2,-t.height/2);ctx.strokeStyle='#edf8d5';ctx.lineWidth=2/camera.scale;ctx.strokeRect(0,0,t.width,t.height);if(showRotation){ctx.beginPath();ctx.moveTo(t.width/2,0);ctx.lineTo(t.width/2,-28/camera.scale);ctx.stroke();}ctx.fillStyle='#edf8d5';const handles=showRotation?[...points,[t.width/2,-28/camera.scale]]:points;for(const [x,y]of handles)ctx.fillRect(x-size/2,y-size/2,size,size);ctx.restore();
}


function pointOnSegment(a,b,x,y){const cross=(b.x-a.x)*(y-a.y)-(b.y-a.y)*(x-a.x);if(Math.abs(cross)>1e-7)return false;return x>=Math.min(a.x,b.x)-1e-7&&x<=Math.max(a.x,b.x)+1e-7&&y>=Math.min(a.y,b.y)-1e-7&&y<=Math.max(a.y,b.y)+1e-7;}
function ringContains(ring,x,y){let inside=false;for(let i=0,j=ring.length-1;i<ring.length;j=i++){const a=ring[j],b=ring[i];if(pointOnSegment(a,b,x,y))return 2;if((a.y>y)!==(b.y>y)&&x<(b.x-a.x)*(y-a.y)/(b.y-a.y)+a.x)inside=!inside;}return inside?1:0;}
export function pointInRenderBounds(state,floorId,x,y){const polygon=state?.floors?.[floorId]?.renderBounds;return !polygon||!!ringContains(polygon.outer||[],x,y);}
export function pointInWalkablePolygon(polygon,x,y){const outer=ringContains(polygon?.outer||[],x,y);if(!outer)return false;if(outer===2)return true;for(const hole of polygon.holes||[]){const hit=ringContains(hole,x,y);if(hit===1)return false;if(hit===2)return true;}return true;}
export function pointInPlayableArea(state,floorId,x,y){const floor=state?.floors?.[floorId];if(!floor||!pointInRenderBounds(state,floorId,x,y))return false;if(floor.walkableMode!=='restricted')return true;for(const component of floor.walkableComponents||[])if(pointInWalkablePolygon(component.polygon,x,y))return true;return false;}
export function walkableComponentAt(state,floorId,x,y){const components=[...(state?.floors?.[floorId]?.walkableComponents||[])].sort((a,b)=>a.id.localeCompare(b.id));for(let i=components.length-1;i>=0;i--)if(pointInWalkablePolygon(components[i].polygon,x,y))return components[i];return null;}

function renderBoundsSegmentsIntersect(a,b,c,d){
  const cross=(p,q,r)=>(q.x-p.x)*(r.y-p.y)-(q.y-p.y)*(r.x-p.x),on=(p,q,r)=>Math.abs(cross(p,q,r))<=1e-7&&q.x>=Math.min(p.x,r.x)-1e-7&&q.x<=Math.max(p.x,r.x)+1e-7&&q.y>=Math.min(p.y,r.y)-1e-7&&q.y<=Math.max(p.y,r.y)+1e-7,o1=cross(a,b,c),o2=cross(a,b,d),o3=cross(c,d,a),o4=cross(c,d,b);
  return on(a,c,b)||on(a,d,b)||on(c,a,d)||on(c,b,d)||((o1<0)!==(o2<0))&&((o3<0)!==(o4<0));
}

export class FloorRenderBoundsPathCache{
  constructor(){this.sceneId='';this.entries=new Map();this.builds=0;}
  clear(){this.sceneId='';this.entries.clear();}
  syncState(state){const sceneId=state?.scene?.id||'';if(sceneId!==this.sceneId){this.clear();this.sceneId=sceneId;}const floors=state?.floors||{};for(const [floorId,entry]of this.entries){const floor=floors[floorId];if(!floor?.renderBounds||entry.revision!==floor.geometryRevision)this.entries.delete(floorId);}}
  path(state,floor){
    const sceneId=state?.scene?.id||'';if(sceneId!==this.sceneId){this.sceneId=sceneId;this.entries.clear();}
    if(!floor?.renderBounds){this.entries.delete(floor?.id);return null;}const cached=this.entries.get(floor.id);if(cached?.revision===floor.geometryRevision)return cached.path;
    const path=new Path2D(),points=floor.renderBounds.outer||[];if(points.length){path.moveTo(points[0].x,points[0].y);for(let i=1;i<points.length;i++)path.lineTo(points[i].x,points[i].y);path.closePath();}this.entries.set(floor.id,{revision:floor.geometryRevision,path});this.builds++;return path;
  }
}

export function abortUnwantedImageLoads(pending,wanted){let aborted=0;for(const [key,controller]of pending)if(!wanted.has(key)&&!controller.signal.aborted){controller.abort();aborted++;}return aborted;}
const defaultRenderBoundsPaths=new FloorRenderBoundsPathCache();
export function clipFloorRenderBounds(ctx,state,floor,paths=defaultRenderBoundsPaths){const path=paths.path(state,floor);if(!path)return false;ctx.clip(path);return true;}
function validRenderBoundsBasics(polygon){
  const ring=polygon?.outer;if(!Array.isArray(ring)||ring.length<3||ring.length>10000||(polygon.holes?.length||0))return false;
  for(const point of ring)if(!Number.isFinite(point?.x)||!Number.isFinite(point?.y)||Math.abs(point.x)>1000000||Math.abs(point.y)>1000000)return false;
  let area=0;for(let i=0;i<ring.length;i++){const next=ring[(i+1)%ring.length];if(Math.hypot(ring[i].x-next.x,ring[i].y-next.y)<.0005)return false;area+=ring[i].x*next.y-next.x*ring[i].y;}
  return Math.abs(area)>=.000001;
}
export function validRenderBounds(polygon){
  if(!validRenderBoundsBasics(polygon))return false;const ring=polygon.outer;
  for(let i=0;i<ring.length;i++)for(let j=i+1;j<ring.length;j++){if(j===i+1||i===0&&j===ring.length-1)continue;if(renderBoundsSegmentsIntersect(ring[i],ring[(i+1)%ring.length],ring[j],ring[(j+1)%ring.length]))return false;}
  return true;
}
export function validRenderBoundsChange(polygon,changedEdges=[]){
  if(!validRenderBoundsBasics(polygon))return false;const ring=polygon.outer,n=ring.length,edges=[...new Set(changedEdges.map(index=>(index%n+n)%n))];
  for(const i of edges)for(let j=0;j<n;j++){if(j===i||j===(i+1)%n||(j+1)%n===i)continue;if(renderBoundsSegmentsIntersect(ring[i],ring[(i+1)%n],ring[j],ring[(j+1)%n]))return false;}
  return true;
}
function renderBoundsEdgeDistance(point,a,b){const dx=b.x-a.x,dy=b.y-a.y,length=dx*dx+dy*dy;if(!length)return Math.hypot(point.x-a.x,point.y-a.y);const t=Math.max(0,Math.min(1,((point.x-a.x)*dx+(point.y-a.y)*dy)/length)),x=a.x+t*dx,y=a.y+t*dy;return Math.hypot(point.x-x,point.y-y);}
export function renderBoundsHit(polygon,x,y,scale){
  const ring=polygon?.outer||[],radius=14/scale;for(let i=0;i<ring.length;i++)if(Math.hypot(x-ring[i].x,y-ring[i].y)<=radius)return {part:'vertex',index:i};
  for(let i=0;i<ring.length;i++)if(renderBoundsEdgeDistance({x,y},ring[i],ring[(i+1)%ring.length])<=radius)return {part:'edge',index:i};
  return ringContains(ring,x,y)?{part:'polygon',index:-1}:null;
}

function traceWalkablePolygon(ctx,polygon,dx=0,dy=0){for(const ring of [polygon.outer,...(polygon.holes||[])]){if(!ring.length)continue;ctx.moveTo(ring[0].x+dx,ring[0].y+dy);for(let i=1;i<ring.length;i++)ctx.lineTo(ring[i].x+dx,ring[i].y+dy);ctx.closePath();}}
function drawWalkablePolygon(ctx,polygon,camera,{dx=0,dy=0,selected=false,preview=false,subtract=false}={}){ctx.save();ctx.beginPath();traceWalkablePolygon(ctx,polygon,dx,dy);ctx.fillStyle=subtract?'#e68f7d24':preview?'#c8e89b24':'#65d6a414';ctx.strokeStyle=subtract?'#f0a08d':selected?'#edf8d5':'#65d6a4cc';ctx.lineWidth=(selected?3:2)/camera.scale;ctx.setLineDash(preview?[8/camera.scale,5/camera.scale]:[]);ctx.fill('evenodd');ctx.stroke();ctx.restore();}
export function drawWalkableEditor(ctx,state,floorId,camera,{drag=null,selectedId=''}={}){
  const floor=state?.floors?.[floorId];if(!floor)return;const components=floor.walkableComponents||[],moving=drag?.type==='walkableComponent'&&drag.floorId===floorId?drag:null;
  for(const component of components){if(moving?.componentId===component.id)continue;drawWalkablePolygon(ctx,component.polygon,camera,{selected:component.id===selectedId});}
  if(moving){const component=components.find(item=>item.id===moving.componentId);if(component)drawWalkablePolygon(ctx,component.polygon,camera,{dx:moving.dx,dy:moving.dy,selected:true,preview:true});}
  if(drag?.type==='walkableRect'&&drag.floorId===floorId){const left=Math.min(drag.start.x,drag.current.x),top=Math.min(drag.start.y,drag.current.y),right=Math.max(drag.start.x,drag.current.x),bottom=Math.max(drag.start.y,drag.current.y);if(right>left&&bottom>top)drawWalkablePolygon(ctx,{outer:[{x:left,y:top},{x:right,y:top},{x:right,y:bottom},{x:left,y:bottom}],holes:[]},camera,{preview:true,subtract:drag.operation==='subtractWalkableRect'});}
}

function traceRenderBounds(ctx,points,close=true){if(!points?.length)return;ctx.moveTo(points[0].x,points[0].y);for(let i=1;i<points.length;i++)ctx.lineTo(points[i].x,points[i].y);if(close)ctx.closePath();}
export function drawRenderBoundsEditor(ctx,state,floorId,camera,{tool='',draft=null,drag=null,selectedVertex=-1}={}){
  const authoritative=state?.floors?.[floorId]?.renderBounds,rectangle=drag?.type==='renderBoundsRect'&&drag.floorId===floorId?{outer:[drag.start,{x:drag.current.x,y:drag.start.y},drag.current,{x:drag.start.x,y:drag.current.y}],holes:[]}:null,edit=drag?.type==='renderBoundsEdit'&&drag.floorId===floorId?drag.preview:null,polygonDraft=draft?.floorId===floorId?{outer:draft.points,holes:[]}:null,preview=rectangle||edit||polygonDraft||authoritative;
  if(!preview)return;const incomplete=polygonDraft&&polygonDraft.outer.length<3,invalid=!incomplete&&(rectangle?!validRenderBounds(preview):edit?!!drag.invalid:polygonDraft?!!draft.invalid:false);
  ctx.save();ctx.beginPath();traceRenderBounds(ctx,preview.outer,!incomplete);if(polygonDraft&&draft.cursor&&draft.points.length){ctx.moveTo(draft.points.at(-1).x,draft.points.at(-1).y);ctx.lineTo(draft.cursor.x,draft.cursor.y);if(draft.points.length>1)ctx.lineTo(draft.points[0].x,draft.points[0].y);}ctx.fillStyle=invalid?'#e66f6728':'#e2bf5b1f';ctx.strokeStyle=invalid?'#ff8178':'#e7c866';ctx.lineWidth=2/camera.scale;ctx.setLineDash((rectangle||edit||polygonDraft)?[8/camera.scale,5/camera.scale]:[]);if(!incomplete)ctx.fill('evenodd');ctx.stroke();ctx.setLineDash([]);if(tool&&preview.outer.length){const radius=4.5/camera.scale;for(let i=0;i<preview.outer.length;i++){const point=preview.outer[i];ctx.fillStyle=invalid?'#ff8178':i===selectedVertex?'#fff4c5':'#f3d77b';ctx.beginPath();ctx.arc(point.x,point.y,radius,0,Math.PI*2);ctx.fill();}if(tool==='edit'){ctx.strokeStyle='#d7be71aa';ctx.lineWidth=1.5/camera.scale;const size=5/camera.scale;for(let i=0;i<preview.outer.length;i++){const a=preview.outer[i],b=preview.outer[(i+1)%preview.outer.length],x=(a.x+b.x)/2,y=(a.y+b.y)/2;ctx.strokeRect(x-size/2,y-size/2,size,size);}}}ctx.restore();
}

export function drawSceneStack({ctx,state,currentFloorId,view,camera,dpr,requestImage,peekImage,selectedElement,editor,drawTokenLayer,renderIndex,renderBoundsPaths,resourceBounds,clipRenderBounds=!editor,showRotationHandle=true}){
  for(const {floor,alpha}of compositeFloors(state,currentFloorId)){
    if(alpha<=0)continue;const clipped=clipRenderBounds&&floor.renderBounds;if(clipped){ctx.save();clipFloorRenderBounds(ctx,state,floor,renderBoundsPaths);}
    for(const layer of orderedLayers(state,floor.id)){
      if(layer.kind==='visual')drawVisualLayer({ctx,state,layer,floor,floorAlpha:alpha,view,camera,dpr,requestImage,peekImage,renderIndex,resourceBounds:clipped?resourceBounds:null});
      else if(layer.kind==='tokens')drawTokenLayer(floor.id,alpha);
    }
    if(clipped)ctx.restore();
  }
  ctx.globalAlpha=1;if(editor){const selected=state.elements?.[selectedElement];if(selected?.floorId===currentFloorId)drawSelection(ctx,selected,camera,showRotationHandle);}
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
