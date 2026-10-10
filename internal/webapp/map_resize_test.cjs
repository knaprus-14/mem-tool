const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
function fixture(saved){
 const values=new Map(saved?[['mem.mapInspectorWidth',String(saved)]]:[]),events={},attrs={};let resize;
 const layout={clientWidth:1200,style:{setProperty(k,v){this[k]=v;}}};
 const section={hidden:false,classList:{add(){},remove(){}}};
 const handle={hidden:false,addEventListener(k,fn){events[k]=fn;},setAttribute(k,v){attrs[k]=v;},focus(){},setPointerCapture(){},hasPointerCapture(){return false;}};
 const inspector={parentElement:layout,getBoundingClientRect(){return {width:parseFloat(layout.style['--map-inspector-width'])||360};}};
 const context=vm.createContext({localStorage:{getItem:k=>values.get(k),setItem:(k,v)=>values.set(k,v)},matchMedia:()=>({matches:layout.clientWidth<750}),ResizeObserver:class{constructor(fn){resize=fn;}observe(){}}});
 vm.runInContext(fs.readFileSync(__dirname+'/assets/map-resize.js','utf8'),context);
 const result=context.installMemMapResize({$:id=>({'map-section':section,'map-inspector':inspector,'map-inspector-divider':handle}[id])});result.refresh();
 return {values,layout,handle,section,attrs,refresh:result.refresh,resize:()=>resize(),fire:(type,props={})=>events[type]({preventDefault(){},button:0,pointerId:1,...props})};
}
test('dragging widens the panel, clamps extremes and persists on release',()=>{
 const f=fixture();f.fire('pointerdown',{clientX:800});f.fire('pointermove',{clientX:550});assert.equal(f.attrs['aria-valuenow'],'610');
 f.fire('pointermove',{clientX:-2000});assert.equal(f.attrs['aria-valuenow'],'900');
 f.fire('pointerup');assert.equal(f.values.get('mem.mapInspectorWidth'),'900');
 const reopened=fixture(f.values.get('mem.mapInspectorWidth'));assert.equal(reopened.attrs['aria-valuenow'],'900');
});
test('keyboard controls and double click reset the width',()=>{
 const f=fixture();f.fire('keydown',{key:'ArrowLeft'});assert.equal(f.attrs['aria-valuenow'],'384');
 f.fire('keydown',{key:'ArrowRight',shiftKey:true});assert.equal(f.attrs['aria-valuenow'],'304');
 f.fire('keydown',{key:'End'});assert.equal(f.attrs['aria-valuenow'],'900');
 f.fire('keydown',{key:'Home'});assert.equal(f.attrs['aria-valuenow'],'280');
 f.fire('dblclick');assert.equal(f.attrs['aria-valuenow'],'360');
});
test('narrow viewport clamps display without overwriting preferred desktop width',()=>{
 const f=fixture(600);f.layout.clientWidth=374;f.resize();assert.equal(f.attrs['aria-valuenow'],'326');assert.equal(f.values.get('mem.mapInspectorWidth'),'600');
 f.layout.clientWidth=1200;f.resize();assert.equal(f.attrs['aria-valuenow'],'600');
});
test('hiding the panel during a drag prevents further resizing',()=>{
 const f=fixture();f.fire('pointerdown',{clientX:800});f.handle.hidden=true;f.fire('pointermove',{clientX:500});
 assert.equal(f.attrs['aria-valuenow'],'360');
});
