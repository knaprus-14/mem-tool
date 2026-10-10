const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const settle=()=>new Promise(resolve=>setImmediate(resolve));
function fixture(){
  const elements=[],calls=[],urls=[],revoked=[];let nonce=0;
  function node(tag,text,css){let own=text===undefined?'':String(text);const e={tag,className:css||'',children:[],events:{},attrs:{},parentNode:null,hidden:false,disabled:false,open:false,value:'',
    get textContent(){return own+this.children.map(c=>c.textContent).join('');},set textContent(v){own=String(v);this.replaceChildren();},
    append(...children){for(const child of children){child.parentNode=this;this.children.push(child);if(tag==='select'&&!this.value)this.value=child.value;}},
    replaceChildren(...children){for(const child of this.children)child.parentNode=null;this.children=[];this.append(...children);},
    addEventListener(name,fn){(this.events[name]??=[]).push(fn);},setAttribute(k,v){this.attrs[k]=v;},removeAttribute(k){delete this.attrs[k];delete this[k];},
    showModal(){this.open=true;},close(){this.open=false;for(const fn of this.events.close||[])fn();}
  };elements.push(e);return e;}
  const clone=x=>JSON.parse(JSON.stringify(x));
  const f={elements,calls,urls,revoked,receipts:new Map(),state:{selected:'workspace',revision:1,session:{user:'alice'}},confirm:true,
    preview:{digest:'sha256:'+'a'.repeat(64),count:2,current:1,stale:1,missing:0,archived:1,region_count:3,items:[{origin_id:'origin1',source_name:'One.pdf',quote:'<script>literal</script>',note:'Note',state:'current',archived:false},{origin_id:'origin2',source_name:'Two.pdf',quote:'Old source',note:'',state:'stale',archived:true,reason:'Source changed'}],warnings:['<b>Literal warning</b>']}
  };
  f.respond=async(path,body)=>{
    if(path.endsWith('/export'))return {filename:body.format==='json'?'annotations.json':'annotations.md',media_type:body.format==='json'?'application/json':'text/markdown',content:body.format==='json'?'\uFEFF{"literal":"<html>"}':'# Annotation',count:body.ids.length,digest:'sha256:'+'b'.repeat(64)};
    if(path.endsWith('/import-preview'))return clone(f.preview);
    if(path.endsWith('/import')){if(f.receipts.has(body.request_id))return {...clone(f.receipts.get(body.request_id)),replayed:true};const result={items:[{origin_id:'origin1',id:'new1',revision:1},{origin_id:'origin2',id:'new2',revision:1}],imported:2,current:1,stale:1,missing:0,replayed:false};f.receipts.set(body.request_id,result);return clone(result);}
    throw Error('Unexpected API '+path);
  };
  const context=vm.createContext({document:{body:node('body')},window:{confirm:()=>f.confirm},AbortController,TextEncoder,Blob,URL:{createObjectURL:blob=>{const url='blob:test-'+urls.length;urls.push({url,blob});return url;},revokeObjectURL:url=>revoked.push(url)},crypto:{randomUUID:()=>String(++nonce).padStart(32,'0')}});
  vm.runInContext(fs.readFileSync(__dirname+'/assets/annotation-transfer.js','utf8'),context);
  f.module=context.installMemAnnotationTransfer({state:f.state,node,run:fn=>fn,api:async(path,body,retry,signal)=>{calls.push({path,body:clone(body),signal});const result=await(f.handle?f.handle(path,body,signal):f.respond(path,body));if(path.endsWith('/import')&&f.loseReply){f.loseReply=false;throw Error('Response lost');}return result;}});
  f.$=id=>elements.find(e=>e.id===id);f.fire=async(id,event='click')=>{const e=typeof id==='string'?f.$(id):id;assert.ok(e,'Missing '+id);for(const fn of e.events[event]||[])await fn({preventDefault(){}});await settle();};
  f.choose=async(content='{"annotations":[]}',options={})=>{f.$('annotation-transfer-file').files=[{name:options.name||'portable.json',size:options.size??new TextEncoder().encode(content).length,text:options.text||(()=>Promise.resolve(content))}];await f.fire('annotation-transfer-file','change');};
  f.ready=async(options={})=>{f.module.showImport({workspace:'workspace',...options});await f.choose();await f.fire('annotation-transfer-preview');};
  return f;
}

test('export sends only explicitly selected IDs and manages JSON/Markdown download URLs',async()=>{
  const f=fixture();f.module.showExport({workspace:'workspace',ids:['a','b','a']});await f.fire('annotation-transfer-export');assert.deepEqual(f.calls[0].body,{ids:['a','b'],format:'json'});assert.equal(f.$('annotation-transfer-download').hidden,false);assert.equal(f.$('annotation-transfer-download').download,'annotations.json');assert.match(f.$('annotation-transfer-status').textContent,/Подготовлено аннотаций: 2/);
  assert.deepEqual(Buffer.from(await f.urls[0].blob.arrayBuffer()),Buffer.from('\uFEFF{"literal":"<html>"}'));f.$('annotation-transfer-format').value='markdown';await f.fire('annotation-transfer-format','change');assert.deepEqual(f.revoked,['blob:test-0']);assert.equal(f.$('annotation-transfer-download').hidden,true);
  await f.fire('annotation-transfer-export');assert.equal(f.calls[1].body.format,'markdown');assert.equal(f.$('annotation-transfer-download').download,'annotations.md');assert.match(f.elements.map(e=>e.textContent).join(' '),/Markdown предназначен для чтения и не импортируется обратно/);f.module.close();assert.deepEqual(f.revoked,['blob:test-0','blob:test-1']);
});

test('export refuses absent or more than 200 IDs and ignores late results after close',async()=>{
  const f=fixture();f.module.showExport({ids:[]});await f.fire('annotation-transfer-export');assert.equal(f.calls.length,0);f.module.showExport({ids:Array.from({length:201},(_,i)=>String(i))});await f.fire('annotation-transfer-export');assert.equal(f.calls.length,0);
  let resolve;f.handle=()=>new Promise(r=>resolve=r);f.module.showExport({ids:['a']});const pending=f.fire('annotation-transfer-export');await settle();f.module.close();assert.equal(f.calls[0].signal.aborted,true);resolve({filename:'late.json',content:'{}',count:1});await pending;assert.equal(f.urls.length,0);
});

test('import previews literal records, counts and warnings and requires an explicit apply',async()=>{
  const f=fixture();let notified=0;await f.ready({onImported:()=>notified++});assert.equal(f.calls.length,1);assert.match(f.calls[0].path,/\/import-preview$/);assert.match(f.$('annotation-transfer-summary').textContent,/Актуальных: 1; изменившихся источников: 1; недоступных: 0/);assert.match(f.$('annotation-transfer-summary').textContent,/Архивных: 1. Областей PDF: 3/);assert.match(f.$('annotation-transfer-summary').textContent,/<script>literal<\/script>/);assert.match(f.$('annotation-transfer-summary').textContent,/<b>Literal warning<\/b>/);assert.equal(f.$('annotation-transfer-summary').innerHTML,undefined);
  await f.fire('annotation-transfer-apply');assert.equal(f.calls.length,2);assert.equal(f.calls[1].body.content,f.calls[0].body.content);assert.equal(f.calls[1].body.digest,f.preview.digest);assert.match(f.calls[1].body.request_id,/^[0-9]{32}$/);assert.equal(notified,1);assert.equal(f.$('annotation-transfer-apply').disabled,true);await f.fire('annotation-transfer-apply');assert.equal(f.calls.length,2);
});

test('uncertain import retains the exact receipt across close/reopen and replays without duplicates',async()=>{
  const f=fixture();await f.ready();f.loseReply=true;await f.fire('annotation-transfer-apply');assert.equal(f.$('annotation-transfer-retry').hidden,false);assert.equal(f.$('annotation-transfer-file').disabled,true);const original=f.calls[1].body;
  f.module.close();f.module.showImport({workspace:'workspace'});assert.match(f.$('annotation-transfer-status').textContent,/Ответ на импорт не подтверждён/);assert.equal(f.$('annotation-transfer-file').disabled,true);await f.choose('different');assert.equal(f.calls.length,2);
  await f.fire('annotation-transfer-retry');assert.deepEqual(f.calls[2].body,original);assert.equal(f.receipts.size,1);assert.match(f.$('annotation-transfer-status').textContent,/Повторных записей не добавлено/);
});

test('file limits are enforced using bytes before reading and after decoding; empty files cannot preview',async()=>{
  const f=fixture();f.module.showImport({});let reads=0;await f.choose('',{size:8*1024*1024+1,text:async()=>{reads++;return '{}';}});assert.equal(reads,0);assert.match(f.$('annotation-transfer-error').textContent,/8 МиБ/);assert.equal(f.$('annotation-transfer-preview').disabled,true);
  await f.choose('😀'.repeat(2*1024*1024+1),{size:1});assert.match(f.$('annotation-transfer-error').textContent,/8 МиБ/);await f.choose(' \n ');assert.match(f.$('annotation-transfer-error').textContent,/Файл пуст/);await f.fire('annotation-transfer-preview');assert.equal(f.calls.length,0);
});

test('late file reads and previews cannot replace a newly selected file or a different workspace',async()=>{
  const f=fixture();f.module.showImport({});let resolveFile;const choosing=f.choose('',{text:()=>new Promise(r=>resolveFile=r)});await settle();f.module.close();f.module.showImport({});await f.choose('new content',{name:'new.json'});resolveFile('old content');await choosing;await f.fire('annotation-transfer-preview');assert.equal(f.calls[0].body.content,'new content');
  let resolvePreview;f.handle=()=>new Promise(r=>resolvePreview=r);const checking=f.fire('annotation-transfer-preview');await settle();const call=f.calls.at(-1);f.module.close();assert.equal(call.signal.aborted,true);f.module.showImport({});await f.choose('third');resolvePreview(f.preview);await checking;assert.equal(f.$('annotation-transfer-apply').disabled,true);assert.equal(f.$('annotation-transfer-summary').children.length,0);
  f.state.selected='other';await f.fire('annotation-transfer-preview');assert.equal(f.calls.length,2);
});

test('import drafts are isolated by user and workspace; reset of an uncertain receipt requires confirmation',async()=>{
  const f=fixture();await f.ready();f.loseReply=true;await f.fire('annotation-transfer-apply');f.confirm=false;await f.fire('annotation-transfer-reset');assert.equal(f.$('annotation-transfer-file').disabled,true);
  f.module.close();f.state.session.user='bob';f.module.showImport({});assert.equal(f.$('annotation-transfer-file').disabled,false);assert.equal(f.$('annotation-transfer-summary').children.length,0);
  f.module.close();f.state.session.user='alice';f.state.selected='second';f.module.showImport({workspace:'second'});assert.equal(f.$('annotation-transfer-file').disabled,false);
  f.module.close();f.state.selected='workspace';f.module.showImport({});assert.equal(f.$('annotation-transfer-file').disabled,true);f.confirm=true;await f.fire('annotation-transfer-reset');assert.equal(f.$('annotation-transfer-file').disabled,false);assert.equal(f.$('annotation-transfer-summary').children.length,0);
});

test('an inflight import remains frozen on reopen and a late acknowledgement is not applied to another workspace',async()=>{
  const f=fixture();let notified=0;await f.ready({onImported:()=>notified++});let resolve;f.handle=(path,body)=>path.endsWith('/import')?new Promise(r=>resolve=r):f.respond(path,body);const importing=f.fire('annotation-transfer-apply');await settle();f.module.close();f.module.showImport({});assert.equal(f.$('annotation-transfer-file').disabled,true);assert.equal(f.$('annotation-transfer-retry').disabled,true);await f.fire('annotation-transfer-retry');assert.equal(f.calls.length,2);
  f.state.selected='other';resolve({items:[{id:'n1',revision:1},{id:'n2',revision:1}],imported:2,current:1,stale:1,missing:0,replayed:false});await importing;assert.equal(notified,0);f.module.close();f.state.selected='workspace';f.module.showImport({});assert.match(f.$('annotation-transfer-status').textContent,/Этот импорт уже сохранён/);assert.equal(f.$('annotation-transfer-apply').disabled,true);
});

test('failed preview and malformed digest cannot enable import; list refresh failure does not replay a saved import',async()=>{
  const f=fixture();f.module.showImport({onImported:()=>{throw Error('list offline');}});await f.choose();f.handle=()=>Promise.reject(Error('preview offline'));await f.fire('annotation-transfer-preview');assert.equal(f.$('annotation-transfer-apply').disabled,true);assert.equal(f.$('annotation-transfer-preview').disabled,false);
  f.handle=()=>Promise.resolve({...f.preview,digest:'not-a-digest'});await f.fire('annotation-transfer-preview');assert.equal(f.$('annotation-transfer-apply').disabled,true);f.handle=(path,body)=>f.respond(path,body);await f.fire('annotation-transfer-preview');await f.fire('annotation-transfer-apply');assert.match(f.$('annotation-transfer-error').textContent,/Импорт сохранён, но список не обновлён/);assert.equal(f.$('annotation-transfer-retry').hidden,true);assert.equal(f.receipts.size,1);
});

test('reopening during import updates the new callback once and malformed acknowledgements preserve the receipt',async()=>{
  const f=fixture();let old=0,fresh=0;await f.ready({onImported:()=>old++});let resolve;f.handle=(path,body)=>path.endsWith('/import')?new Promise(r=>resolve=r):f.respond(path,body);const pending=f.fire('annotation-transfer-apply');await settle();f.module.close();f.module.showImport({onImported:()=>fresh++});resolve({items:[{id:'n1',revision:1},{id:'n2',revision:1}],imported:2,current:1,stale:1,missing:0,replayed:false});await pending;assert.equal(old,0);assert.equal(fresh,1);assert.equal(f.$('annotation-transfer-retry').hidden,true);
  await f.fire('annotation-transfer-reset');f.handle=(path,body)=>path.endsWith('/import')?Promise.resolve({imported:2}):f.respond(path,body);await f.choose();await f.fire('annotation-transfer-preview');await f.fire('annotation-transfer-apply');assert.equal(f.$('annotation-transfer-retry').hidden,false);assert.match(f.$('annotation-transfer-error').textContent,/Некорректный ответ/);const receipt=f.calls.at(-1).body;f.handle=(path,body)=>f.respond(path,body);await f.fire('annotation-transfer-retry');assert.deepEqual(f.calls.at(-1).body,receipt);assert.equal(fresh,2);
});
