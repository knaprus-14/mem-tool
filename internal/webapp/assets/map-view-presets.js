'use strict';
function installMemMapViewPresets({state,$,node,api,run,viewControls}) {
  const records=new Map(),dialog=$('map-presets-dialog');let current=null;
  const actor=()=>state.session?.user||'',csrf=()=>state.session?.csrf||'';
  const active=m=>!!m&&state.map===m&&state.view==='maps'&&m.selected===state.selected&&m.revision===state.revision&&state.session?.authenticated&&m.owner===actor()&&m.csrf===csrf();
  const valid=c=>!!c&&current===c&&active(c.map)&&dialog.open;
  const key=m=>JSON.stringify([actor(),csrf(),m.selected,m.view.map.id]);
  const route=m=>'workspaces/'+encodeURIComponent(m.selected)+'/maps/'+encodeURIComponent(m.view.map.id)+'/view-presets';
  const chosen=r=>r.items.find(item=>item.id===r.selected);
  const validName=s=>s.trim().length>0&&Array.from(s.trim()).length<=80&&!/[\u0000-\u001f\u007f-\u009f]/u.test(s);
  function close(){current=null;dialog.close();for(const [k] of records){const [user,token]=JSON.parse(k);if(!state.session?.authenticated||user!==actor()||token!==csrf())records.delete(k);}}
  function record(m){const k=key(m);if(!records.has(k)){for(const [old,r] of records){if(records.size<8)break;if(!r.pending&&!r.busy)records.delete(old);}if(records.size>=8)throw Error('Есть восемь карт с неподтверждёнными действиями над именованными видами. Завершите повтор в одной из них.');records.set(k,{revision:null,items:[],selected:'',name:'',pending:null,busy:false,message:''});}return records.get(k);}
  function render(){const c=current;if(!valid(c))return;const r=c.record,item=chosen(r),blocked=r.busy||!!r.pending||r.revision===null;
    $('map-presets-title').textContent='Именованные виды: '+c.map.view.map.title;
    const picker=$('map-presets-picker');picker.replaceChildren();for(const item of r.items){const option=node('option',item.name+(item.changed?' · карта изменилась':''));option.value=item.id;picker.append(option);}picker.value=r.selected;picker.disabled=r.busy||!!r.pending||!r.items.length;
    $('map-presets-name').value=r.name;$('map-presets-name').readOnly=r.busy||!!r.pending;
    $('map-presets-create').disabled=blocked||r.items.length>=20||!validName(r.name);
    $('map-presets-apply').disabled=blocked||!item;
    $('map-presets-update').disabled=blocked||!item||!validName(r.name);
    $('map-presets-rename').disabled=blocked||!item||!validName(r.name);
    $('map-presets-delete').disabled=blocked||!item;
    $('map-presets-refresh').disabled=r.busy||!!r.pending;
    $('map-presets-retry').hidden=!r.pending;$('map-presets-retry').disabled=r.busy;
    $('map-presets-discard').hidden=!r.pending;$('map-presets-discard').disabled=r.busy;
    $('map-presets-status').textContent=r.message;
    $('map-presets-count').textContent='Сохранено: '+r.items.length+' / 20. '+(item?'Вид сохранён для ревизии '+item.map_revision+'. ':'')+'Открытие вида меняет только отображение карты.';
  }
  function checked(value,c){
    if(!value||!Number.isSafeInteger(value.revision)||value.revision<0||value.current_map_revision!==c.map.view.map.revision||!Array.isArray(value.items)||value.items.length>20)throw Error('Список видов недоступен или карта изменилась. Обновите карту.');
    const ids=new Set();for(const item of value.items){if(!item||!/^mvp-[0-9a-f]{32}$/.test(item.id)||ids.has(item.id)||typeof item.name!=='string'||!validName(item.name)||typeof item.changed!=='boolean'||!Number.isSafeInteger(item.map_revision)||item.map_revision<1||item.name!==item.name.trim())throw Error('Некорректный список видов.');ids.add(item.id);viewControls.validate(c.map,{revision:1,map_revision:item.map_revision,current_map_revision:value.current_map_revision,changed:item.changed,replayed:false,state:item.state});}return value;
  }
  async function read(c){if(!valid(c)||c.record.busy)return;const r=c.record;r.busy=true;r.message='Читаем именованные виды…';render();
    try{const result=checked(await api(route(c.map)),c);if(!valid(c))return;r.revision=result.revision;r.items=result.items;
      if(!chosen(r)){r.selected=r.items[0]?.id||'';if(!r.name)r.name=chosen(r)?.name||'';}
      r.message=r.pending?'Есть неподтверждённое действие. Повтор использует прежние параметры.':'Список прочитан. Новые изменения сохраняются только кнопками ниже.';
    }catch(e){if(valid(c)){r.revision=null;r.message=e.message;}}
    finally{r.busy=false;if(valid(current)&&current.record===r){if(current!==c)await read(current);else render();}}
  }
  async function show(){const m=state.map;if(!active(m)||valid(current)&&current.map===m)return;const r=record(m);current={map:m,record:r};if(!dialog.open)dialog.showModal();render();await read(current);}
  async function send(c){if(!valid(c))return;const r=c.record,p=r.pending;if(!p||r.busy)return;r.busy=true;r.message='Сохраняем действие…';render();let accepted=false;
    try{const answer=await api(route(c.map),p);if(!answer||answer.revision!==p.revision+1||answer.map_revision!==p.map_revision||answer.id!==p.id||answer.operation!==p.operation||typeof answer.replayed!=='boolean')throw Error('Сервер не подтвердил именно это действие.');r.pending=null;r.revision=null;r.selected=p.operation==='delete'?'':p.id;r.name=p.name;r.message='Действие подтверждено. Перечитайте список для следующего действия.';accepted=true;}
    catch(e){r.message=e.message+' Действие не подтверждено; повтор отправит те же параметры. При конфликте откажитесь от повтора и обновите список.';}
    finally{r.busy=false;if(valid(current)&&current.record===r)render();}
    const visible=current;if(accepted&&valid(visible)&&visible.record===r)await read(visible);
  }
  async function mutate(operation){const c=current;if(!valid(c))return;const r=c.record,item=chosen(r);if(r.busy||r.pending||r.revision===null||operation!=='create'&&!item)return;
    if(operation==='delete'&&!confirm('Удалить именованный вид «'+item.name+'»? Сама карта сохранится.'))return;
    if(operation==='update'&&!confirm('Заменить настройки вида «'+item.name+'» текущим отображением карты?'))return;
    const id=operation==='create'?'mvp-'+crypto.randomUUID().replaceAll('-',''):item.id;
    r.pending=JSON.parse(JSON.stringify({revision:r.revision,map_revision:c.map.view.map.revision,request_id:crypto.randomUUID().replaceAll('-',''),state:viewControls.snapshot(c.map),operation,id,name:operation==='delete'?'':r.name.trim()}));await send(c);
  }
  $('map-presets-open').addEventListener('click',run(show));$('map-presets-close').addEventListener('click',close);dialog.addEventListener('cancel',e=>{e.preventDefault();close();});
  $('map-presets-picker').addEventListener('change',()=>{if(!valid(current))return;const r=current.record;if(r.busy||r.pending)return;r.selected=$('map-presets-picker').value;r.name=chosen(r)?.name||'';render();});
  $('map-presets-name').addEventListener('input',()=>{if(!valid(current))return;const r=current.record;if(r.busy||r.pending)return;r.name=$('map-presets-name').value;render();});
  for(const operation of ['create','update','rename','delete'])$('map-presets-'+operation).addEventListener('click',run(()=>mutate(operation)));
  $('map-presets-refresh').addEventListener('click',run(()=>{if(valid(current)&&!current.record.pending)return read(current);}));
  $('map-presets-retry').addEventListener('click',run(()=>send(current)));
  $('map-presets-discard').addEventListener('click',run(async()=>{const c=current;if(!valid(c)||c.record.busy||!c.record.pending)return;if(!confirm('Действие могло завершиться. Отказаться от повтора и прочитать актуальный список?'))return;c.record.pending=null;await read(c);}));
  $('map-presets-apply').addEventListener('click',run(()=>{const c=current;if(!valid(c)||c.record.busy||c.record.pending||c.record.revision===null)return;const item=chosen(c.record);if(!item)return;viewControls.apply(c.map,{revision:1,map_revision:item.map_revision,current_map_revision:c.map.view.map.revision,changed:item.changed,replayed:false,state:item.state});close();}));
  return {show,close};
}
