'use strict';
function installMemMapDocument({state,$,node,api,run,annotations,readingWorkspace,renderMapBody,navigation=()=>null}) {
 const dialog=$('map-document-dialog');let current=null;
 const valid=c=>c&&current===c&&dialog.open&&c.workspace===state.selected&&c.screen===state.revision&&c.map===state.map?.view.map.id&&c.node===state.map?.selectedNode&&c.mapRevision===state.map?.view.map.revision&&state.session?.authenticated&&c.user===(state.session.user||'')&&c.csrf===(state.session.csrf||'')&&c.guard();
 const change=fn=>{const nav=navigation();return nav?nav.change(fn):fn();};
 const scroll=el=>Math.max(0,Math.min(1,(el.scrollTop||0)/Math.max(1,(el.scrollHeight||0)-(el.clientHeight||0))));
 function capture(){const c=current;if(!valid(c)||!c.resolved)return null;return {map_id:c.map,node_id:c.node,map_revision:c.mapRevision,source_id:c.resolved,title:c.title,page:c.page,scroll:scroll(dialog),passages_scroll:scroll($('map-document-passages'))};}
 function release(c){c?.controller?.abort();c?.reader?.close();}
 function close(){release(current);current=null;dialog.close();}
 $('map-document-close').addEventListener('click',()=>change(close));dialog.addEventListener('cancel',e=>{e.preventDefault();change(close);});dialog.addEventListener('close',()=>{if(!dialog.open){release(current);current=null;}});
 function route(c){return 'workspaces/'+c.workspace+'/maps/'+encodeURIComponent(c.map)+'/nodes/'+encodeURIComponent(c.node)+'/sources';}
 async function showSource(c,{track=true,restore=null}={}){
  if(!valid(c))return false;if(track)navigation()?.start();release(c);c.resolved=null;c.controller=new AbortController();const source=$('map-document-picker').value,serial=++c.serial;
  $('map-document-error').hidden=true;$('map-document-original').hidden=true;$('map-document-original').removeAttribute('href');$('map-document-passages').replaceChildren();$('map-document-location').textContent='';$('map-document-notice').textContent='Открываем исходный фрагмент…';
  try{
   const base=route(c)+'/'+encodeURIComponent(source),query='?revision='+c.mapRevision;
   const result=await api(base+'/document'+query,undefined,false,c.controller.signal);if(!valid(c)||serial!==c.serial)return;
   $('map-document-location').textContent=result.title+(result.page?' · Страница '+result.page:' · Без номера страницы');
   $('map-document-notice').textContent=result.notice||'Сопоставляйте текст узла с выделенным подтверждением и соседними фрагментами страницы.';
   const rows=$('map-document-passages');let match;
   if(!result.passages.length){rows.append(node('p','Сохранённое подтверждение','muted'),node('pre',result.excerpt,'map-document-match'));}
   if(result.passages.length&&annotations){const selected=state.map.tree.byID.get(c.node);c.reader=annotations.mountReader({host:rows,passages:result.passages,documentID:result.document_id,targetNode:{mapID:c.map,nodeID:c.node,revision:c.mapRevision,label:selected.label,locked:!!selected.locked},isCurrent:()=>valid(c)&&serial===c.serial});}
   else for(const part of result.passages){const row=node('section',undefined,part.match?'map-document-match':'map-document-passage');if(part.marker)row.append(node('p',part.marker,'muted'));if(part.match)row.append(node('strong','Подтверждение для выбранного узла'));row.append(node('pre',part.text));rows.append(row);if(part.match&&!match)match=row;}
   if(match)match.scrollIntoView({block:'nearest'});
   if(readingWorkspace)for(const [i,part] of result.passages.entries()){
    if(!part.pin||!part.entry_id)continue;
    const save=node('button','Сохранить фрагмент '+(i+1)+' в рабочее место чтения','secondary');save.type='button';
    save.addEventListener('click',run(async()=>{if(!valid(c)||serial!==c.serial)return;await readingWorkspace.capture({id:part.entry_id,pin:part.pin,text:part.text,title:result.title,page:result.page},{mapRef:{map_id:c.map,node_id:c.node,revision:c.mapRevision}});}));rows.append(save);
   }
   if(result.original_pdf){const link=$('map-document-original');link.href='/api/v1/'+base+'/file'+query+(result.page?'#page='+result.page:'');link.textContent=result.page?'Открыть оригинал PDF — страница '+result.page+' ↗':'Открыть оригинал PDF ↗';link.hidden=false;}
   c.resolved=source;c.title=Array.from(String(result.title||'Источник')).slice(0,256).join('');c.page=Number.isSafeInteger(result.page)&&result.page>0?result.page:0;c.guard=()=>true;if(restore){dialog.scrollTop=restore.scroll*Math.max(0,dialog.scrollHeight-dialog.clientHeight);$('map-document-passages').scrollTop=restore.passages_scroll*Math.max(0,$('map-document-passages').scrollHeight-$('map-document-passages').clientHeight);}
   if(track)navigation()?.enter();return true;
  }catch(e){if(valid(c)&&serial===c.serial&&e.name!=='AbortError'){$('map-document-notice').textContent='Источник не открыт.';$('map-document-error').textContent=e.message;$('map-document-error').hidden=false;}return false;}
 }
 $('map-document-picker').addEventListener('change',run(()=>showSource(current)));
 async function open(ref=null,{track=true,isCurrent=()=>true}={}){
  const m=state.map,n=m?.tree.byID.get(m.selectedNode);if(!n||!state.session?.authenticated||!isCurrent())return false;
  if(ref&&(ref.map_id!==m.view.map.id||ref.node_id!==m.selectedNode||ref.map_revision!==m.view.map.revision||typeof ref.source_id!=='string'||!ref.source_id||ref.source_id.length>256||![ref.scroll,ref.passages_scroll].every(v=>Number.isFinite(v)&&v>=0&&v<=1)))return false;
  if(track)navigation()?.start();
  release(current);const c={workspace:state.selected,screen:state.revision,map:m.view.map.id,mapRevision:m.view.map.revision,node:m.selectedNode,user:state.session.user||'',csrf:state.session.csrf||'',guard:isCurrent,resolved:null,serial:0,controller:new AbortController()};current=c;
  $('map-document-node-title').textContent=n.label;$('map-document-node-summary').textContent=n.summary||'';if(renderMapBody)renderMapBody($('map-document-node-body'),n.body||'У узла нет подробного текста.');else $('map-document-node-body').textContent=n.body||'У узла нет подробного текста.';
  $('map-document-error').hidden=true;$('map-document-original').hidden=true;$('map-document-passages').replaceChildren();$('map-document-location').textContent='';$('map-document-picker').replaceChildren();$('map-document-picker').disabled=true;$('map-document-notice').textContent='Загружаем источники узла…';if(!dialog.open)dialog.showModal();
  try{
   const page=await api(route(c),undefined,false,c.controller.signal);if(!valid(c))return;
   if(page.revision!==c.mapRevision)throw new Error('Карта изменилась. Закройте сравнение и обновите карту.');
   const sources=page.items.filter(s=>s.kind==='evidence');
   for(const [i,s] of sources.entries()){const option=node('option',(i+1)+'. '+s.title+(s.page?' · стр. '+s.page:''));option.value=s.id;$('map-document-picker').append(option);}
   if(ref&&!sources.some(s=>s.id===ref.source_id))throw Error('Сохранённый источник больше не связан с узлом. Другой фрагмент не открыт.');
   if(!sources.length){$('map-document-notice').textContent='У этого узла нет привязки к фрагментам документов. Прикрепите подтверждение через «Источники узла».';return;}
   $('map-document-picker').disabled=false;if(ref)$('map-document-picker').value=ref.source_id;return await showSource(c,{track,restore:ref});
  }catch(e){if(valid(c)&&e.name!=='AbortError'){$('map-document-notice').textContent='Не удалось открыть источники.';$('map-document-error').textContent=e.message;$('map-document-error').hidden=false;}return false;}
 }
 $('map-document-open').addEventListener('click',run(()=>open()));
 return {open,close,capture};
}
