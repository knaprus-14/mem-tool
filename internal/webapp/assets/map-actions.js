/* Forms for structural actions. All values are rendered as text; mutations use
   the authenticated application API and never invoke terminal commands. */
function installMemMapActions({state, $, node, api, run, showMap, drawMap, selectMapNode, centerMapNode, error,change=fn=>fn()}) {
  let edit = null;
  const kinds = {topic:'Тема',subtopic:'Подтема',fact:'Факт',note:'Заметка',quote:'Цитата',question:'Вопрос',task:'Задача',decision:'Решение',link:'Ссылка'};
  function close(){if(edit?.busy)return;$('map-action-dialog').close();edit=null;}
  function renderParents(preferred){
    const select=$('map-action-parent'),chosen=preferred||select.value;select.replaceChildren();
    const candidates=MemMapLayout.parentChoices(state.map.tree,edit.node.id,$('map-action-parent-query').value);
    candidates.forEach(candidate=>{const option=node('option',candidate.label);option.value=candidate.id;select.append(option);});
    if(candidates.some(c=>c.id===chosen))select.value=chosen;
    $('map-action-save').disabled=!candidates.length;
    $('map-action-parent-count').textContent=candidates.length?'Подходящих узлов: '+candidates.length:'Нет подходящих узлов. Измените поиск.';
  }
  $('map-action-parent-query').addEventListener('input',()=>{if(edit?.action==='move'&&!edit.busy)renderParents();});
  function open(action){
    const m=state.map,n=m?.tree.byID.get(m.selectedNode);if(!n)return;
    edit={action,node:n,mapID:m.view.map.id,selected:state.selected,revision:state.revision,mapRevision:m.view.map.revision,requestID:crypto.randomUUID().replaceAll('-',''),busy:false};
    $('map-action-form').reset();$('map-action-form').querySelectorAll('input,button,select').forEach(x=>x.disabled=false);
    $('map-action-title').textContent={move:'Переместить ветвь',delete:'Удалить узел',properties:'Свойства узла'}[action];
    $('map-action-context').textContent=n.label+' · Ревизия '+edit.mapRevision;
    ['move','delete','properties'].forEach(x=>$('map-action-'+x).hidden=x!==action);
    $('map-action-error').hidden=true;$('map-action-status').textContent='';
    $('map-action-save').textContent=action==='delete'?'Удалить с сохранением в истории':'Сохранить';
    if(action==='move'){
      $('map-action-parent-query').value='';renderParents(n.parent_id);$('map-action-position').value='';
    }
    if(action==='delete')$('map-action-delete-help').textContent='Узлов в выбранной ветви: '+(1+(m.tree.descendants.get(n.id)||0))+'. Удалённое можно восстановить кнопкой «Отменить» в истории. Источники удаляемых узлов также сохраняются в истории.';
    if(action==='properties'){
      const select=$('map-action-kind');select.replaceChildren();Object.entries(kinds).forEach(([value,label])=>{const o=node('option',label);o.value=value;select.append(o);});
      select.value=n.kind;select.disabled=n.locked;$('map-action-locked').checked=n.locked;
    }
    $('map-action-dialog').showModal();
  }
  ['move','delete','properties'].forEach(action=>$('map-node-'+action).addEventListener('click',()=>open(action)));
  $('map-action-locked').addEventListener('change',()=>{$('map-action-kind').disabled=edit?.node.locked&&$('map-action-locked').checked;});
  $('map-action-close').addEventListener('click',close);
  $('map-action-dialog').addEventListener('cancel',e=>{if(edit?.busy)e.preventDefault();else edit=null;});
  $('map-action-form').addEventListener('submit',e=>{
    e.preventDefault();const a=edit;if(!a||a.busy)return;
    const input={revision:a.mapRevision,request_id:a.requestID};
    if(a.action==='move'){input.parent_id=$('map-action-parent').value;input.position=$('map-action-position').value===''?-1:Number($('map-action-position').value)-1;}
    if(a.action==='delete')input.mode=$('map-action-delete-mode').value;
    if(a.action==='properties'){input.locked=$('map-action-locked').checked;if(!a.node.locked||!input.locked)input.kind=$('map-action-kind').value;}
    // Freeze a failed attempt: a lost response may already have committed it.
    if(a.input&&JSON.stringify(a.input)!==JSON.stringify(input)){$('map-action-error').textContent='Сначала повторите прежнюю отправку или закройте форму и обновите карту.';$('map-action-error').hidden=false;return;}
    a.input=input;a.busy=true;const controls=[...$('map-action-form').querySelectorAll('input,select,button')];controls.forEach(x=>x.disabled=true);
    $('map-action-error').hidden=true;$('map-action-status').textContent='Сохраняем изменение и историю…';
    run(async()=>{
      let result;
      try{result=await api('workspaces/'+a.selected+'/maps/'+encodeURIComponent(a.mapID)+'/nodes/'+encodeURIComponent(a.node.id)+'/'+a.action,input);}
      catch(err){$('map-action-error').textContent=err.message;$('map-action-error').hidden=false;$('map-action-status').textContent='Сохранение не подтверждено. При сбое связи повторите отправку; при конфликте закройте форму и обновите карту.';return;}
      finally{a.busy=false;controls.forEach(x=>x.disabled=false);if(a.action==='properties')$('map-action-kind').disabled=a.node.locked&&$('map-action-locked').checked;}
      close();if(state.selected!==a.selected||state.revision!==a.revision)return;
      try{
        await showMap(a.mapID);const m=state.map;if(state.selected!==a.selected||state.revision!==a.revision||state.view!=='maps'||m?.view.map.id!==a.mapID)return;
        const target=a.action==='delete'?a.node.parent_id:result.node_id;
        if(m.tree.byID.has(target)){change(()=>{m.collapsed=MemMapLayout.reveal(m.tree,m.collapsed,target);m.depth=null;drawMap();selectMapNode(target,true);centerMapNode(target);});}
        $('map-edit-status').textContent=result.replayed?'Эта попытка уже сохранена. Показано текущее состояние карты.':'Изменение сохранено. Его можно отменить в истории карты.';$('map-edit-status').hidden=false;
      }catch(err){error('Изменение сохранено, но просмотр не обновлён. Нажмите «Обновить». '+err.message);}
    })();
  });
}
