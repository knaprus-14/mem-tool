'use strict';
function installMemAnnotationTransfer({state,node,api,run}) {
  const maxBytes=8*1024*1024,maxItems=200,drafts=new Map();let current=null;
  const dialog=node('dialog');dialog.id='annotation-transfer-dialog';dialog.setAttribute('aria-labelledby','annotation-transfer-title');
  const heading=node('h2','Перенос аннотаций');heading.id='annotation-transfer-title';
  const head=node('div',undefined,'dialog-head'),closeButton=button('Закрыть',close,'quiet');head.append(heading,closeButton);
  const status=node('p','','notice'),error=node('p','','error');status.id='annotation-transfer-status';status.setAttribute('role','status');error.id='annotation-transfer-error';error.setAttribute('role','alert');error.hidden=true;
  const exportSection=node('section'),format=node('select');format.id='annotation-transfer-format';
  for(const [value,label] of [['json','JSON — данные и области PDF'],['markdown','Markdown — только чтение']]){const option=node('option',label);option.value=value;format.append(option);}
  const formatLabel=node('label','Формат');formatLabel.append(format);
  const exportNotice=node('p','JSON переносит аннотации и сохранённые области PDF. Markdown предназначен для чтения и не импортируется обратно.','muted');
  const prepare=button('Подготовить файл',prepareExport),download=node('a','Скачать файл','secondary');prepare.id='annotation-transfer-export';download.id='annotation-transfer-download';download.hidden=true;
  exportSection.append(formatLabel,exportNotice,prepare,download);
  const importSection=node('section'),file=node('input');file.id='annotation-transfer-file';file.type='file';file.accept='.json,application/json';
  const fileLabel=node('label','JSON с аннотациями — до 8 МиБ');fileLabel.append(file);
  const previewButton=button('Проверить файл',previewImport),applyButton=button('Импортировать',applyImport,'primary'),retryButton=button('Повторить тот же импорт',sendImport),resetButton=button('Выбрать другой файл',resetImport,'quiet');
  previewButton.id='annotation-transfer-preview';applyButton.id='annotation-transfer-apply';retryButton.id='annotation-transfer-retry';resetButton.id='annotation-transfer-reset';
  const preview=node('section');preview.id='annotation-transfer-summary';
  importSection.append(fileLabel,node('p','Сначала проверьте состав файла и состояние источников. Импорт создаст новые аннотации; существующие записи сохранятся.','muted'),previewButton,preview,applyButton,retryButton,resetButton);
  dialog.append(head,status,error,exportSection,importSection);document.body.append(dialog);

  function button(label,action,css='secondary'){const b=node('button',label,css);b.type='button';b.addEventListener('click',run(action));return b;}
  function actor(){return state.session?.user||'';}
  function valid(c){return !!c&&current===c&&dialog.open&&c.workspace===state.selected&&c.screen===state.revision&&c.user===actor();}
  function base(c){return 'workspaces/'+encodeURIComponent(c.workspace)+'/annotations';}
  function release(c){if(!c)return;c.serial++;c.controller?.abort();if(c.url){URL.revokeObjectURL(c.url);c.url=null;}download.hidden=true;download.removeAttribute('href');}
  function close(){release(current);current=null;dialog.close();}
  dialog.addEventListener('cancel',e=>{e.preventDefault();close();});dialog.addEventListener('close',()=>{if(!dialog.open){release(current);current=null;}});
  function begin(workspace,mode){
    if(workspace!==state.selected)return null;release(current);
    const c={workspace,screen:state.revision,user:actor(),mode,serial:0,busy:false,controller:null,url:null};current=c;
    status.textContent='';error.hidden=true;preview.replaceChildren();exportSection.hidden=mode!=='export';importSection.hidden=mode!=='import';
    heading.textContent=mode==='export'?'Экспорт аннотаций':'Импорт аннотаций';if(!dialog.open)dialog.showModal();return c;
  }
  function fail(c,e){if(valid(c)&&e.name!=='AbortError'){error.textContent=e.message;error.hidden=false;}}
  function controls(c){
    if(!valid(c))return;format.disabled=c.busy;prepare.disabled=c.busy||!c.ids?.length;
    if(c.mode!=='import')return;const d=c.draft,locked=c.busy||d.busy||!!d.pending||!!d.result;
    file.disabled=locked;previewButton.disabled=locked||!d.content;applyButton.disabled=locked||!d.preview?.count;
    retryButton.hidden=!d.pending;retryButton.disabled=c.busy||d.busy;resetButton.disabled=c.busy||d.busy;
    applyButton.textContent=d.preview?.count?'Импортировать '+d.preview.count+' аннотаций':'Импортировать';
  }
  function showExport({workspace=state.selected,ids=[]}={}){
    const c=begin(workspace,'export');if(!c)return;c.ids=Array.isArray(ids)?Array.from(new Set(ids)):[];format.value='json';
    if(!c.ids.length||c.ids.length>maxItems||c.ids.some(id=>typeof id!=='string'||!id)){c.ids=[];fail(c,Error('Выберите от 1 до 200 аннотаций для экспорта.'));}
    else status.textContent='Выбрано аннотаций: '+c.ids.length+'. Файл содержит только выбранные записи.';controls(c);
  }
  format.addEventListener('change',()=>{const c=current;if(!valid(c)||c.mode!=='export'||c.busy)return;release(c);status.textContent='Формат изменён. Подготовьте новый файл.';});
  async function prepareExport(){
    const c=current;if(!valid(c)||c.mode!=='export'||c.busy||!c.ids.length)return;
    const chosen=format.value;if(chosen!=='json'&&chosen!=='markdown')return;
    release(c);c.busy=true;error.hidden=true;controls(c);status.textContent='Подготавливаем файл…';const serial=++c.serial,controller=new AbortController();c.controller=controller;
    try{
      const result=await api(base(c)+'/export',{ids:[...c.ids],format:chosen},false,controller.signal);
      if(!valid(c)||serial!==c.serial||controller.signal.aborted)return;
      if(typeof result.content!=='string'||new TextEncoder().encode(result.content).length>maxBytes||result.count!==c.ids.length)throw Error('Экспорт превышает допустимый размер или содержит некорректный ответ.');
      const filename=String(result.filename||('annotations.'+(chosen==='json'?'json':'md'))).split(/[\\/]/).pop().replace(/[\u0000-\u001f\u007f]/g,'');
      c.url=URL.createObjectURL(new Blob([result.content],{type:chosen==='json'?'application/json;charset=utf-8':'text/markdown;charset=utf-8'}));download.href=c.url;download.download=filename;download.textContent='Скачать '+(chosen==='json'?'JSON':'Markdown');download.hidden=false;
      status.textContent='Подготовлено аннотаций: '+result.count+'. '+(result.digest?'Отпечаток: '+result.digest+'. ':'')+'Нажмите ссылку для сохранения файла.';
    }catch(e){fail(c,e);}finally{c.busy=false;controls(c);}
  }
  function draftKey(c){return JSON.stringify([c.user,c.workspace]);}
  function showImport({workspace=state.selected,onImported}={}){
    const c=begin(workspace,'import');if(!c)return;c.onImported=onImported;
    const key=draftKey(c);if(!drafts.has(key))drafts.set(key,{content:'',filename:'',preview:null,pending:null,result:null,busy:false});c.draft=drafts.get(key);file.value='';showPreview(c);controls(c);
    status.textContent=c.draft.result?'Этот импорт уже сохранён. Можно выбрать другой файл.':c.draft.pending?'Ответ на импорт не подтверждён. Повтор использует тот же файл и идентификатор запроса.':c.draft.busy?'Отправленный импорт ещё выполняется.':c.draft.filename?'Выбран файл: '+c.draft.filename+'.':'Выберите JSON-файл для проверки.';
  }
  file.addEventListener('change',run(async()=>{
    const c=current;if(!valid(c)||c.mode!=='import'||c.busy||c.draft.busy||c.draft.pending||c.draft.result)return;
    const selected=file.files?.[0];if(!selected)return;const serial=++c.serial;c.controller?.abort();c.busy=true;error.hidden=true;
    Object.assign(c.draft,{content:'',filename:'',preview:null});preview.replaceChildren();controls(c);status.textContent='Читаем файл…';
    try{
      if(selected.size>maxBytes)throw Error('Файл превышает 8 МиБ.');const content=await selected.text();
      if(!valid(c)||serial!==c.serial)return;if(new TextEncoder().encode(content).length>maxBytes)throw Error('Файл превышает 8 МиБ.');if(!content.trim())throw Error('Файл пуст.');
      c.draft.content=content;c.draft.filename=selected.name;status.textContent='Выбран файл: '+selected.name+'. Нажмите «Проверить файл».';
    }catch(e){fail(c,e);}finally{c.busy=false;controls(c);}
  }));
  async function previewImport(){
    const c=current;if(!valid(c)||c.mode!=='import'||c.busy||c.draft.busy||!c.draft.content||c.draft.pending||c.draft.result)return;
    const d=c.draft,content=d.content,serial=++c.serial,controller=new AbortController();c.controller?.abort();c.controller=controller;c.busy=true;d.preview=null;preview.replaceChildren();error.hidden=true;controls(c);status.textContent='Проверяем файл и источники…';
    try{
      const result=await api(base(c)+'/import-preview',{content},false,controller.signal);
      if(!valid(c)||serial!==c.serial||controller.signal.aborted)return;
      if(!result||typeof result.digest!=='string'||!/^sha256:[0-9a-f]{64}$/.test(result.digest)||!Number.isSafeInteger(result.count)||result.count<1||result.count>maxItems||!Array.isArray(result.items)||result.items.length>result.count||!result.items.every(item=>item&&typeof item.source_name==='string'&&typeof item.quote==='string'&&typeof item.note==='string')||!Array.isArray(result.warnings)||result.warnings.length>maxItems||!result.warnings.every(w=>typeof w==='string')||!['current','stale','missing','archived','region_count'].every(k=>Number.isSafeInteger(result[k])&&result[k]>=0)||result.current+result.stale+result.missing!==result.count||result.archived>result.count)throw Error('Некорректный результат проверки файла.');
      d.preview=result;showPreview(c);status.textContent='Проверка готова. Просмотрите состав и предупреждения, затем подтвердите импорт.';
    }catch(e){fail(c,e);}finally{c.busy=false;controls(c);}
  }
  function showPreview(c){
    if(!valid(c))return;preview.replaceChildren();const d=c.draft,p=d.result||d.preview;if(!p)return;
    preview.append(node('h3',d.result?'Импорт сохранён':'Предпросмотр импорта'),node('p','Аннотаций: '+(p.imported??p.count)+'. Актуальных: '+p.current+'; изменившихся источников: '+p.stale+'; недоступных: '+p.missing+'.'));
    if(d.preview){const source=d.preview;preview.append(node('p','Архивных: '+source.archived+'. Областей PDF: '+source.region_count+'.','muted'));
      for(const warning of (source.warnings||[]).slice(0,maxItems))preview.append(node('p',String(warning),'notice'));
      if(!d.result){for(const item of source.items){const row=node('section',undefined,'card');row.append(node('h4',item.source_name||'Без названия источника'),node('pre',item.quote||''),node('p',item.note||'Без комментария'),node('p',({current:'Источник актуален',stale:'Источник изменился',missing:'Источник недоступен'}[item.state]||item.state)+(item.archived?' · Архив':'')+(item.reason?' · '+item.reason:''),'muted'));preview.append(row);}
        if(source.items.length<source.count)preview.append(node('p','Показано записей: '+source.items.length+' из '+source.count+'. Импорт включает весь проверенный файл.','notice'));}
    }
  }
  async function applyImport(){
    const c=current;if(!valid(c)||c.mode!=='import'||c.busy||c.draft.busy||c.draft.pending||c.draft.result||!c.draft.preview)return;
    c.draft.pending=Object.freeze({content:c.draft.content,digest:c.draft.preview.digest,request_id:crypto.randomUUID().replaceAll('-','')});await sendImport();
  }
  async function sendImport(){
    const c=current;if(!valid(c)||c.mode!=='import'||c.busy||c.draft.busy||!c.draft.pending)return;
    const d=c.draft,pending=d.pending;d.busy=true;c.busy=true;error.hidden=true;controls(c);status.textContent='Импортируем проверенный файл…';
    try{
      const result=await api(base(c)+'/import',pending);
      if(!result||result.imported!==d.preview.count||!Array.isArray(result.items)||result.items.length!==result.imported||!result.items.every(item=>item&&typeof item.id==='string'&&Number.isSafeInteger(item.revision)&&item.revision>0)||!['current','stale','missing'].every(k=>Number.isSafeInteger(result[k])&&result[k]>=0)||result.current+result.stale+result.missing!==result.imported)throw Error('Некорректный ответ на импорт. Повторите ту же отправку для подтверждения.');
      d.result=result;d.pending=null;
      if(!valid(c))return;showPreview(c);status.textContent=result.replayed?'Этот импорт уже был сохранён. Повторных записей не добавлено.':'Импорт сохранён.';
      if(c.onImported){try{await c.onImported(result);}catch(e){if(valid(c)){error.textContent='Импорт сохранён, но список не обновлён: '+e.message;error.hidden=false;}}}
    }catch(e){fail(c,e);if(valid(c))status.textContent='Импорт не подтверждён. Повторите ту же отправку; файл и идентификатор сохранены в этой вкладке.';}
    finally{d.busy=false;c.busy=false;if(valid(current)&&current.mode==='import'&&current.draft===d){const visible=current;showPreview(visible);controls(visible);if(visible!==c){status.textContent=d.result?'Импорт сохранён.':'Импорт не подтверждён. Повторите ту же отправку.';if(d.result&&visible.onImported){try{await visible.onImported(d.result);}catch(e){if(valid(visible)){error.textContent='Импорт сохранён, но список не обновлён: '+e.message;error.hidden=false;}}}}}}
  }
  function resetImport(){
    const c=current;if(!valid(c)||c.mode!=='import'||c.busy||c.draft.busy)return;
    if(c.draft.pending&&!window.confirm('Предыдущий импорт мог сохраниться. Безопасный повтор использует тот же запрос. Сбросить его и выбрать другой файл? Повторный импорт того же файла может создать дубликаты.'))return;
    c.controller?.abort();c.serial++;const d={content:'',filename:'',preview:null,pending:null,result:null,busy:false};drafts.set(draftKey(c),d);c.draft=d;file.value='';preview.replaceChildren();error.hidden=true;status.textContent='Выберите JSON-файл для проверки.';controls(c);
  }
  return {showExport,showImport,close};
}
