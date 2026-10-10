'use strict';
const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),layout=require('./assets/map-layout.js');
const plain=x=>JSON.parse(JSON.stringify(x)),settle=()=>new Promise(setImmediate);
function deferred(){let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};}
const savedState=()=>({display_mode:'table',layout:'bilateral',selected_node_id:'leaf',collapsed:['root'],scale:.5,scroll_x:.2,scroll_y:.6,table_sort:'label',table_descending:true,table_query:'вопрос',table_page:0});
const item=(id=1)=>({id:'mvp-'+String(id).padStart(32,'0'),name:'Вопросы '+id,map_revision:3,state:savedState(),changed:false});
function fixture(){
 let serial=100;const elements=new Map(),f={calls:[],errors:[],draws:0,confirm:true,confirmations:[],data:{revision:1,current_map_revision:3,items:[item()]}};
 function node(tag,text){return {tag,textContent:text||'',value:'',children:[],events:{},disabled:false,hidden:false,open:false,readOnly:false,scrollTop:50,scrollLeft:25,scrollWidth:1000,scrollHeight:1500,clientWidth:200,clientHeight:500,append(...children){this.children.push(...children);},replaceChildren(...children){this.children=children;},addEventListener(type,fn){this.events[type]=fn;},showModal(){this.open=true;},close(){this.open=false;}};}
 const $=id=>{if(!elements.has(id))elements.set(id,node('div'));return elements.get(id);};f.$=$;
 f.state={session:{authenticated:true,user:'alice',csrf:'a'},selected:'ws',view:'maps',revision:7};
 f.map=(id='map')=>({view:{map:{id,title:'Карта',revision:3},nodes:[]},selected:f.state.selected,revision:f.state.revision,owner:f.state.session.user,csrf:f.state.session.csrf,tree:layout.tree([{id:'root',parent_id:'',position:0},{id:'leaf',parent_id:'root',position:0}],'root'),displayMode:'diagram',layoutMode:'right',selectedNode:'root',collapsed:new Set(),scale:1,tableSort:'tree',tablePage:0});f.state.map=f.map();
 f.ack=(p,replayed=false)=>({revision:p.revision+1,map_revision:p.map_revision,id:p.id,operation:p.operation,replayed});
 f.post=p=>{f.data.revision=p.revision+1;if(p.operation==='create')f.data.items.push({id:p.id,name:p.name,map_revision:3,state:p.state,changed:false});if(p.operation==='rename')f.data.items.find(i=>i.id===p.id).name=p.name;if(p.operation==='update')Object.assign(f.data.items.find(i=>i.id===p.id),{name:p.name,state:p.state});if(p.operation==='delete')f.data.items=f.data.items.filter(i=>i.id!==p.id);return f.ack(p);};
 f.respond=({body})=>body?f.post(body):plain(f.data);
 const context=vm.createContext({MemMapLayout:layout,crypto:{randomUUID:()=>String(++serial).padStart(32,'0')},confirm:message=>{f.confirmations.push(message);return f.confirm;}});
 for(const name of ['map-view-controls.js','map-view-presets.js'])vm.runInContext(fs.readFileSync(__dirname+'/assets/'+name,'utf8'),context);
 const run=fn=>(...args)=>Promise.resolve().then(()=>fn(...args)).catch(e=>f.errors.push(e));
 f.controls=context.installMemMapViewControls({state:f.state,$,node,run,api:()=>{throw Error('named views must not save the default view');},drawMap:()=>f.draws++,selectMapNode:id=>{f.state.map.selectedNode=id;},centerMapNode(){}});
 f.module=context.installMemMapViewPresets({state:f.state,$,node,run,viewControls:f.controls,api:async(path,body)=>{assert.match(path,/\/view-presets$/);const call={path,body:body===undefined?undefined:plain(body)};f.calls.push(call);return await f.respond(call);}});
 f.fire=async(id,type='click',value)=>{const el=$(id);if(value!==undefined)el.value=value;if(el.disabled)return;await el.events[type]?.({preventDefault(){}});await settle();};
 f.name=async text=>f.fire('map-presets-name','input',text);
 return f;
}

test('opening only reads and applying restores complete view without API mutations',async()=>{
 const f=fixture(),original=plain(f.state.map.view);await f.module.show();assert.equal(f.$('map-presets-dialog').open,true);assert.equal(f.$('map-presets-name').value,'Вопросы 1');
 await f.fire('map-presets-apply');assert.equal(f.$('map-presets-dialog').open,false);assert.equal(f.state.map.displayMode,'table');assert.equal(f.state.map.layoutMode,'bilateral');assert.equal(f.state.map.selectedNode,'leaf');assert.deepEqual([...f.state.map.collapsed],['root']);assert.equal(f.$('map-structured-host').scrollTop,600);assert.equal(f.draws,1);assert.deepEqual(plain(f.state.map.view),original);assert.ok(f.calls.every(c=>c.body===undefined));
});
test('create freezes current view and rename/delete are explicit operations with collection CAS',async()=>{
 const f=fixture();await f.module.show();await f.name('Источники');await f.fire('map-presets-create');const create=f.calls.find(c=>c.body).body;assert.equal(create.revision,1);assert.equal(create.operation,'create');assert.equal(create.name,'Источники');assert.equal(create.state.display_mode,'diagram');assert.equal(f.data.items.length,2);assert.equal(f.calls.at(-1).body,undefined);
 await f.name('Переименован');await f.fire('map-presets-rename');assert.equal(f.calls.filter(c=>c.body).at(-1).body.operation,'rename');assert.equal(f.calls.filter(c=>c.body).at(-1).body.revision,2);
 f.confirm=false;await f.fire('map-presets-delete');assert.equal(f.data.items.length,2);f.confirm=true;await f.fire('map-presets-delete');assert.equal(f.data.items.length,1);assert.equal(f.calls.filter(c=>c.body).at(-1).body.name,'');
});
test('replacement requires confirmation and captures the live state rather than selected preset state',async()=>{
 const f=fixture();await f.module.show();f.state.map.layoutMode='down';f.confirm=false;await f.fire('map-presets-update');assert.equal(f.calls.length,1);f.confirm=true;await f.fire('map-presets-update');const call=f.calls.find(c=>c.body).body;assert.equal(call.state.layout,'down');assert.equal(call.operation,'update');assert.equal(call.id,item().id);
});
test('lost ACK is retried byte-for-byte even after view/name changes and reopening',async()=>{
 const f=fixture();await f.module.show();await f.name('Новый');let first=true;f.respond=call=>{if(!call.body)return plain(f.data);if(first){first=false;f.post(call.body);throw Error('lost ACK');}return f.ack(call.body,true);};
 await f.fire('map-presets-create');const pending=f.calls.find(c=>c.body).body;assert.equal(f.$('map-presets-retry').hidden,false);assert.equal(f.$('map-presets-name').readOnly,true);
 f.module.close();f.state.map.layoutMode='down';await f.module.show();await f.fire('map-presets-retry');const posts=f.calls.filter(c=>c.body);assert.equal(posts.length,2);assert.deepEqual(posts[1].body,pending);assert.equal(f.data.items.length,2);assert.equal(f.$('map-presets-retry').hidden,true);
});
test('ACK followed by failed reload must not repeat the accepted mutation',async()=>{
 const f=fixture();await f.module.show();let wrote=false;f.respond=call=>{if(call.body){wrote=true;return f.post(call.body);}if(wrote)throw Error('GET failed');return plain(f.data);};await f.name('Новый');await f.fire('map-presets-create');assert.equal(f.$('map-presets-retry').hidden,true);assert.equal(f.$('map-presets-create').disabled,true);assert.match(f.$('map-presets-status').textContent,/GET failed/);f.respond=call=>call.body?f.post(call.body):plain(f.data);await f.fire('map-presets-refresh');assert.equal(f.calls.filter(c=>c.body).length,1);assert.equal(f.data.items.length,2);
});
test('pending retry remains available when reopening GET fails and discard explicitly reloads',async()=>{
 const f=fixture();await f.module.show();f.respond=()=>{throw Error('network');};await f.name('Новый');await f.fire('map-presets-create');f.module.close();await f.module.show();assert.equal(f.$('map-presets-retry').disabled,false);f.confirm=false;await f.fire('map-presets-discard');assert.equal(f.$('map-presets-retry').hidden,false);f.confirm=true;f.respond=()=>plain(f.data);await f.fire('map-presets-discard');assert.equal(f.$('map-presets-retry').hidden,true);assert.equal(f.$('map-presets-refresh').disabled,false);
});
test('late reads and writes cannot reopen or repaint a different map or user',async()=>{
 for(const change of [f=>{f.state.map=f.map('other');},f=>{f.state.session={authenticated:true,user:'bob',csrf:'b'};},f=>{f.state.selected='different';},f=>{f.state.view='documents';}]){
  const f=fixture(),wait=deferred();f.respond=()=>wait.promise;const opening=f.module.show();change(f);f.module.close();wait.resolve(plain(f.data));await opening;assert.equal(f.$('map-presets-dialog').open,false);assert.equal(f.draws,0);
 }
 const f=fixture();await f.module.show();await f.name('Draft');const wait=deferred();f.respond=()=>wait.promise;const sending=f.fire('map-presets-create');await settle();const body=f.calls.at(-1).body;f.module.close();f.state.map=f.map('other');wait.resolve(f.ack(body));await sending;assert.equal(f.calls.filter(c=>c.body===undefined).length,1);assert.equal(f.$('map-presets-dialog').open,false);
});
test('wrong ACK stays pending and blocks destructive actions',async()=>{
 for(const wrong of [{revision:200},{operation:'delete'},{map_revision:99},{id:item(99).id},{replayed:undefined}]){const f=fixture();await f.module.show();await f.name('New');f.respond=call=>({...f.ack(call.body),...wrong});await f.fire('map-presets-create');assert.equal(f.$('map-presets-delete').disabled,true);assert.equal(f.$('map-presets-retry').hidden,false);}
});
test('bounds, malformed DTO and stale map pin disable changes without losing pending state',async()=>{
 for(const transform of [d=>{d.items[0].state.scale=0;},d=>{d.current_map_revision=4;},d=>{d.items.push(d.items[0]);},d=>{d.items[0].name='bad\nname';},d=>{d.items=Array.from({length:21},(_,i)=>item(i));}]){const f=fixture();transform(f.data);await f.module.show();assert.equal(f.$('map-presets-create').disabled,true);assert.equal(f.$('map-presets-apply').disabled,true);assert.equal(f.calls.length,1);}
 const f=fixture();f.data.items=Array.from({length:20},(_,i)=>item(i));await f.module.show();await f.name('New');assert.equal(f.$('map-presets-create').disabled,true);assert.equal(f.$('map-presets-rename').disabled,false);await f.name('😀'.repeat(81));assert.equal(f.$('map-presets-rename').disabled,true);
});
test('explicit logout invalidates authentication before boot purges session-owned drafts',async()=>{
 const app=fs.readFileSync(__dirname+'/assets/app.js','utf8'),a=app.indexOf("  $('logout').addEventListener"),b=app.indexOf("  $('workspace-picker')",a);assert.ok(a>0&&b>a);let handler,bootAuth;const state={session:{authenticated:true,user:'alice'},selected:'ws',workspaces:[1],revision:2};
 vm.runInNewContext(app.slice(a,b),{state,$:()=>({addEventListener(_,fn){handler=fn;}}),run:fn=>fn,api:async()=>{},boot:async()=>{bootAuth=state.session.authenticated;}});await handler();assert.equal(bootAuth,false);assert.equal(state.selected,'');assert.deepEqual(plain(state.workspaces),[]);assert.equal(state.revision,3);
});

test('reopening during an old GET waits then reads for the new dialog context',async()=>{
 const f=fixture(),wait=deferred();let count=0;f.respond=()=>++count===1?wait.promise:plain(f.data);const first=f.module.show();f.module.close();await f.module.show();wait.resolve({revision:999,current_map_revision:3,items:[]});await first;
 assert.equal(f.calls.length,2);assert.equal(f.$('map-presets-name').value,'Вопросы 1');assert.equal(f.$('map-presets-apply').disabled,false);
});

test('eight pending contexts are retained; logout releases them before another user opens',async()=>{
 const f=fixture();f.respond=call=>{if(call.body)throw Error('network');return {revision:0,current_map_revision:3,items:[]};};
 for(let i=0;i<8;i++){f.module.close();f.state.map=f.map('map'+i);await f.module.show();await f.name('Pending '+i);await f.fire('map-presets-create');}
 f.module.close();f.state.map=f.map('ninth');await assert.rejects(f.module.show(),/восемь/);
 f.state.session={authenticated:false};f.module.close();f.state.session={authenticated:true,user:'bob',csrf:'b'};f.state.map=f.map('ninth');await f.module.show();assert.equal(f.$('map-presets-retry').hidden,true);assert.equal(f.$('map-presets-name').value,'');
});
