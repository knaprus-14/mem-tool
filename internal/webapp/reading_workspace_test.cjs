const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const clone=x=>JSON.parse(JSON.stringify(x)),settle=()=>new Promise(r=>setImmediate(r));
function fixture(){
  const elements=[],calls=[],databases=new Map(),receipts=new Map();let nonce=0,nextItem=10;
  function node(tag,text,css){let own=text===undefined?'':String(text);const e={tag,className:css||'',children:[],parentNode:null,events:{},attrs:{},hidden:false,disabled:false,open:false,value:'',scrollTop:0,scrollHeight:1000,clientHeight:200,
    get textContent(){return own+this.children.map(c=>c.textContent).join('');},set textContent(v){own=String(v);this.replaceChildren();},
    append(...children){for(const c of children){c.parentNode=this;this.children.push(c);if(tag==='select'&&!this.value)this.value=c.value;}},replaceChildren(...children){for(const c of this.children)c.parentNode=null;this.children=[];this.append(...children);},
    addEventListener(name,fn){(this.events[name]??=[]).push(fn);},setAttribute(k,v){this.attrs[k]=String(v);},contains(other){for(let n=other;n;n=n.parentNode)if(n===this)return true;return false;},showModal(){this.open=true;},close(){this.open=false;for(const fn of this.events.close||[])fn();}
  };elements.push(e);return e;}
  const f={elements,calls,databases,receipts,confirm:false,state:{selected:'ws',revision:1,session:{authenticated:true,user:'alice',csrf:'alice-token'}},opened:[],entry:{id:3,pin:'sha256:'+'3'.repeat(64),text:'Начало 😀 исходного фрагмента'},entries:new Map()};f.entries.set(3,f.entry);
  f.id=n=>'read-'+n.toString(16).padStart(32,'0');
  f.item=(n,text='Сохранённая цитата '+n,extra={})=>({id:f.id(n),title:'Фрагмент '+n,source_name:'Документ.pdf',entry_id:n,pin:'sha256:'+n.toString(16).padStart(64,'0'),document_id:'document-one',document_revision:'sha256:'+'a'.repeat(64),citation_id:'citation-'+n,page:n,block_index:2,block_chunk_index:1,text,start:0,end:Array.from(text).length,scroll:0,source_state:'current',map_state:'',created:'2026-10-10',updated:'2026-10-10',...extra});
  const ownerKey=(user,workspace)=>JSON.stringify([user,workspace]);f.data=(user=f.state.session.user,workspace=f.state.selected)=>{const k=ownerKey(user,workspace);if(!databases.has(k))databases.set(k,{revision:0,active_id:'',items:[]});return databases.get(k);};f.seed=(items,active=items[0]?.id||'',revision=5)=>{const d=f.data();Object.assign(d,{revision,active_id:active,items:clone(items)});};
  f.respond=async call=>{const w=f.data(call.user,call.workspace),p=call.body;if(!p)return clone(w);const k=JSON.stringify([call.user,call.workspace,p.request_id]);if(receipts.has(k))return {workspace:clone(receipts.get(k)),replayed:true};if(p.revision!==w.revision)throw Error('409: рабочий набор изменился');if(p.action==='add'){const e=f.entries.get(p.entry_id);if(!e||e.pin!==p.pin)throw Error('409: фрагмент изменился');if(w.items.some(i=>i.entry_id===p.entry_id&&i.pin===p.pin&&i.start===p.start&&i.end===p.end&&JSON.stringify(i.map_ref)===JSON.stringify(p.map_ref)))throw Error('409: такой фрагмент уже сохранён');const text=Array.from(e.text).slice(p.start,p.end).join(''),item=f.item(nextItem++,text,{entry_id:p.entry_id,pin:p.pin,start:p.start,end:p.end});if(p.map_ref){item.map_ref=clone(p.map_ref);item.map_state='current';}w.items.push(item);w.active_id=item.id;}else{const i=w.items.find(i=>i.id===p.id);if(!i)throw Error('404: фрагмент не найден');if(p.action==='select')w.active_id=p.id;else if(p.action==='position')i.scroll=p.scroll;else if(p.action==='remove'){w.items=w.items.filter(i=>i.id!==p.id);if(w.active_id===p.id)w.active_id='';}else throw Error('invalid action');}w.revision++;receipts.set(k,clone(w));return {workspace:clone(w),replayed:false};};
  const context=vm.createContext({document:{body:node('body')},AbortController,requestAnimationFrame:fn=>setImmediate(fn),confirm:()=>f.confirm,crypto:{randomUUID:()=>String(++nonce).padStart(32,'0')}});
  vm.runInContext(fs.readFileSync(__dirname+'/assets/reading-workspace.js','utf8'),context);
  f.module=context.installMemReadingWorkspace({state:f.state,node,run:fn=>fn,onOpenMap:ref=>f.opened.push(clone(ref)),api:async(path,body,retry,signal)=>{const call={path,body:body===undefined?undefined:clone(body),signal,user:f.state.session.user,workspace:decodeURIComponent(path.split('/')[1])};calls.push(call);const result=await(f.handle?f.handle(call):f.respond(call));if(body&&f.loseReply){f.loseReply=false;throw Error('Ответ потерян');}return result;}});
  f.dialog=elements.find(e=>e.id==='reading-workspace-dialog');f.status=elements.find(e=>e.attrs.role==='status');f.error=elements.find(e=>e.attrs.role==='alert');f.button=label=>elements.findLast(e=>e.tag==='button'&&e.textContent===label&&f.dialog.contains(e));f.buttons=label=>elements.filter(e=>e.tag==='button'&&e.textContent===label&&f.dialog.contains(e));f.readers=()=>elements.filter(e=>e.className==='reading-workspace-text'&&f.dialog.contains(e));f.compare=()=>elements.findLast(e=>e.tag==='select'&&f.dialog.contains(e));f.writes=()=>calls.filter(c=>c.body);
  f.fire=async(target,event='click')=>{const e=typeof target==='string'?f.button(target):target;assert.ok(e,'missing control '+target);for(const fn of e.events[event]||[])await fn({target:e,preventDefault(){}});await settle();};f.show=async()=>{await f.module.show();await settle();};f.capture=async(entry=f.entry,options)=>{await f.module.capture(entry,options);await settle();};
  return f;
}

test('reading set restores its persisted active excerpt and normalized scroll, with explicit position saving',async()=>{
  const f=fixture(),a=f.item(1),b=f.item(2,'<literal> 😀',{scroll:.375});f.seed([a,b],b.id);await f.show();assert.equal(f.readers().length,1);assert.equal(f.readers()[0].textContent,'<literal> 😀');assert.equal(f.readers()[0].scrollTop,300);assert.equal(f.writes().length,0);assert.match(f.dialog.textContent,/Позиция сохраняется отдельной кнопкой/);
  const reader=f.readers()[0];reader.scrollTop=640;await f.fire(reader,'scroll');assert.match(f.dialog.textContent,/Прокрутка не сохранена/);assert.equal(f.writes().length,0);await f.fire('Сохранить позицию');assert.equal(f.writes()[0].body.action,'position');assert.equal(f.writes()[0].body.scroll,.8);assert.equal(f.writes()[0].body.id,b.id);assert.equal(f.data().active_id,b.id);assert.match(f.dialog.textContent,/Позиция сохранена/);
  f.module.close();await f.show();assert.equal(f.readers()[0].scrollTop,640);
});

test('capture uses exact Unicode first 8000 characters, pins the optional map and discloses truncation',async()=>{
  const f=fixture();f.entry.text='😀'.repeat(8001)+'tail';const ref={map_id:'map-1',node_id:'node-1',revision:7};await f.capture(f.entry,{mapRef:ref});const p=f.writes()[0].body;assert.equal(p.action,'add');assert.equal(p.entry_id,3);assert.equal(p.pin,f.entry.pin);assert.equal(p.start,0);assert.equal(p.end,8000);assert.deepEqual(p.map_ref,ref);assert.equal('text' in p,false);assert.equal('quote' in p,false);assert.equal(Array.from(f.readers()[0].textContent).length,8000);assert.match(f.status.textContent,/первые 8000/);assert.equal(f.data().active_id,f.data().items[0].id);assert.equal(f.calls.at(-1).body,undefined);
  const invalid=fixture();await assert.rejects(invalid.capture({...invalid.entry,pin:'invalid'}),/актуальный фрагмент/);assert.equal(invalid.calls.length,0);
});

test('select persists, compare is read-only and shows two saved excerpts, removal is explicit',async()=>{
  const f=fixture(),a=f.item(1),b=f.item(2);f.seed([a,b]);await f.show();const select=f.compare();select.value=b.id;await f.fire(select,'change');assert.deepEqual(f.readers().map(r=>r.textContent),[a.text,b.text]);assert.equal(f.writes().length,0);
  await f.fire('Фрагмент 2 · стр. 2');assert.equal(f.writes()[0].body.action,'select');assert.equal(f.data().active_id,b.id);f.module.close();await f.show();assert.equal(f.readers()[0].textContent,b.text);await f.fire('Удалить из рабочего набора');assert.equal(f.writes().at(-1).body.action,'remove');assert.equal(f.data().items.length,1);assert.equal(f.data().active_id,'');assert.match(f.dialog.textContent,/Выберите сохранённый фрагмент/);
});

test('stale and missing sources keep saved text and original coordinates while map navigation warns and fences',async()=>{
  const f=fixture(),ref={map_id:'m',node_id:'n',revision:3},a=f.item(1,'Прежний <текст> 😀',{source_state:'stale',map_ref:ref,map_state:'changed',page:44}),b=f.item(2,'Удалённый источник',{source_state:'missing',map_ref:ref,map_state:'missing'});f.seed([a,b]);await f.show();assert.equal(f.readers()[0].textContent,a.text);assert.match(f.dialog.textContent,/Источник изменился/);assert.match(f.dialog.textContent,/Физическая страница 44/);assert.match(f.dialog.textContent,/текущее состояние/);await f.fire('Открыть связанную карту');assert.equal(f.dialog.open,false);assert.deepEqual(f.opened,[{workspace:'ws',mapRef:ref,mapState:'changed'}]);assert.equal(f.dialog.textContent.includes(a.text),false);
  await f.show();await f.fire('Фрагмент 2 · стр. 2');assert.equal(f.readers()[0].textContent,b.text);assert.match(f.dialog.textContent,/Источник недоступен/);assert.equal(f.button('Открыть связанную карту').disabled,true);await f.fire('Открыть связанную карту');assert.equal(f.opened.length,1);
});

test('lost mutation response retries the same receipt, blocks changed payload and reloads current membership after historical replay',async()=>{
  const f=fixture();f.loseReply=true;await f.capture();const original=f.writes()[0].body;assert.equal(f.button('Повторить ту же отправку').hidden,false);const other={...f.entry,id:4,pin:'sha256:'+'4'.repeat(64),text:'Другой фрагмент'};f.entries.set(4,other);await f.capture(other);assert.equal(f.writes().length,1);assert.match(f.dialog.textContent,/Другой фрагмент/);
  const current=f.data(),additional=f.item(9,'Сохранено другой вкладкой');current.items.push(additional);current.active_id=additional.id;current.revision++;await f.fire('Повторить ту же отправку');assert.deepEqual(f.writes()[1].body,original);assert.equal(f.receipts.size,1);assert.equal(f.data().items.length,2);assert.equal(f.readers()[0].textContent,additional.text);assert.match(f.dialog.textContent,/Другой фрагмент/);assert.equal(f.button('Повторить ту же отправку').hidden,true);
  await f.fire('Сохранить этот фрагмент');assert.equal(f.writes().at(-1).body.entry_id,4);assert.notEqual(f.writes().at(-1).body.request_id,original.request_id);assert.equal(f.writes().at(-1).body.revision,2);
});

test('an old mutation result refreshes a reopened dialog and cannot be overwritten by its late GET',async()=>{
  const f=fixture();let resolveWrite,resolveRead;f.handle=c=>c.body?new Promise(r=>resolveWrite=()=>f.respond(c).then(r)):f.respond(c);const capture=f.capture();await settle();const p=f.writes()[0].body;f.module.close();f.handle=c=>!c.body?new Promise(r=>resolveRead=()=>r({revision:0,active_id:'',items:[]})):f.respond(c);const showing=f.show();await settle();assert.equal(f.calls.filter(c=>!c.body).length,1,'GET is suppressed while the write owns the workspace');f.handle=c=>f.respond(c);resolveWrite();await capture;await showing;assert.equal(f.readers()[0].textContent,f.entry.text);assert.equal(f.writes().length,1);assert.equal(f.data().revision,p.revision+1);
  let late;f.handle=c=>!c.body?new Promise(r=>late=r):f.respond(c);const reading=f.fire('Обновить список');await settle();const old=f.calls.at(-1);f.module.close();f.handle=c=>f.respond(c);await f.show();late({revision:0,active_id:'',items:[]});await reading;assert.equal(old.signal.aborted,true);assert.equal(f.readers()[0].textContent,f.entry.text);
});

test('actor, CSRF-session, workspace, screen and authentication changes fence late reads and do not expose pending receipts',async()=>{
  for(const change of [f=>f.state.session.user='bob',f=>f.state.session.csrf='new-token',f=>f.state.selected='other',f=>f.state.revision++,f=>f.state.session.authenticated=false]){const f=fixture();f.seed([f.item(1,'PRIVATE')]);let resolve;f.handle=c=>new Promise(r=>resolve=()=>f.respond(c).then(r));const showing=f.show();await settle();change(f);const before=f.dialog.textContent;resolve();await showing;assert.equal(f.dialog.textContent,before);assert.equal(f.dialog.textContent.includes('PRIVATE'),false);}
  const f=fixture();f.loseReply=true;await f.capture();f.module.close();assert.equal(f.dialog.textContent.includes(f.entry.text),false);f.state.session.csrf='new-session';await f.show();assert.equal(f.button('Повторить ту же отправку').hidden,true);await f.fire('Повторить ту же отправку');assert.equal(f.writes().length,1);
});

test('revision conflicts never silently rebase and abandoning the old attempt requires an explicit confirmation',async()=>{
  const f=fixture();f.seed([f.item(1),f.item(2)]);await f.show();f.data().revision++;await f.fire('Фрагмент 2 · стр. 2');const p=f.writes()[0].body;assert.match(f.error.textContent,/409/);assert.equal(f.data().active_id,f.id(1));await f.fire('Повторить ту же отправку');assert.deepEqual(f.writes()[1].body,p);await f.fire('Отказаться от неподтверждённой отправки');assert.equal(f.button('Повторить ту же отправку').hidden,false);f.confirm=true;await f.fire('Отказаться от неподтверждённой отправки');assert.equal(f.button('Повторить ту же отправку').hidden,true);await f.fire('Фрагмент 2 · стр. 2');assert.equal(f.writes().at(-1).body.revision,6);assert.notEqual(f.writes().at(-1).body.request_id,p.request_id);
});

test('malformed mutation acknowledgements preserve the receipt until a matching replay and fresh GET',async()=>{
  for(const corrupt of [()=>({}),r=>({...r,replayed:'yes'}),r=>({...r,workspace:{...r.workspace,revision:99}}),r=>({...r,workspace:{...r.workspace,active_id:''}}),r=>({...r,workspace:{...r.workspace,items:r.workspace.items.map(i=>({...i,pin:'sha256:'+'f'.repeat(64)}))}})]){const f=fixture();f.handle=async c=>{const r=await f.respond(c);return c.body?corrupt(r):r;};await f.capture();const p=f.writes()[0].body;assert.equal(f.button('Повторить ту же отправку').hidden,false);assert.match(f.error.textContent,/не подтвердил/);f.module.close();f.handle=c=>f.respond(c);await f.show();await f.fire('Повторить ту же отправку');assert.deepEqual(f.writes().at(-1).body,p);assert.equal(f.receipts.size,1);assert.equal(f.data().items.length,1);assert.equal(f.button('Повторить ту же отправку').hidden,true);}
});

test('over-limit or inconsistent GET data is rejected and comparison never creates a write',async()=>{
  for(const corrupt of [w=>({...w,items:Array.from({length:25},(_,i)=>fixture().item(i+1))}),w=>({...w,active_id:'read-'+'f'.repeat(32)}),w=>({...w,items:[{...w.items[0],scroll:2}]}),w=>({...w,items:[{...w.items[0],text:'changed without matching range'}]})]){const f=fixture();f.seed([f.item(1)]);f.handle=async c=>corrupt(await f.respond(c));await f.show();assert.match(f.error.textContent,/Некорректный рабочий набор/);assert.equal(f.readers().length,0);assert.equal(f.writes().length,0);}
});

test('scroll changes during save remain visibly dirty and closing never claims to flush them',async()=>{
  const f=fixture();f.seed([f.item(1)]);await f.show();let reader=f.readers()[0];reader.scrollTop=200;await f.fire(reader,'scroll');let resolve;f.handle=c=>c.body?new Promise(r=>resolve=()=>f.respond(c).then(r)):f.respond(c);const saving=f.fire('Сохранить позицию');await settle();reader=f.readers()[0];reader.scrollTop=600;await f.fire(reader,'scroll');resolve();await saving;assert.equal(f.data().items[0].scroll,.25);assert.match(f.dialog.textContent,/Прокрутка не сохранена/);assert.equal(f.readers()[0].scrollTop,600);f.module.close();await f.show();assert.equal(f.readers()[0].scrollTop,200);assert.equal(f.writes().length,1);
});

test('pending contexts are bounded without losing receipts, and identity changes purge previous session attempts',async()=>{
  const f=fixture();for(let i=0;i<32;i++){f.state.selected='pending-'+i;f.loseReply=true;await f.capture();f.module.close();}
  const first=f.writes()[0].body;f.state.selected='pending-overflow';await assert.rejects(f.show(),/32 неподтверждённые отправки/);assert.equal(f.dialog.open,false);assert.equal(f.writes().length,32);
  f.state.selected='pending-0';await f.show();await f.fire('Повторить ту же отправку');assert.deepEqual(f.writes().at(-1).body,first);assert.equal(f.button('Повторить ту же отправку').hidden,true);
  f.state.selected='pending-overflow';f.loseReply=true;await f.capture();f.module.close();
  f.state.session.csrf='new-token';f.state.selected='new-session';await f.show();assert.equal(f.button('Повторить ту же отправку').hidden,true);f.module.close();
  f.state.session.csrf='alice-token';f.state.selected='pending-1';await f.show();assert.equal(f.button('Повторить ту же отправку').hidden,true,'returning to an old token cannot revive the purged receipt');
  f.state.selected='new-pending';f.loseReply=true;await f.capture();f.state.session.authenticated=false;f.module.close();f.state.session.authenticated=true;await f.show();assert.equal(f.button('Повторить ту же отправку').hidden,true,'auth loss purges same-token drafts');
});

test('confirmed and late successful contexts release their cached views instead of filling the pending context cap',async()=>{
  const f=fixture();for(let i=0;i<40;i++){f.state.selected='confirmed-'+i;await f.capture();f.module.close();assert.equal(f.dialog.textContent.includes(f.entry.text),false);}
  for(let i=0;i<40;i++){f.state.selected='late-'+i;let resolve;f.handle=c=>c.body?new Promise(r=>resolve=()=>f.respond(c).then(r)):f.respond(c);const adding=f.capture();await settle();f.module.close();resolve();await adding;assert.equal(f.dialog.open,false);}
  f.handle=c=>f.respond(c);f.state.selected='last';await f.show();assert.equal(f.button('Повторить ту же отправку').hidden,true);assert.equal(f.writes().length,80);
});
