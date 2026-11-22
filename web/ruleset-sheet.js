export function displayRulesetValue(value){
  if(typeof value==='boolean')return value?'Да':'Нет';
  return typeof value==='object'&&value!==null?JSON.stringify(value):String(value??'');
}

export function appendStatFields(card,fields,description,baseInput){
  if(!fields)return;
  if(description)card.title=description;
  const list=document.createElement('dl');
  list.className='ruleset-stat-fields';
  const add=(label,value)=>{
    const name=document.createElement('dt'),shown=document.createElement('dd');
    name.textContent=label;
    if(value instanceof HTMLElement)shown.append(value);
    else shown.textContent=displayRulesetValue(value);
    list.append(name,shown);
  };
  add('Базовое',baseInput||fields.base);
  for(const field of fields.derived||[])add(field.name,field.value);
  card.append(list);
}

// Target selection lives on the scene. The server remains authoritative about
// which characters / items may actually be selected.
export class ActionTargetPicker {
  constructor(send,execute){
    this.send=send;this.execute=execute;this.seq=0;this.request=null;
    this.targets=[];this.ready=false;this.pendingCharacter='';
    this.banner=document.createElement('section');
    this.banner.className='character-action-target-prompt';this.banner.hidden=true;
    this.banner.setAttribute('role','status');
    const title=document.createElement('strong');title.textContent='Выберите цель';
    this.status=document.createElement('small');this.status.textContent='Кликните по персонажу-цели или по карте, если не хотите выбирать цель. Esc — отмена.';
    this.banner.append(title,this.status);
    (document.getElementById('stage')||document.body).append(this.banner);
    document.addEventListener('keydown',event=>{if(event.key==='Escape'&&this.request){event.preventDefault();this.close();}});
  }

  open(sceneId,action,title){
    this.close();
    this.request={type:'actionTargets',sceneId,action,seq:++this.seq};
    this.targets=[];this.ready=false;this.pendingCharacter='';
    this.status.textContent='Кликните по персонажу-цели или по карте, если не хотите выбирать цель. Esc — отмена.';
    this.banner.hidden=false;
    if(!this.send(this.request)){
      this.close();return false;
    }
    return true;
  }

  handle(message){
    if(message.type!=='actionTargets')return false;
    if(!this.request||message.seq!==this.request.seq||message.sceneId!==this.request.sceneId)return true;
    if(message.error){this.status.textContent=message.error;this.targets=[];return true;}
    this.ready=true;this.targets=message.targets||[];
    if(this.pendingCharacter){const character=this.pendingCharacter;this.pendingCharacter='';this.choose(character);}
    return true;
  }

  // A click on empty map space executes with the optional target omitted.
  choose(characterId=''){
    if(!this.request)return false;
    if(!characterId){
      if(this.execute(this.request.sceneId,{...this.request.action}))this.close();
      return true;
    }
    if(!this.ready){this.pendingCharacter=characterId;this.status.textContent='Проверяем доступность выбранной цели…';return true;}
    const matches=this.targets.filter(target=>target.characterId===characterId);
    if(matches.length!==1){
      this.status.textContent=matches.length?'У персонажа несколько доступных предметов. Выберите другую цель или пустое место карты.':'Этот персонаж недоступен как цель. Выберите другую цель или пустое место карты.';
      return true;
    }
    const target=matches[0];
    const action={...this.request.action,target:{characterId:target.characterId,...(target.itemId?{itemId:target.itemId}:{})}};
    if(this.execute(this.request.sceneId,action))this.close();
    return true;
  }

  close(){this.request=null;this.targets=[];this.ready=false;this.pendingCharacter='';this.banner.hidden=true;}
}
