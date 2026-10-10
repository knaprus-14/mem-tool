'use strict';
function installMemMapFromSources({state,$,node,api,run,showMap}) {
  const dialog=$('map-source-dialog');let current=null,bannerKey='',polling=null,terminal=false,progress=null;
  const scope=()=>JSON.stringify([state.session?.user||'',state.session?.csrf||'',state.selected,state.revision,state.map?.view.map.id]);
  function valid(c){return c&&current===c&&dialog.open&&c.workspace===state.selected&&c.screen===state.revision;}
  function close(){current=null;dialog.close();}
  $('map-source-close').addEventListener('click',close);dialog.addEventListener('cancel',close);dialog.addEventListener('close',()=>{if(!dialog.open)current=null;});
  function controls(c){if(!valid(c))return;dialog.querySelectorAll('input,textarea,select,button').forEach(e=>e.disabled=!!c.busy||!!c.pending);$('map-source-close').disabled=false;$('map-source-submit').disabled=!!c.busy||!c.selected.size;const submitting=!!c.busy&&!!c.pending;$('map-source-submit').textContent=submitting?'Запускаем создание…':c.pending?'Повторить отправку':'Создать карту по источникам';$('map-source-submit').classList.toggle('is-loading',submitting);$('map-source-submit').setAttribute('aria-busy',String(submitting));$('map-source-prev').disabled=c.busy||!!c.pending||!c.offset;$('map-source-next').disabled=c.busy||!!c.pending||c.offset+30>=c.total;}
  function notice(c){const chunks=[...c.selected.values()].reduce((n,d)=>n+d.chunks,0);$('map-source-selection').textContent=`Выбрано документов: ${c.selected.size} / 20 · Фрагментов: ${chunks} / 512`;$('map-source-submit').disabled=c.busy||!c.selected.size||chunks>512;}
  async function list(c,offset){if(!valid(c)||c.busy)return;c.busy=true;controls(c);try{const page=await api('workspaces/'+c.workspace+'/library?kind=documents&offset='+offset);if(!valid(c))return;c.offset=offset;c.total=page.total;const rows=$('map-source-documents');rows.replaceChildren();for(const doc of page.documents){const label=node('label',undefined,'check-label card'),check=node('input');check.type='checkbox';check.checked=c.selected.has(doc.id);label.append(check,node('span',doc.name+' · '+doc.chunks+' фрагм.'));check.addEventListener('change',()=>{if(check.checked&&c.selected.size>=20){check.checked=false;return;}if(check.checked)c.selected.set(doc.id,doc);else c.selected.delete(doc.id);notice(c);});rows.append(label);}if(!page.documents.length)rows.append(node('p','В базе нет обработанных документов. Сначала добавьте их в разделе «Документы».','muted'));$('map-source-page').textContent=page.total?`${offset+1}–${Math.min(offset+30,page.total)} из ${page.total}`:'Нет документов';}catch(e){if(valid(c)){$('map-source-error').textContent=e.message;$('map-source-error').hidden=false;}}finally{c.busy=false;if(valid(c)){controls(c);notice(c);}}}
  $('add-source-map').addEventListener('click',()=>{const c={workspace:state.selected,screen:state.revision,selected:new Map(),offset:0,total:0,busy:false};current=c;$('map-source-form').reset();$('map-source-error').hidden=true;$('map-source-documents').replaceChildren();dialog.showModal();run(()=>list(c,0))();});
  $('map-source-prev').addEventListener('click',run(()=>list(current,Math.max(0,current.offset-30))));$('map-source-next').addEventListener('click',run(()=>list(current,current.offset+30)));
  $('map-source-form').addEventListener('submit',e=>{e.preventDefault();const c=current;if(!valid(c)||c.busy||!c.selected.size)return;c.pending=c.pending||{request_id:crypto.randomUUID().replaceAll('-',''),title:$('map-source-name').value,focus:$('map-source-focus').value,detail:$('map-source-detail').value,documents:[...c.selected.keys()]};c.busy=true;controls(c);$('map-source-error').hidden=true;
    run(async()=>{try{const task=await api('workspaces/'+c.workspace+'/maps/from-sources',c.pending);if(!valid(c))return;close();await showMap(task.map_id);bannerKey='';await poll();}catch(e){if(valid(c)){$('map-source-error').textContent=e.message+' Повторите ту же отправку при сбое сети. Для изменения выбора закройте и снова откройте окно.';$('map-source-error').hidden=false;}}finally{c.busy=false;controls(c);}})();
  });
  function setText(id,text){if($(id).textContent!==text)$(id).textContent=text;}
  function phase(task){
    if(task.status==='queued')return task.queue?.position>0?'В очереди этой базы — № '+task.queue.position:'Ожидаем начала обработки';
    if(task.status==='ready')return 'Сохраняем карту';
    switch(task.phase){
      case 'generate':return 'Выделяем темы'+(task.total>0?` · часть ${task.current} из ${task.total}`:'');
      case 'merging':return 'Объединяем темы и подтемы';
      case 'synthesizing':return 'Группируем темы карты';
      case 'reusing':return 'Восстанавливаем готовые части'+(task.total>0?` · ${task.current} из ${task.total}`:'');
      case 'validating':return 'Проверяем структуру и ссылки';
      case 'preview':return 'Готовим карту к сохранению';
      default:return 'Подготавливаем источники';
    }
  }
  function tick(){
    if(!progress||document.hidden||!state.session?.authenticated||$('map-section').hidden||$('map-source-banner').hidden||progress.key!==scope())return;
    const seconds=Math.max(0,Math.floor((Date.now()-progress.started)/1000));
    setText('map-source-elapsed',`Прошло ${Math.floor(seconds/60)}:${String(seconds%60).padStart(2,'0')}`);
  }
  function display(task,key){
    const active=['queued','running'].includes(task.status)||task.status==='ready'&&!task.error;
    const banner=$('map-source-banner');banner.hidden=false;banner.dataset.state=task.status==='queued'?'queued':active?'active':task.status;
    setText('map-source-indicator',task.status==='queued'?'…':active?'':task.status==='applied'?'✓':task.status==='cancelled'?'−':'!');
    setText('map-source-phase',active?phase(task):task.status==='applied'?'Карта готова':task.status==='cancelled'?'Создание отменено':'Не удалось завершить создание');
    let details=active?'Можно закрыть вкладку; программа mem должна работать.':task.status==='applied'?'Раскрывайте темы кнопками + и задавайте вопросы по выбранной ветви.':task.status==='cancelled'?'Вы можете выбрать источники и запустить создание заново.':task.error||'Откройте предложения AI или повторите создание.';
    if(task.status==='queued'){
      const q=task.queue,head=q?.head;
      details=(q?.ahead>0?'Перед вашим заданием в очереди карт этой базы: '+q.ahead+'. ':'Задание принято и ожидает свободного обработчика. ');
      if(head)details+='Карта впереди: «'+(head.map_title||'Без названия')+'». '+phase(head)+'. ';
      details+='Обработчик выполняет задания последовательно; его могут занимать другие базы, импорт или индексирование. Повторно создавать карту не нужно. Можно закрыть вкладку, но программу mem оставьте запущенной.';
    }
    if(task.can_resume)details+=' Готовые части сохранены: '+(task.saved_parts??task.completed_batches??0)+'. Можно продолжить обработку.';
    setText('map-source-banner-text',details+` Документов: ${task.source_documents}; фрагментов: ${task.source_chunks}.`);
    if(active){
      if(progress?.id!==task.id||progress?.key!==key){const created=Date.parse(task.created_at);progress={id:task.id,key,started:Number.isFinite(created)?created:Date.now()};}
    }else progress=null;
    $('map-source-elapsed').hidden=!active;tick();
    return active;
  }
  async function poll(){
    if(!state.session?.authenticated||!state.map||$('map-section').hidden||document.hidden){polling?.controller.abort();polling=null;return;}
    const map=state.map,id=map.view.map.id,ws=state.selected,key=scope();
    const stillHere=()=>!!state.session?.authenticated&&key===scope()&&!$('map-section').hidden;
    if(polling?.key===key)return;if(polling){polling.controller.abort();polling=null;}
    if(key!==bannerKey){bannerKey=key;terminal=false;progress=null;$('map-source-banner').hidden=true;}
    if(terminal)return;const request={key,controller:new AbortController()};polling=request;
    const timeout=setTimeout(()=>request.controller.abort(),20000);
    const isCurrent=()=>polling===request&&stillHere();
    try{
      const tasks=await api('workspaces/'+ws+'/maps/'+encodeURIComponent(id)+'/ai',undefined,false,request.controller.signal);if(!isCurrent())return;
      const task=tasks.find(t=>t.source_overview);if(!task){terminal=true;progress=null;$('map-source-banner').hidden=true;return;}
      const active=display(task,key);
      const ahead=task.status==='queued'&&task.queue?.head?.map_id!==id?task.queue?.head:null;
      $('map-source-ahead').hidden=!ahead?.map_id;
      $('map-source-ahead').onclick=run(async()=>{if(stillHere()&&ahead?.map_id)await showMap(ahead.map_id);});
      $('map-source-cancel').hidden=!active;
      $('map-source-cancel').onclick=run(async()=>{
        if(!stillHere())return;
        $('map-source-cancel').disabled=true;
        try{await api('workspaces/'+ws+'/maps/'+encodeURIComponent(id)+'/ai/'+task.id+'/cancel',{});terminal=false;await poll();}
        finally{$('map-source-cancel').disabled=false;}
      });
      $('map-source-review').hidden=active||task.status==='applied';$('map-source-review').textContent=task.can_resume?'Продолжить с сохранённого места':task.can_reassemble?'Повторить сборку из готовых частей':task.status==='ready'?'Открыть предложения AI':'Выбрать источники и повторить';
      $('map-source-review').onclick=run(async()=>{
        if(!stillHere()||$('map-source-review').disabled)return;
        if(task.can_resume||task.can_reassemble){
          $('map-source-review').disabled=true;
          try{await api('workspaces/'+ws+'/maps/'+encodeURIComponent(id)+'/ai/'+task.id+(task.can_resume?'/resume':'/reassemble'),{},false);if(stillHere()){terminal=false;await poll();}}
          finally{if(stillHere())terminal=false;$('map-source-review').disabled=false;}
        }else if(task.status==='ready')$('map-ai-open').click();else $('add-source-map').click();
      });
      // Keep retrying publication refresh after a transient network failure.
      if(task.status==='applied'&&map.view.map.revision===1){await showMap(id);if(stillHere())$('map-overview').click();}
      terminal=!active;
    }catch(e){
      if(isCurrent()){
        progress=null;$('map-source-banner').hidden=false;$('map-source-banner').dataset.state='offline';$('map-source-elapsed').hidden=true;
        $('map-source-ahead').hidden=true;$('map-source-cancel').hidden=true;$('map-source-review').hidden=true;
        setText('map-source-indicator','!');setText('map-source-phase','Проверяем связь с сервером');
        setText('map-source-banner-text','Не удалось обновить состояние. Проверьте, что программа mem запущена. Повторим автоматически. '+(request.controller.signal.aborted?'Сервер не ответил за 20 секунд.':e.message));
      }
    }finally{clearTimeout(timeout);if(polling===request)polling=null;}
  }
  setInterval(poll,2000);
  setInterval(tick,1000);
  document.addEventListener('visibilitychange',()=>{if(!document.hidden){tick();poll();}});
}
