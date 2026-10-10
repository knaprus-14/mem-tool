const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
function fixture(withAnnotations=false){
 const elements=new Map();
 function element(tag,text,cls){return {tag,textContent:text,className:cls,children:[],events:{},value:'',hidden:false,open:false,addEventListener(k,fn){this.events[k]=fn;},append(...rows){this.children.push(...rows);if(this.id==='map-document-picker'&&!this.value&&rows.length)this.value=rows[0].value;},replaceChildren(){this.children=[];this.value='';},removeAttribute(k){delete this[k];},showModal(){this.open=true;},close(){this.open=false;},scrollIntoView(){this.scrolled=true;}};}
 function $(id){if(!elements.has(id)){const e=element('div');e.id=id;elements.set(id,e);}return elements.get(id);}
 const n={id:'n',label:'Selected <topic>',summary:'Summary',body:'<script>literal text</script>'};
 const f={$,calls:[],signals:[],mounts:[],pending:[],state:{session:{authenticated:true,user:'alice',csrf:'a'},selected:'ws',revision:1,map:{selectedNode:'n',view:{map:{id:'map',revision:2}},tree:{byID:new Map([['n',n]])}}}};
 const context=vm.createContext({AbortController});vm.runInContext(fs.readFileSync(__dirname+'/assets/map-document.js','utf8'),context);
 f.document=context.installMemMapDocument({state:f.state,$,node:element,run:fn=>fn,navigation:()=>f.navigation,annotations:withAnnotations?{mountReader:options=>{const reader={...options,closed:false,close(){this.closed=true;}};f.mounts.push(reader);return reader;}}:undefined,api:(path,body,retry,signal)=>{assert.equal(body,undefined);f.calls.push(path);f.signals.push(signal);return new Promise((resolve,reject)=>f.pending.push({resolve,reject}));}});
 f.fire=(id,event='click')=>$(id).events[event]();f.resolve=async data=>{f.pending.shift().resolve(data);await Promise.resolve();await Promise.resolve();};return f;
}
const sources={revision:2,items:[{id:'s1',kind:'evidence',title:'One',page:4},{id:'s2',kind:'evidence',title:'Two',page:8}]};
const document={title:'One.pdf',page:4,passages:[{text:'Before',match:false},{text:'<b>Evidence</b>',match:true}],original_pdf:true};
test('comparison preserves literal node text, highlights evidence and opens the physical PDF page',async()=>{
 const f=fixture();const open=f.fire('map-document-open');await f.resolve(sources);await f.resolve(document);await open;
 assert.equal(f.$('map-document-node-body').textContent,'<script>literal text</script>');
 const rows=f.$('map-document-passages').children;assert.equal(rows[1].className,'map-document-match');assert.equal(rows[1].children.at(-1).textContent,'<b>Evidence</b>');assert.equal(rows[1].scrolled,true);
 assert.equal(f.$('map-document-original').href,'/api/v1/workspaces/ws/maps/map/nodes/n/sources/s1/file?revision=2#page=4');
});

const reference={map_id:'map',node_id:'n',map_revision:2,source_id:'s2',scroll:.25,passages_scroll:.5};
test('history restores the exact second source and relative scroll; captured history contains metadata only',async()=>{
 const f=fixture();for(const id of ['map-document-dialog','map-document-passages'])Object.assign(f.$(id),{scrollTop:0,scrollHeight:1000,clientHeight:200});
 const open=f.document.open(reference,{track:false});await f.resolve(sources);assert.match(f.calls.at(-1),/sources\/s2\/document\?revision=2$/);await f.resolve({...document,title:'Second.pdf',page:8});assert.equal(await open,true);
 assert.equal(f.$('map-document-dialog').scrollTop,200);assert.equal(f.$('map-document-passages').scrollTop,400);assert.deepEqual(JSON.parse(JSON.stringify(f.document.capture())),{...reference,title:'Second.pdf',page:8});assert(!JSON.stringify(f.document.capture()).includes('Evidence'));
});

test('missing history source never substitutes the first evidence or fetches a different document',async()=>{
 const f=fixture();const open=f.document.open({...reference,source_id:'removed'},{track:false});await f.resolve(sources);assert.equal(await open,false);assert.equal(f.calls.length,1);assert.equal(f.document.capture(),null);assert.match(f.$('map-document-error').textContent,/Другой фрагмент не открыт/);assert.equal(f.$('map-document-original').hidden,true);
});

test('invalid map pins and history scroll values are rejected before any document request',async()=>{
 for(const change of [{map_id:'other'},{node_id:'deleted'},{map_revision:1},{source_id:''},{scroll:2},{passages_scroll:NaN}]){const f=fixture();assert.equal(await f.document.open({...reference,...change},{track:false}),false);assert.equal(f.calls.length,0);assert.equal(f.$('map-document-dialog').open,false);}
});

test('late source responses cannot survive account, token, revision or navigation guard changes',async()=>{
 for(const stage of ['sources','document'])for(const change of [f=>f.state.session.user='bob',f=>f.state.session.csrf='b',f=>f.state.session.authenticated=false,f=>f.state.map.view.map.revision=3,f=>f.allowed=false]){
  const f=fixture(true);f.allowed=true;const open=f.document.open(reference,{track:false,isCurrent:()=>f.allowed});if(stage==='document')await f.resolve(sources);change(f);await f.resolve(stage==='sources'?sources:document);await open;assert.equal(f.document.capture(),null);assert.equal(f.mounts.length,0);assert.equal(f.$('map-document-original').hidden,true);assert.equal(f.calls.length,stage==='sources'?1:2);
 }
});

test('successful source visits and explicit close notify navigation; failed loads do not create visits',async()=>{
 const f=fixture();const recorded=[];f.navigation={start(){recorded.push('start');},enter(){recorded.push(f.document.capture().source_id);},change(fn){recorded.push('close');return fn();}};
 const open=f.fire('map-document-open');assert.equal(recorded.at(-1),'start','source action must cancel any pending history jump before fetching');await f.resolve(sources);await f.resolve(document);await open;assert.equal(recorded.at(-1),'s1');
 f.$('map-document-picker').value='s2';const switching=f.fire('map-document-picker','change');f.pending.shift().reject(Error('Источник удалён'));await switching;assert.equal(recorded.filter(x=>x==='s2').length,0);assert.equal(f.document.capture(),null);
 f.fire('map-document-close');assert.equal(recorded.at(-1),'close');assert.equal(f.$('map-document-dialog').open,false);
});

test('a restored source remains valid after completion, but still follows current account and map guards',async()=>{
 const f=fixture(true);let restoring=true;const open=f.document.open(reference,{track:false,isCurrent:()=>restoring});await f.resolve(sources);await f.resolve({...document,document_id:'doc',passages:[{entry_id:7,pin:'pin',text:'Evidence',match:true}]});await open;restoring=false;
 assert.equal(f.mounts[0].isCurrent(),true);assert.equal(f.document.capture().source_id,'s2');f.state.session.csrf='changed';assert.equal(f.mounts[0].isCurrent(),false);assert.equal(f.document.capture(),null);
});
test('switching sources ignores the earlier response and removes the old original link',async()=>{
 const f=fixture();const open=f.fire('map-document-open');await f.resolve(sources);
 f.$('map-document-picker').value='s2';const change=f.fire('map-document-picker','change');
 await f.resolve(document);assert.equal(f.$('map-document-original').hidden,true);
 await f.resolve({...document,title:'Two.pdf',page:8});await Promise.all([open,change]);assert.match(f.$('map-document-original').href,/s2\/file\?revision=2#page=8$/);
});
test('navigation invalidates a pending comparison; ungrounded nodes explain missing sources',async()=>{
 const f=fixture();const open=f.fire('map-document-open');f.state.selected='other';await f.resolve(sources);await open;assert.equal(f.calls.length,1);
 const g=fixture();const empty=g.fire('map-document-open');await g.resolve({revision:2,items:[]});await empty;assert.match(g.$('map-document-notice').textContent,/нет привязки/);assert.equal(g.$('map-document-picker').disabled,true);
});
test('annotation reader receives passage pins and selected node; source switch releases and invalidates it',async()=>{
 const f=fixture(true);const open=f.fire('map-document-open');await f.resolve(sources);await f.resolve({...document,document_id:'doc-library',passages:[{entry_id:7,pin:'pin',text:'Evidence',match:true}]});await open;
 const reader=f.mounts[0];assert.equal(reader.documentID,'doc-library');assert.equal(reader.passages[0].pin,'pin');assert.equal(reader.targetNode.nodeID,'n');assert.equal(reader.targetNode.revision,2);assert.equal(reader.isCurrent(),true);
 const oldSignal=f.signals.at(-1);f.$('map-document-picker').value='s2';const change=f.fire('map-document-picker','change');assert.equal(reader.closed,true);assert.equal(reader.isCurrent(),false);assert.equal(oldSignal.aborted,true);
 const nextSignal=f.signals.at(-1);f.fire('map-document-close');assert.equal(nextSignal.aborted,true);await f.resolve(document);await change;assert.equal(f.mounts.length,1);
});
