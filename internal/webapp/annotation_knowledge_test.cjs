// Component-level fake DOM regression checks, not browser E2E coverage.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const settle=()=>new Promise(resolve=>setImmediate(resolve));
const clone=x=>JSON.parse(JSON.stringify(x));
function fixture(){
  const elements=[],calls=[];let nonce=0;
  function node(tag,text,css){let own=text===undefined?'':String(text);const e={tag,className:css||'',children:[],parentNode:null,events:{},attrs:{},open:false,hidden:false,disabled:false,value:'',
    get textContent(){return own+this.children.map(c=>c.textContent).join('');},set textContent(v){own=String(v);this.replaceChildren();},
    append(...children){for(const c of children){c.parentNode=this;this.children.push(c);}},replaceChildren(...children){for(const c of this.children)c.parentNode=null;this.children=[];this.append(...children);},
    addEventListener(k,fn){(this.events[k]??=[]).push(fn);},setAttribute(k,v){this.attrs[k]=v;},contains(other){for(let n=other;n;n=n.parentNode)if(n===this)return true;return false;},
    showModal(){this.open=true;},close(){this.open=false;for(const fn of this.events.close||[])fn();}
  };elements.push(e);return e;}
  const f={elements,calls,state:{selected:'ws',revision:1,session:{user:'alice'}},receipts:new Map(),links:[],confirm:false,
    annotation:{id:'ann-'+'a'.repeat(32),revision:1,quote:'<exact quote>',note:'<user body>',state:'current',archived:false}};
  f.result=body=>({node:{id:'manual-node-'+body.request_id,kind:body.kind,label:body.label,body:body.body,status:'draft',origin:'manual'},link:{node_id:'manual-node-'+body.request_id,annotation_id:f.annotation.id,annotation_revision:body.revision},replayed:false});
  f.respond=async(path,body)=>{
    if(body){if(f.receipts.has(body.request_id))return {...clone(f.receipts.get(body.request_id)),replayed:true};const result=f.result(body);f.receipts.set(body.request_id,result);f.links.unshift({creation:result,node:result.node,annotation:clone(f.annotation),snapshot:clone(f.annotation),node_changed:false,evidence_linked:true,missing_node:false});return clone(result);}
    const q=new URLSearchParams(path.split('?')[1]),offset=Number(q.get('offset'));return {items:clone(f.links.slice(offset,offset+20)),total:f.links.length,offset,limit:20};
  };
  const context=vm.createContext({document:{body:node('body')},AbortController,confirm:()=>f.confirm,crypto:{randomUUID:()=>String(++nonce).padStart(32,'0')}});
  vm.runInContext(fs.readFileSync(__dirname+'/assets/annotation-knowledge.js','utf8'),context);
  f.module=context.installMemAnnotationKnowledge({state:f.state,node,run:fn=>fn,api:async(path,body,retry,signal)=>{calls.push({path,body:body===undefined?undefined:clone(body),signal});const result=await(f.handle?f.handle(path,body):f.respond(path,body));if(body&&f.loseReply){f.loseReply=false;throw Error('Response lost');}return result;}});
  f.host=node('section');f.dialog=elements.find(e=>e.id==='annotation-knowledge-dialog');f.alive=true;
  f.mount=()=>{f.host.replaceChildren();f.view=f.module.mount({host:f.host,annotation:f.annotation,workspace:'ws',isCurrent:()=>f.alive});return settle();};
  f.find=(tag,scope=f.host)=>elements.findLast(e=>e.tag===tag&&scope.contains(e));
  f.button=(label,scope=f.host)=>elements.findLast(e=>e.tag==='button'&&e.textContent===label&&scope.contains(e));
  f.fire=async(target,event='click')=>{const e=typeof target==='string'?f.button(target):target;assert.ok(e,'Missing '+target);for(const fn of e.events[event]||[])await fn({preventDefault(){}});await settle();};
  return f;
}

test('annotation knowledge explicitly creates manual draft with source revision and literal text',async()=>{
  const f=fixture();await f.mount();f.find('input').value='<my title>';f.find('textarea').value='My thought <script>';f.find('select').value='hypothesis';
  await f.fire('Создать черновик в графе');const writes=f.calls.filter(c=>c.body);assert.equal(writes.length,1);assert.deepEqual(writes[0].body,{revision:1,kind:'hypothesis',label:'<my title>',body:'My thought <script>',request_id:'1'.padStart(32,'0')});
  assert.equal(f.receipts.size,1);assert.match(f.host.textContent,/<exact quote>/);assert.match(f.host.textContent,/My thought <script>/);assert.equal(f.button('Создать черновик в графе').disabled,true);
  const anchor=f.find('a');assert.equal(anchor.href,'/graph/ws/#node=manual-node-'+writes[0].body.request_id);assert.equal(anchor.rel,'noopener');
  await f.fire('Создать черновик в графе');assert.equal(f.receipts.size,1);await f.fire('Новый черновик');assert.equal(f.button('Создать черновик в графе').disabled,false);
});

test('lost acknowledgement retains exact receipt across mount close and newer annotation revision',async()=>{
  const f=fixture();await f.mount();f.loseReply=true;await f.fire('Создать черновик в графе');const input=f.calls.find(c=>c.body).body;assert.equal(f.button('Повторить ту же отправку').hidden,false);
  f.view.close();f.annotation={...f.annotation,revision:2,note:'Different note'};await f.mount();assert.equal(f.find('textarea').disabled,true);await f.fire('Повторить ту же отправку');
  assert.deepEqual(f.calls.filter(c=>c.body).map(c=>c.body),[input,input]);assert.equal(f.receipts.size,1);assert.match(f.host.textContent,/Эта отправка уже была сохранена/);
});

test('reopened mount observes inflight success and failure without duplicate calls or disabled retry',async()=>{
  for(const fail of [false,true]){
    const f=fixture();let resolve,reject;await f.mount();f.handle=(path,body)=>body?new Promise((r,j)=>{resolve=r;reject=j;}):f.respond(path,body);
    const sending=f.fire('Создать черновик в графе');await settle();const input=f.calls.find(c=>c.body).body;f.view.close();await f.mount();assert.equal(f.button('Повторить ту же отправку').disabled,true);
    if(fail)reject(Error('offline'));else resolve(f.result(input));await sending;
    if(fail){assert.equal(f.button('Повторить ту же отправку').disabled,false);assert.match(f.host.textContent,/offline/);f.handle=(p,b)=>f.respond(p,b);await f.fire('Повторить ту же отправку');assert.equal(f.receipts.size,1);}
    else {assert.match(f.host.textContent,/Создан ручной черновик/);assert.equal(f.button('Повторить ту же отправку').hidden,true);assert.equal(f.calls.filter(c=>c.body).length,1);}
  }
});

test('invalid acknowledgement preserves retry and pending request cannot silently change',async()=>{
  const f=fixture();await f.mount();f.handle=async(p,b)=>b?{...f.result(b),link:{...f.result(b).link,annotation_revision:99}}:f.respond(p,b);await f.fire('Создать черновик в графе');
  assert.match(f.host.textContent,/Некорректный ответ/);assert.equal(f.button('Повторить ту же отправку').hidden,false);await f.fire('Новый черновик');assert.equal(f.find('input').disabled,true);
  f.handle=(p,b)=>f.respond(p,b);await f.fire('Повторить ту же отправку');assert.equal(f.receipts.size,1);
});

test('closed, replaced workspace or actor cannot update DOM or write through stale mount',async()=>{
  for(const transition of ['close','workspace','actor','screen']){
    const f=fixture();let resolve;f.handle=()=>new Promise(r=>resolve=r);await f.mount();const before=f.host.textContent;
    if(transition==='close'){f.view.close();assert.equal(f.calls[0].signal.aborted,true);}else if(transition==='workspace')f.state.selected='other';else if(transition==='actor')f.state.session.user='bob';else f.state.revision++;
    resolve({items:[],offset:0,total:47});await settle();assert.equal(f.host.textContent,before);await f.fire('Создать черновик в графе');assert.equal(f.calls.filter(c=>c.body).length,0);
  }
});

test('inactive source and archived annotation cannot create; latest revision requires explicit new draft',async()=>{
  const f=fixture();await f.mount();f.view.close();f.annotation={...f.annotation,revision:2};await f.mount();assert.equal(f.button('Создать черновик в графе').disabled,true);await f.fire('Новый черновик');assert.equal(f.button('Создать черновик в графе').disabled,false);
  f.view.close();f.annotation={...f.annotation,archived:true};await f.mount();await f.fire('Создать черновик в графе');assert.equal(f.calls.filter(c=>c.body).length,0);
  f.view.close();f.annotation={...f.annotation,archived:false,state:'stale'};await f.mount();await f.fire('Создать черновик в графе');assert.equal(f.calls.filter(c=>c.body).length,0);
});

test('reverse links paginate, distinguish immutable/current/orphan and open correct annotation',async()=>{
  const f=fixture(),created=f.result({request_id:'b'.repeat(32),revision:1,kind:'note',label:'Original',body:'Original body'});
  f.links=Array.from({length:41},(_,i)=>({creation:clone(created),node:{...created.node,label:'Current '+i,body:'New body'},annotation:{...f.annotation,revision:4,note:'Current note'},snapshot:{...f.annotation,note:'Old note'},node_changed:true,evidence_linked:false,missing_node:i===0}));
  let opened;await f.module.showNode({workspace:'ws',nodeID:created.node.id,onOpen:id=>opened=id});assert.match(f.dialog.textContent,/текущая r4/);assert.match(f.dialog.textContent,/Old note/);assert.match(f.dialog.textContent,/Сохранилась история создания/);assert.match(f.dialog.textContent,/Текущее содержимое узла: New body/);
  await f.fire(f.button('Следующие связи',f.dialog));assert.match(f.calls.at(-1).path,/offset=20&limit=20$/);await f.fire(f.button('Следующие связи',f.dialog));assert.match(f.calls.at(-1).path,/offset=40&limit=20$/);assert.equal(f.button('Следующие связи',f.dialog).disabled,true);
  await f.fire(f.button('Открыть аннотацию',f.dialog));assert.equal(opened,f.annotation.id);assert.equal(f.dialog.open,false);
});

test('closed reverse view rejects its late response and closes global mounts',async()=>{
  const f=fixture();await f.mount();let resolve;f.handle=()=>new Promise(r=>resolve=r);const opening=f.module.showNode({workspace:'ws',nodeID:'n'});await settle();const last=f.calls.at(-1);f.module.close();assert.equal(last.signal.aborted,true);const before=f.dialog.textContent;resolve({items:[],total:45,offset:0});await opening;assert.equal(f.dialog.textContent,before);assert.equal(f.dialog.open,false);await f.fire('Создать черновик в графе');assert.equal(f.calls.filter(c=>c.body).length,0);
});
