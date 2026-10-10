'use strict';
function installMemIndexTools({state,$,api,run}) {
  let current=null;
  function valid(c){return current===c&&state.view==='settings'&&state.selected===c.workspace&&state.revision===c.screen;}
  function render(c,s){
    c.value=s;const names={none:'Пересчёт ещё не запускался',preparing:'Подготавливаем векторы',ready:'Готово к применению',applied:'Новые векторы применены',rolled_back:'Прежние векторы восстановлены',cancelled:'Пересчёт отменён',failed:'Пересчёт остановлен'};
    $('index-status').textContent=(names[s.status]||s.status)+(s.total?' · '+s.completed+' / '+s.total+' · '+s.model:'')+(s.error?' — '+s.error:'');
    $('index-start').disabled=c.busy||['preparing','ready'].includes(s.status);
    $('index-apply').hidden=s.status!=='ready';$('index-apply').disabled=c.busy;
    $('index-cancel').hidden=!['preparing','ready'].includes(s.status);$('index-cancel').disabled=c.busy;
    $('index-rollback').hidden=s.status!=='applied';$('index-rollback').disabled=c.busy||!s.can_rollback;
    $('index-rollback-help').hidden=s.status!=='applied'||s.can_rollback;
  }
  async function refresh(c){if(!valid(c)||c.busy)return;try{const s=await api('workspaces/'+c.workspace+'/index');if(valid(c))render(c,s);}catch(e){if(valid(c))$('index-status').textContent=e.message;}}
  async function poll(c){if(!valid(c))return;await refresh(c);if(valid(c))setTimeout(()=>poll(c),2000);}
  function load(){const c={workspace:state.selected,screen:state.revision,busy:false};current=c;$('index-error').textContent='';$('index-status').textContent='Читаем состояние пересчёта…';$('index-apply').hidden=true;$('index-rollback').hidden=true;$('index-cancel').hidden=true;poll(c);}
  async function act(action){
    const c=current;if(!c||!valid(c)||c.busy)return;
    const body=action==='start'?(c.pending||(c.pending={request_id:crypto.randomUUID().replaceAll('-',''),config_revision:state.settings.revision})):{id:c.value?.id};
    if(action!=='start'&&!body.id)return;c.busy=true;render(c,c.value||{status:'none'});$('index-error').textContent='';
    try{const s=await api('workspaces/'+c.workspace+'/index/'+action,body);if(!valid(c))return;c.pending=null;render(c,s);}
    catch(e){if(valid(c))$('index-error').textContent=e.message+' При сбое связи повторите действие или обновите состояние.';}
    finally{c.busy=false;if(valid(c)){await refresh(c);if(c.value)render(c,c.value);}}
  }
  for(const action of ['start','apply','cancel','rollback'])$('index-'+action).addEventListener('click',run(()=>act(action)));
  $('index-refresh').addEventListener('click',run(()=>refresh(current)));
  return {load};
}
