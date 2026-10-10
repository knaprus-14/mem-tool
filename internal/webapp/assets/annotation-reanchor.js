'use strict';
function installMemAnnotationReanchor({state,node,api,run}) {
  const targets=new Map(),drafts=new Map();let current=null;
  const dialog=node('dialog'),title=node('h2','Перепривязка цитаты'),head=node('div',undefined,'dialog-head'),status=node('p','','notice'),error=node('p','','error'),content=node('section');
  dialog.id='annotation-reanchor-dialog';title.id='annotation-reanchor-title';dialog.setAttribute('aria-labelledby',title.id);status.setAttribute('role','status');error.setAttribute('role','alert');
  const apply=button('Создать новую привязку',submit,'primary'),retry=button('Повторить ту же отправку',send),reset=button('Сбросить попытку',resetDraft),closeButton=button('Закрыть',close,'quiet');
  head.append(title,closeButton);dialog.append(head,status,error,content,apply,retry,reset);document.body.append(dialog);
  function button(label,action,css='secondary'){const b=node('button',label,css);b.type='button';b.addEventListener('click',run(action));return b;}
  const actor=()=>state.session?.user||'',key=workspace=>JSON.stringify([actor(),workspace]),contextKey=c=>JSON.stringify([c.user,c.workspace]);
  function valid(c){return c&&current===c&&dialog.open&&state.session?.authenticated!==false&&c.workspace===state.selected&&c.user===actor()&&c.screen===state.revision;}
  function close(){current?.controller?.abort();current=null;dialog.close();}
  dialog.addEventListener('cancel',e=>{e.preventDefault();close();});dialog.addEventListener('close',()=>{if(!dialog.open){current?.controller?.abort();current=null;}});
  function begin(workspace){if(workspace!==state.selected||state.session?.authenticated===false)return null;current?.controller?.abort();const c={workspace,user:actor(),screen:state.revision,controller:new AbortController(),busy:false};current=c;error.hidden=true;status.textContent='';content.replaceChildren();if(!dialog.open)dialog.showModal();controls(c);return c;}
  function controls(c){if(!valid(c))return;apply.hidden=!c.preview;apply.disabled=c.busy||c.draft?.busy||!!c.draft?.pending;retry.hidden=!c.draft?.pending;retry.disabled=c.busy||c.draft?.busy;reset.hidden=!c.draft?.pending;reset.disabled=c.busy||c.draft?.busy;}
  function failure(c,e){if(valid(c)&&e.name!=='AbortError'){error.hidden=false;error.textContent=e.message;}}
  function base(c,id){return 'workspaces/'+encodeURIComponent(c.workspace)+'/annotations/'+encodeURIComponent(id);}
  function active(workspace){return targets.get(key(workspace));}
  function start({workspace,annotation,onApplied}){const c=begin(workspace);if(!c)return;targets.set(key(workspace),{id:annotation.id,revision:annotation.revision,quote:annotation.quote});title.textContent='Выберите новую цитату';content.append(node('pre',annotation.quote),button('Найти цитату в новой версии',()=>{if(valid(c))return findCandidates({workspace,annotation,onApplied});}),node('p','Либо закройте это окно и аннотацию. Откройте нужный документ, выделите актуальную цитату и нажмите «Аннотировать выделение». В редакторе выберите «Перепривязать прежнюю цитату». Перед записью будут показаны обе версии.'));controls(c);}
  function cancel(workspace){targets.delete(key(workspace));}
  const textBound=(text,max)=>typeof text==='string'&&Array.from(text).length<=max,integer=value=>Number.isSafeInteger(value)&&value>=0;
  const candidateLimits={binding_unresolved:'привязка к исходному документу не подтверждена',entry_limit:'достигнут предел проверяемых фрагментов',text_budget:'достигнут предел объёма текста',chunk_limit:'слишком большой фрагмент пропущен',metadata_budget:'часть описаний фрагментов превышает допустимый объём',candidate_limit:'достигнут предел показываемых совпадений',invalid_entry:'некорректный фрагмент пропущен'};
  function candidatePageValid(page,c,limit){
    return page&&page.annotation_id===c.old.id&&page.annotation_revision===c.old.revision&&page.quote===c.old.quote&&page.limit===limit&&typeof page.complete==='boolean'&&integer(page.scanned_entries)&&integer(page.total_entries)&&page.scanned_entries<=page.total_entries&&integer(page.scanned_bytes)&&page.scanned_bytes<=8*1024*1024&&Array.isArray(page.items)&&page.items.length<=limit&&Array.isArray(page.warnings)&&page.warnings.length<=10&&page.warnings.every(w=>textBound(w,512))&&Array.isArray(page.truncated_reasons)&&page.truncated_reasons.length<=10&&page.truncated_reasons.every(w=>textBound(w,128))&&page.items.every(item=>item&&Number.isSafeInteger(item.entry_id)&&item.entry_id>0&&/^sha256:[0-9a-f]{64}$/.test(item.pin||'')&&textBound(item.document_id,1024)&&textBound(item.document_revision,256)&&textBound(item.citation_id,1024)&&textBound(item.source_name,256)&&integer(item.page)&&integer(item.block_index)&&integer(item.block_chunk_index)&&integer(item.start)&&integer(item.end)&&item.end>item.start&&item.end-item.start===Array.from(c.old.quote).length&&item.quote===c.old.quote&&textBound(item.prefix,48)&&textBound(item.suffix,48));
  }
  async function findCandidates({workspace,annotation,onApplied},afterApplied){
    if(!annotation||!/^ann-[0-9a-f]{32}$/.test(annotation.id||'')||!Number.isSafeInteger(annotation.revision)||annotation.revision<1||!textBound(annotation.quote,8000)||!annotation.quote)return;
    const c=begin(workspace);if(!c)return;const old=active(workspace);c.old=old&&old.id===annotation.id&&old.revision===annotation.revision&&old.quote===annotation.quote?old:{id:annotation.id,revision:annotation.revision,quote:annotation.quote};targets.set(contextKey(c),c.old);c.annotationRevision=annotation.document_revision;c.onApplied=onApplied||afterApplied;c.candidateSearch=true;c.candidateLimit=20;title.textContent='Точные совпадения цитаты';await loadCandidates(c,20);
  }
  async function loadCandidates(c,limit){
    if(!valid(c)||!c.candidateSearch||c.busy)return;c.busy=true;error.hidden=true;c.candidatePage=null;c.candidateLimit=limit;renderCandidates(c);controls(c);status.textContent='Ищем точную цитату в актуальных фрагментах документа…';
    try{const result=await api(base(c,c.old.id)+'/reanchor-candidates?revision='+c.old.revision+'&limit='+limit,undefined,false,c.controller.signal);if(!valid(c)||!c.candidateSearch)return;if(!candidatePageValid(result,c,limit))throw Error('Некорректный список совпадений. Повторите поиск.');c.candidatePage=result;}
    catch(e){if(c.candidateSearch)failure(c,e);}finally{c.busy=false;if(valid(c)&&c.candidateSearch){renderCandidates(c);controls(c);}}
  }
  function renderCandidates(c){
    if(!valid(c)||!c.candidateSearch)return;content.replaceChildren(node('h3','Сохранённая цитата'),node('pre',c.old.quote),node('p','Только точное совпадение внутри одного доступного фрагмента исходного документа. Переносы, регистр и пробелы не исправляются; совпадения между фрагментами не ищутся. Даже единственное совпадение нужно выбрать и подтвердить.','muted'));
    const action=(label,callback,host=content)=>{const b=button(label,()=>{if(valid(c)&&!c.busy&&content.contains(b))return callback();});b.disabled=c.busy;host.append(b);};
    for(const [draftKey,d] of drafts){const [user,workspace,id,input]=JSON.parse(draftKey);if(user!==c.user||workspace!==c.workspace||id!==c.old.id||input.revision!==c.old.revision||!d.pending)continue;const row=node('section',undefined,'card');row.append(node('p','В этой вкладке есть неподтверждённая отправка. Сначала проверьте её повтором, чтобы не создать вторую привязку.','notice'),node('pre',input.quote),node('p','Фрагмент '+input.entry_id+' · диапазон Unicode '+input.start+'–'+input.end,'muted'));content.append(row);action('Продолжить прежнюю отправку',()=>preview({workspace:c.workspace,selection:input,onApplied:c.onApplied}),row);}
    const page=c.candidatePage;
    if(page){status.textContent='Показано совпадений: '+page.items.length+'. Проверено фрагментов: '+page.scanned_entries+' из '+page.total_entries+'; байт текста: '+page.scanned_bytes+'. '+(page.complete?'Поиск по доступным фрагментам завершён.':'Поиск неполный: есть непроверенные фрагменты или не показанные совпадения.');
      for(const warning of page.warnings)content.append(node('p',warning,'notice'));
      if(!page.complete)content.append(node('p','Ограничения поиска: '+(page.truncated_reasons.map(reason=>candidateLimits[reason]||'часть данных недоступна для проверки').join('; ')||'не все данные проверены')+'. Результат не доказывает отсутствие цитаты.','notice'));
      if(!page.items.length)content.append(node('p',page.complete?'Точных совпадений в доступных фрагментах нет. Можно выбрать новую цитату вручную.':'В проверенной части совпадений нет. Можно выбрать новую цитату вручную.'));
      page.items.forEach((item,index)=>{const row=node('section',undefined,'card'),context=node('pre');context.append(node('span',item.prefix),node('mark',item.quote),node('span',item.suffix));row.append(node('h3','Совпадение '+(index+1)),node('p',location(item),'muted'),node('p','Блок '+item.block_index+' · фрагмент '+item.block_chunk_index+' · диапазон Unicode ['+item.start+', '+item.end+') — с нуля, конец не включён.','muted'),context);if(c.annotationRevision&&item.document_revision===c.annotationRevision)row.append(node('p','Та же редакция, что у сохранённой цитаты. Выбор остаётся ручным.','notice'));content.append(row);action('Выбрать совпадение '+(index+1),()=>preview({workspace:c.workspace,selection:item,onApplied:c.onApplied}),row);});
    }else if(!c.busy)status.textContent='Поиск не завершён. Изменения не записаны.';
    action('Повторить поиск',()=>loadCandidates(c,c.candidateLimit));
    if(page&&!page.complete&&c.candidateLimit<50)action('Искать до 50 совпадений',()=>loadCandidates(c,50));
    if(page&&!page.complete&&c.candidateLimit===50)content.append(node('p','Предел интерфейса: 50 совпадений; сервер проверяет не более 1024 фрагментов и 8 МиБ текста. Для остальных мест используйте ручной выбор.','notice'));
  }
  async function preview({workspace,selection,onApplied}) {
    const old=active(workspace);if(!old)return;const c=begin(workspace);if(!c)return;c.old=old;c.onApplied=onApplied;
    c.input={revision:old.revision,entry_id:selection.entry_id,pin:selection.pin,start:selection.start,end:selection.end,quote:selection.quote};
    c.draftKey=JSON.stringify([c.user,workspace,old.id,c.input]);if(!drafts.has(c.draftKey))drafts.set(c.draftKey,{});c.draft=drafts.get(c.draftKey);title.textContent='Проверка новой привязки';
    if(c.draft.result){renderResult(c,c.draft.result);return;}
    if(c.draft.pending){c.preview=c.draft.preview;renderPreview(c);status.textContent=c.draft.busy?'Отправка ещё выполняется.':'Ответ не подтверждён. Повтор использует прежнюю отправку.';controls(c);return;}
    c.busy=true;controls(c);status.textContent='Сравниваем цитаты и редакции…';
    try{const result=await api(base(c,old.id)+'/reanchor-preview',c.input,false,c.controller.signal);if(!valid(c))return;
      if(!result||!/^sha256:[0-9a-f]{64}$/.test(result.digest||'')||result.old?.id!==old.id||result.old?.revision!==old.revision||typeof result.old?.quote!=='string'||!result.target||!['entry_id','pin','start','end','quote'].every(k=>result.target[k]===c.input[k])||!Array.isArray(result.warnings)||result.warnings.length>200||!result.warnings.every(w=>typeof w==='string'))throw Error('Некорректный результат сравнения. Повторите проверку.');
      c.preview=result;c.draft.preview=result;renderPreview(c);status.textContent='Проверьте обе цитаты. Изменения ещё не записаны.';}
    catch(e){failure(c,e);}finally{c.busy=false;controls(c);}
  }
  function location(a){return [a.source_name,a.page?'Физическая страница '+a.page:'Без страницы',a.document_revision].filter(Boolean).join(' · ');}
  function renderPreview(c){if(!valid(c)||!c.preview)return;const p=c.preview;content.replaceChildren(node('h3','Прежняя цитата'),node('p',location(p.old),'muted'),node('pre',p.old.quote),node('h3','Новая цитата'),node('p',location(p.target),'muted'),node('pre',p.target.quote));for(const warning of p.warnings||[])content.append(node('p',warning,'notice'));}
  async function submit(){const c=current;if(!valid(c)||c.busy||c.draft?.busy||!c.preview||c.draft.pending)return;c.draft.pending=Object.freeze({...c.input,digest:c.preview.digest,request_id:crypto.randomUUID().replaceAll('-','')});await send();}
  async function send(){
    const c=current;if(!valid(c)||c.busy||!c.draft?.pending||c.draft.busy)return;const d=c.draft,pending=d.pending;c.busy=d.busy=true;error.hidden=true;controls(c);
    try{
      const result=await api(base(c,c.old.id)+'/reanchor',pending),a=result?.annotation,l=result?.lineage;
      if(!a||!/^ann-[0-9a-f]{32}$/.test(a.id||'')||a.id===c.old.id||!Number.isSafeInteger(a.revision)||a.revision<1||a.quote!==pending.quote||!l||!/^alink-[0-9a-f]{32}$/.test(l.id||'')||l.old_id!==c.old.id||l.old_revision!==pending.revision||l.new_id!==a.id||l.new_revision!==a.revision)throw Error('Некорректный ответ на перепривязку. Повторите ту же отправку для подтверждения.');
      d.result=result;d.pending=null;d.error='';if(targets.get(contextKey(c))===c.old)targets.delete(contextKey(c));
    }catch(e){d.error=e.name!=='AbortError'?e.message:'';failure(c,e);}
    finally{
      c.busy=d.busy=false;const visible=current;
      if(valid(visible)&&(visible.draft===d||(visible.candidateSearch&&visible.old===c.old))){if(d.result){visible.draft=d;renderResult(visible,d.result);}else if(visible.draft===d){status.textContent='Запись не подтверждена. Повтор сохранён в этой вкладке.';if(d.error){error.hidden=false;error.textContent=d.error;}controls(visible);}else renderCandidates(visible);}
    }
  }
  function renderResult(c,result){if(!valid(c))return;c.candidateSearch=false;c.controller.abort();c.busy=false;c.preview=null;title.textContent='Новая привязка сохранена';status.textContent=result.replayed?'Эта отправка уже была сохранена.':'Создана новая аннотация с историей перехода.';content.replaceChildren(node('pre',result.annotation.quote),node('p','Прежняя аннотация, её ссылки на карты и области PDF сохранены отдельно.'),button('Открыть новую аннотацию',()=>{if(valid(c)){close();return c.onApplied?.(result);}}));controls(c);}
  function resetDraft(){const c=current;if(!valid(c)||c.busy||c.draft?.busy||!c.draft?.pending)return;if(!confirm('Отправка могла сохраниться. Повтор позволяет проверить её без дубля. Сбросить попытку?'))return;drafts.delete(c.draftKey);close();}
  async function showHistory({workspace,id,onOpen}){
    const c=begin(workspace);if(!c)return;title.textContent='История перепривязок';c.historyButtons=[];
    const load=async offset=>{
      if(!valid(c)||c.busy)return;c.busy=true;error.hidden=true;for(const b of c.historyButtons)b.disabled=true;controls(c);
      try{
        const page=await api(base(c,id)+'/lineage?offset='+offset+'&limit=30',undefined,false,c.controller.signal);if(!valid(c))return;
        if(!page||!Array.isArray(page.items)||page.items.length>30||page.offset!==offset||!Number.isSafeInteger(page.total)||page.total<0)throw Error('Не удалось прочитать страницу истории. Повторите запрос.');
        content.replaceChildren();c.historyButtons=[];
        for(const l of page.items){const row=node('section',undefined,'card'),open=entry=>{if(valid(c)&&!c.busy&&content.contains(row)){close();return onOpen(entry);}};
          const before=button('Прежняя аннотация · ревизия '+l.old_revision,()=>open(l.old_id)),after=button('Новая аннотация · ревизия '+l.new_revision,()=>open(l.new_id));c.historyButtons.push(before,after);row.append(node('p',l.created+' · '+l.actor),before,after);content.append(row);}
        status.textContent=page.total?'Связей: '+page.total+'. Показано: '+(page.items.length?`${offset+1}–${offset+page.items.length}`:'0')+'. Открывается текущее состояние записи; номер в связи — исторический.':'Перепривязок нет.';
        const nav=(label,next)=>{const b=button(label,()=>{if(content.contains(b))return load(next);});c.historyButtons.push(b);content.append(b);};
        if(offset>0)nav('Предыдущие',Math.max(0,offset-30));if(page.items.length&&offset+page.items.length<page.total)nav('Следующие',offset+page.items.length);
      }catch(e){failure(c,e);}finally{c.busy=false;if(valid(c)){for(const b of c.historyButtons)b.disabled=false;controls(c);}}
    };await load(0);
  }
  return {start,active,cancel,preview,findCandidates,showHistory,close};
}
