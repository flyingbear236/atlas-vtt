// Durable commands are sent one at a time. A reconnect resends the same sequence;
// the server's persisted receipt makes that retry idempotent.
export class Outbox {
  constructor(storage, key, send, changed, completed) {
    this.storage=storage; this.key=key; this.send=send; this.changed=changed; this.completed=completed;
    const saved=storage.getItem(key);
    this.data=saved?JSON.parse(saved):{client:Array.from(crypto.getRandomValues(new Uint32Array(4)),n=>n.toString(16)).join('-'),seq:0,queue:[]};
    if(!this.data||typeof this.data.client!=='string'||!this.data.client||!Number.isSafeInteger(this.data.seq)||this.data.seq<0||!Array.isArray(this.data.queue)||this.data.queue.some((c,i,a)=>!c||c.client!==this.data.client||!Number.isSafeInteger(c.seq)||c.seq<1||(i>0&&c.seq!==a[i-1].seq+1))||(this.data.queue.length&&this.data.queue.at(-1).seq!==this.data.seq))throw new Error('Invalid command queue');
    this.inflight=false;
  }
  persist(){this.storage.setItem(this.key,JSON.stringify(this.data));}
  enqueue(type,payload){
    const cmd={type,...payload,client:this.data.client,seq:this.data.seq+1};
    this.data.seq++;this.data.queue.push(cmd);
    try{this.persist();}catch(e){this.data.seq--;this.data.queue.pop();throw e;}
    this.changed();this.flush();return cmd;
  }
  flush(){if(this.inflight||!this.data.queue.length)return;if(this.send(this.data.queue[0]))this.inflight=true;}
  reconnect(){this.inflight=false;this.flush();}
  ack(msg){const cmd=this.data.queue[0];if(!cmd||cmd.seq!==msg.seq)return;
    this.data.queue.shift();this.persist();this.inflight=false;this.completed(cmd,msg.error);this.changed();this.flush();
  }
  retry(){this.inflight=false;this.flush();}
  hasQueuedBehindInflight(){return this.inflight&&this.data.queue.length>1;}
}

export class Drafts {
  constructor(){this.items=new Map();}
  set(id,field,value){this.items.set(id,{...this.items.get(id),[field]:value});}
  get(id){return this.items.get(id)||{};}
  confirm(id,patch){const draft={...this.get(id)};for(const [k,v]of Object.entries(patch))if(draft[k]===v)delete draft[k];if(Object.keys(draft).length)this.items.set(id,draft);else this.items.delete(id);}
}

// IndexedDB is optional. This budget caps Blob data retained by queued write
// callbacks when storage is slower than asset downloads.
export class CacheWriteBudget {
  constructor(maxCount,maxBytes){this.maxCount=maxCount;this.maxBytes=maxBytes;this.count=0;this.bytes=0;}
  acquire(size){
    if(size<0||this.count>=this.maxCount||this.bytes+size>this.maxBytes)return null;
    this.count++;this.bytes+=size;let held=true;
    return()=>{if(!held)return;held=false;this.count--;this.bytes-=size;};
  }
}

// A fixed total budget, not fixed per-image quality. All requested images fit
// together; more distinct visible images means smaller decoded bitmaps.
export function imageEdge(count,budget){let edge=512;while(edge>1&&count*edge*edge*4>budget)edge/=2;return edge;}
