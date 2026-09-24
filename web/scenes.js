export class ScenesRuntime {
  constructor(elements,{open,home,queue}) {
    this.elements=elements;this.open=open;this.home=home;this.queue=queue;this.campaign=null;
    elements.homeAdd.onclick=()=>this.create();
    elements.back.onclick=()=>this.home();
  }

  create(){const name=prompt('Название новой сцены','Новая сцена')?.trim();if(name)this.queue('sceneCreate',{sceneName:name});}
  rename(scene){if(!scene)return;const name=prompt('Название сцены',scene.name)?.trim();if(name)this.queue('sceneUpdate',{sceneId:scene.id,sceneName:name});}
  remove(scene){if(scene&&confirm(`Удалить сцену «${scene.name}»?`))this.queue('sceneDelete',{sceneId:scene.id});}

  update(campaign) {
    this.campaign=campaign;
    const {cards,empty,campaignName}=this.elements;
    campaignName.textContent=campaign.name;
    cards.replaceChildren();
    for(const scene of campaign.scenes||[])cards.append(this.card(scene,campaign.you.role==='gm'));
    empty.hidden=!!campaign.scenes?.length;
    document.querySelectorAll('.player-only').forEach(element=>element.hidden=campaign.you.role==='gm');
  }

  card(scene,isGM) {
    const card=document.createElement('article');card.className='scene-card '+(scene.published?'published':'gm-only');card.dataset.sceneId=scene.id;
    const status=document.createElement('span');status.className='scene-status';status.textContent=scene.published?'Опубликована':'Только GM';
    const name=document.createElement('h2');name.textContent=scene.name;
    const open=document.createElement('button');open.className='primary';open.textContent='Открыть сцену';open.onclick=()=>this.open(scene.id);
    card.append(status,name,open);
    if(isGM){
      const controls=document.createElement('div');controls.className='scene-card-controls';
      const rename=document.createElement('button');rename.className='subtle';rename.textContent='Переименовать';rename.onclick=()=>this.rename(scene);
      const publish=document.createElement('button');publish.className='subtle';publish.textContent=scene.published?'Скрыть от игроков':'Опубликовать';publish.onclick=()=>this.queue('sceneUpdate',{sceneId:scene.id,published:!scene.published});
      const remove=document.createElement('button');remove.className='danger';remove.textContent='Удалить';remove.onclick=()=>this.remove(scene);
      controls.append(rename,publish,remove);card.append(controls);
    }
    return card;
  }

  showHome(){this.elements.home.hidden=false;this.elements.workspace.hidden=true;this.elements.footer.hidden=true;this.elements.back.hidden=true;this.elements.sceneTabs.hidden=true;}
  showScene(){this.elements.home.hidden=true;this.elements.workspace.hidden=false;this.elements.footer.hidden=false;this.elements.back.hidden=false;this.elements.sceneTabs.hidden=false;}
}
