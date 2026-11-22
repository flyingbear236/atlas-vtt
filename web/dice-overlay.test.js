import {test} from 'node:test';
import {strict as assert} from 'node:assert';
import {DiceRuntime} from './dice.js';

// Minimal DOM stand-in to exercise the real event-to-overlay flow without a browser.
class Node {
 constructor(tag='div'){
  this.tag=tag;this.children=[];this.parent=null;this.dataset={};this.className='';this.textContent='';this.hidden=false;this.value='';this.scrollTop=0;
  this.classList={
   contains:name=>this.className.split(' ').includes(name),
   add:name=>{this.className=[...new Set([...this.className.split(' ').filter(Boolean),name])].join(' ');},
   remove:name=>{this.className=this.className.split(' ').filter(x=>x!==name).join(' ');},
   toggle:(name,on)=>{if(on)this.classList.add(name);else this.classList.remove(name);}
  };
 }
 addEventListener(){}
 setCustomValidity(){}
 get scrollHeight(){return this.children.length;}
 get lastElementChild(){return this.children.at(-1);}
 append(...nodes){for(const node of nodes){node.parent=this;this.children.push(node);}}
 prepend(...nodes){for(const node of nodes.reverse()){node.parent=this;this.children.unshift(node);}}
 replaceChildren(...nodes){this.children=[];this.append(...nodes);}
 remove(){if(this.parent)this.parent.children=this.parent.children.filter(node=>node!==this);}
 replaceWith(node){if(!this.parent)return;const i=this.parent.children.indexOf(this);this.parent.children[i]=node;node.parent=this.parent;}
 querySelector(selector){
  const match=node=>selector.startsWith('.')?node.classList.contains(selector.slice(1)):
   selector.startsWith('[data-event-id=')?node.dataset.eventId===selector.split('"')[1]:false;
  for(const node of this.children){if(match(node))return node;const inner=node.querySelector(selector);if(inner)return inner;}
  return null;
 }
 get innerText(){return [this.textContent,...this.children.map(c=>c.innerText)].filter(Boolean).join('\n');}
}

test('live action messages without dice display a single message card; roll and apply still work',()=>{
 const previousDocument=globalThis.document,previousCSS=globalThis.CSS;
 globalThis.document={createElement:tag=>new Node(tag)};
 globalThis.CSS={escape:value=>value};
 try{
  const named=new Map(),get=name=>{if(!named.has(name))named.set(name,new Node());return named.get(name);};
  const entries=new Node(),overlay=new Node(),journal=new Node();
  overlay.hidden=true;
  journal.querySelector=name=>name==='[data-roll-entries]'?entries:get(name);
  const root=new Node();root.diceUI={journal,overlay,toggle:new Node(),setJournal:()=>{}};
  root.querySelector=name=>name.startsWith('[data-dice-pick=')?{
   classList:{toggle(){}},querySelector:()=>new Node()
  }:get(name);
  root.querySelectorAll=()=>[];
  get('[data-dice-expression]').value='1d20';
  const dice=new DiceRuntime(root,{storage:{getItem:()=>null,setItem:()=>{}}});
  dice.setScene('scene','user');
  const action=(id,name,roll)=>({executionId:id,sceneId:'scene',userId:'user',actionName:name,actionId:id,roll});
  dice.handle({type:'actionRoll',event:action('fixed','Пробный выпад',{total:15,dice:[]})});
  assert.equal(overlay.innerText,'15\n15');
  dice.handle({type:'actionMessage',index:0,message:'DC 13',event:action('fixed','Пробный выпад',{total:15,dice:[]})});
  assert.match(overlay.innerText,/DC 13/);
  dice.handle({type:'actionMessage',index:0,message:'Испытатель · сила 16 → 20',event:action('report','Сводка испытателя')});
  assert.equal(overlay.classList.contains('dice-message-only'),true);
  assert.equal(overlay.innerText,'Сводка испытателя\nИспытатель · сила 16 → 20');
  dice.handle({type:'actionMessage',index:1,message:'HP 18/24',event:action('report','Сводка испытателя')});
  assert.equal(overlay.querySelector('.dice-overlay-message')?.textContent,'Испытатель · сила 16 → 20 · HP 18/24');
  assert.equal(overlay.children.length,2);
  dice.handleReceipt({type:'actionExecute'},{action:action('apply','Исцеление'),details:{changes:[{field:'stat.hp.current',before:{value:10},after:{value:12}}]}});
  assert.equal(overlay.innerText,'Исцеление\nstat.hp.current: 10 → 12');
  dice.setScene('new-scene','user');
  assert.equal(overlay.hidden,true);
 }finally{globalThis.document=previousDocument;globalThis.CSS=previousCSS;}
});
