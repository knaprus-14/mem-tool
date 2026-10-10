// Logic regressions using a deliberately small fake DOM. These do not test
// browser layout, native focus traversal, accessibility or end-to-end routing.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const settle=()=>new Promise(resolve=>setImmediate(resolve));
const clone=value=>JSON.parse(JSON.stringify(value));
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};};

function fixture(){
  const elements=new Map();let serial=0,receipt=0,focused=null;
  // Read tag names from the actual page rather than assume that every control
  // is a button: the keyboard guard depends on the active target's real tag.
  const tags=new Map();
  for(const match of fs.readFileSync(__dirname+'/assets/index.html','utf8').matchAll(/<([a-z][a-z0-9]*)\b[^>]*\bid="([^"]+)"/gi))tags.set(match[2],match[1].toUpperCase());
  function element(id,tag=tags.get(id)||'DIV'){
    return {id,tagName:tag,value:'',textContent:'',hidden:false,disabled:false,checked:false,open:false,children:[],events:new Map(),valid:true,
      addEventListener(type,fn){const handlers=this.events.get(type)||[];handlers.push(fn);this.events.set(type,handlers);},
      async emit(type,event={}){for(const fn of this.events.get(type)||[])await fn({target:this,preventDefault(){},...event});},
      click(){if(!this.disabled)this.lastClick=this.emit('click');return this.lastClick;},
      querySelectorAll(selector){const wanted=selector.toUpperCase().split(',');return [...elements.values()].filter(e=>wanted.includes(e.tagName));},
      append(...children){this.children.push(...children);},replaceChildren(...children){this.children=[...children];},
      showModal(){this.open=true;},close(){this.open=false;void this.emit('close');},
      checkValidity(){return this.valid;},reportValidity(){this.validityReported=true;},focus(){focused=this.id;},
    };
  }
  function $(id){if(!elements.has(id))elements.set(id,element(id));return elements.get(id);}
  // Existing HTML controls are available before the first busy() toggle.
  for(const id of tags.keys())if(id.startsWith('map-study-')||id.startsWith('map-workbench-')||id.startsWith('map-branch-')||id.startsWith('map-compare-'))$(id);
  function node(tag,text,css){const e=element('dynamic-'+(++serial),tag.toUpperCase());e.textContent=text;e.className=css;elements.set(e.id,e);return e;}
  const state={selected:'ws',revision:7,map:{selectedNode:'root',view:{map:{id:'map-1',title:'Fixture map',revision:3}},tree:{byID:new Map([['root',{label:'Selected branch'}]])}}};
  const study={manifest:{digest:'a'.repeat(64),node_ids:['root','n1','n2'],evidence:[{citation_id:'E1'}]},unavailable_sources:0,other_sources:0,
    study:{cards:[{id:'c1',node_id:'n1',front:'First question',back:'First answer',sources:[]},{id:'c2',node_id:'n2',front:'Second question',back:'Second answer',sources:[]}],markdown:'# Study'},
    progress:{c1:{current:true,due:true,last_attempt_id:11},c2:{current:true,due:true,last_attempt_id:22}},
  };
  const f={$,elements,state,study,calls:[],focused:()=>focused};
  const context=vm.createContext({AbortController,crypto:{randomUUID:()=>String(++receipt).padStart(32,'0')},setTimeout,URL,Blob});
  vm.runInContext(fs.readFileSync(__dirname+'/assets/map-workbench.js','utf8'),context);
  context.installMemMapWorkbench({state,$,node,run:fn=>fn,selectMapNode(){},drawMap(){},centerMapNode(){},
    api:async(path,body,_retry,signal)=>{
      f.calls.push({path,body:clone(body),signal});
      if(path.endsWith('/study')){const data=f.waitStudy?await f.waitStudy:clone(f.study);f.loaded=data;return data;}
      if(path.endsWith('/study-review')){
        if(f.reviewHandler)return await f.reviewHandler(body,signal);
        return {card_id:body.card_id,progress:{current:true,due:false,due_at:'2026-11-01T12:00:00Z',last_attempt_id:101}};
      }
      if(path.endsWith('/study-history'))return f.waitHistory?await f.waitHistory:(f.history||{items:[]});
      throw Error('unexpected route: '+path);
    },
  });
  // Direct dispatch also allows us to check the handler's safety guard even
  // when the real browser would suppress a click on a disabled button.
  f.fire=async(id,type='click',event={})=>{await $(id).emit(type,event);await settle();};
  f.open=()=>f.fire('map-branch-open');
  f.reviews=()=>f.calls.filter(c=>c.path.endsWith('/study-review'));
  f.key=async(target,key,code=key)=>{let prevented=false;await f.fire('map-workbench-dialog','keydown',{target:$(target),key,code,preventDefault(){prevented=true;}});return prevented;};
  return f;
}

test('study requires reveal before rating and freezes answer, confidence and hint at first reveal',async()=>{
  const f=fixture();await f.open();
  assert.equal(f.focused(),'map-study-front');
  assert.equal(f.$('map-study-front').tabIndex,0);
  assert.equal(f.$('map-study-back').hidden,true);
  for(const id of ['map-study-again','map-study-hard','map-study-know','map-study-easy'])assert.equal(f.$(id).disabled,true);
  await f.fire('map-study-know');assert.equal(f.reviews().length,0);
  f.$('map-study-response').value='My attempt before seeing the answer';f.$('map-study-confidence').value='65';f.$('map-study-hint').checked=true;
  await f.fire('map-study-flip');assert.equal(f.$('map-study-back').hidden,false);assert.equal(f.$('map-study-know').disabled,false);
  for(const id of ['map-study-response','map-study-confidence','map-study-hint'])assert.equal(f.$(id).disabled,true);
  // A later programmatic change or hide/reveal must not overwrite the frozen
  // values sent to the server as the learner's pre-reveal attempt.
  f.$('map-study-response').value='Copied answer';f.$('map-study-confidence').value='100';f.$('map-study-hint').checked=false;
  await f.fire('map-study-flip');await f.fire('map-study-flip');await f.fire('map-study-know');
  const call=f.reviews()[0];assert.equal(call.path,'workspaces/ws/maps/map-1/nodes/root/study-review');
  assert.equal(call.body.response,'My attempt before seeing the answer');assert.equal(call.body.confidence,65);assert.equal(call.body.used_hint,true);
  assert.equal(call.body.grade,'good');assert.equal(call.body.revision,3);assert.equal(call.body.manifest,f.study.manifest.digest);assert.equal(call.body.previous_attempt_id,11);
  assert.equal(f.loaded.progress.c1.last_attempt_id,101);assert.equal(f.$('map-study-back').hidden,true);assert.equal(f.$('map-study-know').disabled,true);
});

test('invalid confidence cannot reveal or submit and an empty confidence stays null',async()=>{
  const f=fixture();await f.open();f.$('map-study-confidence').valid=false;f.$('map-study-confidence').value='101';
  await f.fire('map-study-flip');await f.fire('map-study-easy');assert.equal(f.reviews().length,0);assert.equal(f.$('map-study-back').hidden,true);assert.equal(f.$('map-study-confidence').validityReported,true);
  f.$('map-study-confidence').valid=true;f.$('map-study-confidence').value='';await f.fire('map-study-flip');await f.fire('map-study-hard');
  assert.equal(f.reviews()[0].body.confidence,null);
});

test('an uncertain rating retries the identical receipt and frozen attempt',async()=>{
  const f=fixture();await f.open();f.$('map-study-response').value='first';await f.fire('map-study-flip');
  f.reviewHandler=async()=>{throw Error('network uncertain');};await f.fire('map-study-know');
  assert.equal(f.$('map-study-know').disabled,false);assert.match(f.$('map-workbench-error').textContent,/network uncertain/);
  f.$('map-study-response').value='changed after failure';f.reviewHandler=null;await f.fire('map-study-know');
  assert.equal(f.reviews().length,2);assert.deepEqual(f.reviews()[0].body,f.reviews()[1].body);
  assert.equal(f.loaded.progress.c1.last_attempt_id,101);
});

test('a late rating cannot update a closed dialog or a reopened session',async()=>{
  for(const reopen of [false,true]){
    const f=fixture();await f.open();await f.fire('map-study-flip');const wait=deferred();f.reviewHandler=()=>wait.promise;
    const pending=f.fire('map-study-know');await settle();const signal=f.reviews()[0].signal;assert.equal(signal.aborted,false);
    await f.fire('map-workbench-close');assert.equal(signal.aborted,true);
    if(reopen)await f.open();const summary=f.$('map-branch-summary').textContent;const loaded=f.loaded;
    wait.resolve({card_id:'c1',progress:{current:true,due:false,due_at:'2030-01-01T00:00:00Z',last_attempt_id:999}});await pending;
    assert.equal(f.$('map-branch-summary').textContent,summary);assert.notEqual(loaded.progress.c1.last_attempt_id,999);
    assert.equal(f.$('map-workbench-dialog').open,reopen);
  }
});

test('late study loading and workspace navigation cannot replace the current screen',async()=>{
  const f=fixture(),wait=deferred();f.waitStudy=wait.promise;const pending=f.open();await settle();
  f.state.selected='other';f.state.revision++;f.$('map-study-front').textContent='Other workspace';
  wait.resolve(clone(f.study));await pending;assert.equal(f.$('map-study-front').textContent,'Other workspace');
  assert.equal(f.reviews().length,0);
});

test('due-only mode uses returned server progress and advances without losing other cards',async()=>{
  const f=fixture();await f.open();f.$('map-study-due-only').checked=true;await f.fire('map-study-due-only','change');
  assert.equal(f.$('map-study-front').textContent,'First question');await f.fire('map-study-flip');await f.fire('map-study-know');
  assert.equal(f.$('map-study-front').textContent,'Second question');assert.match(f.$('map-study-position').textContent,/К повторению: 1/);
  assert.equal(f.loaded.progress.c1.due,false);assert.equal(f.loaded.progress.c2.last_attempt_id,22);
  f.reviewHandler=async(body)=>({card_id:body.card_id,progress:{current:true,due:true,due_at:'2026-01-01T00:00:00Z',last_attempt_id:102}});
  await f.fire('map-study-flip');await f.fire('map-study-again');assert.equal(f.$('map-study-front').textContent,'Second question');
  assert.equal(f.loaded.progress.c2.due,true);assert.equal(f.loaded.progress.c2.last_attempt_id,102);
  f.$('map-study-due-only').checked=false;await f.fire('map-study-due-only','change');assert.equal(f.$('map-study-front').textContent,'First question');assert.match(f.$('map-study-position').textContent,/Следующее:/);assert.equal(f.loaded.progress.c1.due_at,'2026-11-01T12:00:00Z');
});

test('stale source blocks grading and keyboard shortcuts do not intercept text entry',async()=>{
  const f=fixture();await f.open();
  for(const target of ['map-study-response','map-study-confidence','map-study-hint']){
    for(const [key,code] of [[' ','Space'],['1','Digit1'],['3','Digit3']])assert.equal(await f.key(target,key,code),false);
  }
  assert.equal(f.$('map-study-back').hidden,true);assert.equal(f.reviews().length,0);
  assert.equal(await f.key('map-study-front',' ','Space'),true);assert.equal(f.$('map-study-back').hidden,false);
  await f.key('map-study-front','3','Digit3');assert.equal(f.reviews().length,1);
  f.loaded.progress.c1.current=false;await f.fire('map-study-flip');assert.equal(f.$('map-study-know').disabled,true);await f.fire('map-study-know');assert.equal(f.reviews().length,1);
});

test('answer history cannot expose the answer before the attempt is frozen',async()=>{
  const f=fixture();f.history={items:[{created:'2026-10-09T12:00:00Z',grade:'good',revision:2,matches_current:false,used_hint:true,confidence:60,prompt:'Previous prompt',response:'<script>personal response</script>',answer:'Previous source answer'}],more:true};
  await f.open();assert.equal(f.$('map-study-history').disabled,true);await f.fire('map-study-history');
  assert.equal(f.calls.filter(c=>c.path.endsWith('/study-history')).length,0);
  f.$('map-study-response').value='attempt before history';await f.fire('map-study-flip');
  assert.equal(f.$('map-study-history').disabled,false);await f.fire('map-study-history');
  const call=f.calls.find(c=>c.path.endsWith('/study-history'));assert.deepEqual(call.body,{revision:3,card_id:'c1'});
  const text=e=>[e.textContent,...e.children.map(text)].filter(Boolean).join('\n');
  const history=text(f.$('map-study-history-rows'));
  for(const fragment of ['Previous prompt','<script>personal response</script>','Previous source answer','Прежний материал','60%','20 записей'])assert.ok(history.includes(fragment),fragment);
  for(const e of f.elements.values())assert.equal(e.innerHTML,undefined);
  await f.fire('map-study-know');assert.equal(f.reviews()[0].body.response,'attempt before history');
});

test('closing a pending history request aborts it and ignores late content',async()=>{
  const f=fixture();await f.open();await f.fire('map-study-flip');const wait=deferred();f.waitHistory=wait.promise;
  const pending=f.fire('map-study-history');await settle();const call=f.calls.at(-1);assert.match(call.path,/study-history$/);
  await f.fire('map-workbench-close');assert.equal(call.signal.aborted,true);
  wait.resolve({items:[{created:'2026-10-09',grade:'easy',prompt:'Late history',response:'Late content',answer:'Late answer'}]});await pending;
  assert.equal(f.$('map-study-history-rows').children.length,0);
});
