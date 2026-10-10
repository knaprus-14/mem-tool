'use strict';
function installMemGraphDocuments({state,$,node,api,run,loadView,showUpload}) {
  const section=node('section');section.id='graph-documents';section.hidden=true;document.querySelector('main').append(section);
  let active=null,timer=null;
  const button=(label,action,id)=>{const b=node('button',label,'secondary');b.type='button';if(id)b.id=id;b.addEventListener('click',run(action));return b;};
  function valid(c){return active===c&&state.selected===c.selected&&state.revision===c.revision&&state.view==='graph';}
  async function load(revision,w){
    const c={selected:w.id,revision,offset:0,serial:0,coverageSerial:0};active=c;section.hidden=false;
    const route='workspaces/'+w.id;
    const heading=node('div',undefined,'section-head'),actions=node('div',undefined,'head-actions');
    const viewer=node('a','Открыть граф ↗','button-link');viewer.href='/graph/'+w.id+'/';viewer.target='_blank';viewer.rel='noopener';viewer.id='graph-open';
    actions.append(button('Добавить документы',()=>showUpload(),'graph-upload'),viewer);
    heading.append(node('h1','Граф знаний'),actions);
    section.replaceChildren(heading,node('p','Добавьте файлы, дождитесь импорта и выберите документ для полного разбора. Результаты дополняют общий граф выбранной базы.','muted'));
    const imports=node('div'),error=node('p',undefined,'error');error.id='graph-error';error.setAttribute('role','alert');error.hidden=true;
    section.append(imports);
    const form=node('form',undefined,'card'),select=node('select');select.id='graph-document';select.required=true;
    const label=node('label','Обработанный документ');label.append(select);form.append(label);
    let chosen=state.graphWorkspace===w.id?state.graphDocument||'':'';
    const pages=node('div',undefined,'pagination'),pageLabel=node('span');
    const prev=button('Предыдущие документы',async()=>{c.offset=Math.max(0,c.offset-100);await documents();},'graph-prev');
    const next=button('Следующие документы',async()=>{c.offset+=100;await documents();},'graph-next');
    pages.append(prev,pageLabel,next);form.append(pages);
    const focusLabel=node('label','Что раскрыть'),focus=node('textarea');focus.id='graph-focus';focus.rows=2;focus.maxLength=2000;focus.required=true;focus.value='Полный разбор документа: темы, определения, требования, условия, исключения и связи';focusLabel.append(focus);form.append(focusLabel);
    const advanced=node('details');advanced.append(node('summary','Параметры обработки'));
    const charsLabel=node('label','Символов контекста на запрос'),chars=node('input');chars.id='graph-context';chars.type='number';chars.min=1000;chars.max=48000;chars.value=12000;chars.required=true;charsLabel.append(chars);advanced.append(charsLabel);advanced.hidden=true;form.append(advanced);
    form.append(node('p','Сначала сохраняются структура и исходный текст. Затем модель добавляет темы и связи. Повторный запуск обрабатывает только незавершённые места.','muted'));
    const start=node('button','Построить / дополнить весь документ','primary');start.id='graph-start';start.type='submit';start.disabled=true;form.append(start);
    form.append(node('p','Используется модель ответов из настроек базы. Вкладку можно закрыть; mem должен продолжать работать. Через два часа задание остановится, и его можно продолжить.','muted'));
    const coverage=node('div');coverage.id='graph-coverage';coverage.setAttribute('role','status');
    section.append(form,error,node('h2','Покрытие документа'),coverage,button('Обновить документы и покрытие',async()=>{await documents();await showCoverage();},'graph-refresh'));
    const tasks=node('div');tasks.id='graph-tasks';section.append(node('h2','Построение графа'),node('p','Структура документа доступна сразу. Проверенные результаты сохраняются по мере обработки. Сбой модели оставляет исходный текст и отметку о незавершённом разборе.','muted'),tasks);
    let settings,pending=null,busy=false,importSignature=null;
    function modelReady(){const model=(settings?.answer_model||'').trim().toLowerCase();return model!==''&&model!=='bge-m3'&&!model.startsWith('bge-m3:');}
    const modelNotice=node('p',undefined,'notice');modelNotice.id='graph-model-notice';modelNotice.setAttribute('role','status');
    const configure=button('Настроить модель ответов',async()=>{state.view='settings';state.document='';await loadView();},'graph-configure');
    form.append(modelNotice,configure);
    function fail(err){if(valid(c)){error.textContent=err.message;error.hidden=false;}}
    function choose(reset=true){chosen=select.value;state.graphWorkspace=w.id;state.graphDocument=chosen;if(reset)pending=null;start.disabled=!chosen||busy;}
    select.addEventListener('change',run(async()=>{choose();await showCoverage();}));
    form.addEventListener('input',()=>{if(!busy)pending=null;});
    async function documents(){
      const serial=++c.serial,data=await api(route+'/library?kind=documents&limit=100&offset='+c.offset);
      if(!valid(c)||serial!==c.serial)return;
      select.replaceChildren();const placeholder=node('option','Выберите документ');placeholder.value='';select.append(placeholder);
      const docs=(data.documents||[]).filter(d=>d.document_id);
      if(chosen&&!docs.some(d=>d.document_id===chosen)){const option=node('option','Выбранный документ');option.value=chosen;select.append(option);}
      docs.forEach(d=>{const o=node('option',d.name+' · '+d.chunks+' фрагм.');o.value=d.document_id;select.append(o);});
      if(!chosen&&docs.length===1&&data.total===1)chosen=docs[0].document_id;
      select.value=chosen;choose(false);prev.disabled=c.offset===0;next.disabled=c.offset+100>=data.total;pages.hidden=data.total<=100;pageLabel.textContent=data.total?`${c.offset+1}–${Math.min(c.offset+100,data.total)} из ${data.total}`:'Нет документов';
      if(!data.total)coverage.replaceChildren(node('p','Добавьте документ и дождитесь завершения импорта.','notice'));
    }
    async function showCoverage(){
      const document=chosen,serial=++c.coverageSerial;
      if(!document){coverage.replaceChildren(node('p','Выберите документ для проверки.','muted'));return;}
      const report=await api(route+'/maintenance/coverage',{document});
      if(!valid(c)||serial!==c.coverageSerial||document!==chosen)return;
      const s=report.summary;
      coverage.replaceChildren(node('p',`Фрагментов с текстом: ${s.chunks_with_text}. Обработано: ${s.processed_chunks}. Покрыто ссылками графа: ${s.covered_chunks}.`),node('p',`Узлов: ${s.extracted_nodes}. Связей: ${s.extracted_relations}. Пропущено при импорте страниц: ${s.failed_physical_pages}.`,'muted'));
      coverage.append(node('p',`Смысловой разбор завершён: ${s.semantic_completed_chunks||0}. Ожидают разбора: ${s.semantic_pending_chunks||0}.`));
      if(s.semantic_pending_chunks){const details=node('details');details.append(node('summary','Места, требующие повторного разбора'));(report.extraction_issues||[]).slice(0,30).forEach(issue=>details.append(node('p',(issue.page?'Стр. '+issue.page+' · ':'')+'Фрагмент '+(issue.block_index+1)+': '+issue.reason)));if((report.extraction_issues||[]).length>30)details.append(node('p','Показаны первые 30 мест.'));coverage.append(details,node('p','Нажмите «Построить / дополнить весь документ», чтобы повторить незавершённый разбор. Готовые результаты сохранятся.','notice'));}
      if(s.documents_without_manifest)coverage.append(node('p','История импорта неполна: полноту всех страниц по этой базе подтвердить нельзя.','notice'));
      if(s.failed_physical_pages)coverage.append(node('p','Некоторые страницы не импортированы. Откройте «Обработка» и устраните ошибки импорта.','notice'));
      coverage.append(node('p','Ссылки графа включают исходный текст. Это не означает, что модель извлекла все утверждения или проверила их правильность.','muted'));
    }
    form.addEventListener('submit',e=>{e.preventDefault();if(busy||!chosen)return;busy=true;start.disabled=true;error.hidden=true;select.disabled=true;focus.disabled=true;chars.disabled=true;
      run(async()=>{try{
        pending ||= {request_id:crypto.randomUUID().replaceAll('-',''),kind:'graph-extract',config_revision:settings.revision,document:chosen,focus:focus.value,batches:16,context_chars:Number(chars.value),all_chunks:true};
        await api(route+'/maintenance',pending);pending=null;if(valid(c))await refresh();
      }catch(err){fail(err);}finally{busy=false;select.disabled=false;focus.disabled=false;chars.disabled=false;start.disabled=!chosen;}})();
    });
    async function refresh(){
      clearTimeout(timer);if(!valid(c)||c.refreshing)return;c.refreshing=true;
      try{
        const [jobs,operations]=await Promise.all([api(route+'/jobs'),api(route+'/maintenance')]);if(!valid(c))return;
        const signature=JSON.stringify(jobs.map(j=>[j.id,j.status,j.chunks]));
        if(importSignature!==null&&signature!==importSignature&&!busy)await documents();importSignature=signature;if(!valid(c))return;
        imports.replaceChildren();
        const unfinished=jobs.filter(j=>!['succeeded','cancelled'].includes(j.status));
        if(unfinished.length){imports.append(node('h2','Импорт документов'));unfinished.slice(0,10).forEach(j=>{imports.append(node('p',(j.source_label||j.filename)+' · '+({queued:'В очереди',running:'Обрабатывается',cancelling:'Отменяется',partial:'Есть пропущенные страницы',failed:'Ошибка',interrupted:'Прервано',conflict:'Документ изменился'}[j.status]||j.status)+(j.total?' · '+j.current+'/'+j.total:'')));});imports.append(button('Открыть обработку документов',async()=>{state.view='jobs';state.document='';await loadView();}));}
        // Preserve keyboard focus across progress refreshes.
        const focused=document.activeElement?.dataset.graphAction;
        tasks.replaceChildren();const items=operations.filter(o=>o.kind==='graph-extract');
        if(!items.length)tasks.append(node('p','Заданий пока нет.','muted'));
        items.forEach(o=>{const row=node('article',undefined,'card');row.append(node('h3',o.all_chunks?'Весь документ':'Порция фрагментов'),node('p',({queued:'В очереди',running:'Выполняется',completed:'Завершено',failed:'Ошибка',interrupted:'Прервано',cancelled:'Отменено'}[o.status]||o.status)+' · '+o.current+'/'+o.total+(o.all_chunks?' фрагм.':' частей')),node('p',o.stage||''));
          if(o.document){const link=button('Выбрать документ',async()=>{chosen=o.document;await documents();await showCoverage();});link.dataset.graphAction=o.id+'-document';row.append(link);}
          if(o.error)row.append(node('p',o.error,'error'));
          const action=['queued','running'].includes(o.status)?'cancel':['failed','interrupted','cancelled'].includes(o.status)?'retry':'';
          if(action){const b=button(action==='cancel'?'Отменить':'Продолжить',async()=>{b.disabled=true;try{await api(route+'/maintenance/'+o.id+'/'+action,{});await refresh();}finally{b.disabled=false;}});b.dataset.graphAction=o.id+'-'+action;row.append(b);}
          tasks.append(row);
        });
        if(focused){const target=[...tasks.querySelectorAll('button')].find(b=>b.dataset.graphAction===focused);target?.focus();}
        await showCoverage();
      }catch(err){fail(err);}finally{c.refreshing=false;if(valid(c))timer=setTimeout(refresh,3000);}
    }
    try{settings=await api(route+'/settings');if(!valid(c))return;modelNotice.textContent=modelReady()?'Модель ответов: '+settings.answer_model:'Без модели будет сохранена структура с исходным текстом. Для тем и связей выберите модель ответов в настройках базы.';configure.hidden=modelReady();await documents();if(valid(c))await refresh();}catch(err){fail(err);}
  }
  return {load,hide(){active=null;clearTimeout(timer);section.hidden=true;}};
}
