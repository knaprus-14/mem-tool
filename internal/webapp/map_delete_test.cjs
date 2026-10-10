const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const settle=()=>new Promise(r=>setImmediate(r));
function fixture(){
 const elements=new Map();function $(id){if(!elements.has(id))elements.set(id,{id,dataset:{},hidden:false,events:{},addEventListener(k,fn){this.events[k]=fn;},append(){},querySelectorAll(){return [...elements.values()];},showModal(){this.open=true;},close(){this.open=false;}});return elements.get(id);}
 const f={state:{selected:'ws',revision:1,map:null},calls:[],loaded:0,$};
 const context=vm.createContext({crypto:{randomUUID:()=> 'a'.repeat(32)}});
 vm.runInContext(fs.readFileSync(__dirname+'/assets/map-library.js','utf8'),context);
 f.library=context.installMemMapLibrary({state:f.state,$,node(){return $('dynamic-'+elements.size);},api:async(path,body)=>{f.calls.push({path,body});if(f.fail)throw Error('network');return {};},run:fn=>fn,showMap(){},error(){},loadView:async()=>{f.loaded++;}});
 f.fire=(id,event='click')=>$(id).events[event]({preventDefault(){}});return f;
}
test('library deletion requires confirmation and cancellation leaves the map intact',async()=>{
 const f=fixture();f.library.deleteMap({id:'map-1',title:'Chosen map',revision:7});assert.equal(f.$('map-library-context').textContent,'Chosen map');assert.equal(f.calls.length,0);
 f.fire('map-delete-cancel');f.fire('map-delete-form','submit');await settle();assert.equal(f.calls.length,0);
 f.library.deleteMap({id:'map-1',title:'Chosen map',revision:7});f.fire('map-delete-form','submit');await settle();
 assert.equal(f.calls[0].path,'workspaces/ws/maps/map-1/delete');assert.equal(f.calls[0].body.revision,7);assert.equal(f.loaded,1);
});
test('lost response retries the same deletion receipt',async()=>{
 const f=fixture();f.fail=true;f.library.deleteMap({id:'map-1',title:'Map',revision:1});f.fire('map-delete-form','submit');await settle();
 assert.equal(f.$('map-library-retry').hidden,false);f.fail=false;await f.fire('map-library-retry');await settle();
 assert.equal(f.calls.length,2);assert.deepEqual(f.calls[0],f.calls[1]);assert.equal(f.loaded,1);
});
test('navigation invalidates an old confirmation',async()=>{
 const f=fixture();f.library.deleteMap({id:'map-1',title:'Map',revision:1});f.state.selected='other';f.state.revision++;
 f.fire('map-delete-form','submit');await settle();assert.equal(f.calls.length,0);
});
