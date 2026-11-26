// The action executor commits state immediately. Delay only the corresponding
// visual projections until the result/notice is actually presented to players.
export class ActionVisualGate {
  constructor({fallbackMs=4500,afterPresentationMs=160}={}){
    this.fallbackMs=fallbackMs;
    this.afterPresentationMs=afterPresentationMs;
    this.pending=new Map();
    this.presented=new Map();
  }
  defer(executionId,apply){
    if(!executionId||typeof apply!=='function')return false;
    let entry=this.pending.get(executionId);
    if(!entry){
      entry={callbacks:[],timer:setTimeout(()=>this.release(executionId),this.presented.has(executionId)?this.afterPresentationMs:this.fallbackMs)};
      this.pending.set(executionId,entry);
    }
    entry.callbacks.push(apply);
    // Never allow long-running scenes to accumulate unbounded execution IDs.
    if(this.pending.size>64)this.release(this.pending.keys().next().value);
    return true;
  }
  present(executionId){
    if(!executionId)return;
    const old=this.presented.get(executionId);
    if(old)clearTimeout(old);
    this.presented.set(executionId,setTimeout(()=>this.presented.delete(executionId),this.fallbackMs));
    if(this.presented.size>128){const oldest=this.presented.keys().next().value;clearTimeout(this.presented.get(oldest));this.presented.delete(oldest);}
    const entry=this.pending.get(executionId);
    if(entry){clearTimeout(entry.timer);entry.timer=setTimeout(()=>this.release(executionId),this.afterPresentationMs);}
  }
  release(executionId){
    const entry=this.pending.get(executionId);
    if(!entry)return;
    this.pending.delete(executionId);
    clearTimeout(entry.timer);
    for(const apply of entry.callbacks)apply();
  }
  clear(){
    for(const entry of this.pending.values())clearTimeout(entry.timer);
    for(const timer of this.presented.values())clearTimeout(timer);
    this.pending.clear();this.presented.clear();
  }
}

// Unlike the state projection gate, a reaction must never reveal an image
// before the dice renderer actually presents the result. Expired work is
// discarded, not released early. The receipt may arrive before or after it.
export class ObjectReactionGate {
 constructor({afterPresentationMs=900,expiryMs=60000}={}){this.delay=afterPresentationMs;this.expiry=expiryMs;this.pending=new Map();this.presented=new Map();this.done=new Set();}
 defer(id,apply){if(!id||this.done.has(id)||this.pending.has(id))return;const entry={apply,timer:setTimeout(()=>this.drop(id),this.expiry)};this.pending.set(id,entry);if(this.presented.has(id))this.schedule(id);while(this.pending.size>64)this.drop(this.pending.keys().next().value);}
 present(id){if(!id||this.done.has(id))return;if(!this.presented.has(id)){this.presented.set(id,setTimeout(()=>{this.presented.delete(id);},this.expiry));while(this.presented.size>128){const oldest=this.presented.keys().next().value;clearTimeout(this.presented.get(oldest));this.presented.delete(oldest);}}this.schedule(id);}
 schedule(id){const e=this.pending.get(id);if(!e||e.scheduled)return;e.scheduled=true;clearTimeout(e.timer);e.timer=setTimeout(()=>{this.drop(id);this.done.add(id);if(this.done.size>128)this.done.delete(this.done.values().next().value);e.apply();},this.delay);}
 drop(id){const e=this.pending.get(id);if(e)clearTimeout(e.timer);this.pending.delete(id);}
 clear(){for(const id of this.pending.keys())this.drop(id);for(const timer of this.presented.values())clearTimeout(timer);this.presented.clear();this.done.clear();}
}
