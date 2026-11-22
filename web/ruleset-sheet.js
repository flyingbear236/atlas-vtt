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

export class ActionTargetPicker {
  constructor(send,execute){
    this.send=send;
    this.execute=execute;
    this.seq=0;
    this.dialog=document.createElement('dialog');
    this.dialog.className='character-action-target';
    this.dialog.innerHTML='<form><h3></h3><label>Цель<select aria-label="Цель"></select></label><p class="muted" role="status"></p><div class="character-resource-controls"><button type="button" class="subtle">Отмена</button><button type="submit" class="primary">Выполнить</button></div></form>';
    this.title=this.dialog.querySelector('h3');
    this.select=this.dialog.querySelector('select');
    this.status=this.dialog.querySelector('p');
    this.submit=this.dialog.querySelector('[type=submit]');
    this.dialog.querySelector('[type=button]').onclick=()=>this.close();
    this.dialog.addEventListener('close',()=>{
      if(!this.dialog.open){this.request=null;this.targets=[];}
    });
    this.dialog.querySelector('form').onsubmit=event=>{
      event.preventDefault();
      if(!this.request||this.submit.disabled)return;
      const target=this.targets[Number(this.select.value)-1];
      const action={...this.request.action};
      if(target)action.target={characterId:target.characterId,...(target.itemId?{itemId:target.itemId}:{})};
      if(this.execute(this.request.sceneId,action))this.close();
    };
    document.body.append(this.dialog);
  }

  open(sceneId,action,title){
    if(this.dialog.open)return;
    this.request={type:'actionTargets',sceneId,action,seq:++this.seq};
    this.targets=[];
    this.title.textContent=title;
    this.select.replaceChildren(new Option('Без цели','0'));
    this.status.textContent='Загрузка целей…';
    this.submit.disabled=true;
    this.dialog.showModal();
    if(!this.send(this.request))this.status.textContent='Нет связи с сервером';
  }

  handle(message){
    if(message.type!=='actionTargets')return false;
    if(!this.dialog.open||message.seq!==this.request?.seq||message.sceneId!==this.request.sceneId)return true;
    this.targets=message.targets||[];
    this.targets.forEach((target,index)=>{
      const suffix=target.itemId?` · ${target.itemId}`:` · #${target.characterId.slice(0,8)}`;
      this.select.add(new Option(target.name+suffix,String(index+1)));
    });
    this.status.textContent=message.error||(this.targets.length?'':'Нет доступных целей');
    this.submit.disabled=!!message.error;
    return true;
  }

  close(){if(this.dialog.open)this.dialog.close();}
}
