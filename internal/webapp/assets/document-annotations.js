'use strict';
function installMemDocumentAnnotations({state,$,node,api,run,showMap,selectMapNode,drawMap,centerMapNode,regions,transfer,reanchor,knowledge,change=fn=>fn()}) {
  const colors={yellow:'Жёлтый',green:'Зелёный',blue:'Синий',pink:'Розовый'};
  const sourceStates={current:'Источник актуален',stale:'Источник изменился: сохранена прежняя цитата',missing:'Источник недоступен: сохранена цитата'};
  const highlightPageSize=50,highlightLimit=500;
  const drafts=new Map(),readers=new Map();let current=null;
  const dialog=node('dialog');dialog.id='annotation-dialog';dialog.setAttribute('aria-labelledby','annotation-title');
  const heading=node('h2','Аннотации');heading.id='annotation-title';
  const closeButton=button('Закрыть',()=>close(),'quiet');closeButton.setAttribute('aria-label','Закрыть аннотации');
  const head=node('div',undefined,'dialog-head');head.append(heading,closeButton);
  const status=node('p','', 'notice');status.id='annotation-status';status.setAttribute('role','status');
  const error=node('p','', 'error');error.id='annotation-error';error.setAttribute('role','alert');error.hidden=true;
  const back=button('К списку аннотаций',()=>loadList(current,0),'quiet');back.id='annotation-back';
  const list=node('section');list.id='annotation-list';
  const archiveFilter=node('input');archiveFilter.type='checkbox';archiveFilter.id='annotation-archived';
  const archiveLabel=node('label','Показывать архивные', 'check-label');archiveLabel.append(archiveFilter);
  const listRows=node('div'),listPosition=node('span'),listPrev=button('Назад',()=>loadList(current,Math.max(0,current.offset-30))),listNext=button('Далее',()=>loadList(current,current.offset+30));
  listRows.id='annotation-list-rows';listPrev.id='annotation-list-prev';listNext.id='annotation-list-next';
  const importOpen=button('Импорт аннотаций',()=>{const c=current;if(transfer&&valid(c))transfer.showImport({workspace:c.workspace,onImported:()=>valid(c)&&loadList(c,0)});});importOpen.hidden=!transfer;
  const listNav=node('div',undefined,'pagination');listNav.append(listPrev,listPosition,listNext);list.append(archiveLabel,importOpen,listRows,listNav);
  const detail=node('section');detail.id='annotation-detail';
  const location=node('p','', 'muted'),quote=node('pre'),form=node('form'),note=node('textarea'),color=node('select');
  location.id='annotation-location';quote.id='annotation-quote';form.id='annotation-form';note.id='annotation-note';note.rows=5;note.maxLength=16000;color.id='annotation-color';
  for(const [value,label] of Object.entries(colors)){const option=node('option',label);option.value=value;color.append(option);}
  const noteLabel=node('label','Комментарий (до 8000 символов)'),colorLabel=node('label','Цвет');noteLabel.append(note);colorLabel.append(color);
  const save=button('Сохранить',()=>{} ,'primary');save.type='submit';save.id='annotation-save';
  const reset=button('Перечитать и сбросить черновик',resetDraft,'quiet');reset.id='annotation-reset';
  const reanchorApply=button('Перепривязать прежнюю цитату',()=>{const c=current;if(reanchor&&valid(c)&&c.selection&&!c.annotation)return reanchor.preview({workspace:c.workspace,selection:c.selection,onApplied:result=>{updateHighlights(c,result.annotation);return valid(c)&&openAnnotation(c,result.annotation.id);}});}),reanchorCancel=button('Отменить выбор прежней цитаты',()=>{if(reanchor&&valid(current)){reanchor.cancel(current.workspace);controls(current);}});
  form.append(noteLabel,colorLabel,save,reset,reanchorApply,reanchorCancel);
  const actions=node('div',undefined,'job-actions');
  const archive=button('В архив',()=>archiveAnnotation(current)),history=button('История изменений',()=>loadHistory(current,0)),source=button('К источнику',()=>loadSource(current)),links=button('Связанные узлы',()=>loadLinks(current)),attach=button('Привязать к выбранному узлу',()=>attachAnnotation(current));
  archive.id='annotation-archive';history.id='annotation-history';source.id='annotation-source';links.id='annotation-links';attach.id='annotation-attach';
  const regionOpen=button('Области PDF',()=>mountRegions(current));regionOpen.id='annotation-regions';
  const exportOpen=button('Экспорт аннотации',()=>{const c=current;if(transfer&&valid(c)&&c.annotation)transfer.showExport({workspace:c.workspace,ids:[c.annotation.id]});});exportOpen.hidden=!transfer;
  const reanchorStart=button('Выбрать новый источник',()=>{const c=current;if(reanchor&&valid(c)&&c.annotation&&!c.busy&&!c.draft?.pending)reanchor.start({workspace:c.workspace,annotation:c.annotation,onApplied:result=>{updateHighlights(c,result.annotation);return valid(c)&&openAnnotation(c,result.annotation.id);}});}),lineage=button('История перепривязок',()=>{const c=current;if(reanchor&&valid(c)&&c.annotation)return reanchor.showHistory({workspace:c.workspace,id:c.annotation.id,onOpen:id=>valid(c)&&openAnnotation(c,id)});});reanchorStart.hidden=lineage.hidden=!reanchor;
  const knowledgeOpen=button('Объекты графа',()=>mountKnowledge(current));knowledgeOpen.hidden=!knowledge;
  actions.append(archive,history,source,links,attach,regionOpen,exportOpen,reanchorStart,lineage,knowledgeOpen);
  const retry=button('Повторить ту же отправку',()=>send(current),'secondary');retry.id='annotation-retry';
  const refreshMap=button('Обновить карту',()=>openLinkedMap(current,current.lastLinkedTarget||current.targetNode),'secondary');refreshMap.id='annotation-refresh-map';refreshMap.hidden=true;
  const extras=node('section');extras.id='annotation-extras';
  detail.append(location,quote,form,actions,retry,refreshMap,extras);dialog.append(head,status,error,back,list,detail);document.body.append(dialog);

  function button(text,action,css='secondary'){const b=node('button',text,css);b.type='button';b.addEventListener('click',run(action));return b;}
  function user(){return state.session?.user||'';}
  function base(c){return 'workspaces/'+encodeURIComponent(c.workspace)+'/annotations';}
  function route(c){return base(c)+'/'+encodeURIComponent(c.annotation.id);}
  function valid(c){return !!c&&current===c&&dialog.open&&c.workspace===state.selected&&c.screen===state.revision&&c.user===user()&&(!c.isCurrent||c.isCurrent());}
  function message(c,text,bad=false){if(!valid(c))return;(bad?error:status).textContent=text;if(bad)error.hidden=false;}
  function key(c,id){return JSON.stringify([c.user,c.workspace,id]);}
  function cancelReads(c){if(!c)return;for(const controller of Object.values(c.controllers||{}))controller.abort();c.controllers={};}
  function close(){cancelReads(current);current?.extraReader?.close();current=null;dialog.close();}
  closeButton.addEventListener('pointerdown',e=>e.stopPropagation());
  dialog.addEventListener('cancel',e=>{e.preventDefault();close();});dialog.addEventListener('close',()=>{if(!dialog.open){cancelReads(current);current?.extraReader?.close();current=null;}});
  function begin(options={}){
    cancelReads(current);current?.extraReader?.close();
    const c={workspace:state.selected,screen:state.revision,user:user(),documentID:options.documentID||'',targetNode:options.targetNode?{...options.targetNode}:null,isCurrent:options.isCurrent,controllers:{},offset:0,archived:false,busy:false};
    current=c;error.hidden=true;status.textContent='';extras.replaceChildren();refreshMap.hidden=true;
    if(!dialog.open)dialog.showModal();return c;
  }
  async function read(c,path,channel='main'){
    c.controllers[channel]?.abort();const controller=new AbortController();c.controllers[channel]=controller;
    const data=await api(path,undefined,false,controller.signal);
    return valid(c)&&c.controllers[channel]===controller&&!controller.signal.aborted?data:null;
  }
  function onError(c,e){if(e.name!=='AbortError')message(c,e.message,true);}
  function controls(c){
    if(!valid(c))return;const pending=!!c.draft?.pending;
    save.disabled=c.busy||pending||!!c.annotation?.archived;note.disabled=c.busy||pending||!!c.annotation?.archived;color.disabled=note.disabled;
    reset.disabled=c.busy;archive.disabled=c.busy||pending;back.disabled=c.busy;
    attach.hidden=!c.targetNode||!c.annotation;attach.disabled=c.busy||pending||!!c.annotation?.archived||c.annotation?.state!=='current'||!!c.targetNode?.locked||!!c.linkedRevision||!targetCurrent(c);
    attach.textContent=c.targetNode?.locked?'Узел заблокирован':'Привязать к «'+(c.targetNode?.label||'выбранному узлу')+'»';
    regionOpen.hidden=!regions||!(c.annotation?.page>0);regionOpen.disabled=c.busy;
    reanchorStart.disabled=c.busy||pending;reanchorApply.hidden=reanchorCancel.hidden=!!c.annotation||!reanchor?.active(c.workspace);reanchorApply.disabled=c.busy||pending;reanchorCancel.disabled=c.busy||pending;
    retry.hidden=!pending;retry.disabled=c.busy;
  }
  function targetCurrent(c){const m=state.map,t=c.targetNode;return !!t&&m?.view.map.id===t.mapID&&m.selectedNode===t.nodeID&&m.view.map.revision===t.revision;}
  function draftFor(c,id,annotation){
    c.draftKey=key(c,id);
    if(!drafts.has(c.draftKey))drafts.set(c.draftKey,{note:annotation?.note||'',color:annotation?.color||'yellow',revision:annotation?.revision});
    c.draft=drafts.get(c.draftKey);
  }
  function clearExtras(c,...children){c?.extraReader?.close();if(c)c.extraReader=null;extras.replaceChildren(...children);}
  function displayEditor(c){
    if(!valid(c))return;list.hidden=true;detail.hidden=false;back.hidden=false;clearExtras(c);refreshMap.hidden=!c.linkedRevision;error.hidden=true;
    const a=c.annotation;heading.textContent=a?'Аннотация':'Новая аннотация';quote.textContent=a?.quote||c.selection.quote;
    location.textContent=a?[a.source_name,a.page?'Страница '+a.page:'Без номера страницы',sourceStates[a.state]||a.state,'Ревизия '+a.revision,a.archived?'В архиве':''].filter(Boolean).join(' · '):'Выделенная цитата из импортированного текста';
    note.value=c.draft.note;color.value=c.draft.color;
    save.textContent=a?'Сохранить комментарий':'Создать аннотацию';reset.hidden=!a;
    actions.hidden=!a;archive.textContent=a?.archived?'Вернуть из архива':'В архив';
    if(c.draft.pending)status.textContent='Ответ на сохранение не подтверждён. Повтор использует тот же запрос.';
    else if(c.draft.revision&&a&&c.draft.revision!==a.revision)status.textContent='Сохранён черновик для прежней ревизии. Перечитайте и сбросьте его, если хотите начать заново.';
    controls(c);
  }
  note.addEventListener('input',()=>{if(valid(current)&&current.draft&&!current.busy&&!current.draft.pending)current.draft.note=note.value;});
  color.addEventListener('change',()=>{if(valid(current)&&current.draft&&!current.busy&&!current.draft.pending)current.draft.color=color.value;});
  form.addEventListener('submit',e=>{e.preventDefault();run(()=>saveAnnotation(current))();});
  async function saveAnnotation(c){
    if(!valid(c)||c.busy||c.draft.pending||c.annotation?.archived)return;
    c.draft.note=note.value;c.draft.color=color.value;
    if(Array.from(c.draft.note).length>8000||!colors[c.draft.color]){message(c,'Комментарий — не более 8000 символов; выберите цвет.',true);return;}
    const body={note:c.draft.note,color:c.draft.color,request_id:crypto.randomUUID().replaceAll('-','')};
    if(c.annotation)body.revision=c.draft.revision;else Object.assign(body,c.selection);
    c.draft.pending={path:c.annotation?route(c)+'/edit':base(c),body,kind:c.annotation?'edit':'create'};await send(c);
  }
  async function send(c){
    if(!valid(c)||c.busy||!c.draft?.pending)return;
    const draft=c.draft,pending=draft.pending;c.busy=true;error.hidden=true;controls(c);message(c,'Сохраняем…');
    try{
      const result=await api(pending.path,pending.body);draft.pending=null;
      if(pending.kind==='attach'){
        if(!valid(c))return;c.lastLinkedTarget=pending.target;c.linkedRevision=c.targetNode?.mapID===pending.target.mapID&&c.targetNode?.nodeID===pending.target.nodeID?result.revision:null;refreshMap.hidden=false;message(c,'Аннотация привязана. Изменение сохранено в истории карты; обновите карту для просмотра.');
      }else{
        const a=result.annotation;
        Object.assign(draft,{note:a.note||'',color:a.color,revision:a.revision,savedAnnotation:a});
        drafts.set(key(c,'annotation:'+a.id),draft);
        updateHighlights(c,a);
        if(!valid(c))return;c.annotation=a;c.documentID=a.document_id;c.linkedRevision=null;displayEditor(c);message(c,result.replayed?'Эта отправка уже сохранена.':'Аннотация сохранена.');
      }
    }catch(e){onError(c,e);message(c,'Сохранение не подтверждено. Черновик сохранён в этой вкладке; повторите ту же отправку.');}
    finally{c.busy=false;controls(c);}
  }
  async function resetDraft(){
    const c=current;if(!valid(c)||c.busy||!c.annotation)return;
    if(c.draft.pending&&!window.confirm('Ответ на предыдущую отправку не подтверждён. Она могла сохраниться на сервере. Перечитать аннотацию и сбросить черновик?'))return;
    c.busy=true;controls(c);
    try{const a=await read(c,route(c));if(!a)return;
      updateHighlights(c,a,false);c.annotation=a;c.draft.pending=null;Object.assign(c.draft,{note:a.note||'',color:a.color,revision:a.revision});displayEditor(c);message(c,'Показана сохранённая аннотация.');
    }catch(e){onError(c,e);}finally{c.busy=false;controls(c);}
  }
  async function archiveAnnotation(c){
    if(!valid(c)||c.busy||!c.annotation||c.draft.pending)return;
    if(c.draft.note!==(c.annotation.note||'')||c.draft.color!==c.annotation.color){message(c,'Сначала сохраните комментарий или перечитайте аннотацию. Черновик не потерян.',true);return;}
    c.draft.pending={path:route(c)+'/edit',body:{revision:c.annotation.revision,archived:!c.annotation.archived,request_id:crypto.randomUUID().replaceAll('-','')},kind:'archive'};await send(c);
  }
  async function loadList(c,offset){
    if(!valid(c)||c.busy)return;c.mode='list';c.annotation=null;c.draft=null;cancelReads(c);clearExtras(c);detail.hidden=true;list.hidden=false;archiveLabel.hidden=false;listNav.hidden=false;back.hidden=true;error.hidden=true;heading.textContent=c.documentID?'Аннотации документа':'Аннотации базы';status.textContent='Загружаем…';listRows.replaceChildren();archiveFilter.checked=c.archived;
    try{
      const query=new URLSearchParams({document_id:c.documentID,archived:String(c.archived),offset:String(offset),limit:'30'});
      const page=await read(c,base(c)+'?'+query);if(!page)return;c.offset=page.offset;
      for(const a of page.items){const row=node('section',undefined,'card');row.append(node('h3',a.source_name+(a.page?' · Страница '+a.page:'')),node('pre',a.quote),node('p',a.note||'Без комментария'),node('p',(sourceStates[a.state]||a.state)+' · '+(colors[a.color]||a.color),'muted'),button('Открыть аннотацию',()=>openAnnotation(c,a.id)));listRows.append(row);}
      if(!page.items.length)listRows.append(node('p',c.archived?'Архивных аннотаций нет.':'Аннотаций пока нет. Выделите текст в просмотре документа.','muted'));
      listPosition.textContent=page.total?`${page.offset+1}–${page.offset+page.items.length} из ${page.total}`:'0';listPrev.disabled=page.offset===0;listNext.disabled=page.offset+page.items.length>=page.total;status.textContent=c.archived?'Архивные аннотации':'Сохранённые аннотации';
    }catch(e){onError(c,e);}
  }
  archiveFilter.addEventListener('change',run(()=>{if(valid(current)){current.archived=archiveFilter.checked;return loadList(current,0);}}));
  async function openAnnotation(c,id){
    if(!valid(c)||c.busy)return;cancelReads(c);c.busy=true;controls(c);message(c,'Открываем аннотацию…');
    try{const a=await read(c,base(c)+'/'+encodeURIComponent(id));if(!a)return;updateHighlights(c,a,false);c.annotation=a;c.documentID=a.document_id;c.selection=null;c.mode='detail';draftFor(c,'annotation:'+a.id,a);c.linkedRevision=null;displayEditor(c);if(!c.draft.pending&&c.draft.revision===a.revision)message(c,'Цитата и её происхождение сохранены независимо от комментария.');}catch(e){onError(c,e);}finally{c.busy=false;controls(c);}
  }
  async function loadHistory(c,offset){
    if(!valid(c)||!c.annotation)return;const id=c.annotation.id;
    try{const page=await read(c,route(c)+'/history?offset='+offset+'&limit=30','extras');if(!page||c.annotation?.id!==id)return;clearExtras(c,node('h3','История изменений'));
      for(const item of page.items){const a=item.annotation,row=node('section',undefined,'card');row.append(node('h4','Ревизия '+item.revision+' · '+({create:'Создание',edit:'Изменение',archive:'Архивирование',restore:'Возврат из архива'}[item.action]||item.action)),node('p',[item.actor,item.created].filter(Boolean).join(' · '),'muted'),node('pre',a.quote),node('p',a.note||'Без комментария'),node('p',(colors[a.color]||a.color)+(a.archived?' · В архиве':''),'muted'));extras.append(row);}
      const nav=node('div',undefined,'pagination'),prev=button('Предыдущие изменения',()=>loadHistory(c,Math.max(0,page.offset-page.limit))),next=button('Следующие изменения',()=>loadHistory(c,page.offset+page.limit));prev.disabled=page.offset===0;next.disabled=page.offset+page.items.length>=page.total;nav.append(prev,node('span',page.total?`${page.offset+1}–${page.offset+page.items.length} из ${page.total}`:'0'),next);extras.append(nav);
    }catch(e){onError(c,e);}
  }
  async function loadSource(c){
    if(!valid(c)||!c.annotation)return;const a=c.annotation;
    try{const result=await read(c,route(c)+'/source','extras');if(!result||c.annotation?.id!==a.id||c.annotation.revision!==a.revision)return;
      clearExtras(c,node('h3',result.title+(result.page?' · Страница '+result.page:'')),node('p',result.notice||sourceStates[result.state]||'','notice'));
      if(!result.passages?.length)extras.append(node('pre',result.excerpt||a.quote,'map-document-match'));
      else{const host=node('div');extras.append(host);c.extraReader=mountReader({host,passages:result.passages,documentID:a.document_id,targetNode:c.targetNode,afterOpenIsCurrent:c.isCurrent||(()=>true),isCurrent:()=>valid(c)&&c.annotation?.id===a.id&&c.annotation.revision===a.revision&&extras.contains(host)});}
      if(result.original_pdf&&result.state==='current'){const link=node('a','Открыть оригинал PDF ↗');link.href='/api/v1/'+route(c)+'/file?revision='+a.revision+(result.page?'#page='+result.page:'');link.target='_blank';link.rel='noopener noreferrer';extras.append(link);}
    }catch(e){onError(c,e);}
  }
  async function loadLinks(c){
    if(!valid(c)||!c.annotation)return;const id=c.annotation.id;
    try{const result=await read(c,route(c)+'/links','extras');if(!result||c.annotation?.id!==id)return;clearExtras(c,node('h3','Связанные узлы'));
      for(const link of result.items){const b=button(link.map_title+' / '+link.node_label,()=>openLinkedMap(c,{mapID:link.map_id,nodeID:link.node_id}));extras.append(b);}
      extras.append(node('p',result.total>result.items.length?'Показано '+result.items.length+' из '+result.total+'.':result.items.length?'Связи ведут к текущему состоянию карты.':'Активных связей с картами нет.','muted'));
    }catch(e){onError(c,e);}
  }
  function mountRegions(c){
    if(!regions||!valid(c)||c.busy||!(c.annotation?.page>0))return;
    c.controllers.extras?.abort();const a=c.annotation,host=node('div');clearExtras(c,host);
    c.extraReader=regions.mount({host,annotation:a,workspace:c.workspace,isCurrent:()=>valid(c)&&c.annotation?.id===a.id&&c.annotation.revision===a.revision&&host.isConnected});
  }
  function mountKnowledge(c){
    if(!knowledge||!valid(c)||c.busy||!c.annotation)return;
    c.controllers.extras?.abort();const a=c.annotation,host=node('div');clearExtras(c,host);
    c.extraReader=knowledge.mount({host,annotation:a,workspace:c.workspace,isCurrent:()=>valid(c)&&c.annotation?.id===a.id&&c.annotation.revision===a.revision&&host.isConnected});
  }
  async function attachAnnotation(c){
    if(!valid(c)||c.busy||c.draft.pending||!c.annotation||c.annotation.archived||c.annotation.state!=='current'||c.targetNode?.locked||!targetCurrent(c)||c.linkedRevision)return;
    const t=c.targetNode;c.draft.pending={path:'workspaces/'+encodeURIComponent(c.workspace)+'/maps/'+encodeURIComponent(t.mapID)+'/nodes/'+encodeURIComponent(t.nodeID)+'/annotations',body:{revision:t.revision,annotation_id:c.annotation.id,annotation_revision:c.annotation.revision,request_id:crypto.randomUUID().replaceAll('-','')},kind:'attach',target:{...t}};await send(c);
  }
  async function openLinkedMap(c,target){
    if(!valid(c)||c.busy||!target)return;const workspace=c.workspace,actor=c.user;close();
    try{await showMap(target.mapID);if(state.selected!==workspace||user()!==actor||state.map?.view.map.id!==target.mapID)return;
      const m=state.map;if(!m.tree.byID.has(target.nodeID))throw new Error('Связанный узел удалён или недоступен.');change(()=>{m.collapsed=MemMapLayout.reveal(m.tree,m.collapsed,target.nodeID);m.depth=null;drawMap();selectMapNode(target.nodeID,true);centerMapNode(target.nodeID);});
    }catch(e){if(state.selected===workspace&&user()===actor){const next=begin({documentID:c.documentID});message(next,e.message,true);list.hidden=true;detail.hidden=true;back.hidden=false;}}
  }
  function showList(options={}){const c=begin(options);return loadList(c,0);}
  function capture(reader){
    if(!reader.valid())return null;const selection=window.getSelection();if(!selection||selection.rangeCount!==1||selection.isCollapsed)return null;
    const range=selection.getRangeAt(0),part=reader.parts.find(p=>p.pre.contains(range.startContainer)&&p.pre.contains(range.endContainer));if(!part)return null;
    const before=range.cloneRange();before.selectNodeContents(part.pre);before.setEnd(range.startContainer,range.startOffset);
    const through=range.cloneRange();through.selectNodeContents(part.pre);through.setEnd(range.endContainer,range.endOffset);
    const start=Array.from(before.toString()).length,end=Array.from(through.toString()).length,quote=Array.from(part.data.text).slice(start,end).join('');
    if(!quote.trim()||Array.from(quote).length>8000||range.toString()!==quote)return null;
    return {entry_id:part.data.entry_id,pin:part.data.pin,start,end,quote};
  }
  function openSelection(reader,selection=capture(reader)){
    if(!reader.valid()||!selection)return;
    const c=begin({documentID:reader.documentID,targetNode:reader.targetNode,isCurrent:reader.openValid});c.selection=selection;c.mode='create';
    draftFor(c,'selection:'+JSON.stringify(selection));
    if(c.draft.savedAnnotation){run(()=>openAnnotation(c,c.draft.savedAnnotation.id))();return;}
    displayEditor(c);if(!c.draft.pending)message(c,'Цитата будет сохранена вместе с точной версией источника.');
  }
  function matchingHighlight(reader,part,a){
    return a&&typeof a.id==='string'&&a.id&&a.archived===false&&a.state==='current'&&colors[a.color]&&
      a.document_id===reader.documentID&&part.data.citation_id&&a.citation_id===part.data.citation_id&&
      part.data.document_revision&&a.document_revision===part.data.document_revision&&
      Number.isSafeInteger(a.start)&&Number.isSafeInteger(a.end)&&a.start>=0&&a.end>a.start&&a.end<=part.runes.length&&a.end-a.start<=8000&&
      part.runes.slice(a.start,a.end).join('')===a.quote;
  }
  // Range offsets are UTF-16; persist them across markup changes, then capture() converts to runes.
  function readerRange(reader){
    const selection=window.getSelection();if(!selection||selection.rangeCount!==1)return null;
    const range=selection.getRangeAt(0),start=reader.parts.find(p=>p.pre.contains(range.startContainer)),end=reader.parts.find(p=>p.pre.contains(range.endContainer));
    if(!start||!end)return null;
    const offset=(part,container,n)=>{const prefix=range.cloneRange();prefix.selectNodeContents(part.pre);prefix.setEnd(container,n);return prefix.toString().length;};
    return {selection,start:start.pre,end:end.pre,startOffset:offset(start,range.startContainer,range.startOffset),endOffset:offset(end,range.endContainer,range.endOffset),backward:selection.anchorNode===range.endContainer&&selection.anchorOffset===range.endOffset};
  }
  function restoreReaderRange(saved){
    if(!saved)return;
    const position=(pre,offset)=>{const walker=document.createTreeWalker(pre,4);let text,last;
      while((text=walker.nextNode())){last=text;if(offset<=text.textContent.length)return [text,offset];offset-=text.textContent.length;}
      return last?[last,last.textContent.length]:[pre,0];};
    const start=position(saved.start,saved.startOffset),end=position(saved.end,saved.endOffset),selection=saved.selection;
    if(selection.setBaseAndExtent){const anchor=saved.backward?end:start,focus=saved.backward?start:end;selection.setBaseAndExtent(...anchor,...focus);}
    else {const range=document.createRange();range.setStart(...start);range.setEnd(...end);selection.removeAllRanges();selection.addRange(range);}
  }
  async function openHighlight(reader,ids){
    if(!reader.valid())return;const items=ids.map(id=>reader.annotations.get(id)).filter(a=>reader.parts.some(p=>matchingHighlight(reader,p,a)));if(!items.length)return;
    const c=begin({documentID:reader.documentID,targetNode:reader.targetNode,isCurrent:reader.openValid});
    const open=async a=>{await openAnnotation(c,a.id);if(valid(c)&&c.annotation?.id===a.id)await loadLinks(c);};
    if(items.length===1){await open(items[0]);return;}
    c.mode='list';list.hidden=false;detail.hidden=true;back.hidden=false;archiveLabel.hidden=true;listNav.hidden=true;listRows.replaceChildren();heading.textContent='Пересекающиеся аннотации';message(c,'В этом участке '+items.length+' аннотаций. Выберите запись, чтобы открыть комментарий и связанные узлы.');
    for(const a of items){const row=node('section',undefined,'card');row.append(node('pre',a.quote),node('p',a.note||'Без комментария'),button('Открыть: '+colors[a.color]+' · '+(a.note||a.quote).slice(0,100),()=>open(a)));listRows.append(row);}
  }
  function renderHighlights(reader){
    if(!reader.valid())return;const saved=readerRange(reader),matched=new Set();
    for(const part of reader.parts){
      const matches=Array.from(reader.annotations.values()).filter(a=>matchingHighlight(reader,part,a));for(const a of matches)matched.add(a.id);
      if(!reader.enabled||!matches.length){part.pre.textContent=part.data.text;continue;}
      const points=new Map([[0,{start:[],end:[]}],[part.runes.length,{start:[],end:[]}]]);
      for(const a of matches){if(!points.has(a.start))points.set(a.start,{start:[],end:[]});if(!points.has(a.end))points.set(a.end,{start:[],end:[]});points.get(a.start).start.push(a);points.get(a.end).end.push(a);}
      const offsets=Array.from(points.keys()).sort((a,b)=>a-b),active=new Map(),children=[];
      for(let i=0;i<offsets.length-1;i++){
        const point=points.get(offsets[i]);for(const a of point.end)active.delete(a.id);for(const a of point.start)active.set(a.id,a);
        const text=part.runes.slice(offsets[i],offsets[i+1]).join('');if(!active.size){children.push(node('span',text));continue;}
        const items=Array.from(active.values()).sort((a,b)=>a.id.localeCompare(b.id)),ids=items.map(a=>a.id),palette=Object.keys(colors).filter(color=>items.some(a=>a.color===color));
        const mark=node('mark',text,'annotation-highlight annotation-colors-'+palette.join('-'));mark.tabIndex=0;mark.setAttribute('role','button');mark.setAttribute('aria-haspopup','dialog');
        mark.setAttribute('aria-label',(items.length===1?'Аннотация':'Аннотаций: '+items.length)+' · '+palette.map(color=>colors[color]).join(', ')+' · '+text.slice(0,160));
        mark.title=items.length===1?items[0].note||'Открыть аннотацию и связанные узлы':'Открыть '+items.length+' пересекающиеся аннотации';
        mark.addEventListener('click',run(()=>{if(part.pre.contains(mark)&&window.getSelection()?.isCollapsed!==false)return openHighlight(reader,ids);}));
        mark.addEventListener('keydown',e=>{if(e.key==='Enter'||e.key===' '){e.preventDefault();e.stopPropagation();if(part.pre.contains(mark))run(()=>openHighlight(reader,ids))();}});children.push(mark);
      }
      part.pre.replaceChildren(...children);
    }
    reader.matched=matched.size;restoreReaderRange(saved);highlightStatus(reader);reader.update?.();
  }
  function highlightStatus(reader){
    if(!reader.valid())return;reader.toggle.textContent=reader.enabled?'Выключить подсветку':'Включить подсветку';reader.toggle.setAttribute('aria-pressed',String(reader.enabled));reader.toggle.disabled=!reader.exact;
    reader.more.hidden=!reader.exact||reader.offset>=reader.total||reader.offset>=highlightLimit||reader.exhausted;reader.more.disabled=reader.loading||reader.dirty;
    reader.refresh.disabled=!reader.exact||reader.loading;
    let text=!reader.exact?'Подсветка недоступна: нет точной версии фрагмента.':reader.loading?'Загружаем подсветку…':
      (reader.offset>reader.total?'Получено ранее: '+reader.offset+'. Сейчас в списке: '+reader.total+'. ':'Получено из списка: '+reader.offset+' из '+reader.total+'. ')+(reader.enabled?'Подсвечено':'Подсветка выключена; совпадений')+': '+reader.matched+' в показанных фрагментах.';
    if(reader.offset>=highlightLimit&&reader.offset<reader.total)text+=' Лимит просмотра — '+highlightLimit+' записей; остальные доступны в списке аннотаций.';
    if(reader.dirty||reader.exhausted)text+=' Список изменился; обновите подсветку перед загрузкой следующих записей.';
    if(reader.exact&&!reader.loading)text+=' Изменения из других вкладок появятся после обновления.';
    if(reader.loadError)text+=' '+reader.loadError;
    reader.highlightStatus.textContent=text;
  }
  async function loadHighlights(reader,reset=false){
    if(!reader.valid()||!reader.exact||reader.loading||(!reset&&(reader.dirty||reader.offset>=highlightLimit||reader.exhausted)))return;
    reader.controller?.abort();const controller=new AbortController();reader.controller=controller;reader.loading=true;reader.loadError='';highlightStatus(reader);
    const offset=reset?0:reader.offset,limit=Math.min(highlightPageSize,highlightLimit-offset);
    try{
      const query=new URLSearchParams({document_id:reader.documentID,archived:'false',offset:String(offset),limit:String(limit)}),page=await api('workspaces/'+encodeURIComponent(reader.workspace)+'/annotations?'+query,undefined,false,controller.signal);
      if(!reader.valid()||reader.controller!==controller||controller.signal.aborted)return;
      if(!page||!Array.isArray(page.items)||page.items.length>limit||page.offset!==offset||!Number.isSafeInteger(page.total)||page.total<0||(page.items.length&&offset+page.items.length>page.total))throw Error('Не удалось прочитать страницу подсветки. Обновите список.');
      if(reset)reader.annotations.clear();for(const a of page.items)if(a&&typeof a.id==='string')reader.annotations.set(a.id,a);
      reader.offset=offset+page.items.length;reader.total=page.total;reader.exhausted=!page.items.length&&(reader.offset<page.total||offset>0);reader.dirty=false;renderHighlights(reader);
    }catch(e){if(reader.valid()&&reader.controller===controller&&!controller.signal.aborted&&e.name!=='AbortError')reader.loadError='Подсветка загружена не полностью: '+e.message;}
    finally{if(reader.valid()&&reader.controller===controller){reader.loading=false;highlightStatus(reader);}}
  }
  function updateHighlights(context,a,mutation=true){
    for(const reader of readers.values()){
      if(!reader.valid()||reader.workspace!==context.workspace||reader.actor!==context.user||reader.documentID!==a.document_id||!reader.exact)continue;
      const prior=reader.annotations.get(a.id);
      if(prior?.revision>a.revision||(!mutation&&prior&&['revision','note','color','archived','state'].every(key=>prior[key]===a[key])))continue;
      reader.controller?.abort();reader.controller=null;reader.loading=false;reader.dirty=true;
      if(reader.annotations.has(a.id)||reader.annotations.size<highlightLimit)reader.annotations.set(a.id,a);
      renderHighlights(reader);
    }
  }
  function mountReader({host,passages,documentID='',targetNode=null,isCurrent=()=>true,afterOpenIsCurrent}){
    readers.get(host)?.close();host.replaceChildren();const workspace=state.selected,screen=state.revision,actor=user();
    const reader={host,documentID,targetNode,workspace,actor,parts:[],selection:null,annotations:new Map(),enabled:true,offset:0,total:0,matched:0,loading:false,dirty:false,exhausted:false,valid:()=>readers.get(host)===reader&&state.selected===workspace&&state.revision===screen&&user()===actor&&isCurrent()};
    reader.close=()=>{reader.controller?.abort();if(readers.get(host)===reader)readers.delete(host);if(reader.update){host.removeEventListener('keyup',reader.update);host.removeEventListener('mouseup',reader.update);}reader.selection=null;};readers.set(host,reader);reader.openValid=afterOpenIsCurrent||reader.valid;
    const tools=node('div',undefined,'job-actions'),create=button('Аннотировать выделение',()=>openSelection(reader),'primary'),all=button('Аннотации документа',()=>reader.valid()&&showList({documentID,targetNode,isCurrent:reader.openValid}));
    create.disabled=true;tools.append(create,all);host.append(node('p','Выделите до 8000 символов внутри одного фрагмента и нажмите «Аннотировать выделение».','muted'),tools);
    const highlights=node('div',undefined,'annotation-highlight-controls');reader.toggle=button('Выключить подсветку',()=>{if(reader.valid()){reader.enabled=!reader.enabled;renderHighlights(reader);}});reader.more=button('Загрузить ещё подсветки',()=>loadHighlights(reader));reader.refresh=button('Обновить подсветку',()=>loadHighlights(reader,true));reader.highlightStatus=node('p','','muted');reader.highlightStatus.setAttribute('role','status');highlights.append(reader.toggle,reader.more,reader.refresh,reader.highlightStatus);host.append(highlights);
    for(const part of passages||[]){const row=node('section',undefined,part.match?'map-document-match':'map-document-passage');if(part.marker)row.append(node('p',part.marker,'muted'));if(part.match)row.append(node('strong',targetNode?'Подтверждение для выбранного узла':'Фрагмент с сохранённой цитатой'));const pre=node('pre',part.text);row.append(pre);host.append(row);if(part.entry_id>0&&part.pin){const runes=Array.from(part.text);reader.parts.push({pre,data:part,runes});const length=runes.length;if(length<=8000&&part.text.trim())row.append(button('Аннотировать весь фрагмент',()=>openSelection(reader,{entry_id:part.entry_id,pin:part.pin,start:0,end:length,quote:part.text}),'quiet'));}if(part.match)row.scrollIntoView({block:'nearest'});}
    const update=()=>{reader.selection=capture(reader);create.disabled=!reader.selection;};reader.update=update;
    create.addEventListener('pointerdown',e=>{update();e.preventDefault();});host.addEventListener('keyup',update);host.addEventListener('mouseup',update);
    reader.exact=!!documentID&&reader.parts.some(p=>p.data.citation_id&&p.data.document_revision);highlightStatus(reader);if(reader.exact)run(()=>loadHighlights(reader,true))();
    all.disabled=!documentID;return reader;
  }
  document.addEventListener('selectionchange',()=>{for(const reader of readers.values())reader.update();});
  return {mountReader,showList,show:async id=>{const c=begin();return openAnnotation(c,id);},close};
}
