'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const layout=require('./assets/map-layout.js');
const app=fs.readFileSync(__dirname+'/assets/app.js','utf8');
const plain=value=>JSON.parse(JSON.stringify(value));
const settle=()=>new Promise(setImmediate);
function deferred(){let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no;});return {promise,resolve,reject};}
function extract(start,end){const a=app.indexOf(start),b=app.indexOf(end,a+start.length);assert.ok(a>=0&&b>a,'application function boundaries');return app.slice(a,b);}
function saved(overrides={}){return {revision:1,map_revision:3,current_map_revision:3,changed:false,replayed:false,...overrides,state:{display_mode:'table',layout:'bilateral',selected_node_id:'deep',collapsed:['branch'],scale:.7,scroll_x:.25,scroll_y:.5,table_sort:'tree',table_descending:false,table_query:'',table_page:0,...overrides.state}};}
function fixture(count=4){
 const elements=[],ids=new Map();let sequence=0;
 function node(tag,text,css=''){
  let own=text===undefined?'':String(text);const classes=new Set(css.split(' ').filter(Boolean));
  const e={tag,children:[],parentNode:null,attrs:{},dataset:{},events:{},disabled:false,hidden:false,value:'',className:css,scrollLeft:0,scrollTop:0,scrollWidth:1200,scrollHeight:1800,clientWidth:400,clientHeight:600,
   classList:{add:(...names)=>names.forEach(v=>classes.add(v)),remove:(...names)=>names.forEach(v=>classes.delete(v)),toggle:(v,on)=>on?classes.add(v):classes.delete(v),contains:v=>classes.has(v)},
   get textContent(){return own+this.children.map(c=>c.textContent).join('');},set textContent(v){own=String(v);this.replaceChildren();},
   append(...items){for(const child of items){child.parentNode=this;this.children.push(child);}},replaceChildren(...items){for(const child of this.children)child.parentNode=null;this.children=[];this.append(...items);},
   contains(child){for(let current=child;current;current=current.parentNode)if(current===this)return true;return false;},
   setAttribute(k,v){this.attrs[k]=String(v);if(k.startsWith('data-'))this.dataset[k.slice(5).replace(/-([a-z])/g,(_,c)=>c.toUpperCase())]=String(v);},
   querySelectorAll(selector){const attribute=selector.match(/^\[([^\]]+)\]$/)?.[1];return elements.filter(child=>child!==this&&this.contains(child)&&attribute in child.attrs);},
   open:false,showModal(){this.open=true;},close(){this.open=false;},addEventListener(type,fn){(this.events[type]??=[]).push(fn);},focus(){f.focused=this;}};
  elements.push(e);return e;
 }
 function $(id){if(!ids.has(id))ids.set(id,node('div'));return ids.get(id);}
 const nodes=Array.from({length:count},(_,i)=>({id:i===0?'root':i===1?'branch':i===2?'deep':'n'+i,parent_id:i===0?'':i===2?'branch':'root',position:i,label:i===0?'Корень':i===1?'Ветвь':i===2?'Ёлка <script>':'Узел '+String(i).padStart(3,'0'),kind:i%2?'fact':'topic',summary:'Описание '+i,body:'Текст '+i,source_count:i%4,locked:false}));
 const f={elements,$,node,errors:[],calls:[],searchCloses:0,state:{selected:'ws',revision:7,view:'maps',session:{authenticated:true,user:'alice',csrf:'csrf-a',local:true}},views:new Map(),saved:saved()};
 f.makeView=(id='map',revision=3)=>({map:{id,title:'Карта '+id,description:'Описание',status:'draft',revision,root_node_id:'root',node_count:nodes.length,source_count:4},nodes:plain(nodes),has_import_source:false});
 f.views.set('map',f.makeView());f.views.set('other',f.makeView('other'));
 f.respond=async(path,body)=>{if(path.endsWith('/view-state')){assert.equal(body,undefined,'implicit view writes are forbidden');return plain(f.saved);}return plain(f.views.get(decodeURIComponent(path.split('/').at(-1))));};
 const context=vm.createContext({state:f.state,$,node,MemMapLayout:layout,document:{body:node('body'),createElementNS:(_,tag)=>node(tag)},crypto:{randomUUID:()=>String(++sequence).padStart(32,'0')},confirm:()=>true,
  api:async(path,body)=>{f.calls.push({path,body:body===undefined?undefined:plain(body)});return (f.handle||f.respond)(path,body);},run:fn=>(...args)=>Promise.resolve().then(()=>fn(...args)).catch(e=>f.errors.push(e)),
  current:()=>({name:'База',path:'C:\\test'}),mapStatus:()=> 'Черновик',closeMapSearch:()=>f.searchCloses++,renderMapBody:(host,text)=>{host.textContent=text;},mapResize:{refresh(){}},mapViewPresets:{close(){}},mapDocument:{close(){f.source=null;},capture:()=>f.source||null,open:async(ref,options)=>{f.sourceOpens??=[];f.sourceOpens.push(plain(ref));if(f.sourceHandle)return f.sourceHandle(ref,options);f.source=plain(ref);return true;}},toggleMapBranch(){throw Error('unexpected branch toggle');}});
 for(const name of ['map-structured-view.js','map-view-controls.js','map-navigation.js'])vm.runInContext(fs.readFileSync(__dirname+'/assets/'+name,'utf8'),context,{filename:name});
 vm.runInContext([
  extract('  async function showMap(','  async function openReadingMap('),
  extract('  function selectMapNode(','  function mapAnchor('),
  extract('  function mapAnchor(','  function setMapDetails('),
  extract('  function scaleMap(','  function drawMap('),
  extract('  function showMapDepth(',"  $('map-overview')"),
  extract('  function setMapDetails(','  function scaleMap('),
  extract('  function drawMap(','  async function createMapForm('),
  'var mapStructured=installMemMapStructuredView({state,$,node,api,run,showMap,selectMapNode,onChanged:()=>mapViewControls.render(),change:fn=>mapNavigation.change(fn)});',
  'var mapViewControls=installMemMapViewControls({state,$,node,api,run,drawMap,selectMapNode,centerMapNode,change:fn=>mapNavigation.change(fn)});',
  'var mapNavigation=installMemMapNavigation({state,$,node,run,viewControls:mapViewControls,showMap,setMapDetails,readSource:()=>mapDocument.capture(),closeSource:()=>mapDocument.close(),openSource:(ref,options)=>mapDocument.open(ref,options)});'
 ].join('\n'),context,{filename:'app-map-view-integration.js'});
 f.context=context;f.show=id=>context.showMap(id||'map');f.select=(id,reveal)=>context.selectMapNode(id,reveal);f.draw=()=>context.drawMap();
 f.fire=async(id,event='click',value)=>{const e=typeof id==='string'?$(id):id;if(value!==undefined)e.value=value;if(e.disabled)return;for(const fn of e.events[event]||[])await fn({preventDefault(){}});await settle();};
 f.rows=()=>elements.filter(e=>$('map-structured-host').contains(e)&&e.attrs['data-structured-node']);
 return f;
}

test('real showMap restores a table selection hidden in collapsed diagram and its scroll position',async()=>{
 const f=fixture();await f.show();const m=f.state.map;
 assert.equal(m.displayMode,'table');assert.equal(m.layoutMode,'bilateral');assert.equal(m.selectedNode,'deep');assert.equal(m.collapsed.has('branch'),true);assert.equal(m.layout.positions.has('deep'),false);
 assert.ok(f.rows().some(e=>e.attrs['data-structured-node']==='deep'&&e.attrs['aria-pressed']==='true'));
 assert.equal(f.$('map-node-title').textContent,'Ёлка <script>');assert.equal(f.$('map-canvas').hidden,true);assert.equal(f.$('map-structured-host').hidden,false);
 assert.equal(f.$('map-structured-host').scrollLeft,200);assert.equal(f.$('map-structured-host').scrollTop,600);assert.equal(m.viewScroll,null);
 assert.equal(f.$('map-fit').disabled,true);assert.equal(f.$('map-layout-mode').disabled,true);assert.equal(f.$('map-view-svg').disabled,true);assert.equal(f.errors.length,0);
});

test('same-map showMap refresh retains live view options, folds, details and selection without overwriting source topology',async()=>{
 const f=fixture(230);await f.show();const previous=f.state.map;
 Object.assign(previous,{displayMode:'outline',layoutMode:'down',tableSort:'sources',tableDescending:true,tableQuery:'Узел',tablePage:2,selectedNode:'n220',scale:.45,depth:null,details:true});
 previous.collapsed=new Set(['root','branch']);const topology=plain(previous.view.nodes);f.views.set('map',f.makeView('map',4));await f.show();
 const m=f.state.map;assert.notEqual(m,previous);assert.equal(m.view.map.revision,4);assert.equal(m.viewState,previous.viewState);
 for(const key of ['displayMode','layoutMode','tableSort','tableDescending','tableQuery','tablePage','selectedNode','scale','depth','details'])assert.equal(m[key],previous[key],key);
 assert.deepEqual([...m.collapsed],['root','branch']);assert.deepEqual(plain(m.view.nodes),topology);assert.equal(f.calls.filter(c=>c.path.endsWith('/view-state')).length,1);assert.equal(f.calls.filter(c=>c.body).length,0);
});

test('real mode and layout controls preserve selected node and map data; switching to diagram reveals its ancestry',async()=>{
 const f=fixture();await f.show();const data=plain(f.state.map.view);
 await f.fire('map-display-mode','change','outline');assert.equal(f.state.map.selectedNode,'deep');assert.equal(f.state.map.layoutMode,'bilateral');assert.equal(f.rows().length,4);
 await f.fire('map-display-mode','change','diagram');assert.equal(f.state.map.selectedNode,'deep');assert.equal(f.state.map.collapsed.has('branch'),false);assert.equal(f.state.map.layout.positions.has('deep'),true);assert.equal(f.$('map-canvas').hidden,false);assert.equal(f.$('map-structured-host').hidden,true);
 assert.equal(f.$('map-layout-mode').disabled,false);assert.equal(f.$('map-view-svg').disabled,false);
 await f.fire('map-layout-mode','change','down');assert.equal(f.state.map.layoutMode,'down');assert.equal(f.state.map.selectedNode,'deep');
 const chosen=f.$('map-canvas').querySelectorAll('[data-map-node]').find(e=>e.dataset.mapNode==='deep');assert.equal(chosen.attrs['aria-pressed'],'true');assert.equal(chosen.classList.contains('selected'),true);
 await f.fire('map-display-mode','change','table');assert.equal(f.state.map.layoutMode,'down');assert.equal(f.state.map.selectedNode,'deep');assert.equal(f.$('map-fit').disabled,true);assert.deepEqual(plain(f.state.map.view),data);assert.equal(f.calls.filter(c=>c.body).length,0);
});

test('real inspector navigation reveals a filtered node on its correct page without changing tree order',async()=>{
 const f=fixture(230);await f.show();const m=f.state.map,data=plain(m.view.nodes);m.tableSort='label';m.tableDescending=true;m.tableQuery='Ёлка';m.tablePage=0;f.draw();assert.equal(f.rows().length,1);
 f.select('n220',true);assert.equal(m.selectedNode,'n220');assert.equal(m.details,true);assert.equal(m.tableQuery,'');assert.equal(m.tableSort,'label');assert.equal(m.tableDescending,true);assert.match(f.$('map-structured-host').textContent,/Фильтр сброшен/);
 assert.ok(f.rows().some(e=>e.attrs['data-structured-node']==='n220'&&e.attrs['aria-pressed']==='true'));assert.deepEqual(plain(m.view.nodes),data);assert.equal(f.calls.filter(c=>c.body).length,0);
});

test('late map and saved-view GETs cannot publish after sidebar, workspace, screen or identity changes',async()=>{
 const changes=[f=>f.state.view='library',f=>f.state.selected='other-ws',f=>f.state.revision++,f=>f.state.session.user='bob',f=>f.state.session.csrf='new-csrf',f=>f.state.session.authenticated=false];
 for(const stage of ['map','view-state'])for(const change of changes){
  const f=fixture(),pending=deferred();f.handle=(path,body)=>(path.endsWith('/view-state')===(stage==='view-state'))?pending.promise:f.respond(path,body);
  const showing=f.show();await settle();change(f);pending.resolve(stage==='map'?f.makeView():saved());await showing;
  assert.equal(f.state.map,undefined,stage);assert.equal(f.$('map-title').textContent,'',stage);assert.equal(f.rows().length,0,stage);assert.equal(f.calls.filter(c=>c.body).length,0,stage);
 }
});

test('newer map opening wins whether an older request waits on map data or saved view',async()=>{
 for(const stage of ['map','view-state']){
  const f=fixture(),pending=deferred();f.handle=(path,body)=>path.includes('/maps/map')&&(path.endsWith('/view-state')===(stage==='view-state'))?pending.promise:f.respond(path,body);
  const old=f.show('map');await settle();await f.show('other');const winner=f.state.map;assert.equal(winner.view.map.id,'other');
  pending.resolve(stage==='map'?f.makeView():saved());await old;assert.equal(f.state.map,winner);assert.equal(f.$('map-title').textContent,'Карта other');assert.equal(f.state.map.selectedNode,'deep');
 }
});

test('saved-view read failure keeps the map usable but disables saves until a successful explicit load',async()=>{
 const f=fixture();f.handle=async(path,body)=>{if(path.endsWith('/view-state'))throw Error('429: занято');return f.respond(path,body);};await f.show();
 assert.equal(f.state.map.displayMode,'diagram');assert.equal(f.$('map-view-save').disabled,true);assert.match(f.$('map-view-status').textContent,/429/);assert.ok(f.$('map-canvas').querySelectorAll('[data-map-node]').length>0);
 f.handle=null;await f.fire('map-view-restore');assert.equal(f.state.map.displayMode,'table');assert.equal(f.state.map.selectedNode,'deep');assert.equal(f.$('map-view-save').disabled,false);assert.equal(f.errors.length,0);
});

test('view assets have defer order, server embedding and explicit route wiring before app startup',()=>{
 const html=fs.readFileSync(__dirname+'/assets/index.html','utf8'),server=fs.readFileSync(__dirname+'/server.go','utf8');
 const scripts=[...html.matchAll(/<script\b[^>]*src="([^"]+)"[^>]*>/g)].map(m=>({url:m[1],tag:m[0]}));
 const appIndex=scripts.findIndex(s=>s.url==='/assets/app.js');assert.ok(appIndex>0);
 for(const file of ['map-layout.js','map-structured-view.js','map-view-controls.js','map-view-export.js','map-navigation.js']){
  const matches=scripts.filter(s=>s.url==='/assets/'+file);assert.equal(matches.length,1,file);assert.match(matches[0].tag,/\bdefer\b/);assert.ok(scripts.indexOf(matches[0])<appIndex,file);
  assert.ok(server.split('\n').find(line=>line.startsWith('//go:embed ')).includes('assets/'+file),file+' embed');assert.ok(server.includes('"/assets/'+file+'": "assets/'+file+'"'),file+' route');
 }
 assert.ok(html.includes('<link rel="stylesheet" href="/assets/map-structured-view.css">'));assert.ok(server.includes('"/assets/map-structured-view.css": "assets/map-structured-view.css"'));
 for(const id of ['map-structured-host','map-display-mode','map-layout-mode','map-view-save','map-view-restore','map-view-discard','map-view-svg','map-view-status'])assert.equal([...html.matchAll(new RegExp('id="'+id+'"','g'))].length,1,id);
 assert.ok(app.indexOf('const mapStructured=installMemMapStructuredView(')<app.lastIndexOf('run(boot)'));assert.ok(app.indexOf('const mapViewControls=installMemMapViewControls(')<app.lastIndexOf('run(boot)'));
});

test('navigation back and forward restores live table, inspector and scroll without content writes',async()=>{
 const f=fixture(230);await f.show();const data=plain(f.state.map.view);
 f.$('map-structured-host').scrollTop=960;f.$('map-inspector').scrollTop=360;
 f.select('n220',true);assert.equal(f.$('map-nav-status').textContent,'Переход 2 из 2');
 await f.fire('map-nav-back');assert.equal(f.state.map.selectedNode,'deep');assert.equal(f.state.map.details,false);assert.equal(f.$('map-structured-host').scrollTop,960);assert.equal(f.$('map-inspector').scrollTop,360);
 await f.fire('map-nav-forward');assert.equal(f.state.map.selectedNode,'n220');assert.equal(f.state.map.tablePage,2);assert.equal(f.state.map.details,true);
 assert.deepEqual(plain(f.state.map.view),data);assert.equal(f.calls.filter(c=>c.body).length,0);assert.deepEqual(f.errors,[]);
});

test('mode and layout changes, then named-view application, are individually reversible navigation',async()=>{
 const f=fixture();await f.show();await f.fire('map-display-mode','change','diagram');await f.fire('map-layout-mode','change','down');
 f.context.mapViewControls.apply(f.state.map,saved({state:{display_mode:'outline',selected_node_id:'root',scale:.4}}));
 await f.fire('map-nav-back');assert.equal(f.state.map.displayMode,'diagram');assert.equal(f.state.map.layoutMode,'down');assert.equal(f.state.map.selectedNode,'deep');
 await f.fire('map-nav-back');assert.equal(f.state.map.layoutMode,'bilateral');
 await f.fire('map-nav-back');assert.equal(f.state.map.displayMode,'table');assert.equal(f.state.map.collapsed.has('branch'),true);assert.equal(f.state.map.scale,.7);
 assert.equal(f.$('map-nav-back').disabled,true);assert.equal(f.calls.filter(c=>c.body).length,0);
});

test('table sort, filter and pagination can be restored through real controls',async()=>{
 const f=fixture(230);await f.show();const find=label=>f.elements.find(e=>f.$('map-structured-host').contains(e)&&e.attrs['aria-label']===label);
 await f.fire(find('Сортировка таблицы'),'change','sources');
 const next=f.elements.find(e=>f.$('map-structured-host').contains(e)&&e.tag==='button'&&e.textContent==='Следующие 100');await f.fire(next);
 const query=find('Фильтр узлов: до 200 символов');query.value='Ёлка';const form=f.elements.find(e=>e.tag==='form'&&e.contains(query));await f.fire(form,'submit');assert.equal(f.state.map.tableQuery,'Ёлка');
 await f.fire('map-nav-back');assert.equal(f.state.map.tableQuery,'');assert.equal(f.state.map.tablePage,1);assert.equal(f.state.map.tableSort,'sources');
 await f.fire('map-nav-back');assert.equal(f.state.map.tablePage,0);assert.equal(f.state.map.tableSort,'sources');
 await f.fire('map-nav-back');assert.equal(f.state.map.tableSort,'tree');
});

test('cross-map navigation re-fetches current maps and new selection discards the forward branch',async()=>{
 const f=fixture();await f.show();f.select('root');await f.show('other');
 await f.fire('map-nav-back');assert.equal(f.state.map.view.map.id,'map');assert.equal(f.state.map.selectedNode,'root');assert.equal(f.$('map-nav-forward').disabled,false);
 f.select('branch');assert.equal(f.$('map-nav-forward').disabled,true);assert.equal(f.$('map-nav-status').textContent,'Переход 3 из 3');
 await f.fire('map-nav-list');assert.equal(f.$('map-nav-dialog').open,true);assert.equal(f.$('map-nav-items').children.length,3);assert(!f.$('map-nav-items').textContent.includes('Карта other'));assert.equal(f.calls.filter(c=>c.body).length,0);
});

test('source entry survives map reload and reopens the exact source through mirrored controls',async()=>{
 const f=fixture();await f.show();f.context.mapNavigation.checkpoint();f.source={map_id:'map',node_id:'deep',map_revision:3,source_id:'source-2',scroll:.3,passages_scroll:.6};f.context.mapNavigation.enter();
 await f.show('other');await f.fire('map-document-nav-back');assert.equal(f.state.map.view.map.id,'map');assert.equal(f.source.source_id,'source-2');
 await f.fire('map-document-nav-back');assert.equal(f.source,null);await f.fire('map-nav-forward');assert.equal(f.source.source_id,'source-2');assert.equal(f.source.passages_scroll,.6);assert.equal(f.sourceOpens.length,2);
});

test('changed map and removed node fall back to current root without opening a stale source',async()=>{
 const f=fixture();await f.show();f.context.mapNavigation.checkpoint();f.source={map_id:'map',node_id:'deep',map_revision:3,source_id:'s1',scroll:0,passages_scroll:0};f.context.mapNavigation.enter();await f.show('other');
 const next=f.makeView('map',4);next.nodes=next.nodes.filter(n=>n.id!=='deep');next.map.node_count--;f.views.set('map',next);
 await f.fire('map-nav-back');assert.equal(f.state.map.view.map.revision,4);assert.equal(f.state.map.selectedNode,'root');assert.equal(f.source,null);assert(!f.sourceOpens);assert.match(f.$('map-nav-status').textContent,/Узел удалён/);assert.match(f.$('map-nav-status').textContent,/Источник прежней ревизии не открыт/);
});

test('failed source and deleted map give explicit fallback and allow later history retry',async()=>{
 const f=fixture();await f.show();f.context.mapNavigation.checkpoint();f.source={map_id:'map',node_id:'deep',map_revision:3,source_id:'missing',scroll:0,passages_scroll:0};f.context.mapNavigation.enter();await f.show('other');
 f.handle=async(path,body)=>{if(path.endsWith('/maps/map'))throw Error('Карта недоступна');return f.respond(path,body);};await f.fire('map-nav-back');assert.equal(f.state.map.view.map.id,'other');assert.match(f.$('map-nav-status').textContent,/Карта недоступна/);assert.equal(f.$('map-nav-back').disabled,false);
 f.handle=null;f.sourceHandle=async()=>false;await f.fire('map-nav-back');assert.equal(f.state.map.view.map.id,'map');assert.equal(f.source,null);assert.match(f.$('map-nav-status').textContent,/другой фрагмент не подставлен/);
});

test('ordinary map refresh replaces current history entry and retains forward navigation',async()=>{
 const f=fixture();await f.show();f.select('root');f.select('branch');await f.fire('map-nav-back');await f.show();assert.equal(f.$('map-nav-forward').disabled,false);
 await f.fire('map-nav-list');assert.equal(f.$('map-nav-items').children.length,3);await f.fire('map-nav-forward');assert.equal(f.state.map.selectedNode,'branch');
});

test('history is bounded, literal text is safe, clear keeps only the current location',async()=>{
 const f=fixture();await f.show();for(let i=0;i<105;i++)f.select(i%2?'root':'deep');await f.fire('map-nav-list');assert.equal(f.$('map-nav-items').children.length,100);assert.match(f.$('map-nav-items').textContent,/<script>/);
 await f.fire('map-nav-clear');assert.equal(f.$('map-nav-items').children.length,1);assert.equal(f.$('map-nav-back').disabled,true);assert.equal(f.$('map-nav-forward').disabled,true);
});

test('workspace, user, CSRF and logout clear history; leaving and re-entering maps in same scope retains it',async()=>{
 for(const change of [f=>f.state.selected='ws2',f=>f.state.session.user='bob',f=>f.state.session.csrf='csrf-b',f=>f.state.session.authenticated=false]){const f=fixture();await f.show();f.select('root');change(f);f.context.mapNavigation.leave();assert.equal(f.$('map-nav-status').textContent,'');assert.equal(f.$('map-nav-back').disabled,true);}
 const f=fixture();await f.show();f.select('root');f.context.mapNavigation.leave();f.state.view='library';f.state.revision++;f.state.view='maps';await f.show('other');await f.fire('map-nav-back');assert.equal(f.state.map.view.map.id,'map');assert.equal(f.state.map.selectedNode,'root');
});

test('late history GET cannot replace a newer direct map open, selection or sidebar navigation',async()=>{
 for(const action of ['map','node','sidebar']){
  const f=fixture();await f.show();await f.show('other');const pending=deferred();f.handle=(path,body)=>path.endsWith('/maps/map')?pending.promise:f.respond(path,body);
  const jumping=f.context.mapNavigation.jump(0);await settle();if(action==='map')await f.show('other');else if(action==='node')f.select('root');else{f.context.mapNavigation.leave();f.state.view='library';f.state.revision++;}
  const winner=f.state.map;pending.resolve(f.makeView());await jumping;assert.equal(f.state.map,winner,action);assert.equal(f.state.map.view.map.id,'other',action);if(action==='node')assert.equal(f.state.map.selectedNode,'root');assert.equal(f.errors.length,0);
 }
});

test('late source return is invalidated by a new map open',async()=>{
 const f=fixture();await f.show();f.context.mapNavigation.checkpoint();f.source={map_id:'map',node_id:'deep',map_revision:3,source_id:'s1',scroll:0,passages_scroll:0};f.context.mapNavigation.enter();await f.show('other');
 const pending=deferred();let current;f.sourceHandle=async(ref,options)=>{current=options.isCurrent;await pending.promise;return current();};const jumping=f.context.mapNavigation.jump(1);await settle();assert.equal(current(),true);await f.show('other');assert.equal(current(),false);pending.resolve();await jumping;assert.equal(f.state.map.view.map.id,'other');assert.equal(f.source,null);
});

test('branch folding, zoom and depth navigation preserve their complete preceding view',async()=>{
 const f=fixture();await f.show();await f.fire('map-display-mode','change','diagram');const snapshot=()=>plain(f.context.mapViewControls.snapshot(f.state.map));const initial=snapshot();
 f.context.toggleMapBranch('branch');const folded=snapshot();assert.equal(f.state.map.selectedNode,'branch');f.context.scaleMap(.35);const scaled=snapshot();f.context.showMapDepth(1,true);
 await f.fire('map-nav-back');assert.deepEqual(snapshot(),scaled);await f.fire('map-nav-back');assert.deepEqual(snapshot(),folded);await f.fire('map-nav-back');assert.deepEqual(snapshot(),initial);assert.equal(f.state.map.selectedNode,'deep');
});

test('large history is trimmed by metadata budget before the 100-entry limit',async()=>{
 const f=fixture();await f.show();const original=f.context.mapViewControls.snapshot;f.context.mapViewControls.snapshot=m=>({...original(m),collapsed:Array.from({length:1000},(_,i)=>'id-'+i+'x'.repeat(100))});
 for(let i=0;i<80;i++)f.select(i%2?'root':'deep');await f.fire('map-nav-list');const count=f.$('map-nav-items').children.length;assert(count>1&&count<80);assert.equal(f.$('map-nav-items').children[0].attrs['aria-current'],'step');
});

test('closing history releases detached rows, and identity reset purges previously rendered metadata',async()=>{
 const f=fixture();await f.show();f.select('root');await f.fire('map-nav-list');assert.equal(f.$('map-nav-items').children.length,2);const oldRow=f.$('map-nav-items').children[1];
 await f.fire('map-nav-close');assert.equal(f.$('map-nav-items').children.length,0);assert.equal(f.$('map-nav-dialog').open,false);await f.fire('map-nav-list');assert.equal(f.$('map-nav-items').children.length,2);
 f.state.session.authenticated=false;f.context.mapNavigation.leave();assert.equal(f.$('map-nav-items').children.length,0);const before=f.calls.length;await f.fire(oldRow);assert.equal(f.calls.length,before);assert.equal(f.$('map-nav-status').textContent,'');
});
