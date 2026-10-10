'use strict';
function installMemAnnotationKnowledge({state,node,api,run}) {
  const drafts=new Map(), mounts=new Set();
  const kinds={note:'Заметка',question:'Вопрос',card:'Карточка',hypothesis:'Гипотеза',decision:'Решение',task:'Задача'};
  let current=null;
  const dialog=node('dialog',undefined,'annotation-dialog');dialog.id='annotation-knowledge-dialog';
  const head=node('div',undefined,'panel-head'),title=node('h2','Аннотации объекта графа');title.id='annotation-knowledge-title';dialog.setAttribute('aria-labelledby',title.id);
  head.append(title,button('Закрыть',close));dialog.append(head);document.body.append(dialog);
  dialog.addEventListener('cancel',e=>{e.preventDefault();close();});
  dialog.addEventListener('close',()=>{if(!dialog.open){current?.close();current=null;}});
  function button(label,action,css='secondary'){const b=node('button',label,css);b.type='button';b.addEventListener('click',run(action));return b;}
  function actor(){return state.session?.user||'';}
  function base(workspace){return 'workspaces/'+encodeURIComponent(workspace)+'/annotations';}
  function graphLink(workspace,id,label){const a=node('a',label||'Открыть в графе');a.href='/graph/'+encodeURIComponent(workspace)+'/#node='+encodeURIComponent(id);a.target='_blank';a.rel='noopener';return a;}
  function sourceState(a){return ({current:'Источник актуален',stale:'Редакция источника изменилась',missing:'Источник отсутствует'}[a.state]||'Состояние источника неизвестно')+(a.archived?' · Аннотация в архиве':'');}
  function close(){current?.close();current=null;dialog.close();for(const mounted of [...mounts])mounted.close();}

  // All reads are scoped to the workspace, actor, screen and owning view. A
  // detached mount can finish a write receipt without rendering into a new view.
  function reader({host,workspace,isCurrent=()=>true,onOpen,path}) {
    const user=actor(),screen=state.revision,controllers=new Set();let alive=true,serial=0,offset=0,total=0;
    const valid=()=>alive&&workspace===state.selected&&user===actor()&&screen===state.revision&&isCurrent();
    const status=node('p','','muted'),error=node('p','','error'),rows=node('div'),pager=node('div',undefined,'row');
    error.setAttribute('role','alert');status.setAttribute('role','status');
    const prev=button('Предыдущие связи',()=>load(Math.max(0,offset-20))),next=button('Следующие связи',()=>load(offset+20)),refresh=button('Обновить связи',()=>load(offset));pager.append(prev,next,refresh);host.append(status,error,rows,pager);
    function render(data){
      rows.replaceChildren();offset=data.offset;total=data.total;status.textContent='Связей: '+total+'.'+(total?' Показаны '+(offset+1)+'–'+(offset+data.items.length)+'.':'');
      prev.disabled=offset===0;next.disabled=offset+data.items.length>=total;
      for(const item of data.items){
        const snapshot=item.snapshot,currentAnnotation=item.annotation,created=item.creation.node,live=item.node;
        const row=node('article',undefined,'annotation-knowledge-item');
        row.append(node('h4',item.missing_node?'Узел удалён: '+created.label:live.label),node('p','Создан из аннотации r'+snapshot.revision+' · текущая r'+currentAnnotation.revision+' · '+sourceState(currentAnnotation),'muted'),node('blockquote',snapshot.quote));
        row.append(node('p','Комментарий на момент создания: '+(snapshot.note||'—')),node('p','Исходный текст узла: '+(created.body||'—')));
        if(item.node_changed&&!item.missing_node)row.append(node('p','Текущее содержимое узла: '+(live.body||'—')));
        row.append(node('p',item.missing_node?'Сохранилась история создания; узла больше нет.':(item.node_changed?'Узел изменён после создания. ':'Узел соответствует сохранённому снимку. ')+(item.evidence_linked?'Точная цитата остаётся привязанной.':'Исходная точная цитата больше не привязана.'),'notice'));
        if(!item.missing_node)row.append(graphLink(workspace,live.id));
        if(onOpen)row.append(button('Открыть аннотацию',()=>{if(!valid())return;close();return onOpen(currentAnnotation.id);}));
        rows.append(row);
      }
      if(!data.items.length)rows.append(node('p',total?'На этой странице связей нет.':'Связей с аннотациями пока нет.','muted'));
    }
    async function load(nextOffset=0){
      if(!valid())return;const version=++serial;for(const c of controllers)c.abort();controllers.clear();const controller=new AbortController();controllers.add(controller);
      prev.disabled=next.disabled=refresh.disabled=true;error.textContent='';
      try{const data=await api(path+'?offset='+nextOffset+'&limit=20',undefined,false,controller.signal);if(!valid()||version!==serial||controller.signal.aborted)return;
        if(!data||!Array.isArray(data.items)||data.items.length>20||data.offset!==nextOffset||!Number.isSafeInteger(data.total)||data.total<0)throw Error('Некорректный список связей.');render(data);
      }catch(e){if(valid()&&version===serial&&e.name!=='AbortError')error.textContent=e.message;}
      finally{controllers.delete(controller);if(valid()&&version===serial){refresh.disabled=false;prev.disabled=offset===0;next.disabled=offset+20>=total;}}
    }
    return {valid,load,close(){alive=false;for(const c of controllers)c.abort();controllers.clear();}};
  }

  function mount({host,annotation:a,workspace,isCurrent=()=>true}) {
    const user=actor(),screen=state.revision,key=JSON.stringify([user,workspace,a.id]);let alive=true;
    const valid=()=>alive&&workspace===state.selected&&user===actor()&&screen===state.revision&&isCurrent();
    const form=node('form'),kind=node('select'),label=node('input'),body=node('textarea'),status=node('p','','notice'),error=node('p','','error'),result=node('div');
    status.setAttribute('role','status');error.setAttribute('role','alert');
    label.maxLength=1024;body.maxLength=16000;body.rows=5;
    for(const [value,text] of Object.entries(kinds)){const option=node('option',text);option.value=value;kind.append(option);}
    function field(text,input){const wrap=node('label',text);wrap.append(input);return wrap;}
    const save=button('Создать черновик в графе',saveNode,'primary'),retry=button('Повторить ту же отправку',send),fresh=button('Новый черновик',newDraft);
    form.append(field('Тип объекта',kind),field('Название',label),field('Ваш текст',body),save,retry,fresh);
    host.append(node('h3','Объекты графа'),node('p','Создаётся отдельный ручной черновик с точной цитатой. Связь запоминает эту ревизию аннотации; изменения аннотации и узла не переписывают историю.','muted'),status,error,form,result);
    const list=reader({host,workspace,isCurrent:valid,path:base(workspace)+'/'+encodeURIComponent(a.id)+'/knowledge'});
    let draft=drafts.get(key),seenResult=null;
    if(!draft){
      // Settled drafts may be discarded; unresolved receipts are never evicted.
      if(drafts.size>=100)for(const [k,d] of drafts){if(!d.pending&&!d.busy&&d.listeners.size===0){drafts.delete(k);if(drafts.size<100)break;}}
      draft={revision:a.revision,kind:'note',label:Array.from(a.quote||'').slice(0,80).join(''),body:a.note||'',pending:null,busy:false,result:null,error:'',listeners:new Set()};
      if(drafts.size<100)drafts.set(key,draft);else draft.blocked=true;
    }
    function fill(){kind.value=draft.kind;label.value=draft.label;body.value=draft.body;}
    function update(){
      if(!valid())return;const locked=draft.busy||!!draft.pending||!!draft.result||draft.blocked;
      kind.disabled=label.disabled=body.disabled=locked;save.disabled=locked||a.archived||a.state!=='current'||draft.revision!==a.revision;
      retry.hidden=!draft.pending;retry.disabled=draft.busy;fresh.disabled=draft.busy||draft.blocked;
      error.textContent=draft.error||'';result.replaceChildren();
      if(draft.result){status.textContent=(draft.result.replayed?'Эта отправка уже была сохранена.':'Создан ручной черновик.')+' Для отдельного объекта нажмите «Новый черновик».';result.append(graphLink(workspace,draft.result.node.id,draft.result.node.label));}
      else if(draft.blocked)status.textContent='Слишком много незавершённых черновиков. Завершите прежние отправки перед созданием новых.';
      else if(draft.busy)status.textContent='Отправка ещё выполняется…';
      else if(draft.pending)status.textContent='Результат отправки не подтверждён. Повтор использует тот же запрос.';
      else if(a.archived||a.state!=='current')status.textContent='Создание доступно для активной аннотации с актуальным источником.';
      else if(draft.revision!==a.revision)status.textContent='Черновик относится к прежней ревизии. Перечитайте аннотацию и начните новый черновик.';
      else status.textContent='Будет использована ревизия аннотации '+a.revision+'. Текст остаётся вашим ручным черновиком.';
      if(draft.result&&seenResult!==draft.result){seenResult=draft.result;run(()=>list.load(0))();}
    }
    function notify(){for(const fn of draft.listeners)fn();}
    function capture(){if(!valid()||draft.busy||draft.pending||draft.result||draft.blocked)return;draft.kind=kind.value;draft.label=label.value;draft.body=body.value;}
    kind.addEventListener('change',capture);label.addEventListener('input',capture);body.addEventListener('input',capture);
    form.addEventListener('submit',e=>{e.preventDefault();return run(saveNode)();});
    async function saveNode(){
      if(!valid()||save.disabled)return;capture();
      if(!kinds[draft.kind]||!draft.label.trim()||Array.from(draft.label).length>512||Array.from(draft.body).length>8000){draft.error='Название — от 1 до 512 символов, текст — до 8000.';update();return;}
      draft.pending={revision:draft.revision,kind:draft.kind,label:draft.label,body:draft.body,request_id:crypto.randomUUID().replaceAll('-','')};await send();
    }
    async function send(){
      if(!valid()||draft.busy||!draft.pending)return;const submission=draft.pending;draft.busy=true;draft.error='';notify();
      try{const saved=await api(base(workspace)+'/'+encodeURIComponent(a.id)+'/knowledge',submission);
        if(!saved||!/^manual-node-[0-9a-f]{32}$/.test(saved.node?.id||'')||saved.node.kind!==submission.kind||saved.node.label!==submission.label||saved.node.body!==submission.body||saved.node.status!=='draft'||saved.node.origin!=='manual'||saved.link?.node_id!==saved.node.id||saved.link?.annotation_id!==a.id||saved.link?.annotation_revision!==submission.revision)throw Error('Некорректный ответ сохранения; повторите ту же отправку.');
        if(draft.pending===submission){draft.result=saved;draft.pending=null;}
      }catch(e){draft.error=e.message+' Повтор сохранит идентификатор отправки.';}
      finally{draft.busy=false;notify();}
    }
    function newDraft(){
      if(!valid()||draft.busy||draft.blocked)return;
      if(draft.pending&&!confirm('Отправка могла уже создать узел. Для проверки используйте повтор. Всё равно начать новый черновик?'))return;
      draft.pending=null;draft.result=null;draft.error='';draft.revision=a.revision;seenResult=null;fill();update();
    }
    const mounted={close(){alive=false;list.close();draft.listeners.delete(update);mounts.delete(mounted);}};
    draft.listeners.add(update);mounts.add(mounted);fill();update();if(!draft.result)run(()=>list.load(0))();return mounted;
  }

  async function showNode({workspace,nodeID,onOpen}) {
    current?.close();current=null;
    if(workspace!==state.selected||typeof nodeID!=='string'||!nodeID)return;
    const host=node('section');dialog.replaceChildren(head,host);if(!dialog.open)dialog.showModal();
    const view=reader({host,workspace,onOpen,path:base(workspace)+'/knowledge-node/'+encodeURIComponent(nodeID),isCurrent:()=>current===view&&dialog.open});current=view;await view.load(0);
  }
  return {mount,showNode,close};
}
