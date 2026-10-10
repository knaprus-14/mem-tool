const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const settle=()=>new Promise(r=>setImmediate(r));
function fixture(){
 const elements=new Map();let serial=0;
 function element(id){return {id,dataset:{},hidden:false,disabled:false,checked:false,children:[],events:{},addEventListener(k,fn){this.events[k]=fn;},append(...items){this.children.push(...items);},replaceChildren(...items){this.children=[...items];},querySelectorAll(){return [...elements.values()];},showModal(){this.open=true;},close(){this.open=false;this.events.close?.();}};}
 function $(id){if(!elements.has(id))elements.set(id,element(id));return elements.get(id);}
 function node(tag,text,css){const e=element('dynamic-'+(++serial));e.tag=tag;e.textContent=text;e.className=css;elements.set(e.id,e);return e;}
 const snapshot={id:11,revision:1,reason:'Before edit',document_digest:'sha256:'+'e'.repeat(64),created:'2026-10-09'};
 const preview={revision:3,snapshot,map:{title:'Historical map',description:'Description',status:'draft',mode:'manual'},current_node_count:2,nodes:[{id:'root',parent_id:'',position:0,label:'<script>fixture</script>',kind:'topic',origin:'manual',locked:true,summary:'summary',body:'body',sources:[{title:'Source',state:'stale',excerpt:'Saved excerpt',page:4}]}]};
 const f={$,elements,preview,calls:[],state:{selected:'ws',revision:1,map:{view:{map:{id:'map-1',title:'Current map',revision:3}},tree:{byID:new Map()},selectedNode:'root'}},shown:0};
 const context=vm.createContext({AbortController,crypto:{randomUUID:()=> 'a'.repeat(32)}});
 vm.runInContext(fs.readFileSync(__dirname+'/assets/map-library.js','utf8'),context);
 context.installMemMapLibrary({state:f.state,$,node,api:async(path,body,_retry,signal)=>{f.calls.push({path,body,signal});if(body){if(f.fail)throw Error('network');return {revision:4};}if(path.includes('/snapshots/')){if(f.waitPreview)return await f.waitPreview;return preview;}return {revision:f.state.map.view.map.revision,items:[snapshot]};},run:fn=>fn,showMap:async()=>{f.shown++;f.state.map.view.map.revision=4;},error(){},loadView(){}});
 f.fire=async(id,event='click')=>{const result=$(id).events[event]?.({preventDefault(){}});await result;await settle();};
 f.open=async()=>{await f.fire('map-snapshots-open');};
 f.previewButton=()=>[...elements.values()].find(e=>e.textContent==='Просмотреть снимок');
 return f;
}
test('snapshot restore requires loaded preview and explicit confirmation with exact pins',async()=>{
 const f=fixture();await f.open();await f.fire('map-snapshot-restore-form','submit');assert.equal(f.calls.filter(c=>c.body).length,0);
 await f.fire(f.previewButton().id);assert.equal(f.$('map-snapshot-preview').hidden,false);assert.match(f.$('map-snapshot-preview-context').textContent,/Historical map/);assert.equal(f.$('map-snapshot-restore').disabled,true);
 const heading=[...f.elements.values()].find(e=>e.tag==='summary');assert.equal(heading.textContent,'<script>fixture</script> · закреплён');assert.equal(heading.innerHTML,undefined);
 await f.fire('map-snapshot-restore-form','submit');assert.equal(f.calls.filter(c=>c.body).length,0);
 f.$('map-snapshot-confirm').checked=true;await f.fire('map-snapshot-confirm','change');assert.equal(f.$('map-snapshot-restore').disabled,false);
 await f.fire('map-snapshot-restore-form','submit');const call=f.calls.find(c=>c.body);assert.equal(call.path,'workspaces/ws/maps/map-1/snapshots/11/restore');assert.equal(call.body.revision,3);assert.equal(call.body.snapshot_digest,f.preview.snapshot.document_digest);assert.equal(f.shown,1);assert.match(f.$('map-library-status').textContent,/можно отменить/);
});
test('closing a pending snapshot preview aborts and ignores the late result',async()=>{
 const f=fixture();await f.open();let resolve;f.waitPreview=new Promise(r=>resolve=r);
 const pending=f.fire(f.previewButton().id);await settle();const request=f.calls.at(-1);assert.equal(request.signal.aborted,false);
 await f.fire('map-library-close');assert.equal(request.signal.aborted,true);resolve(f.preview);await pending;
 assert.equal(f.$('map-snapshot-preview').hidden,true);assert.equal(f.calls.filter(c=>c.body).length,0);
});
test('navigation invalidates snapshot confirmation',async()=>{
 const f=fixture();await f.open();await f.fire(f.previewButton().id);f.$('map-snapshot-confirm').checked=true;await f.fire('map-snapshot-confirm','change');f.state.selected='other';f.state.revision++;
 await f.fire('map-snapshot-restore-form','submit');assert.equal(f.calls.filter(c=>c.body).length,0);
});
test('uncertain snapshot restore retries the same receipt and does not create a fresh confirmation',async()=>{
 const f=fixture();await f.open();await f.fire(f.previewButton().id);f.$('map-snapshot-confirm').checked=true;await f.fire('map-snapshot-confirm','change');f.fail=true;
 await f.fire('map-snapshot-restore-form','submit');assert.equal(f.$('map-library-retry').hidden,false);assert.equal(f.shown,0);f.fail=false;await f.fire('map-library-retry');
 const calls=f.calls.filter(c=>c.body);assert.equal(calls.length,2);assert.deepEqual(calls[0].body,calls[1].body);assert.equal(f.shown,1);
});
