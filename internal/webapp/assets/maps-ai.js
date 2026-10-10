'use strict';
function installMemMapAI({state,$,node,api,run,showMap,drawMap,selectMapNode,centerMapNode,change=fn=>fn()}) {
  const dialog=$('map-ai-dialog');let current=null;
  function valid(c){return current===c&&dialog.open&&state.selected===c.workspace&&state.revision===c.screen;}
  function route(c){return 'workspaces/'+c.workspace+'/maps/'+encodeURIComponent(c.mapID);}
  function message(text){$('map-ai-error').textContent=text;$('map-ai-error').hidden=!text;}
  function close(){current=null;dialog.close();}
  $('map-ai-close').addEventListener('click',close);dialog.addEventListener('cancel',close);dialog.addEventListener('close',()=>{if(!dialog.open)current=null;});
  function controls(c){
    if(!valid(c))return;dialog.querySelectorAll('button,input,textarea,select').forEach(x=>x.disabled=Boolean(c.busy));$('map-ai-close').disabled=false;
    if(c.pendingStart)$('map-ai-form').querySelectorAll('input,textarea,select').forEach(x=>x.disabled=true);
    $('map-ai-start').textContent=c.pendingStart?'Повторить отправку задания':'Подготовить предложение';
    $('map-ai-start').disabled=c.busy||state.map.tree.byID.get(c.nodeID)?.locked;
    $('map-ai-apply').disabled=c.busy||!c.review||c.review.task.status!=='ready'||!c.selected?.size;
    if(c.pendingApply)$('map-ai-proposals').querySelectorAll('input').forEach(x=>x.disabled=true);
    $('map-ai-apply').textContent=c.pendingApply?'Повторить применение':'Применить выбранное к карте';
    $('map-ai-candidate-prev').disabled=c.busy||!c.offset;$('map-ai-candidate-next').disabled=c.busy||c.nextOffset===undefined||c.nextOffset<0;
  }
  async function call(c,path,body){if(!valid(c)||c.busy)return;c.busy=true;controls(c);message('');try{return await api(path,body);}catch(e){if(valid(c))message(e.message);}finally{c.busy=false;controls(c);}}
  async function loadTasks(c){
    if(!valid(c)||c.busy||c.polling)return;c.polling=true;
    try{const tasks=await api(route(c)+'/ai');if(!valid(c))return;const rows=$('map-ai-tasks');rows.replaceChildren();
      if(!tasks.length)rows.append(node('p','Предложений для этой карты пока нет.','muted'));
      const names={queued:'В очереди',running:'Генерация',ready:'Готово к просмотру',applied:'Применено',failed:'Остановлено',cancelled:'Отменено'};
      for(const task of tasks){const row=node('section',undefined,'card');row.append(node('h3',(names[task.status]||task.status)+' · '+(state.map.tree.byID.get(task.node_id)?.label||'Удалённый узел')),node('p',task.prompt.slice(0,300)));
        if(task.total)row.append(node('p',`Часть ${task.current} / ${task.total}`,'muted'));if(task.error)row.append(node('p',task.error,'error'));
        if(['failed','cancelled'].includes(task.status)){const b=node('button','Заполнить задачу заново','secondary');b.type='button';b.addEventListener('click',()=>{if(!valid(c)||c.busy)return;const target=state.map.tree.byID.get(task.node_id);if(!target){message('Узел удалён. Выберите другую ветвь карты.');return;}c.nodeID=task.node_id;c.revision=state.map.view.map.revision;c.pendingStart=null;c.pins.clear();$('map-ai-form').reset();$('map-ai-use-branch').checked=true;$('map-ai-prompt').value=task.prompt;$('map-ai-action').value=task.action;$('map-ai-candidates').hidden=true;$('map-ai-selected').textContent='Выбрано дополнительных фрагментов: 0 / 30';$('map-ai-context').textContent=state.map.view.map.title+' / '+target.label;$('map-ai-status').textContent='Задача заполнена. Проверьте источники заново перед отправкой.';controls(c);$('map-ai-form').scrollIntoView({block:'start'});});row.append(b);}
        if(['ready','applied'].includes(task.status)){const b=node('button','Посмотреть предложение','secondary');b.type='button';b.addEventListener('click',run(()=>review(c,task.id)));row.append(b);}
        if(['queued','running','ready'].includes(task.status)){const b=node('button','Отменить предложение','quiet');b.type='button';b.addEventListener('click',run(async()=>{const result=await call(c,route(c)+'/ai/'+task.id+'/cancel',{});if(!result||!valid(c))return;if(c.review?.task.id===task.id){c.review=null;$('map-ai-review').hidden=true;}await loadTasks(c);}));row.append(b);}
        rows.append(row);
      }controls(c);
    }catch(e){if(valid(c))message(e.message);}finally{c.polling=false;}
  }
  async function poll(c){if(!valid(c))return;await loadTasks(c);if(valid(c))setTimeout(()=>poll(c),2000);}
  $('map-ai-open').addEventListener('click',()=>{
    const m=state.map;if(!m)return;const c={workspace:state.selected,screen:state.revision,mapID:m.view.map.id,nodeID:m.selectedNode,revision:m.view.map.revision,pins:new Map(),busy:false};current=c;
    $('map-ai-form').reset();$('map-ai-use-branch').checked=true;$('map-ai-selected').textContent='Выбрано дополнительных фрагментов: 0 / 30';$('map-ai-context').textContent=m.view.map.title+' / '+m.tree.byID.get(c.nodeID).label;
    $('map-ai-review').hidden=true;$('map-ai-candidates').hidden=true;$('map-ai-status').textContent='';$('map-ai-tasks').replaceChildren();message('');dialog.showModal();controls(c);poll(c);
  });
  $('map-ai-refresh').addEventListener('click',run(()=>loadTasks(current)));
  $('map-ai-form').addEventListener('submit',e=>{e.preventDefault();const c=current;if(!valid(c)||c.busy)return;
    c.pendingStart=c.pendingStart||{request_id:crypto.randomUUID().replaceAll('-',''),revision:c.revision,action:$('map-ai-action').value,prompt:$('map-ai-prompt').value,use_branch:$('map-ai-use-branch').checked,allow_ungrounded:$('map-ai-ungrounded').checked,entries:[...c.pins.values()]};
    run(async()=>{const task=await call(c,route(c)+'/nodes/'+encodeURIComponent(c.nodeID)+'/ai',c.pendingStart);if(!valid(c))return;if(task){c.pendingStart=null;$('map-ai-status').textContent='Задание сохранено. Вкладку можно закрыть; программа mem должна работать.';await loadTasks(c);}else $('map-ai-status').textContent='Отправка не подтверждена. При сбое сети повторите её. Для исправления параметров закройте и заново откройте окно.';controls(c);})();
  });
  async function candidates(c,offset){const page=await call(c,'workspaces/'+c.workspace+'/maps/candidates?offset='+offset);if(!page||!valid(c))return;c.offset=offset;c.nextOffset=page.next_offset;$('map-ai-candidates').hidden=false;const rows=$('map-ai-candidate-rows');rows.replaceChildren();
    for(const hit of page.items){const row=node('section',undefined,'card'),label=node('label',undefined,'check-label'),check=node('input');check.type='checkbox';check.checked=c.pins.has(hit.id);label.append(check,node('span',hit.source+(hit.page?' · стр. '+hit.page:'')));row.append(label,node('p',hit.text));check.addEventListener('change',()=>{if(check.checked&&c.pins.size>=30){check.checked=false;message('Можно выбрать до 30 дополнительных фрагментов.');return;}if(check.checked)c.pins.set(hit.id,{id:hit.id,pin:hit.pin});else c.pins.delete(hit.id);$('map-ai-selected').textContent='Выбрано дополнительных фрагментов: '+c.pins.size+' / 30';});rows.append(row);}
    if(!page.items.length)rows.append(node('p','Нет фрагментов с происхождением из документа.','muted'));controls(c);
  }
  $('map-ai-picker').addEventListener('click',run(()=>candidates(current,0)));$('map-ai-candidate-prev').addEventListener('click',run(()=>candidates(current,Math.max(0,current.offset-30))));$('map-ai-candidate-next').addEventListener('click',run(()=>candidates(current,current.nextOffset)));
  async function review(c,id){const result=await call(c,route(c)+'/ai/'+id);if(!result||!valid(c))return;c.review=result;c.selected=new Set(result.proposals.map(p=>p.id));c.pendingApply=null;
    $('map-ai-review').hidden=false;$('map-ai-grounding').textContent=result.grounded?'Предложения содержат ссылки на выбранные фрагменты. Проверьте содержание перед применением.':'Предложение создано без источников. Это идеи модели, а не подтверждённые факты.';
    $('map-ai-review-context').textContent=(state.map.tree.byID.get(result.task.node_id)?.label||'Удалённый узел')+' · ревизия '+result.task.revision+(result.synthesis?.status==='grouped'?(result.synthesis.method==='hierarchical_whole_subtrees'?' · Многопроходная группировка: ':' · Глобальная группировка: ')+result.synthesis.groups+' тем, '+result.synthesis.input_proposals+' исходных узлов; утверждения не объединялись.':'');
    const rows=$('map-ai-proposals');rows.replaceChildren();const byID=new Map(result.proposals.map(p=>[p.id,p]));
    for(const p of result.proposals){const row=node('section',undefined,'card'),label=node('label',undefined,'check-label'),check=node('input');check.type='checkbox';check.checked=true;check.dataset.proposal=p.id;label.append(check,node('span',p.label||({fill_node:'Дополнение текста',find_sources:'Источник для узла'}[result.task.action]||'Предложение')));row.append(label);
      if(p.kind)row.append(node('p','Тип: '+({topic:'тема',subtopic:'подтема',fact:'факт',note:'заметка',quote:'цитата',question:'вопрос',task:'задача',decision:'решение',link:'ссылка'}[p.kind]||p.kind),'muted'));if(p.parent_proposal_id)row.append(node('p','Внутри: '+byID.get(p.parent_proposal_id)?.label,'muted'));if(p.summary)row.append(node('p',p.summary));if(p.body_markdown)row.append(node('pre',p.body_markdown));if(p.reason)row.append(node('p',p.reason));
      for(const source of p.evidence||[]){const detail=node('details');detail.append(node('summary',(source.source_path||'Документ')+' · стр. '+source.page),node('pre',source.excerpt));row.append(detail);}
      check.addEventListener('change',()=>{if(check.checked){let item=p;for(let i=0;item&&i<1000;i++){c.selected.add(item.id);item=byID.get(item.parent_proposal_id);}}else{c.selected.delete(p.id);let changed=true;while(changed){changed=false;for(const item of result.proposals){if(item.parent_proposal_id&&!c.selected.has(item.parent_proposal_id)&&c.selected.delete(item.id))changed=true;}}}rows.querySelectorAll('input').forEach(x=>x.checked=c.selected.has(x.dataset.proposal));controls(c);});rows.append(row);
    }controls(c);$('map-ai-review').scrollIntoView({block:'start'});
  }
  $('map-ai-apply').addEventListener('click',run(async()=>{const c=current;if(!c?.review||c.busy)return;const task=c.review.task;
    c.pendingApply=c.pendingApply||{request_id:crypto.randomUUID().replaceAll('-',''),revision:task.revision,digest:c.review.digest,proposal_ids:[...c.selected]};
    const result=await call(c,route(c)+'/ai/'+task.id+'/apply',c.pendingApply);if(!valid(c))return;
    if(result){c.pendingApply=null;await showMap(c.mapID);if(!valid(c))return;c.revision=state.map.view.map.revision;const m=state.map;change(()=>{m.collapsed=MemMapLayout.reveal(m.tree,m.collapsed,task.node_id);m.collapsed.delete(task.node_id);drawMap();selectMapNode(task.node_id,true);centerMapNode(task.node_id);});c.review.task.status='applied';$('map-ai-status').textContent='Выбранные изменения применены. Их можно отменить через историю карты.';await loadTasks(c);}controls(c);
  }));
}
