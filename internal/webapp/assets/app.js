'use strict';
(() => {
  const $ = id => document.getElementById(id);
  const state = {session:null,workspaces:[],selected:'',view:'documents',document:'',documentName:'',managedID:'',uploadTarget:null,queryWorkspace:'',queryDocument:'',queryDocumentName:'',queryController:null,querySerial:0,offset:0,total:0,revision:0,dialog:'create',jobs:[],jobsWorkspace:'',polling:false,uploading:false};
  let lastClientLog=0;
  let entryReader=null,entrySerial=0,entryReadingContext=null;
  function reportClientError(kind,event){
    if(!state.session?.authenticated||Date.now()-lastClientLog<1000)return;lastClientLog=Date.now();
    let asset='';try{const url=new URL(event?.filename||'');if(url.origin===location.origin&&url.pathname.startsWith('/assets/'))asset=url.pathname.slice(8);}catch{}
    fetch('/api/v1/client-events',{method:'POST',headers:{'Content-Type':'application/json','X-Mem-CSRF':state.session.csrf},credentials:'same-origin',body:JSON.stringify({kind,asset,line:event?.lineno||0,column:event?.colno||0})}).catch(()=>{});
  }
  window.addEventListener('error',event=>reportClientError('error',event));
  window.addEventListener('unhandledrejection',()=>reportClientError('rejection'));
  const wanted = new URLSearchParams(location.hash.slice(1)).get('workspace');
  const node = (tag,text,className) => {const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(className)n.className=className;return n;};
  function error(message){$('error').textContent=message;$('error').hidden=!message;}
  async function api(path,body,retried=false,signal){
    const headers={};if(body!==undefined){headers['Content-Type']='application/json';if(state.session?.csrf)headers['X-Mem-CSRF']=state.session.csrf;}
    const response=await fetch('/api/v1/'+path,{method:body===undefined?'GET':'POST',headers,body:body===undefined?undefined:JSON.stringify(body),credentials:'same-origin',cache:'no-store',signal});
    const value=await response.json();if(!response.ok){if(response.status===401 && path!=='login'){if(state.session?.local){if(!retried){state.session=await api('session');return api(path,body,true,signal);}throw new Error('Сессия недоступна. Разрешите cookie для этого адреса и обновите страницу.');}state.session.authenticated=false;state.revision++;readingWorkspace.close();mapImport.close();mapStructured.close();mapViewControls.close();mapViewPresets.close();mapNavigation.leave();annotationTransfer.close();annotationReanchor.close();annotationKnowledge.close();annotations.close();entryReader?.close();entrySerial++;$('entry-dialog').close();closeMapSearch();closeMapEditor();closeMapHistory();$('map-action-dialog').close();$('map-library-dialog').close();$('map-document-dialog').close();$('map-workbench-dialog').close();$('map-ai-dialog').close();$('job-history-dialog').close();$('map-source-dialog').close();document.body.classList.remove('map-view','map-full');$('app').hidden=true;$('login').hidden=false;}throw new Error(value.error||'Не удалось выполнить запрос');}return value;
  }
  function run(action){return (...args)=>{Promise.resolve().then(()=>action(...args)).catch(e=>{reportClientError('action_error');error(e.message);});};}
  function current(){return state.workspaces.find(w=>w.id===state.selected);}
  async function boot(){
    readingWorkspace.close();mapImport.close();mapStructured.close();mapViewControls.close();mapViewPresets.close();mapNavigation.leave();annotationTransfer.close();annotationReanchor.close();annotationKnowledge.close();annotations.close();entryReader?.close();entrySerial++;
    closeMapSearch();closeMapEditor();closeMapHistory();$('map-action-dialog').close();$('map-library-dialog').close();$('map-document-dialog').close();$('map-workbench-dialog').close();$('map-ai-dialog').close();$('job-history-dialog').close();$('map-source-dialog').close();
    document.body.classList.remove('map-view','map-full');
    error('');state.session=await api('session');$('loading').hidden=true;$('login').hidden=state.session.authenticated;$('app').hidden=!state.session.authenticated;
    $('logout').hidden=!state.session.authenticated||state.session.local;$('username').textContent=state.session.user||'';
    $('mode').textContent=state.session.local?'На этом компьютере':'Личная библиотека';
    $('version').textContent='v'+state.session.version;$('open').hidden=!state.session.local;$('empty-local').hidden=!state.session.local;
    if(state.session.authenticated){await loadWorkspaces(state.selected||wanted);await openAnnotationLink();}
  }
  async function openAnnotationLink(){
    const params=new URLSearchParams(location.hash.slice(1)),workspace=params.get('workspace');
    if(!workspace||workspace!==state.selected||!state.session?.authenticated||current()?.offline)return;
    const annotation=params.get('annotation'),knowledgeNode=params.get('knowledge_node');
    if(annotation&&/^ann-[0-9a-f]{32}$/.test(annotation))await annotations.show(annotation);
    else if(knowledgeNode&&knowledgeNode.length<=256)await annotationKnowledge.showNode({workspace,nodeID:knowledgeNode,onOpen:id=>annotations.show(id)});
  }
  async function loadWorkspaces(preferred){
    state.workspaces=await api('workspaces');const picker=$('workspace-picker');picker.replaceChildren();
    state.workspaces.forEach(w=>{const option=node('option',w.name+(w.offline?' · недоступна':''));option.value=w.id;picker.append(option);});
    if(!state.workspaces.length){picker.append(node('option','Базы ещё нет'));picker.disabled=true;state.selected='';}else{picker.disabled=false;state.selected=state.workspaces.some(w=>w.id===preferred)?preferred:(state.workspaces.find(w=>!w.offline)||state.workspaces[0]).id;picker.value=state.selected;}
    state.offset=0;state.document='';await loadView();
  }
  async function loadView(){
    readingWorkspace.close();mapImport.close();mapStructured.close();mapViewControls.close();mapViewPresets.close();mapNavigation.leave();annotationTransfer.close();annotationReanchor.close();annotationKnowledge.close();annotations.close();entryReader?.close();entrySerial++;
    closeMapSearch();closeMapEditor();closeMapHistory();$('map-action-dialog').close();$('map-library-dialog').close();$('map-document-dialog').close();$('map-workbench-dialog').close();$('map-ai-dialog').close();$('job-history-dialog').close();$('map-source-dialog').close();
    document.body.classList.remove('map-view','map-full');$('map-fullscreen').textContent='На весь экран';$('map-fullscreen').setAttribute('aria-pressed','false');
    maintenance.close();operations.hide();graphDocuments.hide();stopQuery();const revision=++state.revision;const w=current();error('');$('entry-dialog').close();
    $('map-section').hidden=true;$('add-map').hidden=state.view!=='maps';$('import-map').hidden=state.view!=='maps';$('add-source-map').hidden=state.view!=='maps';$('map-archive-label').hidden=state.view!=='maps';$('map-trash-label').hidden=state.view!=='maps';
    $('empty').hidden=Boolean(w);$('library').hidden=!w||state.view==='settings'||state.view==='query';$('query-section').hidden=!w||state.view!=='query';$('settings').hidden=!w||state.view!=='settings';
    document.querySelectorAll('[data-view]').forEach(b=>{if(b.dataset.view===state.view)b.setAttribute('aria-current','page');else b.removeAttribute('aria-current');b.disabled=!w;});
    $('document-annotations-open').hidden=!w||w.offline||state.view!=='documents';
    $('reading-workspace-open').disabled=!w||!!w.offline;$('database-path').textContent=w?.path||'';if(!w)return;
    $('workspace-name').textContent=w.name;$('settings-workspace').textContent=w.name;
    $('section-title').textContent=state.document?(state.documentName||'Фрагменты документа'):state.view==='notes'?'Заметки':state.view==='jobs'?'Обработка документов':state.view==='trash'?'Корзина заметок':state.view==='maps'?'Ментальные карты':'Документы';
    $('add-documents').hidden=['notes','trash','maps'].includes(state.view);$('add-note').hidden=state.view!=='notes';$('note-notice').hidden=true;$('update-document').hidden=true;$('document-graph').hidden=true;$('search-document').hidden=!state.document;
    if(state.jobsWorkspace!==w.id){state.jobs=[];state.jobsWorkspace=w.id;$('upload-notice').hidden=true;$('jobs-nav').textContent='Обработка';}
    $('back-documents').hidden=!state.document;$('counts').textContent='Загрузка…';$('rows').replaceChildren();$('settings-values').replaceChildren();$('pagination').hidden=true;
    state.settings=null;$('settings-form').hidden=true;$('settings-status').textContent='Читаем настройки…';$('settings-error').hidden=true;
    if(w.offline){$('library').hidden=false;$('query-section').hidden=true;$('settings').hidden=true;$('counts').textContent='База недоступна';for(const id of ['add-documents','add-note','add-map','import-map','add-source-map','map-archive-label','map-trash-label','search-document'])$(id).hidden=true;
      const notice=node('div',undefined,'empty-list'),retry=node('button','Проверить доступность','secondary');retry.type='button';retry.addEventListener('click',run(()=>loadWorkspaces(w.id)));notice.append(node('h2','Каталог базы сейчас недоступен'),node('p','Подключите диск или верните папку базы на прежнее место. Остальные базы доступны; недоступные локальные каталоги временно не входят в общий подсчёт места.'),retry);$('rows').append(notice);return;}
    const id=w.id;
    try{
      if(state.view==='graph'){$('library').hidden=true;await graphDocuments.load(revision,w);return;}
      if(state.view==='maintenance'){$('library').hidden=true;await operations.load(revision,w);return;}
      if(state.view==='query'){await showQuery(revision,w);return;}
      if(state.view==='jobs'){
        const jobs=await api('workspaces/'+id+'/jobs');if(revision!==state.revision)return;
        state.jobs=jobs;renderJobs();return;
      }
      if(state.view==='settings'){
        const s=await api('workspaces/'+id+'/settings');if(revision!==state.revision)return;
        renderSettings(s);return;
      }
      if(state.view==='maps'){await showMaps(revision,id);return;}
      if(state.view==='trash'){await showTrash(revision,id);return;}
      const q=new URLSearchParams({kind:state.document?'entries':state.view,offset:String(state.offset),limit:'30'});if(state.document)q.set('document',state.document);
      const data=await api('workspaces/'+id+'/library?'+q);if(revision!==state.revision)return;
      if(state.offset>0 && state.offset>=data.total){state.offset=Math.max(0,Math.floor((data.total-1)/data.limit)*data.limit);return loadView();}
      if(data.selected_document){state.documentName=data.selected_document.name;state.managedID=data.selected_document.managed_id||'';$('section-title').textContent=state.documentName;$('update-document').hidden=!state.managedID;$('document-graph').hidden=!data.selected_document.document_id;state.graphDocument=data.selected_document.document_id||'';}
      state.total=data.total;$('counts').textContent=`Документов: ${data.document_count} · Заметок: ${data.note_count} · Всего записей: ${data.entry_count}`;
      const docs=state.view==='documents'&&!state.document;const records=docs?data.documents:data.entries;
      if(!records.length){const empty=node('div',undefined,'empty-list');empty.append(node('h2',docs?'Пока нет документов':'Записей нет'),node('p',docs?'Нажмите «Добавить документы». После обработки здесь появятся текст и фрагменты источников.':'Нажмите «Добавить заметку» или выберите другую базу.','muted'));$('rows').append(empty);}
      records.forEach(item=>{
        const row=node('button',undefined,'row');row.type='button';const text=node('span',undefined,'row-main');text.append(node('span',docs?item.name:(item.title||'Запись '+item.id),'row-title'));
        if(docs){text.append(node('span',`Фрагментов: ${item.chunks}`+(item.pages?` · Страниц с фрагментами: ${item.pages}`:''),'row-sub'));row.append(node('span',item.name.includes('.')?item.name.split('.').pop().toUpperCase().slice(0,6):'ФАЙЛ','file-mark'));row.addEventListener('click',run(async()=>{state.document=item.id;state.documentName=item.name;state.managedID=item.managed_id||'';state.offset=0;await loadView();}));}
        else{text.append(node('span',[item.page?'Страница '+item.page:'',item.important?'Важная':'',(item.tags||[]).join(', ')].filter(Boolean).join(' · ')||'Без тегов','row-sub'));row.addEventListener('click',run(()=>state.view==='notes'?showNote(item.id):openEntry(item.id)));}
        row.append(text,node('span',docs?'Фрагменты →':'Открыть →','row-action'));$('rows').append(row);
      });
      $('pagination').hidden=data.total<=data.limit;$('previous').disabled=state.offset===0;$('next').disabled=state.offset+data.limit>=data.total;$('page-position').textContent=data.total?`${state.offset+1}–${Math.min(state.offset+data.limit,data.total)} из ${data.total}`:'0';
    }catch(e){if(revision===state.revision)throw e;}
  }
  async function showNote(id=0){
    const selected=state.selected,revision=state.revision;
    const note=id?await api('workspaces/'+selected+'/notes/'+id):{id:0,pin:'',text:'',title:'',tags:[],important:false,text_editable:true};
    if(selected!==state.selected||revision!==state.revision)return;
    state.note={selected,revision,original:note,requestID:crypto.randomUUID().replaceAll('-',''),deleteRequestID:crypto.randomUUID().replaceAll('-',''),historyOffset:0,busy:false};
    $('note-form').reset();$('note-form').querySelectorAll('input,textarea,button').forEach(el=>el.disabled=false);
    $('note-heading').textContent=id?'Редактировать заметку':'Новая заметка';$('note-workspace').textContent=current()?.name||'';
    $('note-title').value=note.title;$('note-text').value=note.text;$('note-text').readOnly=!note.text_editable;$('note-text').maxLength=note.text_editable?2000:Math.max(2000,note.text.length);
    $('note-tags').value=note.tags.join(', ');$('note-important').checked=note.important;
    $('note-text-help').textContent=note.text_editable?'До 2000 символов. Для большого текста добавьте документ.':'Заметка длиннее 2000 символов. Здесь можно изменить название, теги и важность; текст сохранится целиком.';
    $('note-history').hidden=!id;$('note-delete').hidden=!id;$('note-history-section').hidden=true;
    $('note-error').hidden=true;$('note-progress').hidden=true;$('note-dialog').showModal();
  }
  $('add-note').addEventListener('click',run(()=>showNote()));
  $('note-dialog').addEventListener('cancel',event=>{if(state.note?.busy)event.preventDefault();});
  $('note-form').addEventListener('submit',event=>{
    event.preventDefault();const note=state.note;if(!note||note.busy)return;
    const text=$('note-text').value;
    const body={id:note.original.id,pin:note.original.pin,request_id:note.requestID,text:note.original.id&&text===note.original.text?null:text,title:$('note-title').value,tags:$('note-tags').value.split(',').map(x=>x.trim()).filter(Boolean),important:$('note-important').checked};
    note.busy=true;$('note-error').hidden=true;$('note-progress').hidden=false;$('note-progress').textContent='Сохраняем заметку. При изменении текста ожидаем модель поиска, до 100 секунд. Дождитесь результата.';
    const controls=[...$('note-form').querySelectorAll('input,textarea,button')];controls.forEach(el=>el.disabled=true);
    run(async()=>{try{
      const result=await api('workspaces/'+note.selected+'/notes',body);
      if(state.note!==note)return;$('note-dialog').close();
      if(state.selected===note.selected&&state.revision===note.revision){state.view='notes';state.offset=0;await loadView();$('note-notice').textContent=result.replayed?'Эта попытка уже была сохранена. Список заметок обновлён.':'Заметка сохранена.';$('note-notice').hidden=false;}
    }catch(e){if(state.note===note){$('note-error').textContent=e.message;$('note-error').hidden=false;$('note-progress').textContent='Сохранение не подтверждено. Можно повторить эту же отправку. Перед созданием другой заметки проверьте список.';}}
    finally{note.busy=false;if(state.note===note)controls.forEach(el=>el.disabled=false);}})();
  });
  const mapStatus = value => ({draft:'Черновик',ready:'Готова',archived:'В архиве'}[value]||value);
  async function showMaps(revision,id){
    const page=await api('workspaces/'+id+'/maps?offset='+state.offset+'&archived='+$('map-archived').checked+'&deleted='+$('map-trash').checked);if(revision!==state.revision)return;
    if(state.offset>0&&state.offset>=page.total){state.offset=Math.max(0,Math.floor((page.total-1)/page.limit)*page.limit);return loadView();}
    $('counts').textContent=($('map-trash').checked?'Удалённых карт: ':'Классических карт: ')+page.total+' · Выберите карту для просмотра или создайте новую.';
    if(!page.items.length)$('rows').append(node('p','Карт пока нет. Начните с пустой карты или готового шаблона.','empty-list'));
    page.items.forEach(item=>{if($('map-trash').checked){const row=node('section',undefined,'card'),restore=node('button','Восстановить','secondary');row.append(node('h3',item.title),node('p','Узлов: '+item.node_count+' · Источников: '+item.source_count),restore);const requestID=crypto.randomUUID().replaceAll('-','');restore.addEventListener('click',run(async()=>{restore.disabled=true;try{await api('workspaces/'+id+'/maps/'+encodeURIComponent(item.id)+'/restore',{revision:item.revision,request_id:requestID});if(revision===state.revision)await loadView();}finally{restore.disabled=false;}}));$('rows').append(row);return;}const row=node('button',undefined,'row');row.type='button';const main=node('span',undefined,'row-main');main.append(node('span',item.title,'row-title'),node('span',`${mapStatus(item.status)} · Узлов: ${item.node_count} · Источников: ${item.source_count} · ${new Date(item.updated).toLocaleDateString('ru-RU')}`,'row-sub'));row.append(main,node('span','Открыть карту →','row-action'));row.addEventListener('click',run(()=>showMap(item.id)));const group=node('div',undefined,'map-library-row'),remove=node('button','Удалить','quiet danger');remove.type='button';remove.setAttribute('aria-label','Удалить карту «'+item.title+'»');remove.addEventListener('click',()=>mapLibrary.deleteMap(item));group.append(row,remove);$('rows').append(group);});
    $('pagination').hidden=page.total<=page.limit;$('previous').disabled=state.offset===0;$('next').disabled=state.offset+page.limit>=page.total;$('page-position').textContent=`${state.offset+1}–${Math.min(state.offset+page.limit,page.total)} из ${page.total}`;
  }
  async function showMap(id,{record=true,isCurrent=()=>true}={}){
    if(record)mapNavigation.start();
    if(state.map?.selected!==state.selected||state.map?.view.map.id!==id)$('map-source-banner').hidden=true;
    const selected=state.selected,revision=state.revision,owner=state.session?.user||'',csrf=state.session?.csrf||'',serial=state.mapSerial=(state.mapSerial||0)+1;
    const view=await api('workspaces/'+selected+'/maps/'+encodeURIComponent(id));
    const valid=()=>isCurrent()&&selected===state.selected&&revision===state.revision&&serial===state.mapSerial&&state.view==='maps'&&state.session?.authenticated&&owner===(state.session.user||'')&&csrf===(state.session.csrf||'');
    if(!valid())return;
    closeMapSearch();
    const previous=state.map,tree=MemMapLayout.tree(view.nodes,view.map.root_node_id);const same=previous?.selected===selected&&previous.view.map.id===id&&previous.owner===owner&&previous.csrf===csrf&&previous.revision===revision;
    const next={view,selected,revision,owner,csrf,tree,displayMode:'diagram',layoutMode:'right',tableSort:'tree',tableDescending:false,tableQuery:'',tablePage:0,depth:same?previous.depth:1,scale:same?previous.scale:1,collapsed:same?new Set([...previous.collapsed].filter(id=>(tree.children.get(id)||[]).length)):MemMapLayout.initialCollapsed(tree)};
    await mapViewControls.prepare(next,same?previous:null);if(!valid())return;mapDocument.close();mapViewPresets.close();mapNavigation.silence(()=>{state.map=next;
    document.body.classList.add('map-view');$('library').hidden=true;$('map-section').hidden=false;
    $('map-workspace').textContent=current()?.name||'';$('map-title').textContent=view.map.title;$('map-title').title=view.map.title;$('map-description').textContent=view.map.description;
    $('map-import-source').hidden=!view.has_import_source;$('map-import-source').href=view.has_import_source?'/api/v1/workspaces/'+encodeURIComponent(selected)+'/maps/'+encodeURIComponent(id)+'/import-source':'';
    $('map-meta').textContent=`${mapStatus(view.map.status)} · Узлов: ${view.map.node_count} · Источников: ${view.map.source_count} · Ревизия ${view.map.revision}`;
    $('map-local-command').hidden=!state.session.local;$('map-local-command').textContent='Прежний редактор: mem --dir '+"'"+(current()?.path||'').replaceAll("'","''")+"'"+' mindmap open';
    drawMap();selectMapNode(state.map.displayMode==='diagram'?MemMapLayout.visibleAncestor(tree,state.map.layout,state.map.selectedNode||tree.rootID):(tree.byID.has(state.map.selectedNode)?state.map.selectedNode:tree.rootID));setMapDetails(same?previous.details:false);if(state.map.viewRestored)mapViewControls.restoreScroll(state.map);else centerMapNode(state.map.selectedNode);mapViewControls.render();});
    if(record)mapNavigation.enter({replace:same});return state.map;
  }
  async function openReadingMap({workspace=state.selected,mapRef,mapState}){
    if(!mapRef||workspace!==state.selected||!state.session?.authenticated)return;
    const csrf=state.session.csrf;mapNavigation.checkpoint();readingWorkspace.close();$('map-document-dialog').close();state.view='maps';state.document='';state.offset=0;
    const loading=loadView(),screen=state.revision;await loading;
    if(state.selected!==workspace||state.revision!==screen||state.session?.csrf!==csrf)return;
    try{
      await showMap(mapRef.map_id);
      if(state.selected!==workspace||state.revision!==screen||state.session?.csrf!==csrf)return;
      const m=state.map;if(!m||m.view.map.id!==mapRef.map_id)return;
      if(!m.tree.byID.has(mapRef.node_id)){error('Сохранённый узел удалён. Открыта текущая карта; исходный фрагмент сохранён в «Рабочем месте чтения».');return;}
      mapNavigation.change(()=>{m.collapsed=MemMapLayout.reveal(m.tree,m.collapsed,mapRef.node_id);m.depth=null;drawMap();selectMapNode(mapRef.node_id,true);centerMapNode(mapRef.node_id);});
      if(mapState==='changed'||mapRef.revision&&m.view.map.revision!==mapRef.revision)error('Открыта текущая редакция карты. Сохранённая ссылка относилась к ревизии '+mapRef.revision+'; фрагмент рабочего места не изменён.');
    }catch(e){if(state.selected===workspace&&state.revision===screen&&state.session?.csrf===csrf)error(e.message+' Сохранённый фрагмент доступен в «Рабочем месте чтения».');}
  }
  function renderMapBody(host,text){renderMemMarkdown(host,text,{onObjectLink:ref=>run(()=>openReadingMap({mapRef:{map_id:ref.mapID,node_id:ref.nodeID}}))()});}
  function selectMapNode(id,openDetails=false){return mapNavigation.change(()=>{
    const n=state.map?.view.nodes.find(n=>n.id===id);if(!n)return;if(state.map.selectedNode!==id){$('map-edit-status').hidden=true;$('map-inspector').scrollTop=0;}state.map.selectedNode=id;
    $('map-node-move').disabled=n.locked||id===state.map.tree.rootID;$('map-node-delete').disabled=n.locked||id===state.map.tree.rootID;$('map-node-edit').disabled=n.locked;$('map-node-add').disabled=n.locked;$('map-node-lock').hidden=!n.locked;
    if(openDetails)setMapDetails(true);
    $('map-document-open').disabled=!n.source_count;$('map-document-open').title=n.source_count?'Открыть документ на странице подтверждения':'Сначала прикрепите источник к узлу';$('map-node-title').textContent=n.label;$('map-node-meta').textContent='Тип: '+({topic:'тема',subtopic:'подтема',fact:'факт',note:'заметка',quote:'цитата',question:'вопрос',task:'задача',decision:'решение',link:'ссылка'}[n.kind]||n.kind);
    $('map-node-summary').textContent=n.summary;renderMapBody($('map-node-body'),n.body||'У этого узла пока нет подробного текста.');
    $('map-node-sources').textContent=n.source_count?'Прикреплённых источников: '+n.source_count+'. Откройте «Источники узла», чтобы посмотреть и изменить их.':'Источники не прикреплены.';
    mapStructured.select(id,openDetails);
    $('map-canvas').querySelectorAll('[data-map-node]').forEach(el=>{const chosen=el.dataset.mapNode===id;el.classList.toggle('selected',chosen);el.setAttribute('aria-pressed',String(chosen));});
  });}
  function mapAnchor(id){const p=state.map?.layout.positions.get(id),c=$('map-canvas');return p?{x:p.x*state.map.scale-c.scrollLeft,y:p.y*state.map.scale-c.scrollTop}:null;}
  function restoreMapAnchor(id,anchor){const p=state.map.layout.positions.get(id),c=$('map-canvas');if(p&&anchor){c.scrollLeft=p.x*state.map.scale-anchor.x;c.scrollTop=p.y*state.map.scale-anchor.y;}}
  function toggleMapBranch(id,focusControl=false){return mapNavigation.change(()=>{
    const m=state.map,anchor=mapAnchor(id);m.depth=null;if(m.collapsed.has(id))m.collapsed.delete(id);else m.collapsed.add(id);
    drawMap();selectMapNode(MemMapLayout.visibleAncestor(m.tree,m.layout,m.selectedNode));restoreMapAnchor(id,anchor);
    if(focusControl)[...$('map-canvas').querySelectorAll('[data-map-toggle]')].find(el=>el.dataset.mapToggle===id)?.focus({preventScroll:true});
  });}
  function setMapDetails(open){
    if(!state.map)return;state.map.details=open;$('map-inspector').hidden=!open;$('map-inspector-divider').hidden=!open;$('map-section').classList.toggle('with-details',open);$('map-details-toggle').setAttribute('aria-expanded',String(open));mapResize.refresh();
  }
  function centerMapNode(id){const m=state.map,p=m.layout.positions.get(id),c=$('map-canvas');if(!p)return;c.scrollLeft=(p.x+105)*m.scale-c.clientWidth/2;c.scrollTop=p.y*m.scale-c.clientHeight/2;}
  function scaleMap(value){return mapNavigation.change(()=>{
    const m=state.map,c=$('map-canvas'),old=m.scale,cx=(c.scrollLeft+c.clientWidth/2)/old,cy=(c.scrollTop+c.clientHeight/2)/old;
    m.scale=Math.min(2,Math.max(0.05,value));drawMap();selectMapNode(m.selectedNode);c.scrollLeft=cx*m.scale-c.clientWidth/2;c.scrollTop=cy*m.scale-c.clientHeight/2;
  });}
  function fitMap(){return mapNavigation.change(()=>{
    const m=state.map,c=$('map-canvas');if(!m)return;
    m.scale=Math.max(0.05,Math.min(1,(c.clientWidth-24)/m.layout.width,(c.clientHeight-24)/m.layout.height));drawMap();selectMapNode(m.selectedNode);c.scrollTop=0;c.scrollLeft=0;
  });}
  function drawMap(){
    const m=state.map,layout=MemMapLayout.layout(m.tree,m.collapsed,m.layoutMode);m.layout=layout;
    const diagram=m.displayMode==='diagram';$('map-canvas').hidden=!diagram;$('map-structured-host').hidden=diagram;
    for(const id of ['map-overview','map-depth','map-collapse-all','map-expand-all','map-fit','map-home','map-zoom-out','map-zoom-in'])$(id).disabled=!diagram;
    mapStructured.render();mapViewControls.render();if(!diagram){$('map-visible').textContent='План и таблица показывают все узлы независимо от свёрнутых ветвей схемы.';return;}
    const {positions,visible,width,height}=layout,ns='http://www.w3.org/2000/svg';
    const svg=document.createElementNS(ns,'svg');svg.setAttribute('viewBox',`0 0 ${width} ${height}`);svg.setAttribute('width',String(width*m.scale));svg.setAttribute('height',String(height*m.scale));svg.setAttribute('aria-label','Схема '+m.view.map.title);
    const make=(tag,attrs)=>{const e=document.createElementNS(ns,tag);Object.entries(attrs).forEach(([k,v])=>e.setAttribute(k,String(v)));return e;};
    visible.forEach(n=>{const p=positions.get(n.id),parent=positions.get(n.parent_id);if(parent)svg.append(make('path',{d:layout.edgePaths.get(n.id),class:'map-edge'}));});
    visible.forEach(n=>{
      const p=positions.get(n.id),kids=m.tree.children.get(n.id)||[],collapsed=m.collapsed.has(n.id);
      const g=make('g',{transform:`translate(${p.x},${p.y-30})`,role:'button',tabindex:0,'aria-label':n.label,'data-map-node':n.id,class:'map-node','aria-pressed':'false'});
      g.append(make('rect',{width:210,height:60,rx:9}));const title=make('title',{});title.textContent=n.label;g.append(title);
      const caption=make('svg',{x:12,y:7,width:186,height:24,overflow:'hidden'}),text=make('text',{x:0,y:18});const chars=Array.from(n.label);text.textContent=chars.length>23?chars.slice(0,22).join('')+'…':n.label;caption.append(text);g.append(caption);
      const hint=make('text',{x:12,y:45,class:'map-node-hint'});hint.textContent=kids.length?(collapsed?'Скрыто узлов: '+m.tree.descendants.get(n.id):'Ветвей: '+kids.length):'Источников: '+n.source_count;g.append(hint);
      g.addEventListener('click',()=>selectMapNode(n.id,true));g.addEventListener('keydown',e=>{
        if(e.key==='Enter'||e.key===' '){e.preventDefault();selectMapNode(n.id,true);}
        if(kids.length&&(e.key==='ArrowLeft'||e.key==='ArrowRight')){e.preventDefault();if((e.key==='ArrowLeft')!==m.collapsed.has(n.id))toggleMapBranch(n.id);[...$('map-canvas').querySelectorAll('[data-map-node]')].find(el=>el.dataset.mapNode===n.id)?.focus({preventScroll:true});}
      });svg.append(g);
      if(kids.length){const toggle=layout.togglePositions.get(n.id);const control=make('g',{transform:`translate(${toggle.x},${toggle.y})`,role:'button',tabindex:0,'aria-label':(collapsed?'Раскрыть ветвь: ':'Свернуть ветвь: ')+n.label,'aria-expanded':String(!collapsed),'data-map-toggle':n.id,class:'map-branch-toggle'});control.append(make('circle',{r:17}));const symbol=make('text',{x:0,y:5,'text-anchor':'middle'});symbol.textContent=collapsed?'+':'−';control.append(symbol);control.addEventListener('click',()=>toggleMapBranch(n.id,true));control.addEventListener('keydown',e=>{if(e.key==='Enter'||e.key===' '){e.preventDefault();toggleMapBranch(n.id,true);}});svg.append(control);}
    });
    $('map-canvas').replaceChildren(svg);$('map-scale').textContent=Math.round(m.scale*100)+'%';$('map-zoom-out').disabled=m.scale<=0.05;$('map-zoom-in').disabled=m.scale>=2;
    $('map-depth').value=m.depth?String(m.depth):'';
    $('map-visible').textContent=`Видно ${visible.length} из ${m.view.nodes.length} узлов`;
  }
  async function createMapForm(){
    const selected=state.selected,revision=state.revision;const templates=await api('workspaces/'+selected+'/maps/templates');if(selected!==state.selected||revision!==state.revision)return;
    state.mapCreate={selected,revision,templates,requestID:crypto.randomUUID().replaceAll('-',''),busy:false};$('map-create-form').reset();$('map-create-form').querySelectorAll('button,input,select,textarea').forEach(b=>b.disabled=false);
    $('map-create-workspace').textContent=current()?.name||'';const picker=$('map-template');picker.replaceChildren();const empty=node('option','Пустая карта — только центральная тема');empty.value='';picker.append(empty);templates.forEach(t=>{const o=node('option',t.title);o.value=t.id;picker.append(o);});$('map-template-description').textContent='';$('map-create-error').hidden=true;$('map-create-progress').hidden=true;$('map-create-dialog').showModal();
  }
  $('map-template').addEventListener('change',()=>{$('map-template-description').textContent=state.mapCreate.templates.find(t=>t.id===$('map-template').value)?.description||'';});
  $('map-trash').addEventListener('change',run(async()=>{state.offset=0;await loadView();}));
  $('map-archived').addEventListener('change',run(async()=>{state.offset=0;await loadView();}));
  $('add-map').addEventListener('click',run(createMapForm));$('map-back').addEventListener('click',run(loadView));$('map-refresh').addEventListener('click',run(()=>showMap(state.map.view.map.id)));
  const mapChangeName = action => action.startsWith('undo:')?'Отмена: '+mapChangeName(action.slice(5)):action.startsWith('redo:')?'Повтор: '+mapChangeName(action.slice(5)):({'ai:expand_branch':'AI: добавление ветвей','ai:fill_node':'AI: дополнение текста','ai:find_sources':'AI: привязка источников',restore_snapshot:'Восстановление снимка',import_sources:'Импорт источников',create_map:'Создание карты',edit_node:'Редактирование узла',add_node:'Добавление ветви',delete_node:'Удаление ветви','delete_node:branch':'Удаление ветви','delete_node:promote_children':'Удаление узла с сохранением детей',move_node:'Перемещение ветви',edit_map:'Настройки карты',attach_source:'Добавление источника','attach_source:url':'Добавление веб-ссылки',attach_annotation:'Привязка аннотации',attach_evidence:'Добавление фрагмента',move_source:'Порядок источников',detach_source:'Удаление источника'}[action]||'Изменение карты');
  function closeMapHistory(){ $('map-history-dialog').close();state.mapHistory=null; }
  async function loadMapHistory(h,before=0){
    if(!h||state.mapHistory!==h||h.busy)return;
    const serial=h.serial=(h.serial||0)+1;h.pending=null;
    $('map-undo-confirm').hidden=true;$('map-history-items').replaceChildren();$('map-history-error').hidden=true;$('map-history-status').textContent='Загружаем историю…';
    $('map-history-older').disabled=true;$('map-history-newest').disabled=true;
    try{
      const page=await api('workspaces/'+h.selected+'/maps/'+encodeURIComponent(h.mapID)+'/history?before='+before);
      if(state.mapHistory!==h||h.serial!==serial)return;
      h.page=page;h.before=before;$('map-history-context').textContent=h.title+' · Ревизия '+page.revision;
      $('map-history-status').textContent=page.items.length?'Показано действий: '+page.items.length+(page.undo_id||page.redo_id?'':' · Нет доступных действий для отмены или возврата.'):'История пуста.';
      page.items.forEach(item=>{
        const row=node('section',undefined,'map-history-row');row.append(node('strong',mapChangeName(item.action)),node('p','№ '+item.id+' · Ревизия '+item.revision+' · '+new Date(item.created).toLocaleString('ru-RU'),'muted'));
        if(item.node_id){
          row.append(node('p',(item.node_present?'Узел сейчас: ':'Удалённый узел: ')+item.node_label,'muted'));
          if(item.node_present){const jump=node('button','Показать узел','secondary');jump.type='button';jump.addEventListener('click',()=>showHistoryNode(h,item.node_id));row.append(jump);}
        }
        if(item.reverted)row.append(node('p',item.action.startsWith('undo:')?'Изменение возвращено':'Отменено','muted'));
        if(item.reverts)row.append(node('p',(item.action.startsWith('redo:')?'Возвращает действие из отмены № ':'Отменяет действие № ')+item.reverts,'muted'));
        if(item.id===page.undo_id||item.id===page.redo_id){const redo=item.id===page.redo_id;const button=node('button',redo?'Вернуть отменённое':'Отменить это изменение','secondary');button.type='button';button.addEventListener('click',()=>{
          if(h.busy||state.mapHistory!==h)return;h.redo=redo;h.pending={revision:page.revision,change_id:item.id,request_id:crypto.randomUUID().replaceAll('-','')};
          $('map-undo-description').textContent=redo?'Вернуть «'+mapChangeName(item.action.slice(5))+'» из отмены № '+item.id+'?':'Отменить «'+mapChangeName(item.action)+'» — действие № '+item.id+'?';
          if(item.node_id)$('map-undo-description').textContent+=' '+(item.node_present?'Узел сейчас: ':'Удалённый узел: ')+item.node_label+'.';
          $('map-restore-help').textContent=redo?'Карта вернётся к результату отменённого действия. Возврат тоже сохранится в истории; его можно отменить.':'Карта вернётся к состоянию до этого действия. При необходимости отменённое можно вернуть через историю.';
          $('map-undo-submit').textContent=redo?'Подтвердить возврат':'Подтвердить отмену';$('map-undo-cancel').textContent=redo?'Не возвращать':'Оставить изменение';$('map-undo-confirm').hidden=false;$('map-undo-submit').focus();
        });row.append(button);}
        $('map-history-items').append(row);
      });
      $('map-history-newest').disabled=before===0;$('map-history-older').disabled=!page.next_before;
    }catch(err){if(state.mapHistory===h&&h.serial===serial){$('map-history-status').textContent='';$('map-history-error').textContent=err.message;$('map-history-error').hidden=false;}}
  }
  async function showHistoryNode(h,id){
    if(state.mapHistory!==h||h.busy)return;h.busy=true;
    const buttons=[...$('map-history-dialog').querySelectorAll('button')];buttons.forEach(b=>b.disabled=true);
    $('map-history-error').hidden=true;$('map-history-status').textContent='Открываем узел на текущей карте…';
    try{
      await showMap(h.mapID);
      if(state.mapHistory!==h||state.selected!==h.selected||state.revision!==h.revision||state.map?.view.map.id!==h.mapID)return;
      const m=state.map;
      if(!m.tree.byID.has(id))throw new Error('Узел больше недоступен. Обновите историю карты.');
      closeMapHistory();mapNavigation.change(()=>{m.collapsed=MemMapLayout.reveal(m.tree,m.collapsed,id);m.depth=null;m.scale=1;drawMap();selectMapNode(id,true);centerMapNode(id);});
    }catch(err){if(state.mapHistory===h){$('map-history-status').textContent='';$('map-history-error').textContent=err.message;$('map-history-error').hidden=false;}}
    finally{h.busy=false;if(state.mapHistory===h){buttons.forEach(b=>b.disabled=false);$('map-history-newest').disabled=!h.before;$('map-history-older').disabled=!h.page?.next_before;}}
  }
  $('map-history-open').addEventListener('click',()=>{
    const m=state.map;if(!m)return;
    const h=state.mapHistory={selected:state.selected,revision:state.revision,mapID:m.view.map.id,title:m.view.map.title,busy:false};
    $('map-history-dialog').querySelectorAll('button').forEach(b=>b.disabled=false);$('map-history-dialog').showModal();loadMapHistory(h);
  });
  $('map-history-close').addEventListener('click',closeMapHistory);
  $('map-history-dialog').addEventListener('cancel',e=>{e.preventDefault();if(!state.mapHistory?.busy)closeMapHistory();});
  $('map-history-refresh').addEventListener('click',()=>loadMapHistory(state.mapHistory));
  $('map-history-newest').addEventListener('click',()=>loadMapHistory(state.mapHistory));
  $('map-history-older').addEventListener('click',()=>{const h=state.mapHistory;if(h?.page?.next_before)loadMapHistory(h,h.page.next_before);});
  $('map-undo-cancel').addEventListener('click',()=>{state.mapHistory.pending=null;$('map-undo-confirm').hidden=true;});
  $('map-undo-submit').addEventListener('click',()=>{
    const h=state.mapHistory;if(!h?.pending||h.busy)return;h.busy=true;
    const buttons=[...$('map-history-dialog').querySelectorAll('button')];buttons.forEach(b=>b.disabled=true);
    $('map-history-error').hidden=true;$('map-history-status').textContent=h.redo?'Возвращаем изменение и сохраняем историю…':'Отменяем изменение и сохраняем историю…';
    run(async()=>{
      let result;
      try{result=await api('workspaces/'+h.selected+'/maps/'+encodeURIComponent(h.mapID)+(h.redo?'/redo':'/undo'),h.pending);}
      catch(err){if(state.mapHistory===h){$('map-history-error').textContent=err.message;$('map-history-error').hidden=false;$('map-history-status').textContent='Действие не подтверждено. При сбое связи повторите ту же отправку. Если карта изменилась, учтите причину конфликта.';}return;}
      finally{h.busy=false;if(state.mapHistory===h){buttons.forEach(b=>b.disabled=false);$('map-history-newest').disabled=!h.before;$('map-history-older').disabled=!h.page?.next_before;}}
      if(state.mapHistory!==h)return;h.pending=null;$('map-undo-confirm').hidden=true;
      try{await showMap(h.mapID);}catch(err){if(state.mapHistory===h){$('map-history-error').textContent='Действие сохранено, но просмотр не обновлён. Закройте историю и обновите карту. '+err.message;$('map-history-error').hidden=false;$('map-history-status').textContent=h.redo?'Изменение возвращено.':'Изменение отменено.';}return;}
      if(state.mapHistory!==h||state.selected!==h.selected||state.revision!==h.revision)return;
      await loadMapHistory(h);
      if(state.mapHistory===h)$('map-history-status').textContent=result.replayed?'Это действие уже выполнено. Показано текущее состояние.':h.redo?'Изменение возвращено. Карта и история обновлены.':'Изменение отменено. Карта и история обновлены.';
    })();
  });
  function closeMapEditor(){ $('map-edit-dialog').close();$('map-edit-form').reset();state.mapEdit=null; }
  function mapEditChanged(){
    const edit=state.mapEdit;if(!edit)return;
    $('map-edit-save').disabled=edit.busy||!$('map-edit-label').value.trim()||(!edit.adding&&($('map-edit-label').value===edit.node.label&&$('map-edit-summary').value===edit.node.summary&&$('map-edit-body').value===edit.node.body));
  }
  function openMapNodeForm(adding=false){
    const m=state.map,n=m?.tree.byID.get(m.selectedNode);if(!n||n.locked)return;
    state.mapEdit={selected:m.selected,revision:state.revision,mapID:m.view.map.id,mapRevision:m.view.map.revision,node:n,adding,requestID:crypto.randomUUID().replaceAll('-',''),busy:false};
    $('map-edit-form').querySelectorAll('input,textarea,button').forEach(el=>el.disabled=false);
    $('map-edit-label').value=adding?'':n.label;$('map-edit-summary').value=adding?'':n.summary;$('map-edit-body').value=adding?'':n.body;
    $('map-edit-title').textContent=adding?'Добавить дочернюю ветвь':'Редактировать текст узла';$('map-edit-save').textContent=adding?'Добавить':'Сохранить';
    $('map-edit-help').textContent='Название — до 512 символов; описание и текст — до 65 536 каждый. '+(adding?'Новая подтема добавится последней дочерней ветвью выбранного узла. Действие сохранится в истории карты.':'Предыдущее состояние сохраняется в истории карты. Название центрального узла не меняет название самой карты.');
    $('map-edit-context').textContent=(adding?'Родитель: '+n.label+' · ':'')+m.view.map.title+' · Ревизия '+m.view.map.revision;
    $('map-edit-error').hidden=true;$('map-edit-progress').hidden=true;$('map-edit-status').hidden=true;mapEditChanged();$('map-edit-dialog').showModal();
  }
  $('map-node-edit').addEventListener('click',()=>openMapNodeForm());
  $('map-node-add').addEventListener('click',()=>openMapNodeForm(true));
  $('map-edit-form').addEventListener('input',mapEditChanged);
  $('map-edit-dialog').addEventListener('cancel',e=>{if(state.mapEdit?.busy)e.preventDefault();});
  $('map-edit-form').addEventListener('submit',e=>{
    e.preventDefault();const edit=state.mapEdit;if(!edit||edit.busy)return;edit.busy=true;
    const input={revision:edit.mapRevision,request_id:edit.requestID,label:$('map-edit-label').value,summary:$('map-edit-summary').value,body:$('map-edit-body').value};
    const controls=[...$('map-edit-form').querySelectorAll('input,textarea,button')];controls.forEach(el=>el.disabled=true);
    $('map-edit-error').hidden=true;$('map-edit-progress').hidden=false;$('map-edit-progress').textContent='Сохраняем узел и историю карты…';
    run(async()=>{
      let result;
      try{result=await api('workspaces/'+edit.selected+'/maps/'+encodeURIComponent(edit.mapID)+'/nodes/'+encodeURIComponent(edit.node.id)+(edit.adding?'/children':''),input);}
      catch(err){if(state.mapEdit===edit){$('map-edit-error').textContent=err.message;$('map-edit-error').hidden=false;$('map-edit-progress').textContent='Сохранение не подтверждено. Ваш текст остался в форме. При сбое связи можно повторить ту же отправку; при конфликте скопируйте правки перед закрытием формы.';}return;}
      finally{edit.busy=false;if(state.mapEdit===edit){controls.forEach(el=>el.disabled=false);mapEditChanged();}}
      if(state.mapEdit!==edit)return;closeMapEditor();
      if(state.selected!==edit.selected||state.revision!==edit.revision)return;
      try{
        await showMap(edit.mapID);if(state.selected!==edit.selected||state.revision!==edit.revision||state.view!=='maps'||state.map?.view.map.id!==edit.mapID)return;let message=result.replayed?'Эта попытка уже сохранена. Показано текущее состояние карты.':'Изменения сохранены в истории карты.';
        if(edit.adding){
          const m=state.map;
          if(m.tree.byID.has(result.node_id)){
            mapNavigation.change(()=>{m.collapsed=MemMapLayout.reveal(m.tree,m.collapsed,result.node_id);m.depth=null;m.scale=1;drawMap();selectMapNode(result.node_id,true);centerMapNode(result.node_id);});
            message=result.replayed?'Эта ветвь уже была добавлена. Показано её текущее состояние.':'Ветвь добавлена и сохранена в истории карты.';
          }else message='Создание уже подтверждено, но ветвь сейчас отсутствует. Показано текущее состояние карты.';
        }
        $('map-edit-status').textContent=message;$('map-edit-status').hidden=false;
      }
      catch(err){error('Узел сохранён, но обновить просмотр не удалось. Нажмите «Обновить». '+err.message);}
    })();
  });
  function closeMapSearch(){ $('map-search-dialog').close();state.mapSearch=null; }
  $('map-search-dialog').addEventListener('keydown',e=>{
    if(e.key==='Escape'){e.preventDefault();e.stopPropagation();$('map-search-dialog').close();}
  });
  function renderMapSearch(){
    const search=state.mapSearch;if(!search||search.map!==state.map)return;
    const {results,offset,map}=search,total=results.length;$('map-search-results').replaceChildren();
    $('map-search-count').textContent=search.query?(total?'Найдено узлов: '+total:'Совпадений нет. Попробуйте другое слово или более короткую фразу.'):'Введите слово или фразу и нажмите «Найти».';
    for(const hit of results.slice(offset,offset+30)){
      const n=map.tree.byID.get(hit.id),row=node('button',undefined,'row');row.type='button';
      const main=node('span',undefined,'row-main'),path=MemMapLayout.ancestors(map.tree,n.id);
      main.append(node('span',n.label.slice(0,200)+(n.label.length>200?'…':''),'row-title'));
      if(path.length)main.append(node('span',(path.length>3?'… / ':'')+path.slice(-3).map(x=>x.slice(0,80)).join(' / '),'row-sub'));
      main.append(node('span',({label:'Название',summary:'Описание',body:'Текст'}[hit.field])+': '+hit.preview,'row-sub'));
      row.append(main,node('span','К узлу →','row-action'));
      row.addEventListener('click',()=>mapNavigation.change(()=>{
        if(state.map!==map||state.selected!==map.selected||state.view!=='maps')return;
        const revealed=MemMapLayout.reveal(map.tree,map.collapsed,n.id);if(revealed.size!==map.collapsed.size)map.depth=null;
        map.collapsed=revealed;map.scale=1;drawMap();selectMapNode(n.id,true);centerMapNode(n.id);$('map-search-dialog').close();
        [...$('map-canvas').querySelectorAll('[data-map-node]')].find(el=>el.dataset.mapNode===n.id)?.focus({preventScroll:true});
      }));$('map-search-results').append(row);
    }
    $('map-search-pages').hidden=total<=30;$('map-search-prev').disabled=offset===0;$('map-search-next').disabled=offset+30>=total;
    $('map-search-position').textContent=total?`${offset+1}–${Math.min(offset+30,total)} из ${total}`:'';
    $('map-search-dialog').scrollTop=0;
  }
  $('map-search-open').addEventListener('click',()=>{
    if(!state.map)return;
    if(state.mapSearch?.map!==state.map)state.mapSearch={map:state.map,query:'',results:[],offset:0};
    $('map-search-query').value=state.mapSearch.query;renderMapSearch();$('map-search-dialog').showModal();$('map-search-query').focus();
  });
  $('map-search-form').addEventListener('submit',e=>{
    e.preventDefault();const search=state.mapSearch;if(!search||search.map!==state.map)return;
    search.query=$('map-search-query').value.trim();search.results=MemMapLayout.search(search.map.tree,search.query);search.offset=0;renderMapSearch();
  });
  for(const [id,delta] of [['map-search-prev',-30],['map-search-next',30]])$(id).addEventListener('click',()=>{
    const search=state.mapSearch;if(!search)return;search.offset=Math.max(0,Math.min(Math.floor(Math.max(0,search.results.length-1)/30)*30,search.offset+delta));renderMapSearch();
  });
  function showMapDepth(depth,overview=false){return mapNavigation.change(()=>{
    const m=state.map;if(!m||![1,2,3].includes(depth))return;
    m.depth=depth;m.collapsed=MemMapLayout.collapsedAtDepth(m.tree,depth);m.scale=1;drawMap();
    selectMapNode(overview?m.tree.rootID:MemMapLayout.visibleAncestor(m.tree,m.layout,m.selectedNode));centerMapNode(m.selectedNode);
  });}
  $('map-overview').addEventListener('click',()=>showMapDepth(1,true));
  $('map-depth').addEventListener('change',e=>showMapDepth(Number(e.target.value)));
  $('map-home').addEventListener('click',()=>mapNavigation.change(()=>{selectMapNode(state.map.tree.rootID);centerMapNode(state.map.tree.rootID);}));
  $('map-fit').addEventListener('click',fitMap);
  for(const [id,factor] of [['map-zoom-out',1/1.25],['map-zoom-in',1.25]])$(id).addEventListener('click',()=>scaleMap(state.map.scale*factor));
  $('map-collapse-all').addEventListener('click',()=>mapNavigation.change(()=>{state.map.depth=null;state.map.collapsed=new Set(state.map.tree.children.keys());drawMap();selectMapNode(state.map.tree.rootID);fitMap();}));
  $('map-expand-all').addEventListener('click',()=>mapNavigation.change(()=>{state.map.depth=null;state.map.collapsed.clear();drawMap();selectMapNode(state.map.selectedNode);fitMap();}));
  $('map-details-toggle').addEventListener('click',()=>setMapDetails(!state.map.details));
  $('map-details-close').addEventListener('click',()=>{setMapDetails(false);$('map-details-toggle').focus();});
  function setMapFullscreen(full){document.body.classList.toggle('map-full',full);$('map-fullscreen').textContent=full?'Выйти из полного экрана':'На весь экран';$('map-fullscreen').setAttribute('aria-pressed',String(full));}
  $('map-fullscreen').addEventListener('click',()=>setMapFullscreen(!document.body.classList.contains('map-full')));
  document.addEventListener('keydown',e=>{if(e.key==='Escape'&&!document.querySelector('dialog[open]')&&document.body.classList.contains('map-full')){e.preventDefault();setMapFullscreen(false);$('map-fullscreen').focus();}});
  let mapDrag=null;
  $('map-canvas').addEventListener('pointerdown',e=>{if(e.pointerType!=='mouse'||e.button!==0||e.target.closest('[data-map-node],[data-map-toggle]'))return;const c=$('map-canvas');mapDrag={id:e.pointerId,x:e.clientX,y:e.clientY,left:c.scrollLeft,top:c.scrollTop};c.setPointerCapture(e.pointerId);c.classList.add('dragging');});
  $('map-canvas').addEventListener('pointermove',e=>{if(!mapDrag||e.pointerId!==mapDrag.id)return;const c=$('map-canvas');c.scrollLeft=mapDrag.left+mapDrag.x-e.clientX;c.scrollTop=mapDrag.top+mapDrag.y-e.clientY;});
  for(const event of ['pointerup','pointercancel','lostpointercapture'])$('map-canvas').addEventListener(event,()=>{mapDrag=null;$('map-canvas').classList.remove('dragging');});
  $('map-create-dialog').addEventListener('cancel',e=>{if(state.mapCreate?.busy)e.preventDefault();});
  $('map-create-form').addEventListener('submit',e=>{e.preventDefault();const m=state.mapCreate;if(!m||m.busy)return;m.busy=true;
    const body={title:$('map-create-title').value,description:$('map-create-description').value,template_id:$('map-template').value,request_id:m.requestID};const controls=[...$('map-create-form').querySelectorAll('input,select,textarea,button')];controls.forEach(b=>b.disabled=true);$('map-create-error').hidden=true;$('map-create-progress').hidden=false;$('map-create-progress').textContent='Создаём карту…';
    run(async()=>{try{const result=await api('workspaces/'+m.selected+'/maps',body);if(state.mapCreate!==m)return;$('map-create-dialog').close();if(state.selected===m.selected&&state.revision===m.revision){state.view='maps';state.offset=0;const nextRevision=state.revision+1;await loadView();if(state.selected===m.selected&&state.revision===nextRevision)await showMap(result.id);}}
    catch(err){if(state.mapCreate===m){$('map-create-error').textContent=err.message;$('map-create-error').hidden=false;$('map-create-progress').textContent='Создание не подтверждено. Повторите ту же отправку; после перезагрузки сначала проверьте библиотеку.';}}
    finally{m.busy=false;if(state.mapCreate===m)controls.forEach(b=>b.disabled=false);}})();
  });
  const historyLabel = item => ({created:'Создана',edited:'Изменена',before:'До изменения',deleted:'Удалена в корзину',restored:'Восстановлена'}[item.action]||'Сохранена')+' · '+new Date(item.created).toLocaleString('ru-RU');
  function historyRow(item,open){
    const row=node('button',undefined,'row');row.type='button';const main=node('span',undefined,'row-main');
    main.append(node('span',item.title||'Заметка '+item.id,'row-title'),node('span',historyLabel(item),'row-sub'));
    row.append(main,node('span','Посмотреть →','row-action'));row.addEventListener('click',run(open));return row;
  }
  async function showTrash(revision,id){
    const data=await api('workspaces/'+id+'/notes/trash?offset='+state.offset);if(revision!==state.revision)return;
    if(state.offset>0&&state.offset>=data.total){state.offset=Math.max(0,Math.floor((data.total-1)/data.limit)*data.limit);return loadView();}
    state.total=data.total;$('counts').textContent='Удалённых заметок: '+data.total+' · Откройте заметку, чтобы восстановить её. Документы сюда не попадают.';
    if(!data.items.length)$('rows').append(node('p','Корзина пуста. Удалённые через веб заметки появятся здесь.','empty-list'));
    data.items.forEach(item=>$('rows').append(historyRow(item,()=>showNoteVersion(item,null))));
    $('pagination').hidden=data.total<=data.limit;$('previous').disabled=state.offset===0;$('next').disabled=state.offset+data.limit>=data.total;$('page-position').textContent=`${state.offset+1}–${Math.min(state.offset+data.limit,data.total)} из ${data.total}`;
  }
  async function showNoteHistory(offset=0){
    const note=state.note;if(!note||note.busy)return;
    const serial=note.historySerial=(note.historySerial||0)+1;
    try{const page=await api('workspaces/'+note.selected+'/notes/'+note.original.id+'/history?offset='+offset);
      if(state.note!==note||note.historySerial!==serial||!$('note-dialog').open)return;
      note.historyOffset=offset;$('note-history-section').hidden=false;$('note-history-rows').replaceChildren();
      if(!page.items.length)$('note-history-rows').append(node('p','Сохранённых версий пока нет. Они появятся после изменения через веб.','muted'));
      page.items.forEach(item=>$('note-history-rows').append(historyRow(item,()=>showNoteVersion(item,note))));
      $('history-previous').disabled=offset===0;$('history-next').disabled=offset+page.limit>=page.total;$('history-position').textContent=page.total?`${offset+1}–${Math.min(offset+page.limit,page.total)} из ${page.total}`:'0';
    }catch(e){if(state.note===note){$('note-error').textContent=e.message;$('note-error').hidden=false;}}
  }
  async function showNoteVersion(item,note){
    const selected=state.selected,revision=state.revision;
    if(note?.busy)return;
    const serial=state.versionSerial=(state.versionSerial||0)+1;
    const version=await api('workspaces/'+selected+'/notes/'+item.id+'/history/'+item.version_id);
    if(state.versionSerial!==serial||state.selected!==selected||state.revision!==revision||(note&&(state.note!==note||!$('note-dialog').open||note.busy)))return;
    state.noteVersion={selected,revision,note,version,requestID:crypto.randomUUID().replaceAll('-',''),busy:false};
    $('note-version-title').textContent=version.note.title||'Заметка '+item.id;
    $('note-version-meta').textContent=[historyLabel(version),version.note.important?'Важная':'',(version.note.tags||[]).join(', ')].filter(Boolean).join(' · ');
    $('note-version-text').textContent=version.note.text;$('note-version-error').hidden=true;$('note-version-progress').hidden=true;
    $('note-version-help').textContent=(note?'Эта версия заменит сохранённую заметку. Текущее сохранённое состояние останется в истории. Несохранённые правки в форме будут закрыты.':'Заметка вернётся в список с прежним ID.')+' Текст, теги и вектор восстановятся без вызова модели. Дата записи обновится. При смене модели старый вектор может не подходить для поиска по смыслу.';
    $('note-version-restore').textContent=note?'Восстановить эту версию':'Вернуть из корзины';
    $('note-version-dialog').querySelectorAll('button').forEach(b=>b.disabled=false);$('note-version-dialog').showModal();
  }
  async function finishNoteAction(selected,revision,message){
    if(state.selected!==selected||state.revision!==revision)return;
    await loadView();$('note-notice').textContent=message;$('note-notice').hidden=false;
  }
  $('note-history').addEventListener('click',run(()=>showNoteHistory()));
  $('history-previous').addEventListener('click',run(()=>showNoteHistory(Math.max(0,state.note.historyOffset-30))));
  $('history-next').addEventListener('click',run(()=>showNoteHistory(state.note.historyOffset+30)));
  $('note-delete').addEventListener('click',run(async()=>{
    const note=state.note;if(!note||note.busy)return;note.busy=true;
    const controls=[...$('note-form').querySelectorAll('input,textarea,button')];controls.forEach(b=>b.disabled=true);
    $('note-error').hidden=true;$('note-progress').hidden=false;$('note-progress').textContent='Перемещаем сохранённую заметку в корзину…';
    try{await api('workspaces/'+note.selected+'/notes/actions',{action:'delete',id:note.original.id,pin:note.original.pin,request_id:note.deleteRequestID});
      if(state.note!==note)return;$('note-dialog').close();await finishNoteAction(note.selected,note.revision,'Заметка перемещена в корзину. Её можно восстановить в разделе «Корзина заметок».');
    }catch(e){if(state.note===note){$('note-error').textContent=e.message;$('note-error').hidden=false;$('note-progress').textContent='Удаление не подтверждено. Можно повторить эту же попытку.';}}
    finally{note.busy=false;if(state.note===note)controls.forEach(b=>b.disabled=false);}
  }));
  $('note-version-dialog').addEventListener('cancel',event=>{if(state.noteVersion?.busy)event.preventDefault();});
  $('note-version-restore').addEventListener('click',run(async()=>{
    const v=state.noteVersion;if(!v||v.busy)return;v.busy=true;if(v.note)v.note.busy=true;
    $('note-version-dialog').querySelectorAll('button').forEach(b=>b.disabled=true);$('note-version-error').hidden=true;$('note-version-progress').hidden=false;$('note-version-progress').textContent='Восстанавливаем заметку…';
    try{await api('workspaces/'+v.selected+'/notes/actions',{action:v.note?'restore-version':'restore',id:v.version.id,pin:v.note?.original.pin||'',version_id:v.version.version_id,version_pin:v.version.pin,request_id:v.requestID});
      if(state.noteVersion!==v)return;$('note-version-dialog').close();if(v.note)$('note-dialog').close();await finishNoteAction(v.selected,v.revision,'Заметка восстановлена. Она доступна в разделе «Заметки».');
    }catch(e){if(state.noteVersion===v){$('note-version-error').textContent=e.message;$('note-version-error').hidden=false;$('note-version-progress').textContent='Восстановление не подтверждено. При конфликте закройте окно и перечитайте заметку.';}}
    finally{v.busy=false;if(v.note)v.note.busy=false;if(state.noteVersion===v)$('note-version-dialog').querySelectorAll('button').forEach(b=>b.disabled=false);}
  }));
  function renderSettings(s){
    $('settings-tools-status').textContent='';$('settings-preview-results').replaceChildren();$('settings-preview-text').value='';
    state.settings=s;$('settings-form').hidden=false;$('settings-form').reset();
    $('settings-form').querySelectorAll('input,select,button').forEach(el=>el.disabled=false);
    $('settings-status').textContent='Изменения сохранятся только после нажатия кнопки.';
    $('settings-backend').value=s.backend;$('settings-embedding').value=s.embedding_model;$('settings-answer').value=s.answer_model;
    for(const [id,key] of [['size','chunk_size'],['overlap','chunk_overlap'],['strategy','chunk_strategy'],['timeout','answer_timeout'],['tokens','answer_max_tokens'],['context','answer_context_chars']])$('settings-'+id).value=s[key];
    for(const name of ['embedding','answer']){$('settings-'+name).hidden=!s.local;$('settings-'+name).disabled=!s.local;$('settings-'+name+'-choice').hidden=s.local;$('settings-'+name+'-choice').disabled=s.local;$('settings-'+name+'-label').htmlFor='settings-'+name+(s.local?'':'-choice');}
    [...$('settings-backend').options].forEach(option=>option.disabled=!s.local&&option.value!==s.backend&&!s.models.some(x=>x.role==='embedding'&&x.backend===option.value));
    $('settings-model-help').textContent=s.local?'Укажите точные имена доступных моделей. Сохранение не устанавливает модели и не проверяет их доступность. Адреса подключений и ключи задаются через mem config.':'Доступны модели, разрешённые администратором. Адреса подключений и ключи настраивает администратор; сохранение не проверяет доступность модели.';
    fillModelChoices('embedding',s.embedding_model);fillModelChoices('answer',s.answer_model);settingsChanged();indexTools.load();
  }
  function fillModelChoices(role,selected){
    const s=state.settings,backend=role==='answer'?'ollama':$('settings-backend').value,picker=$('settings-'+role+'-choice');picker.replaceChildren();
    const models=s.models.filter(x=>x.role===role&&x.backend===backend).map(x=>x.model);
    const current=role==='answer'?s.answer_model:(s.backend===backend?s.embedding_model:'');if(current&&!models.includes(current))models.unshift(current);
    if(role==='answer'){const option=node('option','Ответы отключены');option.value='';picker.append(option);}
    else if(!models.length){const option=node('option','Нет разрешённых моделей');option.value='';picker.append(option);}
    models.forEach(model=>{const option=node('option',model);option.value=model;picker.append(option);});
    if(selected!==undefined&&[...picker.options].some(o=>o.value===selected))picker.value=selected;
  }
  function settingsEmbedding(){return $(state.settings.local?'settings-embedding':'settings-embedding-choice').value.trim();}
  function settingsChanged(){
    if(!state.settings)return;
    const changed=$('settings-backend').value!==state.settings.backend||settingsEmbedding()!==state.settings.embedding_model;
    $('settings-confirm-label').hidden=!changed;$('settings-confirm').required=changed;
    $('settings-overlap').max=String(Math.min(1000,Math.max(0,Number($('settings-size').value)-1)));
  }
  $('settings-backend').addEventListener('change',()=>{if(!state.settings)return;$('settings-embedding').value=$('settings-backend').value===state.settings.backend?state.settings.embedding_model:'';fillModelChoices('embedding');$('settings-confirm').checked=false;settingsChanged();});
  $('settings-form').addEventListener('input',settingsChanged);
  let settingsToolBusy=false;
  async function settingsTool(action,body){
    if(!state.settings||settingsToolBusy)return;const selected=state.selected,revision=state.revision;settingsToolBusy=true;
    $('settings-tools').querySelectorAll('button').forEach(x=>x.disabled=true);$('settings-tools-status').textContent='Проверяем…';
    try{const result=await api('workspaces/'+selected+'/settings/'+action,body);if(selected!==state.selected||revision!==state.revision)return;
      if(action==='check-model')$('settings-tools-status').textContent='Модель ответила.'+(result.dimensions?' Размер вектора: '+result.dimensions+'.':'');
      else{const rows=$('settings-preview-results');rows.replaceChildren();$('settings-tools-status').textContent='Фрагментов: '+result.total+(result.truncated?'. Показаны первые 100.':'');for(const chunk of result.chunks){const row=node('details');row.append(node('summary','Фрагмент '+(chunk.Index+1)+' · '+Array.from(chunk.Text).length+' символов'),node('pre',chunk.Text));rows.append(row);}}
    }catch(e){if(selected===state.selected&&revision===state.revision)$('settings-tools-status').textContent=e.message;}
    finally{settingsToolBusy=false;$('settings-tools').querySelectorAll('button').forEach(x=>x.disabled=false);}
  }
  for(const role of ['embedding','answer'])$('settings-check-'+role).addEventListener('click',run(()=>settingsTool('check-model',{revision:state.settings.revision,role})));
  $('settings-preview-form').addEventListener('submit',e=>{e.preventDefault();$('settings-preview-results').replaceChildren();run(()=>settingsTool('preview',{text:$('settings-preview-text').value,size:Number($('settings-size').value),overlap:Number($('settings-overlap').value),strategy:$('settings-strategy').value}))();});
  $('settings-reload').addEventListener('click',run(loadView));
  $('settings-form').addEventListener('submit',event=>{
    event.preventDefault();if(!state.settings||$('settings-save').disabled)return;
    const selected=state.selected,revision=state.revision,s=state.settings;
    const body={revision:s.revision,backend:$('settings-backend').value,embedding_model:settingsEmbedding(),answer_model:$(s.local?'settings-answer':'settings-answer-choice').value.trim(),accept_embedding_change:$('settings-confirm').checked};
    for(const [id,key] of [['size','chunk_size'],['overlap','chunk_overlap'],['timeout','answer_timeout'],['tokens','answer_max_tokens'],['context','answer_context_chars']])body[key]=Number($('settings-'+id).value);
    body.chunk_strategy=$('settings-strategy').value;
    const controls=[...$('settings-form').querySelectorAll('input,select,button')],disabled=controls.map(el=>el.disabled);controls.forEach(el=>el.disabled=true);
    $('settings-error').hidden=true;$('settings-status').textContent='Сохраняем настройки…';
    run(async()=>{try{
      const saved=await api('workspaces/'+selected+'/settings',body);if(revision!==state.revision||selected!==state.selected)return;
      renderSettings(saved);$('settings-status').textContent='Настройки сохранены. Существующие документы не пересчитывались.';
    }catch(e){if(revision===state.revision&&selected===state.selected){$('settings-error').textContent=e.message;$('settings-error').hidden=false;$('settings-status').textContent='Сохранение не подтверждено. При конфликте перечитайте настройки.';}}
    finally{if(revision===state.revision&&selected===state.selected)controls.forEach((el,i)=>el.disabled=disabled[i]);}})();
  });
  function clearQueryResults(){
    $('query-results').replaceChildren();$('query-sources').replaceChildren();$('query-answer-box').hidden=true;$('query-error').hidden=true;$('query-count').textContent='';
  }
  function stopQuery(){
    state.querySerial++;state.queryController?.abort();state.queryController=null;
    $('query-form').querySelectorAll('button,input,select,textarea').forEach(el=>el.disabled=false);
    $('query-cancel').hidden=true;$('query-progress').hidden=true;
  }
  async function showQuery(revision,w){
    if(state.queryWorkspace!==w.id){state.queryWorkspace=w.id;state.queryDocument='';state.queryDocumentName='';$('query-form').reset();}
    clearQueryResults();$('query-workspace').textContent=w.name;
    $('query-scope').textContent=state.queryDocument?'В документе: '+state.queryDocumentName:'Во всей базе: документы и заметки';$('query-all').hidden=!state.queryDocument;
    $('query-models').textContent='Читаем настройки моделей…';
    const settings=await api('workspaces/'+w.id+'/settings');if(revision!==state.revision)return;
    $('query-models').textContent='Модель поиска: '+settings.embedding_model+' · Модель ответов: '+(settings.answer_model||'не настроена');
  }
  function appendQueryHit(container,hit,label){
    const row=node('article',undefined,'query-hit');row.append(node('h3',label||hit.title||hit.source));
    row.append(node('p',[hit.source,hit.page?'Страница '+hit.page:'Страница не указана',(hit.tags||[]).join(', '),hit.important?'Важное':''].filter(Boolean).join(' · '),'muted'));
    row.append(node('pre',hit.text));if(hit.truncated)row.append(node('p','Показана часть фрагмента.','muted'));
    if(hit.ocr_warning)row.append(node('p','Фрагмент получен распознаванием с низкой уверенностью. Проверьте оригинал.','muted'));
    const button=node('button','Открыть фрагмент источника','secondary');button.type='button';
    const selected=state.selected;button.addEventListener('click',run(async()=>{
      const revision=state.revision,serial=++entrySerial;button.disabled=true;
      try {const evidence=await api('workspaces/'+selected+'/annotations/entry/'+hit.id+'?pin='+encodeURIComponent(hit.pin));if(selected!==state.selected||revision!==state.revision||serial!==entrySerial)return;
        displayAnnotationEntry(evidence,selected,revision,serial,false);
      } catch(e){if(selected===state.selected&&revision===state.revision){$('query-error').textContent=e.message;$('query-error').hidden=false;}} finally{button.disabled=false;}
    }));row.append(button);container.append(row);
  }
  $('query-form').addEventListener('submit',event=>{
    event.preventDefault();const action=event.submitter?.value==='ask'?'ask':'search';
    run(async()=>{
      stopQuery();clearQueryResults();const serial=state.querySerial,selected=state.selected;
      const controller=new AbortController();state.queryController=controller;
      const body={query:$('query-text').value,mode:$('query-mode').value,limit:Number($('query-limit').value),document:state.queryDocument,tags:$('query-tags').value.split(',').map(x=>x.trim()).filter(Boolean),important:$('query-important').checked,from:$('query-from').value,to:$('query-to').value,page_from:Number($('query-page-from').value),page_to:Number($('query-page-to').value),min_score:Number($('query-score').value)};
      $('query-form').querySelectorAll('button,input,select,textarea').forEach(el=>el.disabled=true);$('query-cancel').disabled=false;$('query-cancel').hidden=false;
      $('query-progress').hidden=false;$('query-progress').textContent=action==='ask'?'Ищем фрагменты и готовим ответ. Запрос можно отменить.':'Ищем подходящие фрагменты…';
      try {
        const result=await api('workspaces/'+selected+'/'+action,body,false,controller.signal);
        if(serial!==state.querySerial||selected!==state.selected)return;
        $('query-count').textContent=`Найдено: ${result.total}. Показано: ${result.hits.length}.`+(result.incompatible_vectors?' Для части записей векторы несовместимы с текущей моделью. Доступен поиск по словам.':'');
        if(result.answer){$('query-answer-box').hidden=false;$('query-answer').textContent=result.answer;result.sources.forEach((hit,i)=>appendQueryHit($('query-sources'),hit,'['+(i+1)+'] '+hit.source));}
        result.hits.forEach(hit=>appendQueryHit($('query-results'),hit));
        if(!result.hits.length)$('query-results').append(node('p','Подходящих фрагментов нет. Попробуйте другой запрос, снимите фильтры или добавьте документы.','muted'));
      }catch(e){if(serial===state.querySerial&&selected===state.selected&&e.name!=='AbortError'){$('query-error').textContent=e.message;$('query-error').hidden=false;}}
      finally{if(serial===state.querySerial)stopQuery();}
    })();
  });
  $('query-cancel').addEventListener('click',()=>{stopQuery();$('query-progress').textContent='Запрос отменён.';$('query-progress').hidden=false;});
  $('query-all').addEventListener('click',run(async()=>{state.queryDocument='';state.queryDocumentName='';await loadView();}));
  $('document-graph').addEventListener('click',run(async()=>{state.graphWorkspace=state.selected;state.view='graph';state.document='';await loadView();}));
  $('search-document').addEventListener('click',run(async()=>{state.queryWorkspace=state.selected;state.queryDocument=state.document;state.queryDocumentName=state.documentName;state.view='query';await loadView();}));
  function displayAnnotationEntry(item,workspace,screen,serial,editable){
    entryReadingContext={item,workspace,screen,serial};$('entry-reading-save').disabled=!item.pin;
    state.entryID=item.id;$('entry-metadata-open').hidden=!editable;$('entry-title').textContent=item.title||item.source||'Запись '+item.id;
    $('entry-meta').textContent=[item.source,item.page?'Страница '+item.page:'',item.important?'Важная':'',(item.tags||[]).join(', '),item.truncated?'Текст ограничен 100 000 символами':'',item.ocr_warning?'Распознавание требует проверки по оригиналу':''].filter(Boolean).join(' · ');
    $('entry-text').hidden=true;$('entry-text').textContent='';if(!$('entry-dialog').open)$('entry-dialog').showModal();
    entryReader?.close();entryReader=annotations.mountReader({host:$('entry-annotation-reader'),passages:[{entry_id:item.id,pin:item.pin,text:item.text,match:false,citation_id:item.citation_id,document_revision:item.document_revision}],documentID:item.document,isCurrent:()=>state.selected===workspace&&state.revision===screen&&entrySerial===serial&&$('entry-dialog').open});
  }
  async function openEntry(id){const revision=state.revision,selected=state.selected,serial=++entrySerial;const item=await api('workspaces/'+selected+'/annotations/entry/'+id);if(revision!==state.revision||selected!==state.selected||serial!==entrySerial)return;displayAnnotationEntry(item,selected,revision,serial,true);}
  const activeJob=j=>['queued','running','cancelling'].includes(j.status);
  const jobStatuses={queued:'В очереди',running:'Обрабатывается',cancelling:'Отменяется',succeeded:'Готово',partial:'Готово с пропущенными страницами',conflict:'Документ изменился',failed:'Не удалось обработать',cancelled:'Отменено',interrupted:'Обработка прервалась'};
  const openJobHistory=installMemJobHistory({state,$,node,api,run,statuses:jobStatuses});
  const stages={analyze:'Анализ файла',text:'Извлечение текста',render:'Подготовка страниц',ocr:'Распознавание текста',embed:'Получение векторов',done:'Документ записан'};
  function renderJobs(){
    const focused=document.activeElement?.dataset.jobAction;const focusedID=document.activeElement?.dataset.jobId;
    const active=state.jobs.filter(activeJob).length;$('jobs-nav').textContent='Обработка'+(active?' · '+active:'');
    $('counts').textContent=active?`Активных заданий: ${active}. Прогресс обновляется автоматически.`:'Последние задания — до 100 записей.';
    $('rows').replaceChildren();$('pagination').hidden=true;
    if(!state.jobs.length){const empty=node('div',undefined,'empty-list');empty.append(node('h2','Заданий пока нет'),node('p','Добавьте документы: здесь появятся этапы обработки и результат.','muted'));$('rows').append(empty);}
    state.jobs.forEach(j=>{
      const row=node('article',undefined,'job-row');row.append(node('h2',j.source_label||j.filename),node('span',jobStatuses[j.status]||j.status,'job-status '+j.status));
      const details=[(j.size/1048576).toLocaleString('ru-RU',{maximumFractionDigits:1})+' МиБ','Попытка '+j.attempt];if(j.is_revision)details.push('Новая редакция');
      if(j.status==='running'){details.push(stages[j.stage]||'Обработка');if(j.page)details.push('Страница '+j.page);if(j.total)details.push(j.current+' из '+j.total);}
      if(j.status==='succeeded'||j.status==='partial'){details.push('Фрагментов: '+j.chunks);if(j.failed_pages)details.push('Пропущено страниц: '+j.failed_pages);}
      row.append(node('p',details.join(' · '),'muted'));
      if(j.status==='running'){const p=node('progress');p.setAttribute('aria-label',stages[j.stage]||'Обработка');if(j.total>0){p.max=j.total;p.value=Math.min(j.current,j.total);}row.append(p);}
      if(j.error)row.append(node('p',j.error,'job-error'));
      const actions=node('div',undefined,'job-actions');
      function action(label,kind){const b=node('button',label,'secondary');b.type='button';b.dataset.jobAction=kind;b.dataset.jobId=j.id;b.setAttribute('aria-label',label+': '+j.filename);b.addEventListener('click',run(async()=>{const selected=state.selected;b.disabled=true;try{await api('workspaces/'+selected+'/jobs/'+j.id+'/'+kind,{});if(selected===state.selected)await loadView();}finally{b.disabled=false;}}));actions.append(b);}
      if(j.status==='queued'||j.status==='running')action('Отменить','cancel');
      if(['failed','cancelled','interrupted'].includes(j.status)||(j.status==='partial'&&!j.is_revision))action('Повторить обработку','retry');
      if(j.status==='partial'&&j.is_revision)row.append(node('p','Для повторной обработки откройте документ и заново загрузите редакцию.','muted'));
      if(j.status==='succeeded'||j.status==='partial'){const b=node('button','Открыть документы','quiet');b.addEventListener('click',run(async()=>{state.view='documents';state.document='';state.offset=0;await loadView();}));actions.append(b);}
      const history=node('button','История обработки','secondary');history.type='button';history.dataset.jobAction='history';history.dataset.jobId=j.id;history.setAttribute('aria-label','История обработки: '+j.filename);history.addEventListener('click',()=>openJobHistory(j));actions.append(history);
      row.append(actions);$('rows').append(row);
    });
    if(focused&&focusedID){const match=Array.from($('rows').querySelectorAll('button')).find(b=>b.dataset.jobAction===focused&&b.dataset.jobId===focusedID);match?.focus({preventScroll:true});}
  }
  async function pollJobs(){
    if(state.polling||!state.session?.authenticated||!current()||document.hidden)return;
    state.polling=true;const id=state.selected,revision=state.revision;
    try{const jobs=await api('workspaces/'+id+'/jobs');if(id!==state.selected||revision!==state.revision)return;
      const changed=JSON.stringify(jobs)!==JSON.stringify(state.jobs);const completed=jobs.some(j=>['succeeded','partial'].includes(j.status)&&!state.jobs.some(old=>old.id===j.id&&old.status===j.status));state.jobs=jobs;
      const active=jobs.filter(activeJob).length;$('jobs-nav').textContent='Обработка'+(active?' · '+active:'');
      if(changed&&state.view==='jobs')renderJobs();else if(completed&&state.view==='documents'&&!$('entry-dialog').open)await loadView();
    }catch(e){if(id===state.selected&&revision===state.revision)error(e.message);}finally{state.polling=false;}
  }
  function selectedFiles(){
    const folder=$('upload-mode').value==='folder';
    const all=Array.from($(folder?'upload-folder':'upload-files').files);
    const supported=all.filter(f=>/\.(md|markdown|pdf|djvu|djv|docx|txt|csv|tsv|json|xml)$/i.test(f.name));
    return {folder,all,files:supported,skipped:all.length-supported.length};
  }
  function previewFiles(){
    const {folder,all,files,skipped}=selectedFiles();
    const max=state.uploadTarget?.revision?1:20;
    const invalid=files.filter(f=>f.size===0||f.size>64*1048576);
    const problem=files.length>max?`Выбрано ${files.length} документов. Допустимо до ${max}; выберите меньшую папку или отдельные файлы.`:invalid.length?'Уберите пустые файлы и файлы больше 64 МиБ.':'';
    $('upload-preview').replaceChildren();$('upload-preview').hidden=!files.length;
    files.slice(0,20).forEach(f=>$('upload-preview').append(node('li',(folder?f.webkitRelativePath:f.name)+' · '+(f.size/1048576).toLocaleString('ru-RU',{maximumFractionDigits:1})+' МиБ')));
    $('upload-summary').textContent=all.length?`Поддерживаемых документов: ${files.length}. Пропущено файлов других форматов: ${skipped}.`:'Выберите '+(folder?'папку.':'файлы.');
    $('upload-error').textContent=problem;$('upload-error').hidden=!problem;
    $('upload-submit').disabled=Boolean(problem)||!files.length||state.uploading;
  }
  async function showUpload(replace=false){
    const selected=state.selected,key=state.managedID,revision=state.revision;
    let pin=null;
    if(replace){pin=await api('workspaces/'+selected+'/revisions/'+key);if(selected!==state.selected||revision!==state.revision)return;}
    state.uploadTarget={selected,revision:pin,returnView:state.view==='graph'?'graph':'jobs'};
    $('upload-form').reset();$('upload-error').hidden=true;$('upload-progress').hidden=true;
    $('upload-title').textContent=replace?'Загрузить новую редакцию':'Добавить документы';
    $('upload-workspace').textContent=current()?.name||'';
    $('upload-mode-label').hidden=replace;$('upload-folder-label').hidden=true;$('upload-files-label').hidden=false;
    $('upload-files').multiple=!replace;$('revision-notice').hidden=!replace;
    $('revision-notice').textContent=replace?'Обновление «'+pin.filename+'». Предыдущий оригинал и история сохранятся.':'';
    $('upload-hint').textContent=replace?'Новая редакция заменит текущую после обработки. Если документ изменится за это время, обновление остановится.':'Изменённый файл здесь добавляется отдельным документом. Для обновления откройте документ и выберите «Загрузить новую редакцию». Одинаковые файлы повторно не обрабатываются.';
    $('upload-document-title-label').hidden=!replace;
    if(pin){$('upload-document-title').value=pin.title;$('upload-tags').value=(pin.tags||[]).join(', ');$('upload-important').checked=pin.important;}
    previewFiles();$('upload-dialog').showModal();
  }
  $('add-documents').addEventListener('click',run(()=>showUpload()));
  $('update-document').addEventListener('click',run(()=>showUpload(true)));
  $('upload-mode').addEventListener('change',()=>{const folder=$('upload-mode').value==='folder';$('upload-files-label').hidden=folder;$('upload-folder-label').hidden=!folder;previewFiles();});
  ['upload-files','upload-folder'].forEach(id=>$(id).addEventListener('change',previewFiles));
  $('upload-dialog').addEventListener('cancel',e=>{if(state.uploading)e.preventDefault();});
  $('upload-form').addEventListener('submit',e=>{e.preventDefault();run(async()=>{
    previewFiles();if($('upload-submit').disabled)return;
    const {files,folder,skipped}=selectedFiles();
    const {selected,revision,returnView}=state.uploadTarget;
    const tags=$('upload-tags').value.split(',').map(x=>x.trim()).filter(Boolean);
    const metadata={tags,important:$('upload-important').checked};
    if(revision)metadata.title=$('upload-document-title').value.trim();
    state.uploading=true;$('upload-error').hidden=true;$('upload-progress').hidden=false;
    const controls=Array.from($('upload-form').querySelectorAll('input,button,select'));controls.forEach(b=>b.disabled=true);
    let added=0,duplicates=0;const failures=[];
    try{
      for(let i=0;i<files.length;i++){
        const label=folder?files[i].webkitRelativePath:files[i].name;
        $('upload-progress').textContent=`Передача ${i+1} из ${files.length}: ${label}. Дождитесь окончания загрузки.`;
        const body=new FormData();body.append('file',files[i]);body.append('metadata',JSON.stringify({...metadata,relative_path:folder?label:''}));
        if(revision)body.append('state',revision.state);
        const route=revision?'revisions/'+revision.document_key:'uploads';
        try{
          const response=await fetch('/api/v1/workspaces/'+selected+'/'+route,{method:'POST',credentials:'same-origin',headers:{'X-Mem-CSRF':state.session.csrf},body});
          const value=await response.json();if(!response.ok)throw new Error(value.error||'Загрузка не удалась');
          if(value.duplicate)duplicates++;else added++;
        }catch(err){failures.push(label+': '+err.message);}
      }
      if(failures.length){$('upload-error').textContent=failures.join('\n');$('upload-error').hidden=false;$('upload-progress').textContent=`Принято: ${added}. Повторных загрузок: ${duplicates}. Можно закрыть окно и посмотреть задания.`;}
      else $('upload-dialog').close();
      if(selected===state.selected){state.view=returnView||'jobs';state.document='';await loadView();$('upload-notice').textContent=`Принято файлов: ${added}. Повторных загрузок: ${duplicates}. Пропущено других форматов: ${skipped}.`;$('upload-notice').hidden=false;}
    }finally{state.uploading=false;controls.forEach(b=>b.disabled=false);}
  })();});
  function showWorkspaceDialog(mode){state.dialog=mode;const create=mode==='create';$('dialog-title').textContent=create?'Создать базу':'Открыть существующую базу';$('save-workspace').textContent=create?'Создать':'Открыть';$('name-label').hidden=!create;$('new-name').required=create;$('new-name').value='';$('new-path').value='';$('path-label').hidden=!state.session.local;$('new-path').required=!create;$('path-hint').textContent=create?'Пустое поле — создать базу в хранилище mem-tool. Указанная папка должна существовать.':'Укажите папку проекта или её подкаталог .mem. Содержимое базы будет открыто для просмотра.';$('dialog-error').hidden=true;$('workspace-dialog').showModal();}
  $('login-form').addEventListener('submit',e=>{e.preventDefault();run(async()=>{const button=e.target.querySelector('button');button.disabled=true;try{await api('login',{user:$('login-user').value.trim(),token:$('login-token').value});$('login-token').value='';await boot();}finally{button.disabled=false;}})();});
  $('workspace-form').addEventListener('submit',e=>{e.preventDefault();run(async()=>{const button=$('save-workspace');button.disabled=true;$('dialog-error').hidden=true;try{const create=state.dialog==='create';const body=create?{name:$('new-name').value.trim(),path:state.session.local?$('new-path').value.trim():''}:{path:$('new-path').value.trim()};const w=await api(create?'workspaces':'workspaces/open',body);$('workspace-dialog').close();state.view='documents';await loadWorkspaces(w.id);}catch(err){$('dialog-error').textContent=err.message;$('dialog-error').hidden=false;}finally{button.disabled=false;}})();});
  $('logout').addEventListener('click',run(async()=>{await api('logout',{});state.session={authenticated:false};state.selected='';state.workspaces=[];state.revision++;await boot();}));
  $('workspace-picker').addEventListener('change',run(async e=>{state.selected=e.target.value;state.offset=0;state.document='';await loadView();}));
  document.querySelectorAll('[data-view]').forEach(b=>b.addEventListener('click',run(async()=>{mapNavigation.checkpoint();state.view=b.dataset.view;state.offset=0;state.document='';await loadView();})));
  $('create').addEventListener('click',()=>showWorkspaceDialog('create'));$('first-create').addEventListener('click',()=>showWorkspaceDialog('create'));$('open').addEventListener('click',()=>showWorkspaceDialog('open'));
  $('refresh').addEventListener('click',run(loadView));$('back-documents').addEventListener('click',run(async()=>{state.document='';state.offset=0;await loadView();}));
  $('previous').addEventListener('click',run(async()=>{state.offset=Math.max(0,state.offset-30);await loadView();}));$('next').addEventListener('click',run(async()=>{state.offset+=30;await loadView();}));
  document.querySelectorAll('.close-dialog').forEach(b=>b.addEventListener('click',()=>b.closest('dialog').close()));
  const graphDocuments=installMemGraphDocuments({state,$,node,api,run,loadView,showUpload});
  const operations=installMemOperations({state,$,node,api,run,loadView});
  const maintenance=installMemMaintenance({state,$,node,api,run,loadView,loadWorkspaces});
  const indexTools=installMemIndexTools({state,$,api,run});
  installMemMapActions({state,$,node,api,run,showMap,drawMap,selectMapNode,centerMapNode,error,change:fn=>mapNavigation.change(fn)});
  const annotationRegions=installMemAnnotationRegions({state,node,api,run});
  const annotationTransfer=installMemAnnotationTransfer({state,node,api,run});
  const annotationReanchor=installMemAnnotationReanchor({state,node,api,run});
  const annotationKnowledge=installMemAnnotationKnowledge({state,node,api,run});
  const annotations=installMemDocumentAnnotations({state,$,node,api,run,regions:annotationRegions,transfer:annotationTransfer,reanchor:annotationReanchor,knowledge:annotationKnowledge,showMap:async id=>{const workspace=state.selected,user=state.session?.user;state.view='maps';state.document='';const loading=loadView(),screen=state.revision;await loading;if(state.selected!==workspace||state.session?.user!==user||state.revision!==screen||state.view!=='maps')return;await showMap(id);},selectMapNode,drawMap,centerMapNode,change:fn=>mapNavigation.change(fn)});
  $('document-annotations-open').addEventListener('click',run(()=>annotations.showList({documentID:state.document})));
  $('map-node-annotations-open').addEventListener('click',run(()=>{const m=state.map,n=m?.view.nodes.find(n=>n.id===m.selectedNode);if(n)return annotations.showList({targetNode:{mapID:m.view.map.id,nodeID:n.id,revision:m.view.map.revision,label:n.label,locked:n.locked}});}));
  const readingWorkspace=installMemReadingWorkspace({state,node,api,run,onOpenMap:openReadingMap});
  $('reading-workspace-open').addEventListener('click',run(()=>readingWorkspace.show()));
  $('entry-reading-save').addEventListener('click',run(async()=>{const c=entryReadingContext;if(c&&c.workspace===state.selected&&c.screen===state.revision&&c.serial===entrySerial&&$('entry-dialog').open)await readingWorkspace.capture(c.item);}));
  $('entry-dialog').addEventListener('close',()=>{if(!$('entry-dialog').open){entryReader?.close();entrySerial++;entryReadingContext=null;}});
  const mapDocument=installMemMapDocument({state,$,node,api,run,annotations,readingWorkspace,renderMapBody,navigation:()=>mapNavigation});
  installMemMapAI({state,$,node,api,run,showMap,drawMap,selectMapNode,centerMapNode,change:fn=>mapNavigation.change(fn)});
  installMemMapFromSources({state,$,node,api,run,showMap});
  const mapImport=installMemMapImport({state,$,node,api,run,showMap});
  const mapStructured=installMemMapStructuredView({state,$,node,api,run,showMap,selectMapNode,onChanged:()=>mapViewControls.render(),change:fn=>mapNavigation.change(fn)});
  const mapViewControls=installMemMapViewControls({state,$,node,api,run,drawMap,selectMapNode,centerMapNode,change:fn=>mapNavigation.change(fn)});
  const mapViewPresets=installMemMapViewPresets({state,$,node,api,run,viewControls:mapViewControls});
  const mapNavigation=installMemMapNavigation({state,$,node,run,viewControls:mapViewControls,showMap,setMapDetails,readSource:()=>mapDocument.capture(),closeSource:()=>mapDocument.close(),openSource:(ref,options)=>mapDocument.open(ref,options)});
  installMemMapViewExport({state,$,node,run});
  installMemMapWorkbench({state,$,node,api,run,selectMapNode,drawMap,centerMapNode,change:fn=>mapNavigation.change(fn)});
  const mapLibrary=installMemMapLibrary({state,$,node,api,run,showMap,error,loadView});
  const mapResize=installMemMapResize({$});
  run(boot)();
  setInterval(pollJobs,2000);
})();
