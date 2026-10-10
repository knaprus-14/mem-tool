'use strict';
function installMemMapImport({state,$,node,api,run,showMap}) {
  const maxBytes=8*1024*1024,pageSize=100,drafts=new Map(),formats={native:'JSON mem-tool',opml:'OPML',freemind:'FreeMind',xmind:'XMind',markdown:'Markdown outline'};let current=null;
  const dialog=node('dialog');dialog.id='map-import-dialog';dialog.setAttribute('aria-labelledby','map-import-title');
  const title=node('h2','Импорт карты');title.id='map-import-title';
  const note=node('p','Сначала проверьте структуру и предупреждения. Подтверждение создаёт отдельную карту; существующие карты не заменяются. Для внешних форматов исходный файл сохраняется вместе с новой картой.');
  const format=node('select');format.id='map-import-format';for(const [value,label] of Object.entries(formats)){const option=node('option',label);option.value=value;format.append(option);}format.value='native';
  const formatLabel=node('label','Формат файла');formatLabel.append(format);
  const file=node('input');file.id='map-import-file';file.type='file';file.accept='.json,.opml,.mm,.xmind,.md,application/json';
  const label=node('label','Файл (до 8 МиБ)');label.append(file);
  const status=node('p','','notice');status.id='map-import-status';status.setAttribute('role','status');
  const error=node('p');error.id='map-import-error';error.setAttribute('role','alert');
  const preview=node('div');preview.id='map-import-preview';preview.setAttribute('aria-live','polite');
  const check=button('Проверить файл','map-import-check',checkFile),apply=button('Создать новую карту','map-import-apply',applyImport),reset=button('Выбрать другой файл','map-import-reset',resetImport),open=button('Открыть созданную карту','map-import-open',openResult),closeButton=button('Закрыть','map-import-close',close);
  const buttons=node('div',undefined,'head-actions');buttons.append(check,apply,reset,open,closeButton);dialog.append(title,note,formatLabel,label,status,error,preview,buttons);document.body.append(dialog);
  function button(label,id,action){const b=node('button',label,'secondary');b.id=id;b.type='button';b.addEventListener('click',run(action));return b;}
  const actor=()=>state.session?.user||'',csrf=()=>state.session?.csrf||'',key=c=>JSON.stringify([c.user,c.csrf,c.workspace]);
  const valid=c=>!!c&&current===c&&dialog.open&&c.workspace===state.selected&&c.screen===state.revision&&c.user===actor()&&c.csrf===csrf()&&state.session?.authenticated===true;
  const base=c=>'workspaces/'+encodeURIComponent(c.workspace)+'/maps/import';
  function release(c){if(c){c.serial++;c.controller?.abort();}}
  function purge(){for(const k of drafts.keys()){const [user,token]=JSON.parse(k);if(state.session?.authenticated!==true||user!==actor()||token!==csrf())drafts.delete(k);}}
  function close(){const c=current;release(c);if(c&&!c.draft.pending&&!c.draft.busy&&!c.draft.result&&drafts.get(key(c))===c.draft)drafts.delete(key(c));current=null;purge();dialog.close();file.value='';preview.replaceChildren();status.textContent='';error.textContent='';apply.disabled=true;}
  dialog.addEventListener('cancel',e=>{e.preventDefault();close();});dialog.addEventListener('close',()=>{if(!dialog.open&&current)close();});
  $('import-map').addEventListener('click',run(()=>{
    close();if(!state.selected||state.session?.authenticated!==true)return;
    const c={workspace:state.selected,screen:state.revision,user:actor(),csrf:csrf(),serial:0,busy:false,page:0},k=key(c);
    if(!drafts.has(k)){for(const [old,d] of drafts){if(drafts.size<8)break;if(!d.pending&&!d.busy)drafts.delete(old);}if(drafts.size>=8)throw Error('В этой вкладке уже 8 неподтверждённых импортов. Откройте их рабочие базы и повторите либо явно отмените отправки.');drafts.set(k,{input:null,preview:null,pending:null,result:null,busy:false,error:'',bytes:0,format:'native'});}
    c.draft=drafts.get(k);current=c;format.value=c.draft.format;file.value='';dialog.showModal();render(c);
  }));
  function controls(c){if(!valid(c))return;const d=c.draft,locked=c.busy||d.busy||!!d.pending||!!d.result;file.disabled=locked;format.disabled=locked;check.disabled=locked||!file.files?.length;apply.disabled=c.busy||d.busy||!!d.result||(!d.preview&&!d.pending);apply.textContent=d.pending?'Повторить создание':'Создать новую карту';reset.disabled=c.busy||d.busy;open.hidden=!d.result;open.disabled=c.busy||d.busy;}
  function changed(){const c=current;if(!valid(c)||c.busy||c.draft.busy||c.draft.pending||c.draft.result)return;release(c);Object.assign(c.draft,{input:null,preview:null,error:'',bytes:0,format:format.value});c.page=0;render(c);}
  file.addEventListener('change',()=>{const c=current;if(!valid(c)||c.busy||c.draft.busy||c.draft.pending||c.draft.result)return;const name=file.files?.[0]?.name||'',extension=name.split('.').pop().toLowerCase(),chosen={json:'native',opml:'opml',mm:'freemind',xmind:'xmind',md:'markdown'}[extension];if(chosen)format.value=chosen;changed();});
  format.addEventListener('change',changed);
  const boundedText=(v,n)=>typeof v==='string'&&Array.from(v).length<=n,integer=n=>Number.isSafeInteger(n)&&n>=0,hash=s=>typeof s==='string'&&/^sha256:[0-9a-f]{64}$/.test(s);
  function previewValid(p,d){
    if(!p||!hash(p.digest)||!boundedText(p.title,512)||!integer(p.node_count)||p.node_count<1||p.node_count>1000||!integer(p.source_count)||p.source_count>2000)return false;
    const warnings=p.warnings??[];if(!Array.isArray(warnings)||warnings.length>2000||!warnings.every(w=>boundedText(w,4096)))return false;
    if(d.format==='native')return ['current_sources','stale_sources','missing_sources'].every(k=>integer(p[k]))&&p.current_sources+p.stale_sources+p.missing_sources===p.source_count;
    if(p.format!==d.format||!hash(p.source_sha256)||p.source_bytes!==d.bytes||!Array.isArray(p.warnings)||!Array.isArray(p.nodes)||p.nodes.length!==p.node_count)return false;
    const seen=new Map();let roots=0;for(const n of p.nodes){if(!n||!boundedText(n.ref,256)||!n.ref||seen.has(n.ref)||!boundedText(n.parent_ref,256)||!boundedText(n.label,512)||!integer(n.depth)||n.depth>=1000)return false;if(n.parent_ref){if(!seen.has(n.parent_ref)||seen.get(n.parent_ref)+1!==n.depth)return false;}else{if(n.depth!==0)return false;roots++;}seen.set(n.ref,n.depth);}return roots===1;
  }
  // Full chunks contain a multiple of three bytes: only the final chunk pads.
  // There is no large argument spread or lossy text decoding for source bytes.
  function encodeBytes(bytes){const chunks=[];for(let offset=0;offset<bytes.length;offset+=24576){let chunk='';for(const b of bytes.subarray(offset,offset+24576))chunk+=String.fromCharCode(b);chunks.push(btoa(chunk));}return chunks.join('');}
  async function checkFile(){
    const c=current;if(!valid(c)||c.busy||c.draft.busy||c.draft.pending||c.draft.result)return;const selected=file.files?.[0],chosen=format.value;if(!selected||!Object.hasOwn(formats,chosen))return;
    release(c);const serial=++c.serial,controller=new AbortController();c.controller=controller;c.busy=true;Object.assign(c.draft,{input:null,preview:null,error:'',format:chosen,bytes:0});render(c);
    try{
      if(!Number.isSafeInteger(selected.size)||selected.size>maxBytes)throw Error('Файл превышает 8 МиБ.');const bytes=new Uint8Array(await selected.arrayBuffer());if(!valid(c)||serial!==c.serial)return;if(!bytes.length||bytes.length>maxBytes)throw Error('Нужен непустой файл до 8 МиБ.');
      let input;if(chosen==='native')input={content:new TextDecoder('utf-8',{fatal:true}).decode(bytes)};else{const filename=(selected.name||'').split(/[\\/]/).pop();if(!filename||!boundedText(filename,200)||/[\u0000-\u001f\u007f]/.test(filename))throw Error('Имя файла должно содержать от 1 до 200 символов без управляющих знаков.');input={format:chosen,filename,content_base64:encodeBytes(bytes)};}
      c.draft.bytes=bytes.length;const result=await api(base(c)+(chosen==='native'?'/preview':'/outline-preview'),input,false,controller.signal);if(!valid(c)||serial!==c.serial||controller.signal.aborted)return;
      if(!previewValid(result,c.draft))throw Error('Некорректный результат проверки файла. Повторите предварительный просмотр.');c.draft.input=Object.freeze(input);c.draft.preview=result;c.page=0;
    }catch(e){if(valid(c)&&serial===c.serial&&e.name!=='AbortError')c.draft.error=e.message;}finally{c.busy=false;if(valid(c)&&serial===c.serial)render(c);}
  }
  function render(c){
    if(!valid(c))return;const d=c.draft,p=d.preview;preview.replaceChildren();error.textContent=d.error;status.textContent=d.result?'Карта создана. Можно открыть её или выбрать другой файл.':d.busy?'Создаём отдельную карту…':c.busy?'Читаем и проверяем файл…':d.pending?'Создание не подтверждено. Повтор использует неизменённый файл и тот же идентификатор запроса.':p?'Проверьте предупреждения и структуру, затем нажмите «Создать новую карту».':'Выберите файл и формат. Расширение подставляет формат, его можно изменить перед проверкой.';
    if(p){preview.append(node('h3',p.title),node('p','Формат: '+formats[d.format]+' · Узлов: '+p.node_count+' · Источников: '+p.source_count));if(d.format==='native')preview.append(node('p','Актуальных: '+p.current_sources+' · Устаревших: '+p.stale_sources+' · Отсутствующих: '+p.missing_sources));else preview.append(node('p','Исходный файл: '+d.input.filename+' · '+p.source_bytes+' байт. SHA-256: '+p.source_sha256,'muted'));
      for(const warning of p.warnings||[])preview.append(node('p',warning,'notice'));
      if(d.format!=='native'){
        const count=node('p','Структура: узлы '+(c.page+1)+'–'+Math.min(c.page+pageSize,p.nodes.length)+' из '+p.nodes.length+'. Все узлы входят в новую карту.');count.setAttribute('role','status');preview.append(count);const list=node('ol');list.start=c.page+1;list.setAttribute('aria-label','Структура импортируемой карты');
        for(const n of p.nodes.slice(c.page,c.page+pageSize)){const row=node('li');row.append(node('span',n.label),node('small',' — уровень '+n.depth+'; '+(n.parent_ref?'родитель '+n.parent_ref:'корень')+'; узел '+n.ref,'muted'));list.append(row);}preview.append(list);
        const paging=node('div',undefined,'head-actions'),previous=button('Предыдущие узлы','map-import-previous',()=>page(c,-pageSize)),next=button('Следующие узлы','map-import-next',()=>page(c,pageSize));previous.disabled=c.page===0;next.disabled=c.page+pageSize>=p.nodes.length;paging.append(previous,next);preview.append(paging);
      }
    }controls(c);
  }
  function page(c,delta){if(!valid(c)||!c.draft.preview)return;c.page=Math.max(0,Math.min(Math.floor((c.draft.preview.node_count-1)/pageSize)*pageSize,c.page+delta));render(c);}
  async function applyImport(){const c=current;if(!valid(c)||c.busy||c.draft.busy||c.draft.result||(!c.draft.preview&&!c.draft.pending))return;const d=c.draft;if(!d.pending)d.pending=Object.freeze({...d.input,digest:d.preview.digest,request_id:crypto.randomUUID().replaceAll('-','')});await send(c);}
  async function send(c){
    const d=c.draft,pending=d.pending;d.busy=true;d.error='';render(c);let saved=false;
    try{const result=await api(base(c)+(d.format==='native'?'/apply':'/outline-apply'),pending);if(!result||typeof result.id!=='string'||!/^[A-Za-z0-9][A-Za-z0-9_-]{0,255}$/.test(result.id)||typeof result.replayed!=='boolean')throw Error('Сервер не подтвердил создание карты.');d.result={id:result.id,replayed:result.replayed};d.pending=null;d.input=null;d.preview=null;d.bytes=0;saved=true;}catch(e){d.error=(e.message||'Отправка не подтверждена.')+' Повторите создание с тем же файлом. Для нового файла явно откажитесь от этой попытки.';}finally{d.busy=false;const visible=current;if(valid(visible)&&visible.draft===d)render(visible);}
    if(saved&&valid(c)){const id=d.result.id;close();await showMap(id);}
  }
  function resetImport(){const c=current;if(!valid(c)||c.busy||c.draft.busy)return;if(c.draft.pending&&!confirm('Создание могло уже завершиться. Повтор проверит эту попытку без дубля. Отказаться от повтора и выбрать другой файл?'))return;Object.assign(c.draft,{input:null,preview:null,pending:null,result:null,error:'',bytes:0});file.value='';c.page=0;render(c);}
  async function openResult(){const c=current;if(!valid(c)||!c.draft.result)return;const id=c.draft.result.id;close();await showMap(id);}
  return {close};
}
