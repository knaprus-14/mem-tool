'use strict';
function installMemMapViewControls({state,$,node,api,run,drawMap,selectMapNode,centerMapNode,change=fn=>fn()}) {
  const records=new Map();let generation=0;
  const user=()=>state.session?.user||'',token=()=>state.session?.csrf||'';
  const key=m=>JSON.stringify([user(),token(),m.selected,m.view.map.id]);
  const active=m=>!!m&&state.map===m&&state.selected===m.selected&&state.view==='maps'&&state.revision===m.revision&&state.session?.authenticated&&m.owner===user()&&m.csrf===token();
  const visibleRecord=r=>active(state.map)&&state.map.viewState===r;
  const route=m=>'workspaces/'+encodeURIComponent(m.selected)+'/maps/'+encodeURIComponent(m.view.map.id)+'/view-state';
  function close(){generation++;for(const k of records.keys()){const [actor,csrf]=JSON.parse(k);if(!state.session?.authenticated||actor!==user()||csrf!==token())records.delete(k);}}
  function record(m){
    const k=key(m);if(!records.has(k)){for(const [old,r] of records){if(records.size<8)break;if(!r.pending&&!r.busy)records.delete(old);}if(records.size>=8)throw Error('Есть восемь неподтверждённых сохранений вида. Откройте соответствующие карты и повторите сохранение либо откажитесь от повтора.');records.set(k,{revision:null,pending:null,busy:false,message:'',readSerial:0});}return records.get(k);
  }
  const choices={display_mode:['diagram','outline','table'],layout:['right','down','bilateral'],table_sort:['tree','label','kind','sources']};
  function checked(result,m){
    const s=result?.state,integer=v=>Number.isSafeInteger(v)&&v>=0,ratio=v=>typeof v==='number'&&Number.isFinite(v)&&v>=0&&v<=1;
    if(!result||!integer(result.revision)||!integer(result.map_revision)||!integer(result.current_map_revision)||result.current_map_revision<1||typeof result.changed!=='boolean'||typeof result.replayed!=='boolean'||!s||!Object.entries(choices).every(([k,items])=>items.includes(s[k]))||typeof s.selected_node_id!=='string'||!Array.isArray(s.collapsed)||s.collapsed.length>1000||!s.collapsed.every(id=>typeof id==='string')||new Set(s.collapsed).size!==s.collapsed.length||typeof s.scale!=='number'||!Number.isFinite(s.scale)||s.scale<.05||s.scale>2||!ratio(s.scroll_x)||!ratio(s.scroll_y)||!integer(s.table_page)||s.table_page>999||typeof s.table_descending!=='boolean'||typeof s.table_query!=='string'||Array.from(s.table_query).length>200)throw Error('Некорректный сохранённый вид карты.');
    if(result.current_map_revision!==m.view.map.revision)throw Error('Карта изменилась при чтении вида. Обновите карту перед восстановлением или сохранением.');return result;
  }
  function use(m,result){
    if(!result.revision)return;
    const s=result.state;m.displayMode=s.display_mode;m.layoutMode=s.layout;
    m.collapsed=new Set(s.collapsed.filter(id=>(m.tree.children.get(id)||[]).length));
    m.selectedNode=m.tree.byID.has(s.selected_node_id)?s.selected_node_id:m.tree.rootID;
    if(m.displayMode==='diagram')m.collapsed=MemMapLayout.reveal(m.tree,m.collapsed,m.selectedNode);m.depth=null;m.scale=s.scale;
    m.tableSort=s.table_sort;m.tableDescending=s.table_descending;m.tableQuery=s.table_query;m.tablePage=s.table_page;
    m.viewScroll={x:s.scroll_x,y:s.scroll_y};m.viewRestored=true;
  }
  async function prepare(m,previous){
    if(previous){for(const prop of ['displayMode','layoutMode','tableSort','tableDescending','tableQuery','tablePage','selectedNode'])m[prop]=previous[prop];m.viewState=previous.viewState;return;}
    const r=record(m);m.viewState=r;const g=generation,actor=user(),csrf=token(),serial=++r.readSerial;
    try{const result=checked(await api(route(m)),m);if(g!==generation||actor!==user()||csrf!==token()||serial!==r.readSerial)return;r.revision=result.revision;r.message=result.changed?'Карта изменилась после сохранения вида. Удалённые узлы исключены; сохранённый вид в базе не перезаписан.':result.revision?'Восстановлен ваш сохранённый вид.':'Настройте вид и нажмите «Сохранить вид», чтобы вернуться к нему позже.';use(m,result);}
    catch(e){if(g===generation&&actor===user()&&csrf===token()&&serial===r.readSerial){r.revision=null;r.message=e.message+' Сохранение недоступно до успешного чтения вида.';}}
  }
  function host(m){return $(m.displayMode==='diagram'?'map-canvas':'map-structured-host');}
  function snapshot(m){
    const h=host(m),ratio=(value,max)=>max>0?Math.max(0,Math.min(1,value/max)):0;
    return {display_mode:m.displayMode||'diagram',layout:m.layoutMode||'right',selected_node_id:m.tree.byID.has(m.selectedNode)?m.selectedNode:m.tree.rootID,collapsed:[...m.collapsed].filter(id=>(m.tree.children.get(id)||[]).length).sort(),scale:m.scale,scroll_x:ratio(h.scrollLeft,h.scrollWidth-h.clientWidth),scroll_y:ratio(h.scrollTop,h.scrollHeight-h.clientHeight),table_sort:m.tableSort||'tree',table_descending:!!m.tableDescending,table_query:m.tableQuery||'',table_page:m.tablePage||0};
  }
  function restoreScroll(m){const p=m.viewScroll;if(!p)return;const h=host(m);h.scrollLeft=p.x*Math.max(0,h.scrollWidth-h.clientWidth);h.scrollTop=p.y*Math.max(0,h.scrollHeight-h.clientHeight);m.viewScroll=null;}
  function render(){
    const m=state.map;if(!m)return;const r=m.viewState;
    $('map-display-mode').value=m.displayMode||'diagram';$('map-layout-mode').value=m.layoutMode||'right';$('map-layout-mode').disabled=m.displayMode!=='diagram';$('map-view-svg').disabled=m.displayMode!=='diagram';
    $('map-view-save').disabled=!r||r.busy||(r.revision===null&&!r.pending);$('map-view-save').textContent=r?.pending?'Повторить сохранение вида':'Сохранить вид';
    $('map-view-restore').disabled=!r||r.busy||!!r.pending;$('map-view-discard').hidden=!r?.pending;$('map-view-discard').disabled=!!r?.busy;
    $('map-view-status').textContent=r?.message||'';
  }
  async function save(){
    const m=state.map;if(!active(m))return;const r=m.viewState;if(!r||r.busy||(r.revision===null&&!r.pending))return;
    if(!r.pending)r.pending=JSON.parse(JSON.stringify({revision:r.revision,map_revision:m.view.map.revision,request_id:crypto.randomUUID().replaceAll('-',''),state:snapshot(m)}));
    r.readSerial++;r.busy=true;r.message='Сохраняем ваш вид карты…';render();
    try{const result=await api(route(m),r.pending);if(!result||!Number.isSafeInteger(result.revision)||result.revision!==r.pending.revision+1||typeof result.replayed!=='boolean'||result.map_revision!==r.pending.map_revision||result.current_map_revision!==r.pending.map_revision||result.changed!==false||!result.state||!Object.keys(r.pending.state).every(k=>JSON.stringify(result.state[k])===JSON.stringify(r.pending.state[k])))throw Error('Сервер не подтвердил сохранение вида.');r.readSerial++;r.revision=result.revision;r.pending=null;r.message='Вид, выбранный при отправке, сохранён. Дальнейшие изменения требуют нового сохранения.';}
    catch(e){r.readSerial++;r.message=e.message+' Отправка не подтверждена. Повтор использует прежний вид; при конфликте сначала откажитесь от повтора и загрузите сохранённый вид.';}
    finally{r.busy=false;if(visibleRecord(r))render();}
  }
  async function restore(){
    const m=state.map;if(!active(m))return;const r=m.viewState;if(!r||r.busy||r.pending)return;const serial=++r.readSerial;r.busy=true;render();
    try{const result=checked(await api(route(m)),m);if(!active(m)||serial!==r.readSerial)return;r.revision=result.revision;if(result.revision){apply(m,result);r.message=result.changed?'Восстановлен вид с учётом изменившейся карты; удалённые узлы исключены.':'Сохранённый вид восстановлен.';}else r.message='Для этой карты пока нет сохранённого вида.';}
    catch(e){if(active(m)&&serial===r.readSerial)r.message=e.message;}finally{r.busy=false;if(visibleRecord(r))render();}
  }
  $('map-view-save').addEventListener('click',run(save));$('map-view-restore').addEventListener('click',run(restore));
  $('map-view-discard').addEventListener('click',()=>{const m=state.map,r=m?.viewState;if(!active(m)||!r?.pending||r.busy)return;if(!confirm('Сохранение могло завершиться. Отказаться от повтора и прочитать сохранённый вид?'))return;r.pending=null;run(restore)();});
  $('map-display-mode').addEventListener('change',()=>change(()=>{const m=state.map;if(!active(m))return;const mode=$('map-display-mode').value;if(!choices.display_mode.includes(mode))return;m.displayMode=mode;if(mode==='diagram'){m.collapsed=MemMapLayout.reveal(m.tree,m.collapsed,m.selectedNode);m.depth=null;}drawMap();selectMapNode(m.selectedNode);if(mode==='diagram')centerMapNode(m.selectedNode);render();}));
  $('map-layout-mode').addEventListener('change',()=>change(()=>{const m=state.map;if(!active(m))return;const mode=$('map-layout-mode').value;if(!choices.layout.includes(mode))return;m.layoutMode=mode;drawMap();selectMapNode(m.selectedNode);centerMapNode(m.selectedNode);render();}));
  function apply(m,result){if(!active(m))return;checked(result,m);return change(()=>{use(m,result);drawMap();selectMapNode(m.selectedNode);restoreScroll(m);render();});}
  return {prepare,render,restoreScroll,close,snapshot,validate:(m,result)=>checked(result,m),apply};
}
