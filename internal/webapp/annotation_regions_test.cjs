const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const settle=()=>new Promise(resolve=>setImmediate(resolve));
function fixture(){
  const elements=[],calls=[],fetches=[],bitmaps=[];let nonce=0;
  function node(tag,text,css){let own=text===undefined?'':String(text);const classes=new Set();const e={tag,className:css||'',children:[],parentNode:null,events:{},attributes:{},hidden:false,disabled:false,value:'',
    get textContent(){return own+this.children.map(c=>c.textContent).join('');},set textContent(v){own=String(v);this.replaceChildren();},
    append(...children){for(const child of children){this.children.push(child);child.parentNode=this;if(tag==='select'&&!this.value)this.value=child.value;}},replaceChildren(...children){for(const c of this.children)c.parentNode=null;this.children=[];this.append(...children);},
    addEventListener(name,fn){(this.events[name]??=[]).push(fn);},setAttribute(k,v){this.attributes[k]=v;},contains(other){for(let n=other;n;n=n.parentNode)if(n===this)return true;return false;},
    classList:{toggle(name,enabled){if(enabled)classes.add(name);else classes.delete(name);}},
    getContext:()=>({clearRect(){},drawImage(){},strokeRect(){},fillRect(){}}),getBoundingClientRect:()=>({left:0,top:0,width:100,height:200}),setPointerCapture(id){this.pointer=id;},hasPointerCapture(id){return this.pointer===id;},releasePointerCapture(){this.pointer=null;}
  };elements.push(e);return e;}
  const clone=x=>JSON.parse(JSON.stringify(x));
  const f={elements,calls,fetches,bitmaps,state:{selected:'ws',session:{user:'alice'}},items:[],receipts:new Map(),confirm:false,hash:'sha256:'+'a'.repeat(64),imageWidth:100,imageHeight:200,annotation:{id:'ann-'+'d'.repeat(32),revision:3,page:7,state:'current',archived:false}};
  f.region=body=>({id:'area-'+String(f.items.length+1).padStart(32,'0'),annotation_id:f.annotation.id,annotation_revision:body.revision,state:'current',page:f.annotation.page,file_hash:body.file_hash,x:body.x,y:body.y,width:body.width,height:body.height,author:'alice',created:'2026-10-10T00:00:00Z'});
  f.respond=async(path,body)=>{if(body){if(f.receipts.has(body.request_id))return {region:clone(f.receipts.get(body.request_id)),replayed:true};const r=f.region(body);f.items.push(r);f.receipts.set(body.request_id,r);return {region:clone(r),replayed:false};}return {items:clone(f.items)};};
  f.imageResponse=()=>({ok:true,headers:{get:name=>name==='X-Mem-Source-Hash'?f.hash:'7'},blob:async()=>({type:'image/png',size:500})});
  const context=vm.createContext({AbortController,confirm:()=>f.confirm,crypto:{randomUUID:()=>String(++nonce).padStart(32,'0')},fetch:async(url,options)=>{fetches.push({url,options});return f.fetchHandle?f.fetchHandle(url,options):f.imageResponse();},createImageBitmap:async()=>{const bitmap={width:f.imageWidth,height:f.imageHeight,closed:0,close(){this.closed++;}};bitmaps.push(bitmap);return bitmap;}});
  vm.runInContext(fs.readFileSync(__dirname+'/assets/annotation-regions.js','utf8'),context);
  f.module=context.installMemAnnotationRegions({state:f.state,node,run:fn=>fn,api:async(path,body,retry,signal)=>{calls.push({path,body:body===undefined?undefined:clone(body),signal});const result=await(f.handle?f.handle(path,body):f.respond(path,body));if(body&&f.loseReply){f.loseReply=false;throw Error('Response lost');}return result;}});
  f.mount=(options={})=>{const host=node('div');const view=f.module.mount({host,annotation:f.annotation,workspace:'ws',isCurrent:()=>true,...options});return {host,view,canvas:elements.findLast(e=>e.tag==='canvas'&&host.contains(e)),inputs:elements.filter(e=>e.tag==='input'&&host.contains(e))};};
  f.button=(r,text)=>elements.findLast(e=>e.tag==='button'&&e.textContent===text&&r.host.contains(e));
  f.fire=async(e,event='click',data={})=>{assert.ok(e);for(const fn of e.events[event]||[])await fn({preventDefault(){},...data});await settle();};
  f.load=async r=>f.fire(f.button(r,'Показать оригинальную страницу'));f.save=async r=>f.fire(f.button(r,'Сохранить область'));f.retry=async r=>f.fire(f.button(r,'Повторить сохранение'));
  return f;
}

test('PDF regions require verified page hash and bounded bitmap, and numeric bounds prevent invalid writes',async()=>{
  const f=fixture(),r=f.mount();await settle();assert.equal(f.button(r,'Сохранить область').disabled,true);f.hash='wrong';await f.load(r);assert.equal(f.bitmaps.length,0);assert.match(r.host.textContent,/подтвердить редакцию/);
  f.hash='sha256:'+'a'.repeat(64);f.imageWidth=2002;await f.load(r);assert.equal(f.bitmaps[0].closed,1);assert.equal(f.button(r,'Сохранить область').disabled,true);
  f.imageWidth=100;await f.load(r);assert.equal(f.button(r,'Сохранить область').disabled,false);assert.match(f.fetches.at(-1).url,/\/page\?revision=3$/);assert.equal(f.fetches.at(-1).options.cache,'no-store');
  r.inputs[2].value='95';await f.fire(r.inputs[2],'input');assert.equal(f.button(r,'Сохранить область').disabled,true);await f.save(r);assert.equal(f.calls.filter(c=>c.body).length,0);
  r.inputs[2].value='20';await f.fire(r.inputs[2],'input');await f.save(r);const write=f.calls.find(c=>c.body).body;assert.equal(write.revision,3);assert.equal(write.file_hash,f.hash);assert.equal(write.x,.1);assert.equal(write.width,.2);assert.equal(f.items.length,1);
});

test('lost PDF-region response survives close/remount and retries the exact rectangle even when source becomes stale',async()=>{
  const f=fixture(),r=f.mount();await settle();await f.load(r);r.inputs[0].value='25';await f.fire(r.inputs[0],'input');f.loseReply=true;await f.save(r);const original=f.calls.find(c=>c.body).body;assert.equal(f.button(r,'Повторить сохранение').hidden,false);r.view.close();assert.equal(f.calls[0].signal.aborted,true);assert.equal(f.bitmaps[0].closed,1);
  f.annotation={...f.annotation,state:'stale'};const next=f.mount();await settle();assert.equal(f.button(next,'Показать оригинальную страницу').disabled,true);assert.equal(f.button(next,'Повторить сохранение').hidden,false);await f.retry(next);assert.deepEqual(f.calls.filter(c=>c.body).at(-1).body,original);assert.equal(f.receipts.size,1);assert.equal(f.button(next,'Повторить сохранение').hidden,true);
});

test('late initial region list cannot erase the list read after a successful save',async()=>{
  const f=fixture();let resolve,first=true;f.handle=(path,body)=>{if(!body&&first){first=false;return new Promise(r=>resolve=r);}return f.respond(path,body);};
  const r=f.mount();await f.load(r);await f.save(r);assert.match(r.host.textContent,/Область 1/);resolve({items:[]});await settle();assert.match(r.host.textContent,/Область 1/);assert.match(r.host.textContent,/Область сохранена в базе/);
});

test('closed region reader aborts fetch and disposes a bitmap that completes after close',async()=>{
  const f=fixture(),r=f.mount();await settle();let resolve;f.fetchHandle=()=>new Promise(r=>resolve=r);const loading=f.load(r);await settle();r.view.close();assert.equal(f.fetches[0].options.signal.aborted,true);resolve(f.imageResponse());await loading;assert.equal(f.bitmaps[0].closed,1);assert.equal(r.canvas.hidden,true);
});

test('old save acknowledgement cannot discard a newer pending receipt after explicit reset',async()=>{
  const f=fixture(),r=f.mount();await settle();await f.load(r);let resolveOld,writes=0;f.handle=(path,body)=>{if(body){writes++;if(writes===1)return new Promise(resolve=>resolveOld=resolve);if(writes===2)return Promise.reject(Error('second response lost'));}return f.respond(path,body);};
  const saving=f.save(r);await settle();r.view.close();const next=f.mount();await settle();await f.fire(f.button(next,'Новая область'));assert.equal(f.button(next,'Повторить сохранение').hidden,false);f.confirm=true;await f.fire(f.button(next,'Новая область'));await f.load(next);await f.save(next);const newer=f.calls.filter(c=>c.body).at(-1).body;assert.equal(f.button(next,'Повторить сохранение').hidden,false);
  resolveOld({region:f.region(f.calls.find(c=>c.body).body),replayed:false});await saving;next.view.close();const final=f.mount();await settle();assert.equal(f.button(final,'Повторить сохранение').hidden,false);await f.retry(final);assert.deepEqual(f.calls.filter(c=>c.body).at(-1).body,newer);
});

test('region pending receipts are isolated by user and pointer rectangles are normalized',async()=>{
  const f=fixture(),r=f.mount();await settle();await f.load(r);await f.fire(r.canvas,'pointerdown',{button:0,pointerId:1,clientX:70,clientY:160});await f.fire(r.canvas,'pointerup',{pointerId:1,clientX:20,clientY:40});f.loseReply=true;await f.save(r);const body=f.calls.find(c=>c.body).body;assert.equal(body.x,.2);assert.equal(body.y,.2);assert.ok(Math.abs(body.width-.5)<1e-10);assert.ok(Math.abs(body.height-.6)<1e-10);r.view.close();
  f.state.session.user='bob';const next=f.mount();await settle();assert.equal(f.button(next,'Повторить сохранение').hidden,true);await f.retry(next);assert.equal(f.calls.filter(c=>c.body).length,1);
});

test('malformed or mismatched PDF-region acknowledgements preserve the exact receipt across remount and confirm by replay',async()=>{
  const changeRegion=change=>result=>({...result,region:{...result.region,...change}});
  const malformed=[()=>({}),()=>({region:{},replayed:false}),changeRegion({id:'area-invalid'}),changeRegion({annotation_id:'ann-'+'e'.repeat(32)}),changeRegion({annotation_revision:4}),changeRegion({page:8}),changeRegion({file_hash:'sha256:'+'b'.repeat(64)}),...['x','y','width','height'].map(k=>changeRegion({[k]:1.1})),changeRegion({x:null}),result=>({...result,replayed:'false'}),result=>({region:result.region})];
  for(const corrupt of malformed){const f=fixture(),r=f.mount();await settle();await f.load(r);f.handle=async(path,body)=>{const result=await f.respond(path,body);return body?corrupt(result):result;};await f.save(r);const original=f.calls.find(c=>c.body).body;assert.equal(f.receipts.size,1);assert.match(r.host.textContent,/Некорректное подтверждение области/);assert.equal(f.button(r,'Повторить сохранение').hidden,false);assert.equal(f.button(r,'Сохранить область').disabled,true);
    r.view.close();f.handle=(path,body)=>f.respond(path,body);const next=f.mount();await settle();assert.equal(f.button(next,'Повторить сохранение').hidden,false);await f.retry(next);assert.deepEqual(f.calls.filter(c=>c.body).at(-1).body,original);assert.equal(f.receipts.size,1);assert.equal(f.items.length,1);assert.equal(f.button(next,'Повторить сохранение').hidden,true);assert.match(next.host.textContent,/Область сохранена в базе/);
  }
});
