const SEGMENT_EPSILON=1e-9;
const GEOMETRY_QUANTUM=.001;
const SLIDE_INSET=2*GEOMETRY_QUANTUM;
const MAX_SLIDE_EDGES=8;
const preparedFloors=new WeakMap();

function gridValue(value){const scaled=value/GEOMETRY_QUANTUM;return scaled<0?-Math.round(-scaled):Math.round(scaled);}
function gridPoint(point){return {x:gridValue(point.x),y:gridValue(point.y)};}
function crossSign(a,b,c){
  const abx=b.x-a.x,aby=b.y-a.y,acx=c.x-a.x,acy=c.y-a.y,value=abx*acy-aby*acx;
  if(Math.abs(value)>4096)return Math.sign(value);
  const exact=BigInt(abx)*BigInt(acy)-BigInt(aby)*BigInt(acx);
  return exact<0n?-1:exact>0n?1:0;
}
function onGridSegment(a,b,point){return crossSign(a,b,point)===0&&point.x>=Math.min(a.x,b.x)&&point.x<=Math.max(a.x,b.x)&&point.y>=Math.min(a.y,b.y)&&point.y<=Math.max(a.y,b.y);}
function ringLocation(ring,point){
  if(!ring?.length)return 0;
  let inside=false;
  for(let i=0;i<ring.length;i++){
    const a=ring[i],b=ring[(i+1)%ring.length];
    if(onGridSegment(a,b,point))return 2;
    if((a.y>point.y)!==(b.y>point.y)){
      const x=(b.x-a.x)*(point.y-a.y)/(b.y-a.y)+a.x;
      if(point.x<x)inside=!inside;
    }
  }
  return inside?1:0;
}
function pointInPolygon(entry,point){
  const target=gridPoint(point),outer=ringLocation(entry.gridRings[0],target);
  if(outer===0)return false;
  if(outer===2)return true;
  for(let i=1;i<entry.gridRings.length;i++){const location=ringLocation(entry.gridRings[i],target);if(location===1)return false;if(location===2)return true;}
  return true;
}
function polygonBounds(polygon){
  const points=polygon.outer;let minX=points[0].x,minY=points[0].y,maxX=minX,maxY=minY;
  for(let i=1;i<points.length;i++){const point=points[i];minX=Math.min(minX,point.x);minY=Math.min(minY,point.y);maxX=Math.max(maxX,point.x);maxY=Math.max(maxY,point.y);}
  return {minX,minY,maxX,maxY};
}
function boundsIntersect(a,b){return a.minX<=b.maxX&&a.maxX>=b.minX&&a.minY<=b.maxY&&a.maxY>=b.minY;}
function preparePolygon(polygon,index){const rings=[polygon.outer,...(polygon.holes||[])];return {index,polygon,rings,gridRings:rings.map(ring=>ring.map(gridPoint)),bounds:polygonBounds(polygon)};}
function prepareFloor(floor){
  const cached=preparedFloors.get(floor);if(cached?.revision===floor.geometryRevision)return cached;
  const components=(floor.walkableComponents||[]).map((component,index)=>preparePolygon(component.polygon,index));
  const prepared={revision:floor.geometryRevision,components,renderBounds:floor.renderBounds?preparePolygon(floor.renderBounds,0):null,connections:new Map()};preparedFloors.set(floor,prepared);return prepared;
}
function samePoint(a,b){const ga=gridPoint(a),gb=gridPoint(b);return ga.x===gb.x&&ga.y===gb.y;}
function edgeParameters(a,b,c,d){
  const rx=b.x-a.x,ry=b.y-a.y,sx=d.x-c.x,sy=d.y-c.y,denominator=rx*sy-ry*sx,qx=c.x-a.x,qy=c.y-a.y;
  if(Math.abs(denominator)>SEGMENT_EPSILON){
    const t=(qx*sy-qy*sx)/denominator,u=(qx*ry-qy*rx)/denominator;
    return t>=-SEGMENT_EPSILON&&t<=1+SEGMENT_EPSILON&&u>=-SEGMENT_EPSILON&&u<=1+SEGMENT_EPSILON?[Math.min(1,Math.max(0,t))]:[];
  }
  if(Math.abs(qx*ry-qy*rx)>SEGMENT_EPSILON)return [];
  const lengthSquared=rx*rx+ry*ry;if(lengthSquared===0)return [];
  const result=[],t0=((c.x-a.x)*rx+(c.y-a.y)*ry)/lengthSquared,t1=((d.x-a.x)*rx+(d.y-a.y)*ry)/lengthSquared;
  for(const value of [t0,t1])if(value>=-SEGMENT_EPSILON&&value<=1+SEGMENT_EPSILON)result.push(Math.min(1,Math.max(0,value)));
  return result;
}
function appendRingParameters(parameters,from,to,ring){for(let i=0;i<ring.length;i++)parameters.push(...edgeParameters(from,to,ring[i],ring[(i+1)%ring.length]));}
function ownersAt(polygons,point){const owners=[];for(let i=0;i<polygons.length;i++)if(pointInPolygon(polygons[i],point))owners.push(i);return owners;}
function collinearOverlapHasLength(a,b,c,d){
  if(crossSign(a,b,c)!==0||crossSign(a,b,d)!==0)return false;
  if(Math.abs(b.x-a.x)>=Math.abs(b.y-a.y))return Math.min(b.x,a.x)<Math.max(c.x,d.x)&&Math.min(c.x,d.x)<Math.max(b.x,a.x);
  return Math.min(b.y,a.y)<Math.max(c.y,d.y)&&Math.min(c.y,d.y)<Math.max(b.y,a.y);
}
function polygonsShareBoundarySegment(left,right){
  const leftRings=left.gridRings,rightRings=right.gridRings;
  for(const a of leftRings)for(let i=0;i<a.length;i++)for(const b of rightRings)for(let j=0;j<b.length;j++)if(collinearOverlapHasLength(a[i],a[(i+1)%a.length],b[j],b[(j+1)%b.length]))return true;
  return false;
}
function componentsConnect(left,right,connections){
  if(left.index===right.index)return true;
  const key=left.index<right.index?`${left.index}:${right.index}`:`${right.index}:${left.index}`;
  if(connections?.has(key))return connections.get(key);
  const connected=polygonsShareBoundarySegment(left,right);connections?.set(key,connected);return connected;
}
function ownerSetsConnect(previous,next,polygons,connections){for(const left of previous)for(const right of next)if(componentsConnect(polygons[left],polygons[right],connections))return true;return false;}
function segmentLimitInPolygonUnion(from,to,polygons,connections){
  if(!polygons.length||!ownersAt(polygons,from).length)return {allowed:false,parameter:0};
  if(samePoint(from,to))return {allowed:!!ownersAt(polygons,to).length,parameter:0};
  const parameters=[0,1];
  for(const entry of polygons)for(const ring of entry.rings)appendRingParameters(parameters,from,to,ring);
  parameters.sort((a,b)=>a-b);let write=0;
  for(const value of parameters)if(write===0||value-parameters[write-1]>SEGMENT_EPSILON)parameters[write++]=value;
  parameters.length=write;let previous=[],lastValid=0;
  for(let i=0;i+1<parameters.length;i++){
    if(parameters[i+1]-parameters[i]<=SEGMENT_EPSILON)continue;
    const parameter=(parameters[i]+parameters[i+1])/2,point={x:from.x+(to.x-from.x)*parameter,y:from.y+(to.y-from.y)*parameter},owners=ownersAt(polygons,point);
    if(!owners.length||previous.length&&!ownerSetsConnect(previous,owners,polygons,connections))return {allowed:false,parameter:parameters[i]};
    previous=owners;lastValid=parameters[i+1];
  }
  return ownersAt(polygons,to).length?{allowed:true,parameter:1}:{allowed:false,parameter:lastValid};
}

function movementLimit(state,floorId,from,to){
  if(![from?.x,from?.y,to?.x,to?.y].every(Number.isFinite))return {allowed:false,parameter:0,polygons:[]};
  const floor=state?.floors?.[floorId];if(!floor)return {allowed:false,parameter:0,polygons:[]};
  const prepared=prepareFloor(floor);
  const constraints=[];let endpointJunction=false;
  if(floor.walkableMode==='restricted'){
    const segmentBounds={minX:Math.min(from.x,to.x),minY:Math.min(from.y,to.y),maxX:Math.max(from.x,to.x),maxY:Math.max(from.y,to.y)},candidates=prepared.components.filter(component=>boundsIntersect(component.bounds,segmentBounds));
    constraints.push({polygons:candidates,connections:prepared.connections});
    const endpointOwners=ownersAt(candidates,to);
    for(let i=0;i<endpointOwners.length&&!endpointJunction;i++)for(let j=i+1;j<endpointOwners.length;j++)if(!componentsConnect(candidates[endpointOwners[i]],candidates[endpointOwners[j]],prepared.connections)){endpointJunction=true;break;}
  }else if(floor.walkableMode!=='unrestricted')return {allowed:false,parameter:0,polygons:[]};
  if(prepared.renderBounds)constraints.push({polygons:[prepared.renderBounds],connections:null});
  let allowed=true,parameter=1,polygons=[];
  for(const constraint of constraints){
    const result=segmentLimitInPolygonUnion(from,to,constraint.polygons,constraint.connections);
    if(result.allowed)continue;
    allowed=false;
    if(result.parameter<parameter-SEGMENT_EPSILON){parameter=result.parameter;polygons=constraint.polygons;}
    else if(Math.abs(result.parameter-parameter)<=SEGMENT_EPSILON)polygons.push(...constraint.polygons);
  }
  return {allowed,parameter,polygons,endpointJunction};
}

function interpolate(from,to,parameter){return {x:from.x+(to.x-from.x)*parameter,y:from.y+(to.y-from.y)*parameter};}
function distanceSquared(a,b){const dx=a.x-b.x,dy=a.y-b.y;return dx*dx+dy*dy;}
function closestPointOnEdge(point,a,b){const dx=b.x-a.x,dy=b.y-a.y,lengthSquared=dx*dx+dy*dy;if(!lengthSquared)return {point:{x:a.x,y:a.y},parameter:0};const parameter=Math.max(0,Math.min(1,((point.x-a.x)*dx+(point.y-a.y)*dy)/lengthSquared));return {point:{x:a.x+dx*parameter,y:a.y+dy*parameter},parameter};}
function insetPoints(point,a,b){const dx=b.x-a.x,dy=b.y-a.y,length=Math.hypot(dx,dy);if(!length)return [];const nx=-dy/length*SLIDE_INSET,ny=dx/length*SLIDE_INSET;return [{x:point.x+nx,y:point.y+ny},{x:point.x-nx,y:point.y-ny}];}
function edgeKey(a,b){const ga=gridPoint(a),gb=gridPoint(b),left=ga.x<gb.x||ga.x===gb.x&&ga.y<=gb.y?ga:gb,right=left===ga?gb:ga;return `${left.x}:${left.y}:${right.x}:${right.y}`;}
function collisionEdges(from,to,parameter,polygons){
  const found=new Map();
  for(const entry of polygons)for(let ringIndex=0;ringIndex<entry.rings.length;ringIndex++){const ring=entry.rings[ringIndex];for(let i=0;i<ring.length;i++){
    const a=ring[i],b=ring[(i+1)%ring.length],parameters=edgeParameters(from,to,a,b);
    if(parameters.some(value=>Math.abs(value-parameter)<=SEGMENT_EPSILON*8))found.set(edgeKey(a,b),{entry,ringIndex,edgeIndex:i,a,b});
  }}
  return [...found.values()];
}
function adjacentEdge(edge,atStart){const ring=edge.entry.rings[edge.ringIndex];if(!ring||ring.length<2)return null;const edgeIndex=atStart?(edge.edgeIndex-1+ring.length)%ring.length:(edge.edgeIndex+1)%ring.length;return {entry:edge.entry,ringIndex:edge.ringIndex,edgeIndex,a:ring[edgeIndex],b:ring[(edgeIndex+1)%ring.length]};}
function tangentPoints(point,a,b){const dx=b.x-a.x,dy=b.y-a.y,length=Math.hypot(dx,dy);if(length<=SEGMENT_EPSILON)return [];const step=Math.min(SLIDE_INSET*2,length/4),offsetX=dx*step/length,offsetY=dy*step/length;return [{x:point.x-offsetX,y:point.y-offsetY},{x:point.x+offsetX,y:point.y+offsetY}];}
export function canMoveTokenSegment(state,floorId,from,to){return movementLimit(state,floorId,from,to).allowed;}

// Keep the pointer gesture responsive at a boundary. A bounded local search
// follows adjacent contour edges and edges of another active constraint, but
// every result still has to be a server-valid straight segment from the
// current network anchor.
export function constrainTokenMovement(state,floorId,from,to){
  let limit=movementLimit(state,floorId,from,to);
  if(limit.allowed&&!limit.endpointJunction)return {x:to.x,y:to.y};
  if(![from?.x,from?.y,to?.x,to?.y].every(Number.isFinite))return null;
  if(limit.allowed){const floor=state.floors[floorId];limit={allowed:false,parameter:1,polygons:prepareFloor(floor).components};}
  const contact=interpolate(from,to,Math.max(0,Math.min(1,limit.parameter))),fromDistance=distanceSquared(from,to);
  const edges=[],queued=new Set(),processed=[];
  const enqueue=(edge,edgeContact)=>{if(!edge)return;const key=edgeKey(edge.a,edge.b);if(queued.has(key)||edges.length>=MAX_SLIDE_EDGES)return;queued.add(key);edges.push({...edge,contact:edgeContact});};
  for(const edge of collisionEdges(from,to,limit.parameter,limit.polygons))enqueue(edge,contact);
  let bestStable=null,bestStableDistance=fromDistance,bestTransition=null,bestTransitionDistance=fromDistance;
  const pointAllowed=point=>{const result=movementLimit(state,floorId,point,point);return result.allowed&&!result.endpointJunction;};
  const enqueueBlockers=(candidate,result)=>{if(result.allowed||!result.polygons?.length)return;const blockedContact=interpolate(from,candidate,Math.max(0,Math.min(1,result.parameter)));for(const edge of collisionEdges(from,candidate,result.parameter,result.polygons))enqueue(edge,blockedContact);};
  const consider=(point,edge,requireStable)=>{
    if(distanceSquared(point,from)<=SEGMENT_EPSILON)return;
    const result=movementLimit(state,floorId,from,point);
    if(!result.allowed||result.endpointJunction){enqueueBlockers(point,result);return;}
    const targetDistance=distanceSquared(point,to);
    if(targetDistance+SEGMENT_EPSILON>=fromDistance)return;
    const stable=requireStable&&tangentPoints(point,edge.a,edge.b).every(pointAllowed);
    if(stable){if(targetDistance<bestStableDistance){bestStable=point;bestStableDistance=targetDistance;}}
    else if(targetDistance<bestTransitionDistance){bestTransition=point;bestTransitionDistance=targetDistance;}
  };
  for(let cursor=0;cursor<edges.length&&cursor<MAX_SLIDE_EDGES;cursor++){
    const edge=edges[cursor],projection=closestPointOnEdge(to,edge.a,edge.b);processed.push(edge);
    if(projection.parameter<=SEGMENT_EPSILON)enqueue(adjacentEdge(edge,true),edge.a);
    if(projection.parameter>=1-SEGMENT_EPSILON)enqueue(adjacentEdge(edge,false),edge.b);
    const candidates=insetPoints(projection.point,edge.a,edge.b).sort((left,right)=>distanceSquared(left,to)-distanceSquared(right,to));
    for(const candidate of candidates)consider(candidate,edge,true);
  }
  if(bestStable)return bestStable;
  for(const edge of processed){
    const candidates=insetPoints(edge.contact,edge.a,edge.b).sort((left,right)=>distanceSquared(left,to)-distanceSquared(right,to));
    for(const candidate of candidates)consider(candidate,edge,false);
  }
  return bestTransition||{x:from.x,y:from.y};
}
