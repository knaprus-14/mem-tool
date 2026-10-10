function installMemMapLibrary({state,$,node,api,run,showMap,error,loadView}) {
  let current=null;
  function context(mode){const m=state.map;if(!m)return null;return {mode,selected:state.selected,revision:state.revision,mapID:m.view.map.id,mapRevision:m.view.map.revision,nodeID:m.selectedNode,busy:false};}
  function valid(c){return !!c&&current===c&&$('map-library-dialog').open&&state.selected===c.selected&&state.revision===c.revision;}
  function route(c){return 'workspaces/'+c.selected+'/maps/'+encodeURIComponent(c.mapID);}
  function message(text){$('map-library-error').textContent=text;$('map-library-error').hidden=!text;}
  function controls(disabled){$('map-library-dialog').querySelectorAll('button,input,textarea,select').forEach(x=>x.disabled=disabled||x.dataset.blocked==='true');}
  function close(){if(current?.busy)return;current?.snapshotController?.abort();$('map-library-dialog').close();current=null;}
  function setup(mode,item){
    const c=item?{mode,selected:state.selected,revision:state.revision,mapID:item.id,mapRevision:item.revision,busy:false}:context(mode);if(!c)return null;current?.snapshotController?.abort();current=c;controls(false);message('');$('map-library-status').textContent='';$('map-library-retry').hidden=true;
    ['metadata','export','sources','snapshots','duplicate','delete'].forEach(x=>$('map-library-'+x).hidden=x!==mode);
    $('map-library-title').textContent={delete:'Удаление карты',duplicate:'Создать независимую копию',metadata:'Название и статус карты',export:'Скачать карту',sources:'Источники узла',snapshots:'Сохранённые снимки'}[mode];
    $('map-library-context').textContent=(item?.title||state.map.view.map.title)+(mode==='sources'?' / '+state.map.tree.byID.get(c.nodeID).label:'');
    $('map-library-dialog').showModal();return c;
  }
  async function saved(c,result){
    if(!valid(c))return;if(c.mode==='delete'){$('map-library-dialog').close();current=null;state.map=null;state.offset=0;await loadView();return;}if(c.mode==='duplicate'){$('map-library-dialog').close();current=null;await showMap(result.id);return;}c.pending=null;$('map-library-retry').hidden=true;
    await showMap(c.mapID);if(!valid(c))return;c.mapRevision=state.map.view.map.revision;
    $('map-library-context').textContent=state.map.view.map.title+(c.mode==='sources'?' / '+(state.map.tree.byID.get(c.nodeID)?.label||'Удалённый узел'):'');
    $('map-library-status').textContent=result.replayed?'Эта попытка уже сохранена. Показано текущее состояние.':'Изменение сохранено в истории карты.';
    if(c.mode==='sources')await loadSources(c);
    if(c.mode==='snapshots'){const restored=c.snapshotRestorePending;resetSnapshot(c);await loadSnapshots(c);if(valid(c))$('map-library-status').textContent=restored?'Карта восстановлена как новая ревизия. Восстановление можно отменить в истории карты.':'Снимок сохранён. Содержимое карты не изменено.';c.snapshotRestorePending=false;}
  }
  async function mutate(c,path,body){
    if(!valid(c)||c.busy)return;c.busy=true;controls(true);message('');
    c.pending={path,body};let result;
    try{result=await api(path,body);c.pending=null;await saved(c,result);}
    catch(err){if(valid(c)){message(err.message);$('map-library-status').textContent=result?'Сохранено, но просмотр не обновлён. Закройте окно и обновите карту.':'При сбое связи повторите отправку. При конфликте закройте окно и обновите карту.';$('map-library-retry').hidden=!c.pending;}}
    finally{c.busy=false;if(valid(c)){controls(false);if(c.pending){$('map-library-dialog').querySelectorAll('button,input,textarea,select').forEach(x=>x.disabled=!['map-library-retry','map-library-close'].includes(x.id));}else updateSourceControls(c);}}
  }
  function updateSourceControls(c){if(c.mode!=='sources')return;graphRows.replaceChildren();const locked=state.map.tree.byID.get(c.nodeID)?.locked;attachment.disabled=!!locked;graphPicker.disabled=!!locked;$('map-source-url-save').disabled=locked;$('map-source-picker').disabled=locked;$('map-candidate-prev').disabled=!c.offset;$('map-candidate-next').disabled=c.nextOffset===undefined||c.nextOffset<0;}
  $('map-library-close').addEventListener('click',close);$('map-library-dialog').addEventListener('cancel',e=>{if(current?.busy)e.preventDefault();else {current?.snapshotController?.abort();current=null;}});
  $('map-library-dialog').addEventListener('close',()=>{if(!$('map-library-dialog').open){current?.snapshotController?.abort();current=null;}});
  $('map-library-retry').addEventListener('click',run(()=>{const c=current;if(c?.pending)return mutate(c,c.pending.path,c.pending.body);}));
  async function loadSnapshots(c,before=0){
    const page=await api(route(c)+'/snapshots?before='+before);if(!valid(c))return;if(c.snapshotPreview&&c.snapshotPreview.revision!==page.revision)resetSnapshot(c);c.mapRevision=page.revision;c.before=page.next_before;
    const rows=$('map-snapshot-rows');if(!before)rows.replaceChildren();
    page.items.forEach(item=>{const row=node('section',undefined,'card'),button=node('button','Просмотреть снимок','secondary');button.type='button';button.addEventListener('click',run(()=>previewSnapshot(c,item.id)));row.append(node('h3',item.reason),node('p','Ревизия '+item.revision+' · '+new Date(item.created).toLocaleString('ru-RU'),'muted'),button);rows.append(row);});
    $('map-snapshot-more').hidden=!c.before;
  }
  function resetSnapshot(c){c.snapshotController?.abort();c.snapshotController=null;c.snapshotPreview=null;$('map-snapshot-preview').hidden=true;$('map-snapshot-confirm').checked=false;$('map-snapshot-restore').disabled=true;$('map-snapshot-restore').dataset.blocked='true';}
  async function previewSnapshot(c,id){
    if(!valid(c)||c.busy||c.pending)return;resetSnapshot(c);const controller=new AbortController();c.snapshotController=controller;controls(true);$('map-library-close').disabled=false;message('');$('map-library-status').textContent='Загружаем содержимое снимка…';
    try{
      const preview=await api(route(c)+'/snapshots/'+id+'?revision='+c.mapRevision,undefined,false,controller.signal);
      if(!valid(c)||c.snapshotController!==controller||controller.signal.aborted)return;
      c.snapshotPreview=preview;$('map-snapshot-preview').hidden=false;
      $('map-snapshot-preview-context').textContent=preview.map.title+' · снимок ревизии '+preview.snapshot.revision+' · текущая ревизия '+preview.revision+' · узлов сейчас '+preview.current_node_count+', в снимке '+preview.nodes.length;
      const rows=$('map-snapshot-preview-nodes');rows.replaceChildren();
      const names=new Map(preview.nodes.map(n=>[n.id,n.label]));
      const states={current:'Фрагмент актуален сейчас',stale:'Фрагмент устарел',missing:'Источник недоступен',url:'Веб-ссылка; содержимое не проверялось',graph_node:'Объект графа существует; его доказательства здесь не проверялись',local_file:'Локальный файл не проверялся',attachment:'Вложение сохранено в базе'};
      rows.append(node('p',preview.map.description||'Описание карты отсутствует.'));
      rows.append(node('p','Статус: '+({draft:'черновик',ready:'готова',archived:'в архиве'}[preview.map.status]||'неизвестен')+' · режим: '+({manual:'ручная карта',generated:'создана AI',hybrid:'смешанная'}[preview.map.mode]||'неизвестен'),'muted'));
      const kinds={topic:'Тема',subtopic:'Подтема',fact:'Факт',note:'Заметка',quote:'Цитата',question:'Вопрос',task:'Задача',decision:'Решение',link:'Ссылка'},origins={manual:'вручную',generated:'AI',imported:'импорт'};
      for(const n of preview.nodes){const detail=node('details'),heading=node('summary',n.label+(n.locked?' · закреплён':''));detail.append(heading,node('p',(n.parent_id?'Внутри: '+(names.get(n.parent_id)||n.parent_id):'Корень карты')+' · позиция '+(n.position+1)+' · '+(kinds[n.kind]||'Узел')+' · '+(origins[n.origin]||'происхождение неизвестно'),'muted'));if(n.summary)detail.append(node('p',n.summary));if(n.body)detail.append(node('pre',n.body));for(const source of n.sources){const s=node('section',undefined,'card');s.append(node('strong',source.title),node('p',(states[source.state]||'Состояние не определено')+(source.page?' · страница '+source.page:''),'muted'));if(source.excerpt)s.append(node('pre',source.excerpt));if(source.url)s.append(node('p',source.url));if(source.citation_id)s.append(node('p',source.citation_id,'muted'));detail.append(s);}rows.append(detail);}
      $('map-library-status').textContent='Просмотр ничего не изменяет. Для восстановления подтвердите замену текущего состояния ниже.';
    }catch(err){if(valid(c)&&c.snapshotController===controller&&!controller.signal.aborted){message(err.message);$('map-library-status').textContent='Снимок не загружен. При конфликте закройте окно и обновите карту.';}}
    finally{if(valid(c)&&c.snapshotController===controller){c.snapshotController=null;controls(false);}}
  }
  $('map-snapshot-confirm').addEventListener('change',()=>{const c=current,enabled=valid(c)&&c.mode==='snapshots'&&c.snapshotPreview&&!c.busy&&!c.pending&&$('map-snapshot-confirm').checked;$('map-snapshot-restore').disabled=!enabled;$('map-snapshot-restore').dataset.blocked=String(!enabled);});
  $('map-snapshot-restore-form').addEventListener('submit',e=>{e.preventDefault();const c=current;if(!c||!valid(c)||c.mode!=='snapshots'||!c.snapshotPreview||c.busy||c.pending||!$('map-snapshot-confirm').checked)return;const p=c.snapshotPreview;if(p.revision!==c.mapRevision){resetSnapshot(c);message('Карта изменилась. Заново откройте снимок.');return;}c.snapshotRestorePending=true;run(()=>mutate(c,route(c)+'/snapshots/'+p.snapshot.id+'/restore',{revision:p.revision,snapshot_digest:p.snapshot.document_digest,request_id:crypto.randomUUID().replaceAll('-','')}))();});
  $('map-snapshots-open').addEventListener('click',run(async()=>{const c=setup('snapshots');if(!c)return;resetSnapshot(c);$('map-snapshot-reason').value='';try{await loadSnapshots(c);}catch(err){if(valid(c))message(err.message);}}));
  $('map-snapshot-more').addEventListener('click',run(async()=>{const c=current;if(!c?.before||c.busy)return;c.busy=true;controls(true);try{await loadSnapshots(c,c.before);}catch(err){if(valid(c))message(err.message);}finally{c.busy=false;if(valid(c))controls(false);}}));
  $('map-snapshot-form').addEventListener('submit',e=>{e.preventDefault();const c=current;if(c?.mode!=='snapshots')return;run(()=>mutate(c,route(c)+'/snapshots',{revision:c.mapRevision,request_id:crypto.randomUUID().replaceAll('-',''),reason:$('map-snapshot-reason').value}))();});
  $('map-duplicate-open').addEventListener('click',()=>{const c=setup('duplicate');if(c)$('map-duplicate-title').value=state.map.view.map.title+' — копия';});
  $('map-duplicate-form').addEventListener('submit',e=>{e.preventDefault();const c=current;if(c?.mode!=='duplicate')return;run(()=>mutate(c,route(c)+'/duplicate',{revision:c.mapRevision,request_id:crypto.randomUUID().replaceAll('-',''),title:$('map-duplicate-title').value}))();});
  $('map-delete-open').addEventListener('click',()=>setup('delete'));
  $('map-delete-cancel').addEventListener('click',close);
  $('map-delete-form').addEventListener('submit',e=>{e.preventDefault();const c=current;if(c?.mode!=='delete')return;run(()=>mutate(c,route(c)+'/delete',{revision:c.mapRevision,request_id:crypto.randomUUID().replaceAll('-','')}))();});
  $('map-metadata-open').addEventListener('click',()=>{const c=setup('metadata');if(!c)return;const m=state.map.view.map;$('map-metadata-title').value=m.title;$('map-metadata-description').value=m.description;$('map-metadata-status').value=m.status;});
  $('map-metadata-form').addEventListener('submit',e=>{e.preventDefault();const c=current;if(c?.mode!=='metadata')return;run(()=>mutate(c,route(c)+'/metadata',{revision:c.mapRevision,request_id:crypto.randomUUID().replaceAll('-',''),title:$('map-metadata-title').value,description:$('map-metadata-description').value,status:$('map-metadata-status').value}))();});
  $('map-export-open').addEventListener('click',()=>setup('export'));
  $('map-export-form').addEventListener('submit',e=>{e.preventDefault();const c=current;if(c?.mode!=='export'||c.busy)return;const format=$('map-export-format').value;c.busy=true;controls(true);message('');$('map-library-status').textContent='Готовим полную карту…';
    run(async()=>{try{
      const response=await fetch('/api/v1/'+route(c)+'/export',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-Mem-CSRF':state.session.csrf},body:JSON.stringify({format,revision:c.mapRevision})});
      if(!response.ok){const value=await response.json();throw new Error(value.error||'Экспорт не удался');}
      const blob=await response.blob();if(!valid(c))return;const url=URL.createObjectURL(blob),a=node('a');a.href=url;a.download='mind-map.'+({markdown:'md',obsidian:'md',mermaid:'mmd'}[format]||format);a.click();setTimeout(()=>URL.revokeObjectURL(url),30000);$('map-library-status').textContent='Файл подготовлен и передан браузеру для сохранения.';
    }catch(err){if(valid(c)){message(err.message);$('map-library-status').textContent='Файл не создан. Измените формат или обновите карту.';}}finally{c.busy=false;if(valid(c))controls(false);}})();
  });
  async function loadSources(c){
    const page=await api(route(c)+'/nodes/'+encodeURIComponent(c.nodeID)+'/sources');if(!valid(c))return;c.mapRevision=page.revision;
    const rows=$('map-source-rows');rows.replaceChildren();if(!page.items.length)rows.append(node('p','Источников пока нет. Прикрепите фрагмент документа или веб-ссылку.','muted'));
    page.items.forEach((s,i)=>{
      const row=node('section',undefined,'card');row.append(node('h3',s.title),node('p',({current:s.kind==='url'?'Веб-ссылка сохранена; содержимое сайта не проверялось.':'Источник актуален',stale:'Источник изменился',missing:'Источник недоступен',attachment:'Файл сохранён в базе',local_file:'Локальный файл: открытие доступно в прежнем редакторе'}[s.state]||s.state)+(s.page?' · Страница '+s.page:''),'muted'));
      if(s.attachment){const a=node('a','Скачать вложение');a.href='/api/v1/'+route(c)+'/nodes/'+encodeURIComponent(c.nodeID)+'/sources/'+encodeURIComponent(s.id)+'/attachment?revision='+c.mapRevision;row.append(a);}
      if(s.graph_node_id){const a=node('a','Открыть граф знаний ↗');a.href='/graph/'+c.selected+'/#node='+encodeURIComponent(s.graph_node_id);a.target='_blank';a.rel='noopener';row.append(a);}
      if(s.url){const a=node('a','Открыть веб-ссылку ↗');a.href=s.url;a.target='_blank';a.rel='noopener noreferrer';row.append(a);}
      if(s.excerpt){const details=node('details'),summary=node('summary','Сохранённый фрагмент');details.append(summary,node('pre',s.excerpt));row.append(details);}
      if(s.citation_id)row.append(node('p',s.citation_id,'muted'));
      if(!state.map.tree.byID.get(c.nodeID)?.locked){
        [['remove','Убрать источник',0],['move','Выше',i-1],['move','Ниже',i+1]].forEach(([action,label,position])=>{
          const b=node('button',label,'secondary');b.type='button';b.disabled=action==='move'&&(position<0||position>=page.items.length);b.dataset.blocked=String(b.disabled);b.addEventListener('click',run(()=>mutate(c,route(c)+'/nodes/'+encodeURIComponent(c.nodeID)+'/source-'+action,{revision:c.mapRevision,request_id:crypto.randomUUID().replaceAll('-',''),source_id:s.id,position})));row.append(b);
        });
      }rows.append(row);
    });updateSourceControls(c);
  }
  $('map-node-sources-open').addEventListener('click',run(async()=>{const c=setup('sources');if(!c)return;$('map-source-url-form').reset();$('map-source-candidates').hidden=true;$('map-source-rows').replaceChildren(node('p','Загрузка…'));try{await loadSources(c);}catch(err){if(valid(c))message(err.message);}}));
  $('map-source-url-form').addEventListener('submit',e=>{e.preventDefault();const c=current;if(c?.mode!=='sources')return;run(()=>mutate(c,route(c)+'/nodes/'+encodeURIComponent(c.nodeID)+'/source-add',{revision:c.mapRevision,request_id:crypto.randomUUID().replaceAll('-',''),url:$('map-source-url').value,title:$('map-source-title').value}))();});
  async function candidates(c,offset){
    if(!valid(c)||c.busy)return;c.busy=true;controls(true);message('');
    try{
      const page=await api('workspaces/'+c.selected+'/maps/candidates?offset='+offset);if(!valid(c))return;c.offset=offset;c.nextOffset=page.next_offset;
      $('map-source-candidates').hidden=false;const rows=$('map-candidate-rows');rows.replaceChildren();
      if(!page.items.length)rows.append(node('p','Нет фрагментов с происхождением из документа. Добавьте документ в библиотеку.','muted'));
      page.items.forEach(hit=>{const row=node('section',undefined,'card');row.append(node('h3',hit.title||hit.source),node('p',hit.source+(hit.page?' · Страница '+hit.page:''),'muted'),node('p',hit.text+(hit.truncated?'…':'')));const b=node('button','Прикрепить фрагмент','secondary');b.type='button';b.addEventListener('click',run(()=>mutate(c,route(c)+'/nodes/'+encodeURIComponent(c.nodeID)+'/source-add',{revision:c.mapRevision,request_id:crypto.randomUUID().replaceAll('-',''),entry_id:hit.id,pin:hit.pin})));row.append(b);rows.append(row);});
      $('map-candidate-page').textContent=page.items.length?`${offset+1}–${offset+page.items.length}`:'0';
    }catch(err){if(valid(c))message(err.message);}finally{c.busy=false;if(valid(c)){controls(false);updateSourceControls(c);}}
  }
  $('map-source-picker').addEventListener('click',run(()=>candidates(current,0)));$('map-candidate-prev').addEventListener('click',run(()=>candidates(current,Math.max(0,current.offset-30))));$('map-candidate-next').addEventListener('click',run(()=>{if(current.nextOffset>=0)return candidates(current,current.nextOffset);}));
  const attachment=node('input'),attachLabel=node('label','Прикрепить файл (до 8 МиБ)');attachment.type='file';attachLabel.append(attachment);$('map-library-sources').append(attachLabel);
  attachment.addEventListener('change',run(async()=>{const c=current,file=attachment.files[0];if(!c||c.mode!=='sources'||c.busy||!file)return;if(file.size>8*1024*1024){message('Файл больше 8 МиБ');return;}c.busy=true;controls(true);message('');try{const params=new URLSearchParams({revision:String(c.mapRevision),request_id:crypto.randomUUID().replaceAll('-',''),name:file.name});const response=await fetch('/api/v1/'+route(c)+'/nodes/'+encodeURIComponent(c.nodeID)+'/attachment?'+params,{method:'POST',credentials:'same-origin',headers:{'X-Mem-CSRF':state.session.csrf},body:file});const value=await response.json();if(!response.ok)throw new Error(value.error||'Не удалось сохранить вложение');await saved(c,value);}catch(err){if(valid(c))message(err.message+' Перед повтором обновите список источников.');}finally{c.busy=false;attachment.value='';if(valid(c))controls(false);}}));
  const graphPicker=node('button','Прикрепить узел графа','secondary'),graphRows=node('div');graphPicker.type='button';$('map-library-sources').append(graphPicker,graphRows);
  async function graphCandidates(offset){const c=current;if(!c||c.mode!=='sources'||c.busy)return;const items=await api('workspaces/'+c.selected+'/maps/graph-candidates?offset='+offset);if(!valid(c))return;graphRows.replaceChildren();if(!items.length)graphRows.append(node('p','Узлов нет. Сначала создайте граф знаний.','muted'));items.forEach(item=>{const b=node('button',item.label+' · '+item.kind,'secondary');b.type='button';b.addEventListener('click',run(()=>mutate(c,route(c)+'/nodes/'+encodeURIComponent(c.nodeID)+'/graph-source',{revision:c.mapRevision,request_id:crypto.randomUUID().replaceAll('-',''),graph_id:item.id})));graphRows.append(b);});for(const [label,next]of [['Назад',offset-30],['Далее',offset+30]]){const b=node('button',label,'quiet');b.type='button';b.disabled=next<0||(next>offset&&items.length<30);b.addEventListener('click',run(()=>graphCandidates(next)));graphRows.append(b);}}
  graphPicker.addEventListener('click',run(()=>graphCandidates(0)));
  return {deleteMap:item=>setup('delete',item)};
}
