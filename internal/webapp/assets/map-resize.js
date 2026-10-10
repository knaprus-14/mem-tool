'use strict';
function installMemMapResize({$}) {
  const section=$('map-section'),layout=$('map-inspector').parentElement,handle=$('map-inspector-divider');
  const storageKey='mem.mapInspectorWidth';let preferred=360,drag=null;
  try{const saved=Number(localStorage.getItem(storageKey));if(Number.isFinite(saved)&&saved>=220&&saved<=900)preferred=saved;}catch{}
  function bounds(){const width=layout.clientWidth,mobile=matchMedia('(max-width:750px)').matches;return {min:Math.min(mobile?220:280,Math.max(0,width-60)),max:Math.max(0,Math.min(900,width-(mobile?48:252)))};}
  function refresh(){
    if(section.hidden||handle.hidden)return;
    const b=bounds(),min=Math.min(b.min,b.max),width=Math.round(Math.max(min,Math.min(b.max,preferred)));
    layout.style.setProperty('--map-inspector-width',width+'px');
    handle.setAttribute('aria-valuemin',String(min));handle.setAttribute('aria-valuemax',String(Math.floor(b.max)));handle.setAttribute('aria-valuenow',String(width));handle.setAttribute('aria-valuetext',width+' пикселей');
  }
  function save(){try{localStorage.setItem(storageKey,String(preferred));}catch{}}
  function change(width){const b=bounds();preferred=Math.round(Math.max(Math.min(b.min,b.max),Math.min(b.max,width)));refresh();}
  function stop(){if(!drag)return;const id=drag.id;drag=null;section.classList.remove('resizing-inspector');if(handle.hasPointerCapture(id))handle.releasePointerCapture(id);save();}
  handle.addEventListener('pointerdown',e=>{
    if(e.button!==0||section.hidden||handle.hidden)return;e.preventDefault();handle.focus();
    drag={id:e.pointerId,x:e.clientX,width:$('map-inspector').getBoundingClientRect().width};handle.setPointerCapture(e.pointerId);section.classList.add('resizing-inspector');
  });
  handle.addEventListener('pointermove',e=>{if(!drag||drag.id!==e.pointerId)return;if(section.hidden||handle.hidden){stop();return;}change(drag.width+drag.x-e.clientX);});
  for(const event of ['pointerup','pointercancel','lostpointercapture'])handle.addEventListener(event,stop);
  handle.addEventListener('keydown',e=>{
    const b=bounds(),width=$('map-inspector').getBoundingClientRect().width;
    const values={ArrowLeft:width+(e.shiftKey?80:24),ArrowRight:width-(e.shiftKey?80:24),Home:b.min,End:b.max};
    if(!(e.key in values))return;e.preventDefault();change(values[e.key]);save();
  });
  handle.addEventListener('dblclick',()=>{preferred=360;refresh();save();});
  new ResizeObserver(refresh).observe(layout);
  return {refresh};
}
