const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const settle=()=>new Promise(resolve=>setImmediate(resolve));
function fixture(){
  const elements=[],calls=[];let nonce=0;
  function node(tag,text,css){let own=text===undefined?'':String(text);const e={tag,className:css||'',children:[],parentNode:null,events:{},attrs:{},open:false,hidden:false,disabled:false,
    get textContent(){return own+this.children.map(c=>c.textContent).join('');},set textContent(v){own=String(v);this.replaceChildren();},
    append(...children){for(const c of children){c.parentNode=this;this.children.push(c);}},replaceChildren(...children){for(const c of this.children)c.parentNode=null;this.children=[];this.append(...children);},
    addEventListener(k,fn){(this.events[k]??=[]).push(fn);},setAttribute(k,v){this.attrs[k]=v;},contains(other){for(let n=other;n;n=n.parentNode)if(n===this)return true;return false;},
    showModal(){this.open=true;},close(){this.open=false;for(const fn of this.events.close||[])fn();}
  };elements.push(e);return e;}
  const clone=x=>JSON.parse(JSON.stringify(x));
  const f={elements,calls,state:{selected:'ws',revision:1,session:{user:'alice'}},receipts:new Map(),confirm:false,
    annotation:{id:'ann-'+'a'.repeat(32),revision:3,quote:'Old <literal>',source_name:'Old.pdf',page:4,document_revision:'old-revision'},
    selection:{entry_id:8,pin:'sha256:'+'d'.repeat(64),start:1,end:5,quote:'😀Б В'},
    newID:'ann-'+'b'.repeat(32),lineageID:'alink-'+'c'.repeat(32)
  };
  f.result=body=>({annotation:{id:f.newID,revision:1,quote:body.quote},replayed:false,lineage:{id:f.lineageID,old_id:f.annotation.id,old_revision:body.revision,new_id:f.newID,new_revision:1,actor:'alice',created:'2026-10-10'}});
  f.candidate=(overrides={})=>({entry_id:8,pin:f.selection.pin,document_id:'document-logical-id',document_revision:'new-revision',citation_id:'citation-8',source_name:'New <literal>.pdf',page:9,block_index:3,block_chunk_index:2,start:2,end:2+Array.from(f.annotation.quote).length,quote:f.annotation.quote,prefix:'До 😀 ',suffix:' после <tag>',...overrides});
  f.candidatePage=limit=>({annotation_id:f.annotation.id,annotation_revision:f.annotation.revision,quote:f.annotation.quote,items:[f.candidate()],limit,complete:true,scanned_entries:3,scanned_bytes:500,total_entries:3,warnings:['Поиск только внутри доступных фрагментов.'],truncated_reasons:[]});
  f.respond=async(path,body)=>{
    if(path.includes('/reanchor-candidates?'))return f.candidatePage(Number(new URLSearchParams(path.split('?')[1]).get('limit')));
    if(path.endsWith('/reanchor-preview'))return {digest:'sha256:'+'e'.repeat(64),old:clone(f.annotation),target:{...clone(body),document_revision:'new-revision',source_name:'New.pdf',page:9},warnings:['<b>Old links and regions are retained.</b>'],region_count:2,map_link_count:1};
    if(path.endsWith('/reanchor')){if(f.receipts.has(body.request_id))return {...clone(f.receipts.get(body.request_id)),replayed:true};const result=f.result(body);f.receipts.set(body.request_id,result);return clone(result);}
    if(path.includes('/lineage?')){const q=new URLSearchParams(path.split('?')[1]),offset=Number(q.get('offset')),items=Array.from({length:61},(_,i)=>({...f.result({revision:3,quote:'new'}).lineage,created:'entry-'+i}));return {items:items.slice(offset,offset+30),total:61,offset,limit:30};}
    throw Error('Unexpected API '+path);
  };
  const context=vm.createContext({document:{body:node('body')},AbortController,confirm:()=>f.confirm,crypto:{randomUUID:()=>String(++nonce).padStart(32,'0')}});
  vm.runInContext(fs.readFileSync(__dirname+'/assets/annotation-reanchor.js','utf8'),context);
  f.module=context.installMemAnnotationReanchor({state:f.state,node,run:fn=>fn,api:async(path,body,retry,signal)=>{calls.push({path,body:body===undefined?undefined:clone(body),signal});const result=await(f.handle?f.handle(path,body):f.respond(path,body));if(path.endsWith('/reanchor')&&f.loseReply){f.loseReply=false;throw Error('Response lost');}return result;}});
  f.dialog=elements.find(e=>e.id==='annotation-reanchor-dialog');f.status=elements.find(e=>e.attrs.role==='status');f.error=elements.find(e=>e.attrs.role==='alert');
  f.button=label=>elements.findLast(e=>e.tag==='button'&&e.textContent===label&&f.dialog.contains(e));f.fire=async(target,event='click')=>{const e=typeof target==='string'?f.button(target):target;assert.ok(e,'Missing button '+target);for(const fn of e.events[event]||[])await fn({preventDefault(){}});await settle();};
  f.start=()=>f.module.start({workspace:'ws',annotation:f.annotation});f.preview=options=>f.module.preview({workspace:'ws',selection:f.selection,...options});f.ready=async options=>{f.start();f.module.close();await f.preview(options);};
  f.find=options=>f.module.findCandidates({workspace:'ws',annotation:f.annotation,...options});
  return f;
}

test('reanchor preview shows both literal quotes and explicit apply pins old revision, target selection and digest',async()=>{
  const f=fixture();let opened;await f.ready({onApplied:result=>opened=result.annotation.id});assert.equal(f.calls.length,1);assert.deepEqual(f.calls[0].body,{revision:3,...f.selection});assert.match(f.dialog.textContent,/Old <literal>/);assert.match(f.dialog.textContent,/New.pdf · Физическая страница 9 · new-revision/);assert.match(f.dialog.textContent,/<b>Old links and regions are retained.<\/b>/);
  await f.fire('Создать новую привязку');const write=f.calls[1];assert.match(write.path,/\/reanchor$/);assert.equal(write.body.revision,3);assert.equal(write.body.quote,f.selection.quote);assert.equal(write.body.digest,'sha256:'+'e'.repeat(64));assert.equal(f.receipts.size,1);assert.equal(f.module.active('ws'),undefined);await f.fire('Открыть новую аннотацию');assert.equal(opened,f.newID);assert.equal(f.dialog.open,false);
});

test('lost reanchor response preserves the exact receipt across close and reordered selection properties',async()=>{
  const f=fixture();await f.ready();f.loseReply=true;await f.fire('Создать новую привязку');const original=f.calls[1].body;assert.equal(f.button('Повторить ту же отправку').hidden,false);f.module.close();
  await f.preview({selection:{quote:f.selection.quote,end:5,start:1,pin:f.selection.pin,entry_id:8}});assert.equal(f.calls.length,2);await f.fire('Повторить ту же отправку');assert.deepEqual(f.calls[2].body,original);assert.equal(f.receipts.size,1);assert.match(f.status.textContent,/Эта отправка уже была сохранена/);
});

test('reopening while a reanchor is inflight receives its result and uses the new completion callback',async()=>{
  const f=fixture();let old=0,fresh=0,resolve;await f.ready({onApplied:()=>old++});f.handle=(path,body)=>path.endsWith('/reanchor')?new Promise(r=>resolve=r):f.respond(path,body);const sending=f.fire('Создать новую привязку');await settle();const input=f.calls.at(-1).body;f.module.close();await f.preview({onApplied:()=>fresh++});assert.equal(f.button('Повторить ту же отправку').disabled,true);assert.match(f.status.textContent,/ещё выполняется/);
  resolve(f.result(input));await sending;assert.match(f.status.textContent,/Создана новая аннотация/);assert.equal(f.button('Повторить ту же отправку').hidden,true);await f.fire('Открыть новую аннотацию');assert.equal(old,0);assert.equal(fresh,1);
});

test('inflight failure enables retry in the reopened dialog instead of leaving disabled controls',async()=>{
  const f=fixture();let reject;await f.ready();f.handle=(path,body)=>path.endsWith('/reanchor')?new Promise((_r,j)=>reject=j):f.respond(path,body);const sending=f.fire('Создать новую привязку');await settle();f.module.close();await f.preview();reject(Error('offline'));await sending;assert.equal(f.button('Повторить ту же отправку').disabled,false);assert.match(f.error.textContent,/offline/);f.handle=(path,body)=>f.respond(path,body);await f.fire('Повторить ту же отправку');assert.equal(f.receipts.size,1);
});

test('late preview is aborted and cannot publish after workspace or screen changes',async()=>{
  const f=fixture();f.start();let resolve;f.handle=()=>new Promise(r=>resolve=r);const checking=f.preview();await settle();const request=f.calls[0];f.module.close();assert.equal(request.signal.aborted,true);f.state.selected='other';resolve(await f.respond(request.path,request.body));await checking;assert.equal(f.dialog.open,false);assert.equal(f.button('Создать новую привязку').hidden,true);
  f.state.selected='ws';f.handle=(path,body)=>f.respond(path,body);await f.preview();f.state.revision++;await f.fire('Создать новую привязку');assert.equal(f.calls.filter(c=>c.path.endsWith('/reanchor')).length,0);
});

test('malformed reanchor acknowledgements keep the receipt, and mismatched preview cannot be applied',async()=>{
  const f=fixture();await f.ready();f.handle=(path,body)=>path.endsWith('/reanchor')?Promise.resolve({...f.result(body),lineage:{...f.result(body).lineage,new_id:f.annotation.id}}):f.respond(path,body);await f.fire('Создать новую привязку');const original=f.calls.at(-1).body;assert.equal(f.button('Повторить ту же отправку').hidden,false);assert.match(f.error.textContent,/Некорректный ответ/);f.handle=(path,body)=>f.respond(path,body);await f.fire('Повторить ту же отправку');assert.deepEqual(f.calls.at(-1).body,original);assert.equal(f.receipts.size,1);
  const bad=fixture();bad.handle=async(path,body)=>{const p=await bad.respond(path,body);return {...p,target:{...p.target,quote:'Wrong quote'}};};await bad.ready();assert.equal(bad.button('Создать новую привязку').hidden,true);assert.match(bad.error.textContent,/Некорректный результат сравнения/);await bad.fire('Создать новую привязку');assert.equal(bad.calls.length,1);
});

test('successful old-user acknowledgement clears only its captured target and cannot clear a replacement',async()=>{
  for(const changeActor of [true,false]){const f=fixture();await f.ready();let resolve;f.handle=(path,body)=>path.endsWith('/reanchor')?new Promise(r=>resolve=r):f.respond(path,body);const sending=f.fire('Создать новую привязку');await settle();const input=f.calls.at(-1).body;f.module.close();if(changeActor)f.state.session.user='bob';const replacement={...f.annotation,id:'ann-'+'f'.repeat(32)};f.module.start({workspace:'ws',annotation:replacement});resolve(f.result(input));await sending;assert.equal(f.module.active('ws').id,replacement.id);if(changeActor){f.state.session.user='alice';assert.equal(f.module.active('ws'),undefined);}}
});

test('lineage paging serializes requests, freezes navigation, and cannot navigate stale callbacks',async()=>{
  const f=fixture();const opened=[];await f.module.showHistory({workspace:'ws',id:f.annotation.id,onOpen:id=>opened.push(id)});await f.fire('Следующие');assert.match(f.status.textContent,/Показано: 31–60/);const previous=f.button('Предыдущие'),next=f.button('Следующие');let resolve;f.handle=()=>new Promise(r=>resolve=r);const loading=f.fire(previous);await settle();const before=f.calls.length;assert.equal(next.disabled,true);await f.fire(next);assert.equal(f.calls.length,before);
  resolve(await f.respond(f.calls.at(-1).path));await loading;assert.match(f.status.textContent,/Показано: 1–30/);await f.fire(next);assert.equal(f.calls.length,before);const oldOpen=f.button('Прежняя аннотация · ревизия 3');f.state.selected='other';await f.fire(oldOpen);assert.deepEqual(opened,[]);
});

test('reset of an uncertain reanchor requires confirmation and target cancellation is workspace scoped',async()=>{
  const f=fixture();await f.ready();f.loseReply=true;await f.fire('Создать новую привязку');await f.fire('Сбросить попытку');assert.equal(f.dialog.open,true);f.confirm=true;await f.fire('Сбросить попытку');assert.equal(f.dialog.open,false);await f.preview();assert.equal(f.calls.filter(c=>c.path.endsWith('/reanchor-preview')).length,2);f.module.cancel('other');assert.ok(f.module.active('ws'));f.module.cancel('ws');assert.equal(f.module.active('ws'),undefined);
});

test('candidate discovery requires an explicit choice then preview then apply even for one exact Unicode match',async()=>{
  const f=fixture();f.annotation.quote='😀Б <literal>';let opened;f.module.start({workspace:'ws',annotation:f.annotation,onApplied:result=>opened=result.annotation.id});
  await f.fire('Найти цитату в новой версии');assert.equal(f.calls.length,1);assert.equal(f.calls[0].path,'workspaces/ws/annotations/'+f.annotation.id+'/reanchor-candidates?revision=3&limit=20');assert.equal(f.calls[0].body,undefined);assert.ok(f.calls[0].signal);assert.equal(f.button('Создать новую привязку').hidden,true);assert.match(f.dialog.textContent,/Физическая страница 9/);assert.match(f.dialog.textContent,/До 😀 😀Б <literal> после <tag>/);assert.ok(f.dialog.textContent.includes('диапазон Unicode [2, '+(2+Array.from(f.annotation.quote).length)+')'));assert.match(f.status.textContent,/Показано совпадений: 1/);
  await f.fire('Создать новую привязку');assert.equal(f.calls.length,1);await f.fire('Выбрать совпадение 1');assert.equal(f.calls.length,2);assert.equal(f.calls[1].path.endsWith('/reanchor-preview'),true);assert.deepEqual(f.calls[1].body,{revision:3,entry_id:8,pin:f.selection.pin,start:2,end:2+Array.from(f.annotation.quote).length,quote:f.annotation.quote});assert.equal(f.receipts.size,0);
  await f.fire('Создать новую привязку');assert.equal(f.receipts.size,1);await f.fire('Открыть новую аннотацию');assert.equal(opened,f.newID);
});

test('repeated exact occurrences and matches from the same document revision stay individually selectable',async()=>{
  const f=fixture();const page=f.candidatePage;f.candidatePage=limit=>({...page(limit),items:[f.candidate({document_revision:f.annotation.document_revision}),f.candidate({start:70,end:70+Array.from(f.annotation.quote).length,page:0})]});await f.find();assert.match(f.status.textContent,/Показано совпадений: 2/);assert.match(f.dialog.textContent,/Та же редакция/);assert.match(f.dialog.textContent,/Без страницы/);await f.fire('Выбрать совпадение 2');assert.equal(f.calls.at(-1).body.start,70);assert.equal(f.calls.at(-1).body.entry_id,8);assert.equal(f.calls.filter(c=>c.path.endsWith('/reanchor')).length,0);
});

test('incomplete candidate search expands explicitly from 20 to 50 and reports the scan budget without silent paging',async()=>{
  const f=fixture(),page=f.candidatePage;f.candidatePage=limit=>({...page(limit),complete:false,total_entries:1400,scanned_entries:1024,scanned_bytes:8*1024*1024,items:Array.from({length:limit},(_,i)=>f.candidate({start:i*30,end:i*30+Array.from(f.annotation.quote).length})),warnings:['<raw> Ограниченный поиск.'],truncated_reasons:['candidate_limit','entry_limit']});await f.find();assert.equal(f.calls.length,1);assert.match(f.status.textContent,/Поиск неполный/);assert.match(f.status.textContent,/1024 из 1400/);assert.match(f.dialog.textContent,/<raw> Ограниченный поиск/);assert.match(f.dialog.textContent,/не доказывает отсутствие/);assert.match(f.dialog.textContent,/достигнут предел проверяемых фрагментов/);assert.ok(f.button('Искать до 50 совпадений'));
  await f.fire('Искать до 50 совпадений');assert.equal(f.calls.length,2);assert.match(f.calls[1].path,/limit=50$/);assert.match(f.status.textContent,/Показано совпадений: 50/);assert.match(f.dialog.textContent,/Предел интерфейса: 50/);assert.equal(f.button('Искать до 50 совпадений'),undefined);assert.equal(f.receipts.size,0);
});

test('empty complete and unresolved-boundary searches have different honest explanations',async()=>{
  for(const complete of [true,false]){const f=fixture(),page=f.candidatePage;f.candidatePage=limit=>({...page(limit),items:[],complete,total_entries:0,scanned_entries:0,scanned_bytes:0,truncated_reasons:complete?[]:['binding_unresolved']});await f.find();assert.equal(f.button('Выбрать совпадение 1'),undefined);assert.match(f.dialog.textContent,complete?/Точных совпадений в доступных фрагментах нет/:/В проверенной части совпадений нет/);if(!complete)assert.match(f.dialog.textContent,/привязка к исходному документу не подтверждена/);assert.equal(f.receipts.size,0);}
});

test('candidate DTO rejects foreign pins, malformed Unicode ranges and over-limit pages before displaying choices',async()=>{
  const cases=[p=>({...p,annotation_id:'ann-'+'f'.repeat(32)}),p=>({...p,annotation_revision:4}),p=>({...p,quote:'wrong'}),p=>({...p,limit:50}),p=>({...p,items:Array.from({length:21},()=>p.items[0])}),p=>({...p,items:[{...p.items[0],end:p.items[0].end+1}]}),p=>({...p,items:[{...p.items[0],quote:'not exact'}]}),p=>({...p,items:[{...p.items[0],pin:'wrong'}]}),p=>({...p,items:[{...p.items[0],prefix:'x'.repeat(49)}]}),p=>({...p,scanned_entries:4}),p=>({...p,scanned_bytes:8*1024*1024+1})];
  for(const change of cases){const f=fixture(),page=f.candidatePage;f.candidatePage=limit=>change(page(limit));await f.find();assert.match(f.error.textContent,/Некорректный список/);assert.equal(f.button('Выбрать совпадение 1'),undefined);assert.equal(f.button('Создать новую привязку').hidden,true);assert.equal(f.calls.length,1);f.candidatePage=page;await f.fire('Повторить поиск');assert.ok(f.button('Выбрать совпадение 1'));}
});

test('late candidate responses cannot change a closed reader or a different workspace, actor, screen or authentication',async()=>{
  for(const change of [f=>f.module.close(),f=>f.state.selected='other',f=>f.state.session.user='bob',f=>f.state.revision++,f=>f.state.session.authenticated=false]){const f=fixture();let resolve;f.handle=()=>new Promise(r=>resolve=r);const loading=f.find();await settle();const request=f.calls[0];change(f);const text=f.dialog.textContent;resolve(await f.respond(request.path));await loading;assert.equal(f.dialog.textContent,text);assert.equal(f.button('Выбрать совпадение 1'),undefined);assert.equal(f.receipts.size,0);if(!f.dialog.open)assert.equal(request.signal.aborted,true);}
});

test('candidate refresh prevents old or busy buttons from navigating and a failed preview cannot apply',async()=>{
  const f=fixture();await f.find();const old=f.button('Выбрать совпадение 1'),refresh=f.button('Повторить поиск');let resolve;f.handle=(path,body)=>path.includes('/reanchor-candidates?')?new Promise(r=>resolve=r):f.respond(path,body);const loading=f.fire(refresh);await settle();await f.fire(old);await f.fire(refresh);assert.equal(f.calls.length,2);resolve(await f.respond(f.calls.at(-1).path));await loading;await f.fire(old);assert.equal(f.calls.length,2);
  f.handle=(path,body)=>{if(path.endsWith('/reanchor-preview'))throw Error('Источник изменился; повторите выбор.');return f.respond(path,body);};await f.fire('Выбрать совпадение 1');assert.match(f.error.textContent,/Источник изменился/);assert.equal(f.button('Создать новую привязку').hidden,true);await f.fire('Создать новую привязку');assert.equal(f.receipts.size,0);
});

test('candidate search preserves an uncertain manual draft and resumes its original nonce even if no match is returned',async()=>{
  const f=fixture();await f.ready();f.loseReply=true;await f.fire('Создать новую привязку');const original=f.calls.at(-1).body;f.module.close();const page=f.candidatePage;f.candidatePage=limit=>({...page(limit),items:[]});let opened;await f.find({onApplied:result=>opened=result.annotation.id});assert.match(f.dialog.textContent,/неподтверждённая отправка/);assert.ok(f.dialog.textContent.includes(f.selection.quote));await f.fire('Продолжить прежнюю отправку');assert.equal(f.button('Повторить ту же отправку').hidden,false);await f.fire('Повторить ту же отправку');assert.deepEqual(f.calls.at(-1).body,original);assert.equal(f.receipts.size,1);await f.fire('Открыть новую аннотацию');assert.equal(opened,f.newID);
});

test('candidate search does not expose another actor draft and explicit choice callbacks are fenced after replacement',async()=>{
  const f=fixture();await f.ready();f.loseReply=true;await f.fire('Создать новую привязку');f.module.close();f.state.session.user='bob';await f.find();assert.equal(f.button('Продолжить прежнюю отправку'),undefined);const oldChoice=f.button('Выбрать совпадение 1');f.module.close();f.state.session.user='alice';await f.find();assert.ok(f.button('Продолжить прежнюю отправку'));const count=f.calls.length;await f.fire(oldChoice);assert.equal(f.calls.length,count);assert.equal(f.receipts.size,1);
});

test('an inflight apply completes into a reopened candidate search and fences its late read with the fresh callback',async()=>{
  const f=fixture();let old=0,fresh=0,resolveWrite,resolveRead;await f.ready({onApplied:()=>old++});f.handle=(path,body)=>path.endsWith('/reanchor')?new Promise(r=>resolveWrite=r):path.includes('/reanchor-candidates?')?new Promise(r=>resolveRead=r):f.respond(path,body);
  const writing=f.fire('Создать новую привязку');await settle();const submitted=f.calls.at(-1).body;f.module.close();const finding=f.find({onApplied:()=>fresh++});await settle();const reading=f.calls.at(-1);resolveWrite(f.result(submitted));await writing;assert.equal(reading.signal.aborted,true);assert.match(f.status.textContent,/Создана новая аннотация/);resolveRead(await f.respond(reading.path));await finding;assert.match(f.status.textContent,/Создана новая аннотация/);assert.equal(f.button('Выбрать совпадение 1'),undefined);await f.fire('Открыть новую аннотацию');assert.equal(old,0);assert.equal(fresh,1);
});
