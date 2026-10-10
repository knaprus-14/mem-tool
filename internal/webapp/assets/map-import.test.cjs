'use strict';
const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const digest='sha256:'+'a'.repeat(64),tick=()=>new Promise(setImmediate);
const nativePreview=extra=>({title:'ok',digest,node_count:1,source_count:0,current_sources:0,stale_sources:0,missing_sources:0,warnings:[],...extra});
const outlinePreview=(body,count=1)=>({title:'External',digest,format:body.format,node_count:count,source_count:0,warnings:['Сначала проверьте структуру.'],source_sha256:'sha256:'+'b'.repeat(64),source_bytes:Buffer.from(body.content_base64,'base64').length,nodes:Array.from({length:count},(_,i)=>({ref:'n'+i,parent_ref:i?'n0':'',label:i?'Node '+i:'Root',depth:i?1:0}))});

function fixture(api){
  const ids=new Map(),calls=[];let nonce=0;
  class Element {
    constructor(tag,text){this.tag=tag;this.own=text??'';this.listeners={};this.children=[];this.files=[];this.disabled=false;this.open=false;this.attrs={};this._value='';}
    get textContent(){return this.own+this.children.map(c=>c.textContent).join('');}set textContent(text){this.own=text;this.children=[];}
    set value(v){this._value=v;if(this.type==='file'&&!v)this.files=[];}get value(){return this._value;}
    set id(value){this._id=value;ids.set(value,this);}get id(){return this._id;}
    setAttribute(k,v){this.attrs[k]=String(v);}
    append(...items){this.children.push(...items);}
    replaceChildren(...items){this.children=[...items];}
    addEventListener(name,fn){this.listeners[name]=fn;}
    showModal(){this.open=true;}
    close(){this.open=false;this.listeners.close?.();}
    async event(name){await this.listeners[name]?.({preventDefault(){}});}
  }
  const node=(tag,text)=>new Element(tag,text);
  const button=node('button');button.id='import-map';
  const document={body:node('body')};
  const state={selected:'ws-one',revision:1,session:{authenticated:true,user:'alice',csrf:'token'}};
  const f={confirm:false,handle:api|| (async(route,body)=>route.endsWith('/outline-preview')?outlinePreview(body):route.endsWith('/preview')?nativePreview():{id:'new-map',replayed:false})};
  const shown=[];const context={document,TextDecoder,AbortController,btoa:raw=>Buffer.from(raw,'binary').toString('base64'),confirm:()=>f.confirm,crypto:{randomUUID:()=> (++nonce).toString(16).padStart(32,'0')}};
  vm.createContext(context);vm.runInContext(fs.readFileSync(path.join(__dirname,'map-import.js'),'utf8'),context);
  const module=context.installMemMapImport({state,$:id=>ids.get(id),node,api:(route,body,retried,signal)=>{calls.push({route,body,signal});return f.handle(route,body,retried,signal);},run:fn=>fn,showMap:async id=>shown.push(id)});
  const click=id=>ids.get(id).event('click');
  const choose=async(text,size=Buffer.byteLength(text),name='map.json')=>{ids.get('map-import-file').files=[{name,size,arrayBuffer:async()=>Buffer.from(text)}];await ids.get('map-import-file').event('change');};
  return Object.assign(f,{state,ids,click,choose,shown,calls,module,format:async value=>{ids.get('map-import-format').value=value;await ids.get('map-import-format').event('change');},rows:()=>ids.get('map-import-preview').children.find(e=>e.tag==='ol')?.children||[]});
}

test('preview preserves exact JSON and apply retries the same confirmed input',async()=>{
  const calls=[];let failed=false;
  const f=fixture(async(route,body)=>{calls.push({route,body});if(route.endsWith('/preview'))return nativePreview({title:'<script>bad()</script>',node_count:2,source_count:1,current_sources:0,stale_sources:1,missing_sources:0,warnings:['Устаревший источник']});if(!failed){failed=true;throw new Error('offline');}return{id:'new-map',replayed:true};});
  await f.click('import-map');const content=' {"export_version":1,"export_version":1} ';
  await f.choose(content);assert.equal(f.ids.get('map-import-apply').disabled,true);
  await f.click('map-import-check');assert.equal(calls[0].body.content,content);assert.equal(f.ids.get('map-import-preview').children[0].textContent,'<script>bad()</script>');
  assert.equal(calls.length,1);await f.click('map-import-apply');assert.equal(f.ids.get('map-import-file').disabled,true);assert.equal(f.ids.get('map-import-check').disabled,true);assert.equal(f.ids.get('map-import-apply').disabled,false);
  await f.click('map-import-apply');assert.equal(calls.length,3);assert.deepEqual(calls[1].body,calls[2].body);assert.equal(calls[1].body.content,content);assert.match(calls[1].body.request_id,/^[a-f0-9]{32}$/);assert.deepEqual(f.shown,['new-map']);
});

test('closing or switching workspace invalidates late previews and publications',async()=>{
  let finish;const f=fixture(()=>new Promise(resolve=>{finish=resolve;}));
  await f.click('import-map');await f.choose('{}');const pending=f.click('map-import-check');await new Promise(setImmediate);
  f.state.selected='ws-two';finish({title:'Old',digest:'old',node_count:1,source_count:0});await pending;
  assert.equal(f.ids.get('map-import-preview').children.length,0);await f.click('map-import-apply');assert.deepEqual(f.shown,[]);
  await f.click('map-import-close');await f.click('import-map');await f.choose('{}');const pending2=f.click('map-import-check');await new Promise(setImmediate);await f.click('map-import-close');finish({title:'Closed',digest:'old'});await pending2;assert.equal(f.ids.get('map-import-preview').children.length,0);
});

test('size and invalid UTF-8 fail before the API and replacing file clears preview',async()=>{
  let calls=0;const f=fixture(async()=>{calls++;return nativePreview();});
  await f.click('import-map');await f.choose('{}',8*1024*1024+1);await f.click('map-import-check');assert.equal(calls,0);assert.match(f.ids.get('map-import-error').textContent,/8 МиБ/);
  f.ids.get('map-import-file').files=[{size:1,arrayBuffer:async()=>Buffer.from([0xff])}];await f.click('map-import-check');assert.equal(calls,0);
  await f.choose('{}');await f.click('map-import-check');assert.equal(calls,1);assert.equal(f.ids.get('map-import-apply').disabled,false);
  await f.choose('{"changed":true}');assert.equal(f.ids.get('map-import-apply').disabled,true);assert.equal(f.ids.get('map-import-preview').children.length,0);
});

test('all external formats preserve original bytes and filename, with explicit preview before a new copy',async()=>{
  for(const [extension,format] of [['opml','opml'],['mm','freemind'],['xmind','xmind'],['md','markdown']]){
    const f=fixture(),bytes=extension==='xmind'?Buffer.from([0x50,0x4b,0,255,128,1,2]):Buffer.from('\ufeff<literal> 😀\r\n');await f.click('import-map');await f.choose(bytes,bytes.length,'C:\\private\\input.'+extension);
    assert.equal(f.ids.get('map-import-format').value,format);await f.click('map-import-check');assert.equal(f.calls.length,1);const p=f.calls[0];assert.equal(p.route,'workspaces/ws-one/maps/import/outline-preview');assert.equal(p.body.filename,'input.'+extension);assert.equal(p.body.format,format);assert.deepEqual(Buffer.from(p.body.content_base64,'base64'),bytes);assert.equal('content' in p.body,false);assert.match(f.ids.get('map-import-preview').textContent,/Сначала проверьте структуру/);assert.equal(f.shown.length,0);
    await f.click('map-import-apply');const apply=f.calls[1];assert.equal(apply.route,'workspaces/ws-one/maps/import/outline-apply');assert.equal(apply.body.content_base64,p.body.content_base64);assert.equal(apply.body.filename,p.body.filename);assert.equal(apply.body.digest,digest);assert.match(apply.body.request_id,/^[a-f0-9]{32}$/);assert.deepEqual(f.shown,['new-map']);
  }
});

test('base64 encoding is exact across chunk boundaries up to the eight MiB binary limit',async()=>{
  const f=fixture(),bytes=Buffer.alloc(8*1024*1024);for(let i=0;i<bytes.length;i++)bytes[i]=i%251;await f.click('import-map');await f.choose(bytes,bytes.length,'archive.xmind');await f.click('map-import-check');assert.equal(f.calls.length,1);assert.deepEqual(Buffer.from(f.calls[0].body.content_base64,'base64'),bytes);assert.equal(f.ids.get('map-import-apply').disabled,false);
  const oversized=fixture();await oversized.click('import-map');await oversized.choose('x',8*1024*1024+1,'archive.xmind');await oversized.click('map-import-check');assert.equal(oversized.calls.length,0);
});

test('manual format override invalidates preview and uses the explicit canonical choice',async()=>{
  const f=fixture();await f.click('import-map');await f.choose('# Root',6,'input.md');await f.click('map-import-check');assert.equal(f.ids.get('map-import-apply').disabled,false);await f.format('opml');assert.equal(f.ids.get('map-import-apply').disabled,true);assert.equal(f.ids.get('map-import-preview').children.length,0);await f.click('map-import-check');assert.equal(f.calls.at(-1).body.format,'opml');assert.equal(f.calls.at(-1).body.filename,'input.md');
});

test('large hierarchical previews render only 100 accessible rows per page and warnings as literal text',async()=>{
  const f=fixture(async(route,body)=>{const p=outlinePreview(body,1000);p.nodes[0].label='<script>literal</script>';p.warnings=['<img src=x onerror=alert(1)>'];return p;});await f.click('import-map');await f.choose('outline',7,'input.opml');await f.click('map-import-check');assert.equal(f.rows().length,100);assert.match(f.rows()[0].textContent,/<script>literal<\/script>/);assert.match(f.rows()[0].textContent,/уровень 0; корень/);assert.match(f.rows()[1].textContent,/уровень 1; родитель n0/);assert.equal(f.ids.get('map-import-previous').disabled,true);
  await f.click('map-import-next');assert.equal(f.rows().length,100);assert.match(f.rows()[0].textContent,/Node 100/);for(let i=0;i<8;i++)await f.click('map-import-next');assert.match(f.rows()[0].textContent,/Node 900/);assert.equal(f.ids.get('map-import-next').disabled,true);assert.equal(f.calls.length,1);await f.click('map-import-previous');assert.match(f.rows()[0].textContent,/Node 800/);
});

test('invalid or inconsistent outline previews never enable apply',async()=>{
  const corruptions=[p=>({...p,format:'xmind'}),p=>({...p,source_bytes:999}),p=>({...p,digest:'bad'}),p=>({...p,source_sha256:'bad'}),p=>({...p,node_count:1001}),p=>({...p,node_count:2}),p=>({...p,warnings:[{}]}),p=>({...p,nodes:[{...p.nodes[0],depth:1}]}),p=>({...p,nodes:[{...p.nodes[0],parent_ref:'missing'}]}),p=>({...p,node_count:2,nodes:[p.nodes[0],p.nodes[0]]})];
  for(const corrupt of corruptions){const f=fixture(async(route,body)=>corrupt(outlinePreview(body)));await f.click('import-map');await f.choose('file',4,'file.opml');await f.click('map-import-check');assert.match(f.ids.get('map-import-error').textContent,/Некорректный результат/);assert.equal(f.ids.get('map-import-apply').disabled,true);await f.click('map-import-apply');assert.equal(f.calls.length,1);}
});

test('uncertain imports survive close, lock changed files and formats, and retry exactly the original operation',async()=>{
  const f=fixture();f.handle=async(route,body)=>route.endsWith('outline-preview')?outlinePreview(body):Promise.reject(Error('413: lost reply'));await f.click('import-map');await f.choose('old',3,'old.opml');await f.click('map-import-check');await f.click('map-import-apply');const old=f.calls.at(-1);f.module.close();assert.equal(f.ids.get('map-import-preview').children.length,0);await f.click('import-map');assert.equal(f.ids.get('map-import-format').disabled,true);assert.equal(f.ids.get('map-import-file').disabled,true);await f.choose('new',3,'new.md');await f.format('markdown');await f.click('map-import-check');assert.equal(f.calls.length,2);
  f.handle=async()=>({id:'created-on-first-attempt',replayed:true});await f.click('map-import-apply');assert.deepEqual(f.calls.at(-1).body,old.body);assert.equal(f.calls.at(-1).route,old.route);assert.deepEqual(f.shown,['created-on-first-attempt']);
});

test('discard requires explicit confirmation and a changed payload gets a new nonce',async()=>{
  const f=fixture();f.handle=async(route,body)=>route.endsWith('outline-preview')?outlinePreview(body):Promise.reject(Error('409: stale preview'));await f.click('import-map');await f.choose('old',3,'old.opml');await f.click('map-import-check');await f.click('map-import-apply');const old=f.calls.at(-1).body;await f.click('map-import-reset');assert.equal(f.ids.get('map-import-file').disabled,true);f.confirm=true;await f.click('map-import-reset');assert.equal(f.ids.get('map-import-file').disabled,false);assert.equal(f.ids.get('map-import-apply').disabled,true);await f.choose('new',3,'new.md');await f.click('map-import-check');await f.click('map-import-apply');assert.notEqual(f.calls.at(-1).body.request_id,old.request_id);assert.equal(f.calls.at(-1).body.format,'markdown');
});

test('late file loads and previews are isolated by close, workspace, screen, user, CSRF and authentication',async()=>{
  const changes=[f=>f.module.close(),f=>f.state.selected='other',f=>f.state.revision++,f=>f.state.session.user='bob',f=>f.state.session.csrf='new',f=>f.state.session.authenticated=false];
  for(const change of changes){const f=fixture();let loaded;await f.click('import-map');await f.choose('secret',6,'secret.opml');f.ids.get('map-import-file').files[0].arrayBuffer=()=>new Promise(r=>loaded=r);const checking=f.click('map-import-check');await tick();change(f);loaded(Buffer.from('secret'));await checking;assert.equal(f.calls.length,0);assert.equal(f.ids.get('map-import-preview').children.length,0);}
  for(const change of changes){const f=fixture();let done;f.handle=(route,body)=>new Promise(r=>done=()=>r(outlinePreview(body)));await f.click('import-map');await f.choose('secret',6,'secret.opml');const checking=f.click('map-import-check');await tick();change(f);done();await checking;assert.equal(f.ids.get('map-import-preview').children.length,0);await f.click('map-import-apply');assert.equal(f.calls.length,1);}
});

test('late successful apply never navigates from a closed or superseded context and reopen offers explicit opening',async()=>{
  const f=fixture();let done;f.handle=(route,body)=>route.endsWith('outline-preview')?outlinePreview(body):new Promise(r=>done=r);await f.click('import-map');await f.choose('outline',7,'input.opml');await f.click('map-import-check');const applying=f.click('map-import-apply');await tick();f.module.close();await f.click('import-map');assert.equal(f.ids.get('map-import-apply').disabled,true);done({id:'completed-late',replayed:false});await applying;assert.deepEqual(f.shown,[]);assert.match(f.ids.get('map-import-status').textContent,/Карта создана/);assert.equal(f.ids.get('map-import-apply').disabled,true);await f.click('map-import-open');assert.deepEqual(f.shown,['completed-late']);assert.equal(f.calls.length,2);
  for(const change of [f=>f.state.selected='other',f=>f.state.revision++,f=>f.state.session.user='bob',f=>f.state.session.csrf='changed',f=>f.state.session.authenticated=false]){const f=fixture();let done;f.handle=(route,body)=>route.endsWith('outline-preview')?outlinePreview(body):new Promise(r=>done=r);await f.click('import-map');await f.choose('outline',7,'input.opml');await f.click('map-import-check');const applying=f.click('map-import-apply');await tick();change(f);done({id:'old-map',replayed:false});await applying;assert.deepEqual(f.shown,[]);}
});

test('malformed successful ACKs keep the exact receipt available after close',async()=>{
  for(const ack of [{},{id:'map'},{id:'',replayed:false},{id:'../private',replayed:false},{id:'map',replayed:'yes'}]){const f=fixture();f.handle=(route,body)=>route.endsWith('outline-preview')?outlinePreview(body):ack;await f.click('import-map');await f.choose('outline',7,'input.opml');await f.click('map-import-check');await f.click('map-import-apply');const original=f.calls.at(-1).body;assert.match(f.ids.get('map-import-error').textContent,/не подтвердил/);assert.deepEqual(f.shown,[]);f.module.close();await f.click('import-map');f.handle=async()=>({id:'confirmed',replayed:true});await f.click('map-import-apply');assert.deepEqual(f.calls.at(-1).body,original);assert.deepEqual(f.shown,['confirmed']);}
});

test('pending contexts have a hard cap, session purge, and confirmed results do not consume the cap',async()=>{
  const f=fixture();f.handle=(route,body)=>route.endsWith('outline-preview')?outlinePreview(body):Promise.reject(Error('offline'));for(let i=0;i<8;i++){f.state.selected='pending-'+i;await f.click('import-map');await f.choose('file',4,'input.opml');await f.click('map-import-check');await f.click('map-import-apply');f.module.close();}const first=f.calls[1];f.state.selected='overflow';await assert.rejects(f.click('import-map'),/8 неподтверждённых импортов/);f.state.selected='pending-0';await f.click('import-map');await f.click('map-import-apply');assert.deepEqual(f.calls.at(-1).body,first.body);
  f.state.session.csrf='new-session';f.state.selected='overflow';await f.click('import-map');assert.equal(f.ids.get('map-import-file').disabled,false);f.state.session.csrf='token';f.state.selected='pending-0';await f.click('import-map');assert.equal(f.ids.get('map-import-file').disabled,false,'old session pending is permanently purged');
  f.handle=(route,body)=>route.endsWith('outline-preview')?outlinePreview(body):{id:'confirmed',replayed:false};for(let i=0;i<12;i++){f.state.selected='confirmed-'+i;await f.click('import-map');await f.choose('file',4,'input.opml');await f.click('map-import-check');await f.click('map-import-apply');}assert.equal(f.shown.length,12);
});
