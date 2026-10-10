'use strict';
function installMemMapWorkbench({state,$,node,api,run,selectMapNode,drawMap,centerMapNode,change=fn=>fn()}) {
  let current=null;
  const dialog=$('map-workbench-dialog');
  function valid(c){return current===c&&dialog.open&&state.selected===c.workspace&&state.revision===c.screen;}
  function path(c){return 'workspaces/'+c.workspace+'/maps/'+encodeURIComponent(c.mapID);}
  function message(text){$('map-workbench-error').textContent=text;$('map-workbench-error').hidden=!text;}
  function busy(c,on){c.busy=on;if(!valid(c))return;dialog.querySelectorAll('button,input,select,textarea').forEach(x=>x.disabled=on);$('map-workbench-close').disabled=false;$('map-branch-cancel').hidden=!on;$('map-branch-cancel').disabled=false;if(!on)update(c);}
  function close(){current?.controller?.abort();current=null;dialog.close();}
  $('map-workbench-close').addEventListener('click',close);dialog.addEventListener('cancel',close);dialog.addEventListener('close',()=>{if(!dialog.open){current?.controller?.abort();current=null;}});
  $('map-branch-cancel').addEventListener('click',()=>current?.controller?.abort());
  function setup(mode){
    if(!state.map)return null;current?.controller?.abort();const m=state.map;
    const c={mode,workspace:state.selected,screen:state.revision,mapID:m.view.map.id,revision:m.view.map.revision,nodeID:m.selectedNode,index:0,revealed:false,controller:null};current=c;
    $('map-workbench-title').textContent=mode==='compare'?'Сравнение карт':'Работа с ветвью';
    $('map-workbench-context').textContent=m.view.map.title+(mode==='branch'?' / '+m.tree.byID.get(c.nodeID).label:'');
    $('map-workbench-branch').hidden=mode!=='branch';$('map-workbench-compare').hidden=mode!=='compare';
    $('map-workbench-result').replaceChildren();$('map-branch-answer').replaceChildren();$('map-branch-question').value='';$('map-branch-summary').textContent='';$('map-study-front').textContent='';$('map-study-back').textContent='';
    message('');dialog.showModal();busy(c,false);return c;
  }
  async function request(c,route,body){if(!valid(c)||c.busy)return;c.controller=new AbortController();busy(c,true);message('');try{return await api(route,body,false,c.controller.signal);}catch(e){if(valid(c))message(e.name==='AbortError'?'Запрос отменён.':e.message);}finally{busy(c,false);}}
  function update(c){
    if(c.mode==='compare'){$('map-compare-submit').disabled=!c.maps?.length;$('map-compare-prev').disabled=!c.offset;$('map-compare-next').disabled=!c.maps||c.offset+c.maps.length>=c.total;return;}
    const list=cards(c),card=list[c.index],progress=card&&c.data?.progress?.[card.id];
    $('map-branch-ask').disabled=!c.data?.manifest.evidence.length;
    for(const id of ['map-study-flip','map-study-export','map-study-node'])$(id).disabled=!card;
    $('map-study-history').disabled=!card||!c.revealed;
    for(const id of ['map-study-again','map-study-hard','map-study-know','map-study-easy'])$(id).disabled=!card||!c.revealed||progress?.current===false;
    for(const id of ['map-study-response','map-study-confidence','map-study-hint'])$(id).disabled=!card||c.revealed;
    $('map-study-prev').disabled=!card||c.index===0;$('map-study-next').disabled=!card||c.index>=list.length-1;
  }
  function cards(c){const all=c?.data?.study.cards||[];return $('map-study-due-only').checked?all.filter(card=>c.data.progress?.[card.id]?.due):all;}
  function renderCard(c){
    const list=cards(c);c.index=Math.max(0,Math.min(c.index,list.length-1));const card=list[c.index],progress=card&&c.data.progress?.[card.id];c.revealed=false;c.reviewRequest=null;
    const due=Object.values(c.data.progress||{}).filter(p=>p.due).length;
    $('map-study-position').textContent=card?`${c.index+1} / ${list.length} · К повторению: ${due} · ${progress?.due_at?'Следующее: '+new Date(progress.due_at).toLocaleString():'Новая карточка'}${progress?.current===false?' · Источник изменился — сначала проверьте материал':''}`:($('map-study-due-only').checked?'На сейчас повторений нет. Можно открыть все карточки.':'В этой ветви пока нет материала для карточек. Добавьте текст или подтемы.');
    $('map-study-front').textContent=card?.front||'';$('map-study-back').textContent=card?.back||'';$('map-study-back').hidden=true;$('map-study-flip').textContent='Показать ответ';
    $('map-study-response').value='';$('map-study-confidence').value='';$('map-study-hint').checked=false;$('map-study-history-rows').replaceChildren();
    const rows=$('map-study-sources');rows.replaceChildren();
    for(const s of card?.sources||[]){rows.append(node('p',s.title+' · '+({current:'актуален',stale:'изменился',missing:'недоступен'}[s.evidence_state]||'привязка источника'),'muted'));}
    update(c);$('map-study-front').tabIndex=0;$('map-study-front').focus();
  }
  $('map-branch-open').addEventListener('click',run(async()=>{const c=setup('branch');if(!c)return;$('map-study-due-only').checked=false;const data=await request(c,path(c)+'/nodes/'+encodeURIComponent(c.nodeID)+'/study',{revision:c.revision});if(!data||!valid(c))return;c.data=data;
    $('map-branch-summary').textContent=`Ветвь: ${data.manifest.node_ids.length} узлов. Актуальных фрагментов: ${data.manifest.evidence.length}. Недоступных или изменённых: ${data.unavailable_sources}. Других ссылок: ${data.other_sources}.`;
    renderCard(c);
  }));
  $('map-study-flip').addEventListener('click',()=>{const c=current;if(!c?.data)return;const back=$('map-study-back');if(!c.revealed){const confidence=$('map-study-confidence');if(!confidence.checkValidity()){confidence.reportValidity();return;}c.response=$('map-study-response').value;c.confidence=confidence.value===''?null:Number(confidence.value);c.usedHint=$('map-study-hint').checked;c.revealed=true;}back.hidden=!back.hidden;$('map-study-flip').textContent=back.hidden?'Показать ответ':'Скрыть ответ';update(c);});
  for(const [id,step] of [['map-study-prev',-1],['map-study-next',1]])$(id).addEventListener('click',()=>{const c=current;if(!c?.data)return;c.index=Math.max(0,Math.min(cards(c).length-1,c.index+step));renderCard(c);});
  $('map-study-due-only').addEventListener('change',()=>{if(current?.data){current.index=0;renderCard(current);}});
  for(const [id,grade] of [['map-study-again','again'],['map-study-hard','hard'],['map-study-know','good'],['map-study-easy','easy']])$(id).addEventListener('click',run(async()=>{
    const c=current,card=cards(c)[c?.index];if(!card||!c.revealed||c.busy)return;
    const previous=c.data.progress[card.id];if(previous?.current===false)return;
    if(!c.reviewRequest||c.reviewRequest.grade!==grade)c.reviewRequest={revision:c.revision,manifest:c.data.manifest.digest,card_id:card.id,request_id:crypto.randomUUID().replaceAll('-',''),previous_attempt_id:previous?.last_attempt_id||0,grade,response:c.response||'',confidence:c.confidence,used_hint:!!c.usedHint};
    const result=await request(c,path(c)+'/nodes/'+encodeURIComponent(c.nodeID)+'/study-review',c.reviewRequest);if(!result||!valid(c))return;c.data.progress[card.id]=result.progress;renderCard(c);$('map-branch-summary').textContent='Оценка сохранена. Следующее повторение: '+new Date(result.progress.due_at).toLocaleString()+'. Это ваша самооценка, не автоматическая проверка ответа.';
  }));
  dialog.addEventListener('keydown',e=>{const c=current;if(!c?.data||c.mode!=='branch'||c.busy||/^(INPUT|TEXTAREA|SELECT|BUTTON)$/.test(e.target.tagName)||e.ctrlKey||e.metaKey||e.altKey)return;const target=e.code==='Space'?'map-study-flip':({'1':'map-study-again','2':'map-study-hard','3':'map-study-know','4':'map-study-easy'}[e.key]);if(target&&!$(target).disabled){e.preventDefault();$(target).click();}});
  $('map-study-node').addEventListener('click',()=>{const card=cards(current)[current?.index];if(!card)return;close();const m=state.map;change(()=>{m.collapsed=MemMapLayout.reveal(m.tree,m.collapsed,card.node_id);m.depth=null;m.scale=1;drawMap();selectMapNode(card.node_id,true);centerMapNode(card.node_id);});});
  $('map-study-export').addEventListener('click',()=>{const pack=current?.data?.study;if(!pack)return;const url=URL.createObjectURL(new Blob([pack.markdown],{type:'text/markdown;charset=utf-8'})),a=node('a');a.href=url;a.download='mind-map-study.md';a.click();setTimeout(()=>URL.revokeObjectURL(url),30000);});
  $('map-study-history').addEventListener('click',run(async()=>{const c=current,card=cards(c)[c?.index];if(!card||!c.revealed||c.busy)return;const result=await request(c,path(c)+'/nodes/'+encodeURIComponent(c.nodeID)+'/study-history',{revision:c.revision,card_id:card.id});if(!result||!valid(c))return;const rows=$('map-study-history-rows');rows.replaceChildren(node('h3','Последние ответы'));for(const item of result.items){const block=node('details');block.append(node('summary',new Date(item.created).toLocaleString()+' · '+({again:'Ещё раз',hard:'Трудно',good:'Хорошо',easy:'Легко'}[item.grade]||item.grade)),node('p','Ревизия '+item.revision+(item.matches_current?' · Текущий материал':' · Прежний материал')+(item.used_hint?' · С подсказкой':' · Без отмеченных подсказок')+(item.confidence!==undefined?' · Уверенность: '+item.confidence+'%':'')),node('p',item.prompt),node('pre',item.response||'Ответ не записан'),node('p','Материал на момент попытки'),node('pre',item.answer));rows.append(block);}if(!result.items.length)rows.append(node('p','Сохранённых попыток пока нет.'));if(result.more)rows.append(node('p','Показаны последние попытки в пределах 20 записей и 2 МиБ. Полная история сохранена в базе.','muted'));}));
  $('map-branch-form').addEventListener('submit',e=>{e.preventDefault();const c=current;if(!c?.data||c.busy)return;$('map-branch-answer').replaceChildren(node('p','Модель готовит ответ…'));run(async()=>{
    const result=await request(c,path(c)+'/nodes/'+encodeURIComponent(c.nodeID)+'/ask',{revision:c.revision,question:$('map-branch-question').value,manifest:c.data.manifest.digest});if(!valid(c))return;
    const rows=$('map-branch-answer');rows.replaceChildren();if(!result)return;rows.append(node('h3',result.insufficient?'В источниках недостаточно данных':'Ответ по источникам ветви'),node('pre',result.answer));
    for(const source of result.sources||[]){const details=node('details');details.append(node('summary',source.citation_label),node('pre',source.text));rows.append(details);}
  })();});
  $('map-compare-open').addEventListener('click',run(async()=>{const c=setup('compare');if(!c)return;c.offset=0;await loadChoices(c);}));
  async function loadChoices(c){const page=await request(c,'workspaces/'+c.workspace+'/maps?archived=true&offset='+c.offset+'&limit=30');if(!page||!valid(c))return;c.maps=page.items;c.total=page.total;const picker=$('map-compare-other');picker.replaceChildren();for(const m of c.maps){const option=node('option',m.title+' · ревизия '+m.revision);option.value=m.id;picker.append(option);} $('map-compare-page').textContent=`${page.total?c.offset+1:0}–${c.offset+page.items.length} / ${page.total}`;$('map-compare-prev').disabled=!c.offset;$('map-compare-next').disabled=c.offset+page.items.length>=page.total;update(c);}
  $('map-compare-prev').addEventListener('click',run(()=>{const c=current;c.offset=Math.max(0,c.offset-30);return loadChoices(c);}));$('map-compare-next').addEventListener('click',run(()=>{const c=current;c.offset+=30;return loadChoices(c);}));
  $('map-compare-form').addEventListener('submit',e=>{e.preventDefault();const c=current,other=c?.maps?.find(m=>m.id===$('map-compare-other').value);if(!other)return;run(async()=>{
    const result=await request(c,path(c)+'/compare',{revision:c.revision,other_id:other.id,other_revision:other.revision});if(!result||!valid(c))return;
    const rows=$('map-workbench-result');rows.replaceChildren(node('h3',result.left.title+' → '+result.right.title),node('p',`Добавлено: ${result.added} · Удалено: ${result.removed} · Изменено: ${result.changed} · Перемещено: ${result.moved} · Без изменений: ${result.unchanged}`));
    const names={added:'Добавлен',removed:'Удалён',changed:'Изменён',moved:'Перемещён'};for(const item of result.items.filter(x=>x.status!=='unchanged')){const row=node('section',undefined,'card');row.append(node('h3',names[item.status]+': '+item.label),node('p',item.path+(item.other_path?' → '+item.other_path:'')));if(item.changes?.length)row.append(node('p',item.changes.map(x=>({label:'название',summary:'краткий текст',body:'подробный текст',body_markdown:'подробный текст',kind:'тип узла',locked:'защита узла',sources:'источники',style:'оформление'}[x]||x)).join(', ')));rows.append(row);}
  })();});
}
