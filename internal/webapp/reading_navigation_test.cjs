'use strict';
const fs=require('node:fs'),vm=require('node:vm'),test=require('node:test'),assert=require('node:assert/strict');
const app=fs.readFileSync(__dirname+'/assets/app.js','utf8');
const source=app.slice(app.indexOf('  async function openReadingMap('),app.indexOf('  function renderMapBody('));
function fixture({offset=0,nodeExists=true,onLoad,onShow}={}){
  const events=[],state={selected:'workspace',session:{authenticated:true,csrf:'session'},revision:1,offset,view:'documents',document:'doc'};
  const tree={byID:new Map(nodeExists?[['node',{}]]:[])},context={state,mapNavigation:{change:fn=>fn(),checkpoint(){}},readingWorkspace:{close(){}},$:()=>({close(){}}),
    loadView:async()=>{state.revision++;if(state.offset>0){state.offset=0;state.revision++;}if(onLoad)onLoad(state);},
    showMap:async id=>{events.push(['show',id]);state.map={view:{map:{id,revision:7}},tree,collapsed:new Set(['root'])};if(onShow)onShow(state);},
    MemMapLayout:{reveal:(_tree,_collapsed,id)=>{events.push(['reveal',id]);return new Set();}},drawMap:()=>events.push(['draw']),selectMapNode:(id,details)=>events.push(['select',id,details]),centerMapNode:id=>events.push(['center',id]),error:message=>events.push(['error',message])};
  vm.runInNewContext(source+'\nglobalThis.open=openReadingMap;',context);return {state,events,open:context.open};
}
test('saved map link opens target after a later library page and reveals selected node',async()=>{const f=fixture({offset:30});await f.open({workspace:'workspace',mapRef:{map_id:'map',node_id:'node',revision:7},mapState:'current'});assert.equal(f.state.offset,0);assert.deepEqual(f.events,[['show','map'],['reveal','node'],['draw'],['select','node',true],['center','node']]);});
test('changed and removed nodes preserve a visible explanation and never restore old map data',async()=>{const f=fixture();await f.open({mapRef:{map_id:'map',node_id:'node',revision:6},mapState:'changed'});assert.equal(f.state.map.view.map.revision,7);assert.match(f.events.at(-1)[1],/ревизии 6/);const missing=fixture({nodeExists:false});await missing.open({mapRef:{map_id:'map',node_id:'node',revision:7}});assert.match(missing.events.at(-1)[1],/узел удалён/);assert(!missing.events.some(e=>e[0]==='select'));});
test('late navigation cannot select a node after workspace or session changes',async()=>{const f=fixture({onLoad:s=>s.selected='other'});await f.open({mapRef:{map_id:'map',node_id:'node'}});assert.deepEqual(f.events,[]);const g=fixture({onShow:s=>s.session.csrf='other-session'});await g.open({mapRef:{map_id:'map',node_id:'node'}});assert.deepEqual(g.events,[['show','map']]);});
