'use strict';
const {test}=require('node:test'),assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm'),M=require('./assets/map-layout.js');
const plain=value=>JSON.parse(JSON.stringify(value));
const settle=()=>new Promise(resolve=>setImmediate(resolve));
function deferred(){let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no;});return {promise,resolve,reject};}
const rows=[{id:'r',parent_id:'',position:0},{id:'a',parent_id:'r',position:0},{id:'deep',parent_id:'a',position:0},{id:'b',parent_id:'r',position:1}];
function result(overrides={}){
 const state={display_mode:'diagram',layout:'right',selected_node_id:'r',collapsed:[],scale:1,scroll_x:0,scroll_y:0,table_sort:'tree',table_descending:false,table_query:'',table_page:0,...overrides.state};
 return {revision:0,map_revision:3,current_map_revision:3,changed:false,replayed:false,...overrides,state};
}
function accepted(call,replayed=false){return result({revision:call.body.revision+1,map_revision:call.body.map_revision,current_map_revision:call.body.map_revision,replayed,state:call.body.state});}
function fixture(){
 const elements=new Map();let sequence=0;
 const f={calls:[],events:[],errors:[],confirmations:[],confirm:true,state:{session:{authenticated:true,user:'alice',csrf:'csrf-a'},selected:'ws',view:'maps',revision:4,map:null}};
 function $(id){if(!elements.has(id))elements.set(id,{id,events:{},disabled:false,hidden:false,value:'',textContent:'',scrollLeft:200,scrollTop:300,scrollWidth:1000,scrollHeight:1500,clientWidth:200,clientHeight:300,addEventListener(type,fn){this.events[type]=fn;}});return elements.get(id);}
 const context=vm.createContext({MemMapLayout:M,crypto:{randomUUID:()=>String(++sequence).padStart(32,'0')},confirm:message=>{f.confirmations.push(message);return f.confirm;}});
 vm.runInContext(fs.readFileSync(__dirname+'/assets/map-view-controls.js','utf8'),context);
 f.response=()=>result();
 f.controls=context.installMemMapViewControls({state:f.state,$,node:()=>{},api:async(path,body)=>{const call={path,body:body===undefined?undefined:plain(body)};f.calls.push(call);return await f.response(call);},run:fn=>(...args)=>Promise.resolve().then(()=>fn(...args)).catch(e=>f.errors.push(e)),drawMap:()=>f.events.push(['draw']),selectMapNode:id=>f.events.push(['select',id]),centerMapNode:id=>f.events.push(['center',id])});
 f.$=$;
 f.map=(id='map-1',revision=3)=>({view:{map:{id,revision}},selected:f.state.selected,revision:f.state.revision,owner:f.state.session.user,csrf:f.state.session.csrf,tree:M.tree(rows,'r'),displayMode:'diagram',layoutMode:'right',tableSort:'tree',tableDescending:false,tableQuery:'',tablePage:0,depth:1,scale:1,collapsed:M.initialCollapsed(M.tree(rows,'r'))});
 f.open=async(id='map-1')=>{const m=f.map(id);await f.controls.prepare(m,null);f.state.map=m;f.controls.render();return m;};
 f.fire=async(id,event='click')=>{if($(id).disabled)return;await $(id).events[event]?.();await settle();};
 f.refresh=async()=>{const previous=f.state.map,m=f.map(previous.view.map.id,previous.view.map.revision);m.collapsed=new Set(previous.collapsed);m.scale=previous.scale;m.depth=previous.depth;await f.controls.prepare(m,previous);f.state.map=m;f.controls.render();return m;};
 return f;
}

test('first open reads only and keeps overview defaults when no saved view exists',async()=>{
 const f=fixture(),m=await f.open();
 assert.equal(f.calls.length,1);assert.equal(f.calls[0].body,undefined);assert.equal(f.calls[0].path,'workspaces/ws/maps/map-1/view-state');
 assert.deepEqual([...m.collapsed],['a']);assert.equal(m.depth,1);assert.equal(m.displayMode,'diagram');assert.equal(m.layoutMode,'right');assert.equal(m.viewRestored,undefined);
 assert.equal(m.viewState.revision,0);assert.equal(f.$('map-view-save').disabled,false);
 await f.fire('map-view-restore');assert.equal(f.calls.length,2);assert.ok(f.calls.every(c=>c.body===undefined));assert.deepEqual([...m.collapsed],['a']);
});

test('saved table view restores read-only, prunes removed and leaf folds, and preserves diagram folds',async()=>{
 const f=fixture();f.response=()=>result({revision:7,map_revision:2,changed:true,state:{display_mode:'table',layout:'bilateral',selected_node_id:'deep',collapsed:['removed','b','r','a'],scale:.75,scroll_x:.25,scroll_y:.5,table_sort:'label',table_descending:true,table_query:'Ёлка <script>',table_page:2}});
 const m=await f.open();assert.equal(m.viewState.revision,7);assert.equal(m.displayMode,'table');assert.equal(m.layoutMode,'bilateral');assert.equal(m.selectedNode,'deep');assert.deepEqual([...m.collapsed],['r','a']);assert.equal(m.depth,null);assert.equal(m.scale,.75);assert.equal(m.tableQuery,'Ёлка <script>');assert.equal(m.tablePage,2);assert.match(m.viewState.message,/изменилась/);
 f.controls.restoreScroll(m);assert.equal(f.$('map-structured-host').scrollLeft,200);assert.equal(f.$('map-structured-host').scrollTop,600);assert.equal(m.viewScroll,null);
 const after=f.$('map-structured-host').scrollTop=42;f.controls.restoreScroll(m);assert.equal(f.$('map-structured-host').scrollTop,after);
 assert.equal(f.calls.filter(c=>c.body).length,0);
 f.$('map-display-mode').value='diagram';await f.fire('map-display-mode','change');assert.deepEqual([...m.collapsed],[]);assert.equal(m.selectedNode,'deep');assert.deepEqual(f.events,[['draw'],['select','deep'],['center','deep']]);assert.equal(f.calls.length,1);
});

test('removed selected node falls back to root without writing a normalized saved view',async()=>{
 const f=fixture();f.response=()=>result({revision:2,changed:true,state:{selected_node_id:'removed',collapsed:['removed','a','b']}});
 const m=await f.open();assert.equal(m.selectedNode,'r');assert.deepEqual([...m.collapsed],['a']);assert.equal(f.calls.length,1);
});

test('malformed saved DTO and a newer map revision cannot enable saving',async()=>{
 const invalid=[{state:{scroll_x:NaN}},{state:{scroll_y:Infinity}},{state:{scroll_x:-.1}},{state:{scroll_y:1.1}},{state:{scale:0}},{state:{scale:Infinity}},{state:{collapsed:['a','a']}},{state:{table_query:'😀'.repeat(201)}},{state:{table_page:1000}},{current_map_revision:4}];
 for(const change of invalid){const f=fixture();f.response=()=>result({revision:1,...change});const m=await f.open();assert.equal(m.viewState.revision,null);assert.equal(m.layoutMode,'right');assert.equal(f.$('map-view-save').disabled,true);await f.fire('map-view-save');assert.equal(f.calls.filter(c=>c.body).length,0);}
});

test('snapshot uses finite clamped ratios and only extant branch IDs without changing folds',async()=>{
 const f=fixture(),m=await f.open();m.collapsed=new Set(['removed','b','a','r']);m.selectedNode='missing';
 const h=f.$('map-canvas');h.scrollLeft=-50;h.scrollTop=5000;
 const snap=plain(f.controls.snapshot(m));assert.equal(snap.scroll_x,0);assert.equal(snap.scroll_y,1);assert.deepEqual(snap.collapsed,['a','r']);assert.equal(snap.selected_node_id,'r');assert.deepEqual([...m.collapsed],['removed','b','a','r']);
 h.scrollWidth=h.clientWidth;h.scrollHeight=0;const noScroll=f.controls.snapshot(m);assert.equal(noScroll.scroll_x,0);assert.equal(noScroll.scroll_y,0);assert.ok(Number.isFinite(noScroll.scroll_x)&&Number.isFinite(noScroll.scroll_y));
});

test('late prepare reads do not restore a view after auth, workspace or screen invalidation',async()=>{
 for(const change of [f=>{f.state.session.authenticated=false;},f=>{f.state.session.user='bob';f.state.session.csrf='csrf-b';},f=>{f.state.selected='other';f.state.revision++;},f=>{f.state.view='documents';f.state.revision++;}]){
  const f=fixture(),wait=deferred(),m=f.map();f.response=()=>wait.promise;const preparing=f.controls.prepare(m,null);change(f);f.controls.close();wait.resolve(result({revision:9,state:{layout:'down'}}));await preparing;
  assert.equal(m.layoutMode,'right');assert.equal(m.viewState.revision,null);assert.deepEqual(f.events,[]);assert.equal(f.calls.filter(c=>c.body).length,0);
 }
});

test('late explicit restore reads never repaint a different workspace, screen or session',async()=>{
 for(const change of [f=>{f.state.selected='other';f.state.revision++;},f=>{f.state.view='documents';f.state.revision++;},f=>{f.state.session={authenticated:true,user:'bob',csrf:'csrf-b'};}]){
  const f=fixture(),m=await f.open(),wait=deferred();f.response=()=>wait.promise;const reading=f.fire('map-view-restore');await settle();change(f);f.controls.close();wait.resolve(result({revision:9,state:{layout:'down'}}));await reading;
  assert.equal(m.layoutMode,'right');assert.deepEqual(f.events,[]);assert.equal(f.calls.filter(c=>c.body).length,0);
 }
});

test('lost save acknowledgement retries the exact snapshot and request ID after live view changes',async()=>{
 const f=fixture(),m=await f.open();m.layoutMode='down';m.selectedNode='a';m.tableQuery='исходный запрос';
 let first=true;f.response=call=>{if(first){first=false;throw Error('network');}return accepted(call,true);};
 await f.fire('map-view-save');const original=f.calls.find(c=>c.body).body;assert.ok(m.viewState.pending);assert.equal(f.$('map-view-restore').disabled,true);assert.equal(f.$('map-view-discard').hidden,false);
 m.layoutMode='bilateral';m.tableQuery='новый запрос';m.collapsed.clear();f.$('map-canvas').scrollTop=0;
 await f.fire('map-view-save');const saves=f.calls.filter(c=>c.body);assert.equal(saves.length,2);assert.deepEqual(saves[1].body,original);assert.equal(m.viewState.pending,null);assert.equal(m.viewState.revision,1);assert.equal(m.layoutMode,'bilateral');assert.equal(m.tableQuery,'новый запрос');
 f.response=call=>accepted(call);await f.fire('map-view-save');const next=f.calls.at(-1).body;assert.equal(next.revision,1);assert.notEqual(next.request_id,original.request_id);assert.equal(next.state.layout,'bilateral');
});

test('CAS conflict keeps the exact pending request until explicit confirmed discard and reload',async()=>{
 const f=fixture(),m=await f.open();f.response=()=>{throw Error('Вид изменился в другой вкладке');};await f.fire('map-view-save');const pinned=plain(m.viewState.pending);
 f.confirm=false;await f.fire('map-view-discard');assert.deepEqual(plain(m.viewState.pending),pinned);assert.equal(f.calls.length,2);
 f.confirm=true;f.response=()=>result({revision:5,state:{layout:'bilateral'}});await f.fire('map-view-discard');assert.equal(m.viewState.pending,null);assert.equal(m.viewState.revision,5);assert.equal(m.layoutMode,'bilateral');assert.equal(f.calls.at(-1).body,undefined);assert.equal(f.confirmations.length,2);assert.equal(f.$('map-view-save').disabled,false);
});

test('pending save survives navigation but its late acknowledgement cannot repaint the new screen',async()=>{
 for(const change of [f=>{f.state.selected='other';f.state.revision++;},f=>{f.state.view='documents';f.state.revision++;},f=>{f.state.session={authenticated:true,user:'bob',csrf:'csrf-b'};}]){
  const f=fixture(),m=await f.open(),wait=deferred();f.response=()=>wait.promise;const saving=f.fire('map-view-save');await settle();const call=f.calls.at(-1);change(f);f.controls.close();f.$('map-view-status').textContent='new context';wait.resolve(accepted(call));await saving;
  assert.deepEqual(f.events,[]);assert.equal(f.$('map-view-status').textContent,'new context');assert.equal(m.viewState.pending,null);
 }
});

test('eight uncertain context slots are retained, and an explicit discard frees a slot',async()=>{
 const f=fixture();let original;
 for(let i=0;i<8;i++){f.response=()=>result();const m=await f.open('map-'+i);f.response=()=>{throw Error('network');};await f.fire('map-view-save');if(!i)original=plain(m.viewState.pending);}
 await assert.rejects(()=>f.controls.prepare(f.map('map-8'),null),/восемь/);
 f.response=()=>result();const reopened=await f.open('map-0');assert.deepEqual(plain(reopened.viewState.pending),original);
 await f.fire('map-view-discard');assert.equal(reopened.viewState.pending,null);const ninth=await f.open('map-8');assert.equal(ninth.viewState.revision,0);
});

test('session replacement purges uncertain records without sending another users old request',async()=>{
 const f=fixture(),old=await f.open();f.response=()=>{throw Error('network');};await f.fire('map-view-save');const pending=plain(old.viewState.pending);
 f.state.session={authenticated:true,user:'bob',csrf:'csrf-b'};f.controls.close();f.response=()=>result();const current=await f.open();assert.equal(current.viewState.pending,null);assert.equal(current.viewState.revision,0);
 f.response=call=>accepted(call);await f.fire('map-view-save');assert.notEqual(f.calls.at(-1).body.request_id,pending.request_id);assert.equal(old.viewState.pending.request_id,pending.request_id);
});

test('same-map refresh shares live save result and re-enables current controls after acknowledgement',async()=>{
 const f=fixture(),old=await f.open(),wait=deferred();f.response=()=>wait.promise;const saving=f.fire('map-view-save');await settle();const call=f.calls.at(-1),current=await f.refresh();
 assert.equal(current.viewState,old.viewState);assert.equal(f.$('map-view-save').disabled,true);wait.resolve(accepted(call));await saving;
 assert.equal(current.viewState.revision,1);assert.equal(current.viewState.busy,false);assert.equal(f.$('map-view-save').disabled,false);assert.equal(f.$('map-view-restore').disabled,false);
});

test('same-map refresh shares restore completion and re-enables its controls without applying old geometry',async()=>{
 const f=fixture(),old=await f.open(),wait=deferred();f.response=()=>wait.promise;const reading=f.fire('map-view-restore');await settle();const current=await f.refresh();
 wait.resolve(result({revision:2,state:{layout:'down'}}));await reading;
 assert.equal(current.viewState,old.viewState);assert.equal(current.layoutMode,'right');assert.equal(current.viewState.busy,false);assert.equal(f.$('map-view-restore').disabled,false);assert.deepEqual(f.events,[]);
});

test('older concurrent prepare response cannot replace the revision of a newer active map context',async()=>{
 const f=fixture(),first=deferred(),second=deferred();let request=0;f.response=()=>++request===1?first.promise:second.promise;
 const older=f.map(),newer=f.map(),a=f.controls.prepare(older,null),b=f.controls.prepare(newer,null);
 second.resolve(result({revision:2,state:{layout:'down'}}));await b;f.state.map=newer;f.controls.render();
 first.resolve(result({revision:1,state:{layout:'right'}}));await a;
 assert.equal(newer.layoutMode,'down');assert.equal(newer.viewState.revision,2);
});

test('a read started during an outstanding save cannot downgrade its subsequently confirmed revision',async()=>{
 const f=fixture(),old=await f.open(),write=deferred(),read=deferred();f.response=call=>call.body?write.promise:read.promise;
 const saving=f.fire('map-view-save');await settle();const call=f.calls.at(-1);f.state.revision++;f.controls.close();
 const newer=f.map(),preparing=f.controls.prepare(newer,null);
 write.resolve(accepted(call));await saving;assert.equal(old.viewState.revision,1);
 read.resolve(result({revision:0}));await preparing;f.state.map=newer;f.controls.render();assert.equal(newer.viewState.revision,1);
});

test('failed reopen read does not disable retry of an already pinned uncertain save',async()=>{
 const f=fixture(),old=await f.open();f.response=()=>{throw Error('network');};await f.fire('map-view-save');const pinned=plain(old.viewState.pending);
 f.state.revision++;f.controls.close();const reopened=await f.open();assert.deepEqual(plain(reopened.viewState.pending),pinned);
 assert.equal(f.$('map-view-save').disabled,false);f.response=call=>accepted(call,true);await f.fire('map-view-save');assert.deepEqual(f.calls.at(-1).body,pinned);assert.equal(reopened.viewState.pending,null);
});

test('incomplete or mismatched save acknowledgements retain the exact pending request for retry',async()=>{
 const mutations=[ack=>null,ack=>({revision:ack.revision,replayed:false}),ack=>({...ack,revision:ack.revision+1}),ack=>({...ack,map_revision:4}),ack=>({...ack,current_map_revision:4}),ack=>({...ack,changed:true}),ack=>({...ack,state:{...ack.state,layout:'down'}})];
 for(const mutate of mutations){const f=fixture(),m=await f.open();f.response=call=>mutate(accepted(call));await f.fire('map-view-save');const pinned=plain(f.calls.at(-1).body);assert.deepEqual(plain(m.viewState.pending),pinned);assert.match(m.viewState.message,/не подтвердил/);f.response=call=>accepted(call,true);await f.fire('map-view-save');assert.deepEqual(f.calls.at(-1).body,pinned);assert.equal(m.viewState.pending,null);}
});

test('an older rejected read cannot disable a later successful prepare or replace its message',async()=>{
 const f=fixture(),wait=deferred();let request=0;f.response=()=>++request===1?wait.promise:result({revision:3});
 const old=f.map(),preparing=f.controls.prepare(old,null),current=await f.open();const message=current.viewState.message;wait.reject(Error('old request failed'));await preparing;
 assert.equal(current.viewState.revision,3);assert.equal(current.viewState.message,message);assert.equal(f.$('map-view-save').disabled,false);
});

test('stale action handlers cannot save, restore or change mode after auth or navigation changes',async()=>{
 for(const change of [f=>{f.state.selected='other';},f=>{f.state.revision++;},f=>{f.state.view='documents';},f=>{f.state.session.authenticated=false;},f=>{f.state.session.csrf='other';},f=>{f.state.session.user='bob';}]){
  const f=fixture(),m=await f.open();change(f);await f.fire('map-view-save');await f.fire('map-view-restore');f.$('map-display-mode').value='table';await f.fire('map-display-mode','change');f.$('map-layout-mode').value='down';await f.fire('map-layout-mode','change');
  assert.equal(f.calls.length,1);assert.equal(m.layoutMode,'right');assert.equal(m.displayMode,'diagram');assert.deepEqual(f.events,[]);
 }
});
