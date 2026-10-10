'use strict';
function installMemJobHistory({state,$,node,api,run,statuses}) {
  const dialog=$('job-history-dialog');let current=null;
  function valid(c){return c&&current===c&&dialog.open&&state.selected===c.workspace&&state.revision===c.screen;}
  function close(){current=null;dialog.close();}
  $('job-history-close').addEventListener('click',close);dialog.addEventListener('cancel',close);dialog.addEventListener('close',()=>{if(!dialog.open)current=null;});
  function route(c){return 'workspaces/'+c.workspace+'/jobs/'+c.id;}
  function error(c,e){if(valid(c)){$('job-history-error').textContent=e.message;$('job-history-error').hidden=false;}}
  async function pages(c,attempt,after){if(!valid(c)||c.busy)return;c.busy=true;$('job-history-next').disabled=true;$('job-history-error').hidden=true;
    try{const report=await api(route(c)+'/attempts/'+attempt+'/pages?after='+after);if(!valid(c))return;c.attempt=attempt;c.after=after;c.next=report.next;
      $('job-history-pages').hidden=false;$('job-history-report').textContent=`Попытка ${attempt}: ${statuses[report.status]||'Статус неизвестен'}. Страниц: ${report.physical_pages}; записано: ${report.stored_pages}; пустых: ${report.empty_pages}; пропущено: ${report.failed_pages}. Фрагментов: ${report.chunks}.`;
      $('job-history-report-note').textContent=report.status==='running'?'Обработка ещё идёт. Итоговая таблица появится после завершения попытки.':report.has_error?'Обработка завершилась с ошибкой. Результат записи: '+(report.document_updated?'документ обновлён.':'документ не обновлён.'):'Результат записи: '+(report.document_updated?'документ обновлён.':'документ не обновлён.');
      const rows=$('job-history-page-rows');rows.replaceChildren();for(const p of report.pages){const row=node('section',undefined,'card');row.append(node('h4','Страница '+p.page+' · '+({stored:'Текст записан',empty:'Пустая',failed:'Пропущена'}[p.status]||'Статус неизвестен')),node('p',`Способ: ${{text:'текстовый слой',ocr:'распознавание OCR'}[p.method]||'нет данных'} · Символов: ${p.text_runes} · Блоков: ${p.blocks} · Фрагментов: ${p.chunks}`,'muted'));if(p.method==='ocr'&&p.confidence>=0)row.append(node('p','Уверенность OCR: '+p.confidence.toLocaleString('ru-RU',{maximumFractionDigits:1})+'%'));if(p.has_warnings)row.append(node('p','Есть предупреждения; проверьте текст и ссылки на источник.','notice'));rows.append(row);}
      if(report.has_warnings)rows.append(node('p','В итоговом отчёте есть предупреждения. Проверьте полученные фрагменты.','notice'));if(!report.pages.length)rows.append(node('p',report.status==='running'?'Итоговые записи о страницах ещё не сохранены.':report.physical_pages===0?'Документ не имеет физической нумерации страниц (например, обычный Markdown).':'Записей о страницах в этой части отчёта нет.','muted'));$('job-history-page-first').disabled=after===0;$('job-history-next').hidden=report.next<0;$('job-history-pages').scrollIntoView({block:'start'});
    }catch(e){error(c,e);}finally{c.busy=false;if(valid(c))$('job-history-next').disabled=false;}
  }
  async function load(c){if(!valid(c)||c.busy)return;c.busy=true;$('job-history-error').hidden=true;
    try{const attempts=await api(route(c)+'/history');if(!valid(c))return;const rows=$('job-history-attempts');rows.replaceChildren();for(const a of attempts){const row=node('section',undefined,'card');row.append(node('h3','Попытка '+a.number+' · '+(statuses[a.status]||'Статус неизвестен')),node('p','Начало: '+(a.started?new Date(a.started).toLocaleString('ru-RU'):'—')+(a.finished?' · Завершение: '+new Date(a.finished).toLocaleString('ru-RU'):''),'muted'));if(a.has_error)row.append(node('p','Во время попытки возникла ошибка.','notice'));if(a.has_report){const b=node('button','Страницы попытки '+a.number,'secondary');b.type='button';b.addEventListener('click',run(()=>pages(c,a.number,0)));row.append(b);}rows.append(row);}if(!attempts.length)rows.append(node('p','Обработка ещё не начиналась. Отмена задания в очереди не создаёт попытку.','muted'));
    }catch(e){error(c,e);}finally{c.busy=false;}
  }
  $('job-history-refresh').addEventListener('click',run(async()=>{const c=current;await load(c);if(valid(c)&&c.attempt)await pages(c,c.attempt,c.after);}));
  $('job-history-page-first').addEventListener('click',run(()=>pages(current,current.attempt,0)));$('job-history-next').addEventListener('click',run(()=>pages(current,current.attempt,current.next)));
  return function open(job){const c={workspace:state.selected,screen:state.revision,id:job.id,busy:false};current=c;$('job-history-title').textContent='Обработка: '+job.filename;$('job-history-attempts').replaceChildren();$('job-history-pages').hidden=true;$('job-history-error').hidden=true;dialog.showModal();run(()=>load(c))();};
}
