export class CharacterWatch {
  constructor(send,onChange=()=>{}){this.send=send;this.onChange=onChange;this.characterId='';this.request=0;this.revision=0;this.snapshot=null;this.pending=new Set();}
  watch(characterId){
    if(!characterId){this.unwatch();return;}
    if(this.characterId===characterId&&(this.snapshot||this.pending.has(this.request)))return;
    const previous=this.characterId;this.characterId=characterId;this.snapshot=null;this.revision=0;this.pending.clear();
    const characterWatch=++this.request;this.pending.add(characterWatch);
    this.send({type:'characterWatch',characterId,characterWatch});this.onChange(null,previous?'replaced':'pending');
  }
  unwatch(){
    if(!this.characterId&&!this.snapshot&&!this.pending.size)return;
    const characterId=this.characterId,characterWatch=++this.request;
    this.characterId='';this.snapshot=null;this.revision=0;this.pending.clear();
    this.send({type:'characterUnwatch',characterId,characterWatch});this.onChange(null,'unsubscribed');
  }
  disconnect(){this.snapshot=null;this.revision=0;this.pending.clear();this.onChange(null,'disconnected');}
  reconnect(){
    if(!this.characterId)return;
    const characterWatch=++this.request;this.pending.clear();this.pending.add(characterWatch);
    this.send({type:'characterWatch',characterId:this.characterId,characterWatch});
  }
  handle(message){
    if(message.type!=='characterSnapshot'&&message.type!=='characterWatchClosed')return false;
    if(message.characterWatch!==this.request||message.characterId!==this.characterId)return true;
    this.pending.delete(message.characterWatch);
    if(message.type==='characterWatchClosed'){
      this.characterId='';this.snapshot=null;this.revision=0;this.pending.clear();this.onChange(null,message.reason||'closed');return true;
    }
    if(!Number.isSafeInteger(message.revision)||message.revision<this.revision)return true;
    this.revision=message.revision;this.snapshot=message;this.onChange(message,'snapshot');return true;
  }
}
