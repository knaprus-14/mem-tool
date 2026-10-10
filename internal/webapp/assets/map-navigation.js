'use strict';
function installMemMapNavigation({state,$,node,run,viewControls,showMap,setMapDetails,readSource,closeSource,openSource}) {
  const limit=100,byteLimit=8*1024*1024;let identity='',entries=[],position=-1,serial=0,busy=false,active=false,depth=0,silent=0,message='';
  const who=()=>state.session?.authenticated?JSON.stringify([state.session.user||'',state.session.csrf||'',state.selected]):'';
  const mapActive=()=>active&&state.view==='maps'&&state.map&&state.map.selected===state.selected&&state.map.revision===state.revision&&state.map.owner===(state.session?.user||'')&&state.map.csrf===(state.session?.csrf||'')&&!!state.session?.authenticated;
  function closeList(){$('map-nav-dialog').close();$('map-nav-items').replaceChildren();}
  function sync(){const next=who();if(next!==identity){identity=next;entries=[];position=-1;serial++;busy=false;active=false;message='';closeList();}return !!next;}
  const ratio=el=>Math.max(0,Math.min(1,el.scrollTop/Math.max(1,el.scrollHeight-el.clientHeight)));
  function capture(){if(!mapActive())return null;const m=state.map;return {map_id:m.view.map.id,map_revision:m.view.map.revision,map_title:Array.from(m.view.map.title||'').slice(0,256).join(''),node_title:Array.from(m.tree.byID.get(m.selectedNode)?.label||'Корень').slice(0,256).join(''),state:viewControls.snapshot(m),details:!!m.details,details_scroll:ratio($('map-inspector')),source:readSource?.()||null};}
  const clone=x=>JSON.parse(JSON.stringify(x)),same=(a,b)=>JSON.stringify(a)===JSON.stringify(b);
  function trim(){let bytes=entries.reduce((sum,e)=>sum+JSON.stringify(e).length*2,0);while(entries.length>1&&(entries.length>limit||bytes>byteLimit)){bytes-=JSON.stringify(entries.shift()).length*2;position--;}}
  function checkpoint(){if(!sync()||silent)return;const value=capture();if(value&&position>=0&&entries[position]?.map_id===value.map_id){entries[position]=clone(value);trim();}}
  function enter({replace=false}={}){if(!sync()||silent)return;active=true;const value=capture();if(!value)return;
    if(position>=0&&(replace&&entries[position].map_id===value.map_id||same(entries[position],value)))entries[position]=clone(value);
    else{entries=entries.slice(0,position+1);entries.push(clone(value));position=entries.length-1;}
    trim();message='';render();
  }
  function cancel(){serial++;busy=false;}
  function change(fn){sync();if(silent||depth||!mapActive())return fn();cancel();checkpoint();depth++;let succeeded=false;try{const result=fn();succeeded=true;return result;}finally{depth--;if(succeeded)enter();else render();}}
  function silence(fn){silent++;try{return fn();}finally{silent--;}}
  function start(){cancel();checkpoint();render();}
  function leave(){checkpoint();cancel();active=false;closeList();sync();render();}
  function render(){
    const enabled=mapActive();for(const prefix of ['map-nav','map-document-nav']){const back=$(prefix+'-back'),forward=$(prefix+'-forward');back.disabled=!enabled||busy||position<=0;forward.disabled=!enabled||busy||position<0||position>=entries.length-1;$(prefix+'-list').disabled=!enabled||busy||!entries.length;$(prefix+'-status').textContent=message||(entries.length?'Переход '+(position+1)+' из '+entries.length:'');}
    $('map-nav-clear').disabled=busy||!entries.length;
    if($('map-nav-dialog').open)renderList();
  }
  function renderList(){const host=$('map-nav-items');host.replaceChildren();for(let i=entries.length-1;i>=0;i--){const entry=entries[i],label=(i+1)+'. '+entry.map_title+' → '+entry.node_title+(entry.source?' · '+(entry.source.title||'Источник')+(entry.source.page?' · стр. '+entry.source.page:''):' · '+({diagram:'Схема',outline:'План',table:'Таблица'}[entry.state.display_mode]||'')),button=node('button',label,'row');button.type='button';button.disabled=busy||i===position;button.setAttribute('aria-current',i===position?'step':'false');button.addEventListener('click',run(()=>{if(entries[i]===entry)return jump(i);}));host.append(button);}}
  async function jump(index){
    if(!sync()||!mapActive()||busy||!Number.isInteger(index)||index<0||index>=entries.length||index===position)return;
    checkpoint();const target=clone(entries[index]),owner=identity,screen=state.revision,token=++serial;
    const valid=()=>token===serial&&who()===owner&&state.revision===screen&&state.view==='maps';
    busy=true;message='Возвращаемся к сохранённому месту…';closeList();render();
    try{
      closeSource?.();const m=await showMap(target.map_id,{record:false,isCurrent:valid});if(!valid())return;if(!m||m.view.map.id!==target.map_id)throw Error('Карта не открыта. Переход не выполнен.');
      const missing=!m.tree.byID.has(target.state.selected_node_id),changed=m.view.map.revision!==target.map_revision;
      silence(()=>{viewControls.apply(m,{revision:1,map_revision:target.map_revision,current_map_revision:m.view.map.revision,changed,replayed:false,state:target.state});setMapDetails(target.details);const panel=$('map-inspector');panel.scrollTop=target.details_scroll*Math.max(0,panel.scrollHeight-panel.clientHeight);});
      position=index;let note=missing?'Узел удалён: открыт корень текущей карты.':changed?'Карта изменилась: открыта текущая редакция.':'';
      if(target.source){if(missing||changed)note+=' Источник прежней ревизии не открыт; выберите актуальное подтверждение узла.';else if(!await openSource(target.source,{track:false,isCurrent:valid}))note='Источник недоступен. Открыт связанный узел; другой фрагмент не подставлен.';}
      if(!valid())return;const actual=capture();if(actual)entries[position]=clone(actual);message=note.trim()||'Место восстановлено. Содержимое карты не изменялось.';
    }catch(e){if(valid())message=e.message+' История переходов сохранена.';}
    finally{if(valid()){busy=false;render();}}
  }
  for(const prefix of ['map-nav','map-document-nav']){
    $(prefix+'-back').addEventListener('click',run(()=>jump(position-1)));$(prefix+'-forward').addEventListener('click',run(()=>jump(position+1)));
    $(prefix+'-list').addEventListener('click',()=>{if(!sync()||!mapActive()||busy)return;checkpoint();$('map-nav-dialog').showModal();renderList();});
  }
  $('map-nav-close').addEventListener('click',closeList);
  $('map-nav-dialog').addEventListener('close',()=>{if(!$('map-nav-dialog').open)$('map-nav-items').replaceChildren();});
  $('map-nav-clear').addEventListener('click',()=>{if(!sync()||busy)return;const value=capture();entries=value?[clone(value)]:[];position=entries.length-1;message='История переходов очищена. Текущий вид сохранён на экране.';render();});
  return {change,silence,checkpoint,enter,start,leave,jump,render};
}
