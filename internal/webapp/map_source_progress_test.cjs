const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');

test('partial failure resumes saved work instead of creating another map',async()=>{
  const f=fixture();Object.assign(f.task,{status:'failed',can_resume:true,completed_batches:2});await f.poll();
  assert.equal(f.$('map-source-review').textContent,'Продолжить с сохранённого места');
  assert.match(f.$('map-source-banner-text').textContent,/Готовые части сохранены: 2/);
  f.handle=async path=>{if(path.endsWith('/resume')){f.task.status='queued';return f.task;}return [f.task];};
  await f.$('map-source-review').onclick();
  assert.equal(f.requests.filter(r=>r.path.endsWith('/resume')).length,1);
  assert.equal(f.$('add-source-map').clicked,undefined);
});

test('explicit cancellation retains an available resume action',async()=>{
  const f=fixture();Object.assign(f.task,{status:'cancelled',can_resume:true,completed_batches:1});await f.poll();
  assert.equal(f.$('map-source-review').hidden,false);
  assert.equal(f.$('map-source-review').textContent,'Продолжить с сохранённого места');
});

function fixture(){
  const elements=new Map(),intervals=new Map(),timeouts=new Set();
  function $(id){if(!elements.has(id))elements.set(id,{hidden:false,textContent:'',dataset:{},addEventListener(){},click(){this.clicked=true;}});return elements.get(id);}
  const state={session:{authenticated:true},selected:'ws',revision:1,map:{view:{map:{id:'map',revision:1}}}};
  let now=Date.parse('2026-09-09T10:00:15Z');
  const f={state,$,task:{id:'task',map_id:'map',status:'running',phase:'generate',current:1,total:3,source_overview:true,source_documents:2,source_chunks:9,created_at:'2026-09-09T10:00:00Z'},advance:ms=>now+=ms,showCalls:0,requests:[],timeouts};
  const context=vm.createContext({AbortController,setTimeout(fn){timeouts.add(fn);return fn;},clearTimeout(fn){timeouts.delete(fn);},Date:class extends Date{static now(){return now;}},document:{hidden:false,addEventListener(){}},setInterval(fn,ms){intervals.set(ms,fn);}});
  vm.runInContext(fs.readFileSync(__dirname+'/assets/map-from-sources.js','utf8'),context);
  context.installMemMapFromSources({state,$,node(){},run:fn=>fn,api:async(path,body,retry,signal)=>{f.requests.push({path,signal});if(f.handle)return f.handle(path,signal);if(f.error)throw Error('offline');if(f.response)return f.response;return [f.task];},showMap:async(id='map')=>{f.showCalls++;f.shown=id;if(f.showError)throw Error('refresh');state.map={view:{map:{id,revision:2}}};}});
  f.poll=intervals.get(2000);f.tick=intervals.get(1000);return f;
}
test('active generation shows actual batch and elapsed time from persisted creation',async()=>{
  const f=fixture();await f.poll();
  assert.equal(f.$('map-source-banner').dataset.state,'active');
  assert.equal(f.$('map-source-phase').textContent,'Выделяем темы · часть 1 из 3');
  assert.equal(f.$('map-source-elapsed').textContent,'Прошло 0:15');
  f.advance(5000);f.tick();assert.equal(f.$('map-source-elapsed').textContent,'Прошло 0:20');
  f.task.phase='merging';await f.poll();assert.equal(f.$('map-source-phase').textContent,'Объединяем темы и подтемы');
  f.state.map.view.map.id='other';await f.poll();f.state.map.view.map.id='map';await f.poll();
  assert.equal(f.$('map-source-elapsed').textContent,'Прошло 0:20');
});
test('completion stops spinner and timer and opens the overview',async()=>{
  const f=fixture();await f.poll();f.task.status='applied';await f.poll();
  assert.equal(f.$('map-source-banner').dataset.state,'applied');assert.equal(f.$('map-source-phase').textContent,'Карта готова');
  assert.equal(f.$('map-source-elapsed').hidden,true);assert.equal(f.$('map-source-cancel').hidden,true);assert.equal(f.$('map-overview').clicked,true);
  const text=f.$('map-source-elapsed').textContent;f.advance(5000);f.tick();assert.equal(f.$('map-source-elapsed').textContent,text);
});
test('cancellation and failed publication stop animation and expose recovery',async()=>{
  for(const task of [{status:'cancelled'},{status:'failed',error:'Ошибка модели'},{status:'ready',error:'Источники изменились'}]){
    const f=fixture();Object.assign(f.task,task);await f.poll();
    assert.notEqual(f.$('map-source-banner').dataset.state,'active');assert.equal(f.$('map-source-elapsed').hidden,true);
    assert.equal(f.$('map-source-cancel').hidden,true);assert.equal(f.$('map-source-review').hidden,false);
  }
});
test('connection failure is distinct from task failure and resumes the original clock',async()=>{
  const f=fixture();await f.poll();f.error=true;await f.poll();
  assert.equal(f.$('map-source-banner').dataset.state,'offline');assert.equal(f.$('map-source-elapsed').hidden,true);
  f.advance(60000);f.error=false;await f.poll();assert.equal(f.$('map-source-banner').dataset.state,'active');
  assert.equal(f.$('map-source-elapsed').textContent,'Прошло 1:15');
});
test('stale polling result cannot replace progress after navigation',async()=>{
  const f=fixture();let resolve;f.response=new Promise(r=>resolve=r);const pending=f.poll();
  f.state.map.view.map.id='other';resolve([f.task]);await pending;
  assert.equal(f.$('map-source-banner').hidden,true);assert.equal(f.$('map-source-phase').textContent,'');
});
test('failed final map refresh is retried before marking the display terminal',async()=>{
  const f=fixture();f.task.status='applied';f.showError=true;await f.poll();
  assert.equal(f.$('map-source-banner').dataset.state,'offline');f.showError=false;await f.poll();
  assert.equal(f.showCalls,2);assert.equal(f.$('map-overview').clicked,true);
});

test('queued map explains earlier generation and opens that map without another creation request',async()=>{
  const f=fixture();Object.assign(f.task,{status:'queued',queue:{position:2,ahead:1,pending:2,head:{map_id:'previous-map',map_title:'Предыдущая <карта>',status:'running',phase:'generate',current:14,total:24}}});await f.poll();
  assert.equal(f.$('map-source-banner').dataset.state,'queued');assert.equal(f.$('map-source-indicator').textContent,'…');assert.equal(f.$('map-source-phase').textContent,'В очереди этой базы — № 2');
  assert.match(f.$('map-source-banner-text').textContent,/Предыдущая <карта>/);assert.match(f.$('map-source-banner-text').textContent,/часть 14 из 24/);assert.match(f.$('map-source-banner-text').textContent,/Повторно создавать карту не нужно/);
  assert.equal(f.$('map-source-ahead').hidden,false);await f.$('map-source-ahead').onclick();assert.equal(f.shown,'previous-map');assert.equal(f.requests.length,1);assert.match(f.requests[0].path,/\/ai$/);
});

test('first queued task acknowledges other work instead of claiming generation has started',async()=>{
  const f=fixture();Object.assign(f.task,{status:'queued',queue:{position:1,ahead:0,pending:1}});await f.poll();
  assert.equal(f.$('map-source-ahead').hidden,true);assert.match(f.$('map-source-banner-text').textContent,/ожидает свободного обработчика/);assert.match(f.$('map-source-banner-text').textContent,/другие базы, импорт или индексирование/);assert.equal(f.$('map-source-banner').dataset.state,'queued');
  f.task.status='running';await f.poll();assert.equal(f.$('map-source-banner').dataset.state,'active');assert.match(f.$('map-source-phase').textContent,/часть 1 из 3/);
});

test('hung status request times out, stops animation and permits the next poll',async()=>{
  const f=fixture();f.handle=(_,signal)=>new Promise((resolve,reject)=>signal.addEventListener('abort',()=>{const e=Error('aborted');e.name='AbortError';reject(e);}));
  const pending=f.poll();assert.equal(f.timeouts.size,1);for(const timeout of f.timeouts)timeout();await pending;
  assert.equal(f.requests[0].signal.aborted,true);assert.equal(f.$('map-source-banner').dataset.state,'offline');assert.match(f.$('map-source-banner-text').textContent,/20 секунд/);assert.equal(f.$('map-source-cancel').hidden,true);assert.equal(f.timeouts.size,0);
  f.handle=null;await f.poll();assert.equal(f.$('map-source-banner').dataset.state,'active');assert.equal(f.timeouts.size,0);
});

test('new map polling supersedes a hung old request without waiting for its response',async()=>{
  const f=fixture();let resolve;f.handle=()=>new Promise(r=>resolve=r);const old=f.poll();const oldSignal=f.requests[0].signal;
  f.state.map.view.map.id='other';f.handle=null;await f.poll();assert.equal(oldSignal.aborted,true);assert.equal(f.requests.length,2);const text=f.$('map-source-phase').textContent;
  resolve([{...f.task,status:'failed',error:'OLD RESULT'}]);await old;assert.equal(f.$('map-source-phase').textContent,text);assert(!f.$('map-source-banner-text').textContent.includes('OLD RESULT'));assert.equal(f.timeouts.size,0);
});

test('late status and old ahead-map button are invalid after identity changes',async()=>{
  const f=fixture();Object.assign(f.task,{status:'queued',queue:{position:2,ahead:1,pending:2,head:{map_id:'prior',map_title:'Private',status:'running',phase:'generate',current:1,total:5}}});await f.poll();const open=f.$('map-source-ahead').onclick;
  let resolve;f.handle=()=>new Promise(r=>resolve=r);const pending=f.poll();f.state.session.csrf='new-session';resolve([{...f.task,status:'failed',error:'OLD ERROR'}]);await pending;assert(!f.$('map-source-banner-text').textContent.includes('OLD ERROR'));await open();assert.equal(f.showCalls,0);
});

test('failed assembly reuses saved parts through the same task without opening source selection',async()=>{
  const f=fixture();Object.assign(f.task,{status:'failed',phase:'synthesizing',can_reassemble:true,error:'Сборка карты: превышен контекст'});await f.poll();
  assert.equal(f.$('map-source-review').textContent,'Повторить сборку из готовых частей');
  f.handle=async path=>{if(path.endsWith('/reassemble')){f.task.status='queued';return f.task;}return [f.task];};
  await f.$('map-source-review').onclick();
  assert.equal(f.requests.filter(r=>r.path.endsWith('/reassemble')).length,1);assert.equal(f.$('add-source-map').clicked,undefined);assert.equal(f.$('map-source-review').disabled,false);assert.equal(f.$('map-source-banner').dataset.state,'queued');
});

test('assembly retry prevents duplicate clicks and ignores a response after navigation',async()=>{
  const f=fixture();Object.assign(f.task,{status:'failed',can_reassemble:true});await f.poll();const retry=f.$('map-source-review').onclick;
  let resolve;f.handle=()=>new Promise(r=>resolve=r);const pending=retry();await retry();assert.equal(f.requests.length,2);
  f.state.map.view.map.id='other';resolve({status:'queued'});await pending;assert.equal(f.requests.length,2);assert.equal(f.$('map-source-review').disabled,false);
});

test('synthesis and checkpoint restore display their actual phases',async()=>{
  const f=fixture();f.task.phase='synthesizing';await f.poll();assert.equal(f.$('map-source-phase').textContent,'Группируем темы карты');
  f.task.phase='reusing';await f.poll();assert.match(f.$('map-source-phase').textContent,/Восстанавливаем готовые части/);
});

test('lost retry response allows status polling to discover accepted assembly',async()=>{
  const f=fixture();Object.assign(f.task,{status:'failed',can_reassemble:true});await f.poll();
  f.handle=async()=>{f.task.status='queued';throw Error('lost response');};await assert.rejects(f.$('map-source-review').onclick(),/lost response/);
  f.handle=null;await f.poll();assert.equal(f.$('map-source-banner').dataset.state,'queued');assert.equal(f.$('map-source-review').disabled,false);
});
