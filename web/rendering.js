export class SpatialIndex {
  constructor(cell=512){this.cell=cell;this.buckets=new Map();this.entries=new Map();this.order=0;}
  clear(){this.buckets.clear();this.entries.clear();this.order=0;}
  remove(id){const old=this.entries.get(id);if(!old)return;for(const key of old.keys){const bucket=this.buckets.get(key);bucket.delete(id);if(!bucket.size)this.buckets.delete(key);}this.entries.delete(id);}
  set(t){const order=this.entries.get(t.id)?.order??this.order++;this.remove(t.id);const r=t.size/2,keys=[];for(let y=Math.floor((t.y-r)/this.cell);y<=Math.floor((t.y+r)/this.cell);y++)for(let x=Math.floor((t.x-r)/this.cell);x<=Math.floor((t.x+r)/this.cell);x++){const key=`${x},${y}`;if(!this.buckets.has(key))this.buckets.set(key,new Set());this.buckets.get(key).add(t.id);keys.push(key);}this.entries.set(t.id,{token:t,keys,order});}
  query(left,top,right,bottom){const result=new Set(),x0=Math.floor(left/this.cell),x1=Math.floor(right/this.cell),y0=Math.floor(top/this.cell),y1=Math.floor(bottom/this.cell);if((x1-x0+1)*(y1-y0+1)>this.buckets.size){for(const [key,bucket]of this.buckets){const [x,y]=key.split(',').map(Number);if(x>=x0&&x<=x1&&y>=y0&&y<=y1)for(const id of bucket)result.add(id);}}else{for(let y=y0;y<=y1;y++)for(let x=x0;x<=x1;x++)for(const id of this.buckets.get(`${x},${y}`)||[])result.add(id);}return [...result].map(id=>this.entries.get(id)).filter(({token:t})=>t.x+t.size/2>=left&&t.x-t.size/2<=right&&t.y+t.size/2>=top&&t.y-t.size/2<=bottom).sort((a,b)=>a.order-b.order).map(e=>e.token);}
}

export class LimitedMap {
  constructor(limit){this.limit=Math.max(0,limit);this.items=new Map();}
  get size(){return this.items.size;}
  has(key){return this.items.has(key);}
  get(key){return this.items.get(key);}
  set(key,value){if(this.items.has(key))this.items.delete(key);this.items.set(key,value);while(this.items.size>this.limit)this.items.delete(this.items.keys().next().value);return this;}
  delete(key){return this.items.delete(key);}
  clear(){this.items.clear();}
}

export class MetadataTouchTracker {
  constructor(limit,interval=60000){this.interval=interval;this.entries=new LimitedMap(limit);}
  due(key,now){return (this.entries.get(key)||0)<now-this.interval;}
  record(key,now){this.entries.set(key,now);}
  clear(){this.entries.clear();}
  get size(){return this.entries.size;}
}

const CACHE_BUDGETS_MIB=new Set([64,128,256,512]);

// deviceMemory is intentionally coarse and may be unavailable. Keep the
// automatic policy coarse as well: it is only a starting RAM class, not an
// attempt to estimate how much memory the browser can safely consume now.
export function automaticImageMemoryBudgetMiB(deviceMemory){
  const memory=Number(deviceMemory);
  if(!Number.isFinite(memory)||memory<=0)return 128;
  if(memory<=4)return 64;
  if(memory>=16)return 256;
  return 128;
}

export function resolveImageMemoryBudgetMiB(deviceMemory,override='auto'){
  if(override!=='auto'){
    const manual=Number(override);
    if(CACHE_BUDGETS_MIB.has(manual))return manual;
  }
  return automaticImageMemoryBudgetMiB(deviceMemory);
}

export function planLRUEviction(entries,protectedKeys,projectedBytes,highWatermark,lowRatio=.85){
  if(projectedBytes<=highWatermark)return {keys:[],remaining:projectedBytes,target:highWatermark};
  const target=Math.floor(highWatermark*lowRatio),candidates=[];
  for(const [key,entry]of entries)if(!protectedKeys.has(key))candidates.push([key,entry]);
  candidates.sort((a,b)=>(a[1].used??0)-(b[1].used??0));
  const keys=[];let remaining=projectedBytes;
  for(const [key,entry]of candidates){if(remaining<=target)break;keys.push(key);remaining-=entry.size;}
  return {keys,remaining,target};
}

// A fixed share of the configured image-memory budget. Pinned artwork is never evicted
// in its own frame. If a pathological overlap cannot fit, render it directly.
export class ArtworkCache {
  constructor(limit){this.limit=limit;this.bytes=0;this.items=new Map();this.pinned=new Set();}
  begin(keys){this.pinned=new Set(keys);}
  get(key,w,h,paint){let item=this.items.get(key);if(item)return item.canvas;const size=w*h*4;if(size>this.limit)return null;for(const [k,v]of this.items){if(this.bytes+size<=this.limit)break;if(!this.pinned.has(k)){this.bytes-=v.size;v.canvas.width=0;v.canvas.height=0;this.items.delete(k);}}if(this.bytes+size>this.limit)return null;const canvas=new OffscreenCanvas(w,h);paint(canvas.getContext('2d'));this.items.set(key,{canvas,size});this.bytes+=size;return canvas;}
  clear(){for(const v of this.items.values()){v.canvas.width=0;v.canvas.height=0;}this.items.clear();this.bytes=0;}
}

const DECODE_BUCKETS=[32,48,64,80,96,128,160,192,224,256,320,384,448,512];
function bucketEdge(edge){edge=Math.max(1,edge);for(const bucket of DECODE_BUCKETS)if(edge<=bucket)return bucket;return DECODE_BUCKETS.at(-1);}

export function fitImageSize(width,height,edge){const scale=Math.min(1,edge/Math.max(width,height));return {width:Math.max(1,Math.round(width*scale)),height:Math.max(1,Math.round(height*scale))};}

// Decode into denser buckets than powers of two. The optional currentEdge adds
// hysteresis so a small zoom oscillation around a bucket boundary does not
// repeatedly close and recreate the same ImageBitmap.
export function decodedSize(width,height,screenEdge,currentEdge=0){
  let edge=bucketEdge(screenEdge);
  if(currentEdge>0){
    if(edge>currentEdge&&screenEdge<=currentEdge*1.18)edge=currentEdge;
    else if(edge<currentEdge&&screenEdge>=currentEdge*.72)edge=currentEdge;
  }
  return fitImageSize(width,height,edge);
}

export function smallerDecodedSize(width,height,currentWidth,currentHeight){
  const current=Math.max(currentWidth,currentHeight);
  let previous=32;
  for(const bucket of DECODE_BUCKETS){if(bucket>=current)break;previous=bucket;}
  if(previous>=current)previous=Math.max(32,Math.floor(current*.8));
  return fitImageSize(width,height,previous);
}

// Fits all requested decoded images into a shared byte budget. Each pass lowers
// every still-too-large plan by at most one decode bucket, so the work is
// bounded by O(items * bucketCount) instead of repeatedly rescanning all items
// to choose a single victim.
export function fitPlansToBudget(items,budget){
  let total=0;
  for(const {plan} of items)total+=plan.width*plan.height*4;
  while(total>budget){
    let changed=false;
    for(const {plan} of items){
      if(total<=budget)break;
      const before=plan.width*plan.height*4;
      const next=smallerDecodedSize(plan.native.width,plan.native.height,plan.width,plan.height);
      const after=next.width*next.height*4;
      if(after>=before)continue;
      plan.width=next.width;plan.height=next.height;total-=before-after;changed=true;
    }
    if(!changed)break;
  }
  return total;
}

export function artworkSpec(t,bitmap,diameter){const d=Math.min(512,Math.max(4,2**Math.ceil(Math.log2(Math.max(1,diameter))))),pad=6,key=JSON.stringify([bitmap?t.asset:t.name.slice(0,1),t.color,d,bitmap?.width,bitmap?.height]);return {key,d,pad,w:d+pad*2,h:d+pad*2,paint(g){const r=d/2,c=pad+r;g.fillStyle=t.color||'#c2d89b';g.shadowColor='#0009';g.shadowBlur=5;g.beginPath();g.arc(c,c,r,0,Math.PI*2);g.fill();g.shadowBlur=0;if(bitmap){g.save();g.clip();const scale=Math.max(d/bitmap.width,d/bitmap.height);g.drawImage(bitmap,c-bitmap.width*scale/2,c-bitmap.height*scale/2,bitmap.width*scale,bitmap.height*scale);g.restore();}else{g.fillStyle='#243224';g.font=`600 ${r}px system-ui`;g.textAlign='center';g.textBaseline='middle';g.fillText(t.name.slice(0,1)||'◆',c,c);}g.strokeStyle='#e2dec0';g.lineWidth=1.5;g.stroke();}};}
