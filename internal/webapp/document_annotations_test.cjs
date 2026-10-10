const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const settle=()=>new Promise(resolve=>setImmediate(resolve));
function fixture(options={}){
  const elements=[],events={},calls=[];let sequence=0;
  const textNode=text=>({nodeType:3,textContent:String(text),parentNode:null});
  function element(tag,text,css){
    const e={tag,nodeType:1,className:css||'',children:[],parentNode:null,events:{},attributes:{},dataset:{},hidden:false,disabled:false,checked:false,open:false,value:'',
      get childNodes(){return this.children;},get isConnected(){let n=this;while(n.parentNode)n=n.parentNode;return n.tag==='body';},get textContent(){return this.children.map(c=>c.textContent).join('');},set textContent(v){this.replaceChildren(textNode(v));},
      addEventListener(name,fn){(this.events[name]??=[]).push(fn);},
      removeEventListener(name,fn){this.events[name]=(this.events[name]||[]).filter(item=>item!==fn);},
      append(...children){for(const child of children){this.children.push(child);child.parentNode=this;if(tag==='select'&&!this.value)this.value=child.value;}},
      replaceChildren(...children){for(const child of this.children)child.parentNode=null;this.children=[];this.append(...children);},
      contains(other){for(let item=other;item;item=item.parentNode)if(item===this)return true;return false;},
      setAttribute(name,value){this.attributes[name]=value;},removeAttribute(name){delete this.attributes[name];delete this[name];},
      showModal(){this.open=true;},close(){this.open=false;for(const fn of this.events.close||[])fn();},scrollIntoView(){this.scrolled=true;}
    };if(text!==undefined)e.textContent=text;elements.push(e);return e;
  }
  const $=id=>elements.find(e=>e.id===id);
  const document={body:element('body'),addEventListener(name,fn){(events[name]??=[]).push(fn);}};
  const texts=root=>root.nodeType===3?[root]:root.children.flatMap(texts);
  const at=(root,offset)=>{const nodes=texts(root);for(const n of nodes){if(offset<=n.textContent.length)return [n,offset];offset-=n.textContent.length;}return [root,0];};
  const absolute=(container,offset)=>{let n=container,result=container.nodeType===3?offset:container.children.slice(0,offset).reduce((sum,c)=>sum+c.textContent.length,0);while(n.parentNode){const siblings=n.parentNode.children;result+=siblings.slice(0,siblings.indexOf(n)).reduce((sum,c)=>sum+c.textContent.length,0);n=n.parentNode;}return result;};
  document.createTreeWalker=root=>{const nodes=texts(root);let i=0;return {nextNode:()=>nodes[i++]||null};};
  document.createRange=()=>({startContainer:document.body,startOffset:0,endContainer:document.body,endOffset:0,
    setStart(n,offset){this.startContainer=n;this.startOffset=offset;},setEnd(n,offset){this.endContainer=n;this.endOffset=offset;},
    selectNodeContents(n){this.setStart(n,0);this.setEnd(n,n.children.length);},
    toString(){return document.body.textContent.slice(absolute(this.startContainer,this.startOffset),absolute(this.endContainer,this.endOffset));},
    cloneRange(){const r=document.createRange();r.setStart(this.startContainer,this.startOffset);r.setEnd(this.endContainer,this.endOffset);return r;}
  });
  const annotation={id:'ann-'+ 'a'.repeat(32),revision:3,document_id:'doc-library',document_revision:'rev-1',citation_id:'cite-1',source_name:'Source.pdf',page:4,block_index:0,block_chunk_index:0,quote:'Saved <quote>',start:1,end:14,prefix:'A',suffix:'Z',note:'Initial note',color:'yellow',archived:false,author:'alice',created:'2026-10-10',updated:'2026-10-10',state:'current'};
  const clone=x=>JSON.parse(JSON.stringify(x));
  const f={$,elements,document,calls,annotation:clone(annotation),items:[clone(annotation)],receipts:new Map(),selection:null,selected:[],centered:[],opened:[],draws:0,
    state:{selected:'ws',revision:1,view:'maps',session:{user:'alice'},map:{selectedNode:'n',view:{map:{id:'map',revision:2}},tree:{byID:new Map([['root',{id:'root',parent_id:''}],['n',{id:'n',parent_id:'root',label:'Node'}]])},collapsed:new Set(['root'])}},
    source:{title:'Source.pdf',page:4,excerpt:'Old quote',state:'stale',original_pdf:true,notice:'Источник изменился',passages:[]},
    links:{items:[{map_id:'linked-map',map_title:'Linked map',map_revision:7,node_id:'linked-node',node_label:'Linked node',source_id:'s1'}],total:1,limit:100}
  };
  f.respond=async(path,body)=>{
    if(body){
      if(f.receipts.has(body.request_id))return {...clone(f.receipts.get(body.request_id)),replayed:true};
      let result;
      if(path.endsWith('/nodes/n/annotations'))result={revision:3,node_id:'n',replayed:false};
      else {const a=clone(f.annotation);if(path.endsWith('/edit')){a.revision++;if(body.archived!==undefined)a.archived=body.archived;else {a.note=body.note;a.color=body.color;}}
        else {a.revision=1;Object.assign(a,{quote:body.quote,start:body.start,end:body.end,note:body.note,color:body.color});}f.annotation=a;f.items=[clone(a)];result={annotation:clone(a),replayed:false};}
      f.receipts.set(body.request_id,clone(result));return result;
    }
    if(path.includes('/history?'))return f.history||{items:[{id:1,revision:f.annotation.revision,action:'edit',actor:'alice',created:'2026-10-10',annotation:clone(f.annotation)}],total:1,offset:0,limit:30};
    if(path.endsWith('/source'))return clone(f.source);
    if(path.endsWith('/links'))return clone(f.links);
    if(path.includes('/annotations?')){const params=new URLSearchParams(path.split('?')[1]),offset=Number(params.get('offset')),limit=Number(params.get('limit')),archived=params.get('archived')==='true',items=f.items.filter(a=>a.archived===archived);return {items:clone(items.slice(offset,offset+limit)),total:items.length,offset,limit};}
    return clone(f.annotation);
  };
  const context=vm.createContext({document,window:{getSelection:()=>f.selection,confirm:()=>f.confirm!==false},AbortController,URLSearchParams,crypto:{randomUUID:()=>String(++sequence).padStart(32,'0')},MemMapLayout:{reveal:(tree,collapsed,id)=>{const next=new Set(collapsed);for(let parent=tree.byID.get(id)?.parent_id;parent;parent=tree.byID.get(parent)?.parent_id)next.delete(parent);return next;}}});
  vm.runInContext(fs.readFileSync(__dirname+'/assets/document-annotations.js','utf8'),context);
  f.module=context.installMemDocumentAnnotations({state:f.state,$,node:element,run:fn=>fn,regions:options.regions,transfer:options.transfer,reanchor:options.reanchor,api:async(path,body,retry,signal)=>{calls.push({path,body:body===undefined?undefined:clone(body),signal});const result=await (f.handle?f.handle(path,body,signal):f.respond(path,body));if(body&&f.failAfterSave){f.failAfterSave=false;throw Error('network response lost');}return result;},
    showMap:async id=>{f.opened.push(id);f.module.close();f.state.view='maps';f.state.revision++;f.state.map={view:{map:{id,revision:7}},selectedNode:'root',tree:{byID:new Map([['root',{id:'root',parent_id:''}],['linked-node',{id:'linked-node',parent_id:'root'}],['n',{id:'n',parent_id:'root'}]])},collapsed:new Set(['root'])};},
    drawMap:()=>f.draws++,selectMapNode:(id,open)=>{f.selected.push({id,open});f.state.map.selectedNode=id;},centerMapNode:id=>f.centered.push(id)});
  f.fire=async(target,event='click',fields={})=>{const e=typeof target==='string'?$(target):target;assert.ok(e,'missing element '+target);for(const fn of e.events[event]||[])await fn({preventDefault(){},stopPropagation(){},...fields});await settle();};
  f.button=(text,container=document.body)=>elements.findLast(e=>e.tag==='button'&&e.textContent===text&&container.contains(e));
  f.mount=(options={})=>{const host=element('div');document.body.append(host);const reader=f.module.mountReader({host,passages:[{entry_id:7,pin:'pinned',text:'A😀Б В конец',match:true}],documentID:'doc-library',...options});return {host,reader,pre:elements.findLast(e=>e.tag==='pre'&&host.contains(e)),create:f.button('Аннотировать выделение',host)};};
  f.select=(pre,start,end,other=pre)=>{
    let range;f.selection={rangeCount:1,isCollapsed:false,getRangeAt:()=>range,
      setBaseAndExtent(anchor,anchorOffset,focus,focusOffset){this.anchorNode=anchor;this.anchorOffset=anchorOffset;this.focusNode=focus;this.focusOffset=focusOffset;
        const a=absolute(anchor,anchorOffset),b=absolute(focus,focusOffset);this.isCollapsed=a===b;range=document.createRange();range.setStart(...(a<=b?[anchor,anchorOffset]:[focus,focusOffset]));range.setEnd(...(a<=b?[focus,focusOffset]:[anchor,anchorOffset]));}
    };f.selection.setBaseAndExtent(...at(pre,start),...at(other,end));
    for(const fn of events.selectionchange||[])fn();
  };
  f.marks=r=>elements.filter(e=>e.tag==='mark'&&r.host.contains(e));
  f.exactPassage={entry_id:7,pin:'pinned',text:'A😀Б В конец',citation_id:'cite-1',document_revision:'rev-1'};
  f.highlight=(id,start,end,color='yellow',extra={})=>({...f.annotation,id,start,end,color,quote:Array.from(f.exactPassage.text).slice(start,end).join(''),...extra});

  f.openDetail=async(targetNode)=>{await f.module.showList({documentID:'doc-library',targetNode});await f.fire(f.button('Открыть аннотацию'));};
  return f;
}

test('Unicode and reverse selection create an exact literal quote and retain draft across close',async()=>{
  const f=fixture(),r=f.mount();f.select(r.pre,4,1);assert.equal(r.create.disabled,false);await f.fire(r.create);
  assert.equal(f.$('annotation-quote').textContent,'😀Б');f.$('annotation-note').value='<script>comment</script>';await f.fire('annotation-note','input');f.module.close();await f.fire(r.create);
  assert.equal(f.$('annotation-note').value,'<script>comment</script>');await f.fire('annotation-form','submit');const request=f.calls.find(x=>x.body);
  assert.deepEqual(request.body,{note:'<script>comment</script>',color:'yellow',request_id:'0'.repeat(31)+'1',entry_id:7,pin:'pinned',start:1,end:3,quote:'😀Б'});
  assert.equal(f.$('annotation-quote').innerHTML,undefined);assert.equal(f.$('annotation-note').value,'<script>comment</script>');assert.equal(f.$('annotation-status').textContent,'Аннотация сохранена.');
});

test('selection across passages, outside the reader, or beyond limits never creates an annotation',async()=>{
  const f=fixture(),r=f.mount({passages:[{entry_id:7,pin:'p',text:'first'},{entry_id:8,pin:'p2',text:'second'}]});const pres=f.elements.filter(e=>e.tag==='pre'&&r.host.contains(e));
  f.select(pres[0],0,4,pres[1]);assert.equal(r.create.disabled,true);await f.fire(r.create);assert.equal(f.$('annotation-dialog').open,false);
  const big=f.mount({passages:[{entry_id:9,pin:'p',text:'😀'.repeat(8001)}]});f.select(big.pre,0,16002);assert.equal(big.create.disabled,true);
  assert.equal(f.calls.length,0);
});

test('uncertain create replays the same request and cannot publish after reader invalidation',async()=>{
  const f=fixture();let active=true;const r=f.mount({isCurrent:()=>active});f.select(r.pre,1,4);await f.fire(r.create);f.$('annotation-note').value='Keep draft';await f.fire('annotation-note','input');f.failAfterSave=true;
  await f.fire('annotation-form','submit');assert.equal(f.$('annotation-retry').hidden,false);assert.equal(f.$('annotation-note').disabled,true);f.module.close();await f.fire(r.create);
  assert.equal(f.$('annotation-note').value,'Keep draft');await f.fire('annotation-retry');const writes=f.calls.filter(x=>x.body);assert.deepEqual(writes[0].body,writes[1].body);assert.equal(f.receipts.size,1);
  f.$('annotation-note').value='Late';await f.fire('annotation-note','input');active=false;await f.fire('annotation-form','submit');assert.equal(f.calls.filter(x=>x.body).length,2);
});

test('list and history paginate; edit, archive and restore use distinct pinned mutations',async()=>{
  const f=fixture();f.items=Array.from({length:31},(_,i)=>({...f.annotation,id:'ann-'+i}));await f.module.showList({documentID:'doc-library'});await f.fire('annotation-list-next');assert.match(f.calls.at(-1).path,/offset=30/);
  await f.fire(f.button('Открыть аннотацию'));f.$('annotation-note').value='Edited note';await f.fire('annotation-note','input');f.$('annotation-color').value='blue';await f.fire('annotation-color','change');await f.fire('annotation-form','submit');
  let mutation=f.calls.filter(x=>x.body).at(-1);assert.equal(mutation.body.revision,3);assert.equal(mutation.body.color,'blue');assert.equal(mutation.body.note,'Edited note');
  f.history={items:[{id:4,revision:4,action:'edit',actor:'alice',created:'today',annotation:f.annotation}],total:31,offset:0,limit:30};await f.fire('annotation-history');assert.ok(f.$('annotation-extras').textContent.includes('Edited note'));await f.fire(f.button('Следующие изменения'));assert.match(f.calls.at(-1).path,/history\?offset=30&limit=30/);
  await f.fire('annotation-archive');mutation=f.calls.filter(x=>x.body).at(-1);assert.deepEqual(Object.keys(mutation.body).sort(),['archived','request_id','revision']);assert.equal(mutation.body.archived,true);assert.equal(mutation.body.revision,4);assert.equal(f.$('annotation-save').disabled,true);
  await f.fire('annotation-archive');assert.equal(f.calls.filter(x=>x.body).at(-1).body.archived,false);assert.equal(f.$('annotation-save').disabled,false);
});

test('stale source shows preserved quote and never opens a misleading current PDF',async()=>{
  const f=fixture();await f.openDetail();await f.fire('annotation-source');const extra=f.$('annotation-extras');assert.match(extra.textContent,/Old quote/);assert.match(extra.textContent,/Источник изменился/);assert.equal(f.elements.some(e=>e.tag==='a'&&extra.contains(e)),false);
});

test('late list/source reads are aborted and ignored on close and source switch',async()=>{
  const f=fixture();await f.openDetail();let resolve;f.handle=()=>new Promise(r=>resolve=r);const pending=f.fire('annotation-source');await settle();const call=f.calls.at(-1);f.module.close();assert.equal(call.signal.aborted,true);resolve(f.source);await pending;assert.equal(f.$('annotation-extras').children.length,0);
  let resolveList;f.handle=()=>new Promise(r=>resolveList=r);const list=f.module.showList({documentID:'doc-library'});f.state.selected='other';resolveList({items:[f.annotation],total:1,offset:0,limit:30});await list;assert.equal(f.$('annotation-list-rows').children.length,0);
});

test('selected-node attach pins map and annotation revisions; locked or changed target cannot attach',async()=>{
  const f=fixture(),target={mapID:'map',nodeID:'n',revision:2,label:'Node',locked:false};await f.openDetail(target);await f.fire('annotation-attach');const call=f.calls.find(x=>x.body);assert.match(call.path,/maps\/map\/nodes\/n\/annotations$/);assert.equal(call.body.revision,2);assert.equal(call.body.annotation_revision,3);assert.equal(f.$('annotation-refresh-map').hidden,false);assert.equal(f.opened.length,0);
  const locked=fixture();await locked.openDetail({...target,locked:true});await locked.fire('annotation-attach');assert.equal(locked.calls.filter(x=>x.body).length,0);
  const changed=fixture();await changed.openDetail(target);changed.state.map.selectedNode='other';await changed.fire('annotation-attach');assert.equal(changed.calls.filter(x=>x.body).length,0);
});

test('linked-node navigation survives legitimate screen revision change and reveals the node',async()=>{
  const f=fixture();await f.openDetail();await f.fire('annotation-links');await f.fire(f.button('Linked map / Linked node'));assert.deepEqual(f.opened,['linked-map']);assert.deepEqual(f.selected,[{id:'linked-node',open:true}]);assert.deepEqual(f.centered,['linked-node']);assert.equal(f.state.map.collapsed.has('root'),false);assert.equal(f.$('annotation-dialog').open,false);
});

test('current annotation source can create a second annotation without invalidating its own reader context',async()=>{
  const f=fixture();f.source={...f.source,state:'current',original_pdf:true,passages:[{entry_id:7,pin:'p',text:'A😀Б В конец'}]};await f.openDetail();await f.fire('annotation-source');const pre=f.elements.findLast(e=>e.tag==='pre'&&f.$('annotation-extras').contains(e));f.select(pre,1,4);await f.fire(f.button('Аннотировать выделение',f.$('annotation-extras')));
  assert.equal(f.$('annotation-quote').textContent,'😀Б');await f.fire('annotation-form','submit');assert.equal(f.calls.filter(c=>c.body).length,1);
});

test('whole-fragment action works from the keyboard and enforces rune length without selection',async()=>{
  const f=fixture(),r=f.mount();await f.fire(f.button('Аннотировать весь фрагмент',r.host));assert.equal(f.$('annotation-quote').textContent,'A😀Б В конец');await f.fire('annotation-form','submit');const request=f.calls.find(c=>c.body);assert.equal(request.body.start,0);assert.equal(request.body.end,Array.from('A😀Б В конец').length);assert.equal(request.body.quote,'A😀Б В конец');
  const large=f.mount({passages:[{entry_id:9,pin:'p',text:'😀'.repeat(8001)}]});assert.equal(f.button('Аннотировать весь фрагмент',large.host),undefined);
});

test('archive refuses to lose an edited note; reopened detail retains its original revision pin',async()=>{
  const f=fixture();await f.openDetail();f.$('annotation-note').value='Unsaved';await f.fire('annotation-note','input');await f.fire('annotation-archive');assert.equal(f.calls.filter(c=>c.body).length,0);assert.match(f.$('annotation-error').textContent,/Черновик не потерян/);
  f.module.close();f.annotation.revision=8;await f.openDetail();assert.equal(f.$('annotation-note').value,'Unsaved');assert.match(f.$('annotation-status').textContent,/прежней ревизии/);await f.fire('annotation-form','submit');assert.equal(f.calls.find(c=>c.body).body.revision,3);
});

test('remount releases old host listeners and reset freezes editing until the pinned read completes',async()=>{
  const f=fixture(),first=f.mount(),second=f.module.mountReader({host:first.host,passages:[{entry_id:8,pin:'new',text:'New text'}],documentID:'doc-library'});assert.equal(first.reader.valid(),false);assert.equal(first.host.events.keyup.length,1);assert.equal(first.host.events.mouseup.length,1);second.close();assert.equal(first.host.events.keyup.length,0);assert.equal(first.host.events.mouseup.length,0);
  await f.openDetail();f.$('annotation-note').value='Draft';await f.fire('annotation-note','input');let resolve;f.handle=()=>new Promise(r=>resolve=r);const reset=f.fire('annotation-reset');await settle();assert.equal(f.$('annotation-note').disabled,true);assert.equal(f.$('annotation-save').disabled,true);resolve(f.annotation);await reset;assert.equal(f.$('annotation-note').value,'Initial note');assert.equal(f.$('annotation-note').disabled,false);
});

test('persistent highlights require exact document, citation, revision, current state and rune quote',async()=>{
  const f=fixture();f.items=[f.highlight('ok',1,3,'green'),f.highlight('stale',4,5,'pink',{state:'stale'}),f.highlight('missing',4,5,'pink',{state:'missing'}),
    f.highlight('archived',4,5,'blue',{archived:true}),f.highlight('revision',4,5,'blue',{document_revision:'rev-old'}),f.highlight('citation',4,5,'blue',{citation_id:'another-chunk'}),
    f.highlight('document',4,5,'blue',{document_id:'same-name-other-document'}),f.highlight('quote',4,5,'blue',{quote:'wrong'}),f.highlight('bounds',4,99),
    f.highlight('negative',-1,2),f.highlight('fraction',1.5,3),f.highlight('color',4,5,'untrusted')];
  const r=f.mount({passages:[f.exactPassage]});await settle();const marks=f.marks(r);assert.equal(marks.length,1);assert.equal(marks[0].textContent,'😀Б');assert.equal(marks[0].className,'annotation-highlight annotation-colors-green');assert.equal(r.pre.textContent,f.exactPassage.text);
  assert.match(r.reader.highlightStatus.textContent,/Подсвечено: 1/);assert.match(f.calls[0].path,/document_id=doc-library&archived=false&offset=0&limit=50/);
  const unversioned=f.mount();await settle();assert.equal(unversioned.reader.toggle.disabled,true);assert.match(unversioned.reader.highlightStatus.textContent,/нет точной версии/);assert.equal(f.calls.length,1);
});

test('overlapping marks preserve one text stream and expose every annotation and backlink by keyboard',async()=>{
  const f=fixture();f.items=[f.highlight('left',1,5,'yellow',{note:'Left'}),f.highlight('right',2,7,'blue',{note:'Right'}),f.highlight('inner',2,3,'pink',{note:'Inner'})];
  f.handle=(path,body)=>{const a=f.items.find(a=>path.endsWith('/'+a.id));return a?Promise.resolve(a):f.respond(path,body);};
  const r=f.mount({passages:[f.exactPassage]});await settle();assert.equal(r.pre.textContent,f.exactPassage.text);const marks=f.marks(r);assert.deepEqual(marks.map(m=>m.textContent),['😀','Б',' В',' к']);
  const overlap=marks[1];assert.equal(overlap.tabIndex,0);assert.equal(overlap.attributes.role,'button');assert.equal(overlap.attributes['aria-haspopup'],'dialog');assert.match(overlap.attributes['aria-label'],/Аннотаций: 3/);assert.match(overlap.className,/annotation-colors-yellow-blue-pink/);
  await f.fire(overlap,'keydown',{key:'Enter'});assert.equal(f.$('annotation-dialog').open,true);assert.match(f.$('annotation-status').textContent,/3 аннотаций/);
  for(const name of ['Left','Right','Inner'])assert.match(f.$('annotation-list-rows').textContent,new RegExp(name));
  await f.fire(f.button('Открыть: Синий · Right'));assert.equal(f.$('annotation-note').value,'Right');assert.match(f.$('annotation-extras').textContent,/Linked map \/ Linked node/);assert.match(f.calls.at(-1).path,/right\/links$/);
  f.module.close();await f.fire(marks[0],'keydown',{key:' '});assert.equal(f.$('annotation-note').value,'Left');assert.match(f.calls.at(-1).path,/left\/links$/);
});

test('selection inside overlapping markup survives late rendering and toggle without changing rune offsets',async()=>{
  const f=fixture();f.items=[f.highlight('left',1,5),f.highlight('right',2,7,'green')];let resolve;
  f.handle=()=>new Promise(r=>resolve=r);const r=f.mount({passages:[f.exactPassage]});f.select(r.pre,6,1);assert.equal(r.reader.selection.quote,'😀Б В');
  resolve({items:f.items,total:2,offset:0,limit:50});await settle();assert.equal(r.pre.textContent,f.exactPassage.text);assert.equal(f.selection.getRangeAt(0).toString(),'😀Б В');assert.equal(r.reader.selection.start,1);assert.equal(r.reader.selection.end,5);
  assert.equal(f.selection.anchorNode.textContent.includes('В'),true);assert.equal(f.selection.anchorOffset,f.selection.anchorNode.textContent.length);
  const mark=f.marks(r)[1];await f.fire(mark);assert.equal(f.$('annotation-dialog').open,false,'dragging selected text must not open the mark');
  await f.fire(r.reader.toggle);assert.equal(f.marks(r).length,0);assert.equal(r.pre.textContent,f.exactPassage.text);assert.equal(f.selection.getRangeAt(0).toString(),'😀Б В');assert.equal(r.reader.toggle.attributes['aria-pressed'],'false');
  await f.fire(r.reader.toggle);assert.ok(f.marks(r).length>0);assert.equal(f.selection.getRangeAt(0).toString(),'😀Б В');await f.fire(r.create);assert.equal(f.$('annotation-quote').textContent,'😀Б В');
  f.handle=(path,body)=>f.respond(path,body);await f.fire('annotation-form','submit');const request=f.calls.find(c=>c.body);assert.equal(request.body.start,1);assert.equal(request.body.end,5);assert.equal(request.body.quote,'😀Б В');
});

test('highlight pages are explicit, deduplicated and bounded to 500 with an honest remaining count',async()=>{
  const f=fixture();f.items=Array.from({length:501},(_,i)=>f.highlight('ann-'+i,1,3,i%2?'yellow':'blue'));
  const r=f.mount({passages:[f.exactPassage]});await settle();assert.equal(f.calls.length,1);assert.equal(r.reader.offset,50);assert.match(r.reader.highlightStatus.textContent,/50 из 501/);assert.equal(r.reader.more.hidden,false);
  for(let i=1;i<10;i++)await f.fire(r.reader.more);
  assert.equal(f.calls.length,10);assert.equal(r.reader.annotations.size,500);assert.equal(r.reader.offset,500);assert.equal(r.reader.more.hidden,true);assert.match(r.reader.highlightStatus.textContent,/Лимит просмотра — 500/);assert.equal(r.pre.textContent,f.exactPassage.text);
  await f.fire(r.reader.more);assert.equal(f.calls.length,10);assert.equal(f.marks(r).length,1);assert.match(f.marks(r)[0].attributes['aria-label'],/Аннотаций: 500/);
  await f.fire(r.reader.refresh);assert.equal(f.calls.length,11);assert.equal(r.reader.offset,50);assert.equal(r.reader.annotations.size,50);assert.equal(r.reader.more.hidden,false);
  f.handle=()=>Promise.resolve({items:[f.items[0]],total:51,offset:50,limit:50});await f.fire(r.reader.more);assert.equal(r.reader.annotations.size,50);assert.equal(r.reader.offset,51);assert.equal(r.reader.more.hidden,true);
});

test('closing and remounting a reader aborts highlight fetches and ignores stale workspace responses',async()=>{
  const f=fixture();let resolve;f.handle=()=>new Promise(r=>resolve=r);const r=f.mount({passages:[f.exactPassage]}),call=f.calls[0];
  const next=f.module.mountReader({host:r.host,passages:[{entry_id:8,pin:'p2',text:'Unchanged replacement'}],documentID:'different'});assert.equal(call.signal.aborted,true);assert.equal(r.host.events.keyup.length,1);assert.equal(r.host.events.mouseup.length,1);
  resolve({items:[f.highlight('late',1,3)],total:1,offset:0,limit:50});await settle();assert.equal(f.marks(r).length,0);assert.match(r.host.textContent,/Unchanged replacement/);assert.equal(r.reader.valid(),false);
  next.close();assert.equal(r.host.events.keyup.length,0);assert.equal(r.host.events.mouseup.length,0);
  const other=f.mount({passages:[f.exactPassage]});f.state.selected='another';resolve({items:[f.highlight('late',1,3)],total:1,offset:0,limit:50});await settle();assert.equal(f.marks(other).length,0);other.reader.close();assert.equal(f.calls.at(-1).signal.aborted,true);
});

test('saved color, archive and restore refresh matching marks while retaining the text',async()=>{
  const f=fixture();f.annotation=f.highlight('saved',1,3);f.items=[f.annotation];const r=f.mount({passages:[f.exactPassage]});await settle();await f.fire(f.marks(r)[0]);assert.match(f.calls.at(-1).path,/saved\/links$/);
  f.$('annotation-color').value='pink';await f.fire('annotation-color','change');await f.fire('annotation-form','submit');assert.match(f.marks(r)[0].className,/annotation-colors-pink/);assert.equal(r.reader.dirty,true);
  await f.fire('annotation-archive');assert.equal(f.marks(r).length,0);assert.equal(r.pre.textContent,f.exactPassage.text);
  await f.fire('annotation-archive');assert.equal(f.marks(r).length,1);assert.match(f.marks(r)[0].className,/annotation-colors-pink/);await f.fire(r.reader.refresh);assert.equal(r.reader.dirty,false);
});

test('failed or invalid highlight pages preserve loaded marks and offer a bounded retry',async()=>{
  const f=fixture();f.items=Array.from({length:51},(_,i)=>f.highlight('a'+i,1,3));const r=f.mount({passages:[f.exactPassage]});await settle();
  f.handle=()=>Promise.reject(Error('offline'));await f.fire(r.reader.more);assert.equal(r.reader.offset,50);assert.equal(r.reader.more.disabled,false);assert.match(r.reader.highlightStatus.textContent,/не полностью: offline/);assert.equal(f.marks(r).length,1);
  f.handle=()=>Promise.resolve({items:f.items,total:101,offset:50,limit:50});await f.fire(r.reader.more);assert.equal(r.reader.offset,50);assert.equal(r.reader.annotations.size,50);assert.match(r.reader.highlightStatus.textContent,/Не удалось прочитать страницу/);
  f.handle=()=>Promise.resolve({items:[],total:51,offset:50,limit:50});await f.fire(r.reader.more);assert.equal(r.reader.more.hidden,true);assert.match(r.reader.highlightStatus.textContent,/Список изменился/);
  f.handle=(path,body)=>f.respond(path,body);await f.fire(r.reader.refresh);assert.equal(r.reader.offset,50);assert.equal(r.reader.more.hidden,false);assert.equal(r.pre.textContent,f.exactPassage.text);
});

test('PDF region extension is optional, supports stale source review and disposes on section switch and close',async()=>{
  const mounts=[];let closed=0;const f=fixture({regions:{mount:options=>{mounts.push(options);return {close:()=>closed++};}}});
  f.annotation.state='stale';await f.openDetail();assert.equal(f.$('annotation-regions').hidden,false);await f.fire('annotation-regions');assert.equal(mounts.length,1);assert.equal(mounts[0].isCurrent(),true);assert.equal(mounts[0].workspace,'ws');assert.equal(mounts[0].annotation.state,'stale');
  await f.fire('annotation-history');assert.equal(closed,1);assert.equal(mounts[0].isCurrent(),false);await f.fire('annotation-regions');f.module.close();assert.equal(closed,2);assert.equal(mounts[1].isCurrent(),false);
  await f.openDetail();await f.fire('annotation-regions');await f.fire('annotation-back');assert.equal(closed,3);assert.equal(mounts[2].isCurrent(),false);
  f.annotation.page=0;await f.fire(f.button('Открыть аннотацию'));assert.equal(f.$('annotation-regions').hidden,true);await f.fire('annotation-regions');assert.equal(mounts.length,3);
  const plain=fixture();await plain.openDetail();assert.equal(plain.$('annotation-regions').hidden,true);await plain.fire('annotation-regions');assert.equal(plain.$('annotation-extras').children.length,0);
});

test('opening a mark rechecks source state and removes a newly stale highlight',async()=>{
  const f=fixture();f.annotation=f.highlight('saved',1,3);f.items=[{...f.annotation}];const r=f.mount({passages:[f.exactPassage]});await settle();assert.equal(f.marks(r).length,1);
  f.annotation.state='stale';await f.fire(f.marks(r)[0]);assert.equal(f.marks(r).length,0);assert.match(f.$('annotation-location').textContent,/Источник изменился/);assert.match(f.calls.at(-1).path,/saved\/links$/);assert.equal(r.pre.textContent,f.exactPassage.text);
});

test('inflight page cannot overwrite a saved color and shrinking lists are reported honestly',async()=>{
  const f=fixture();f.annotation=f.highlight('saved',1,3);f.items=Array.from({length:51},(_,i)=>({...f.annotation,id:i?'other-'+i:'saved'}));const r=f.mount({passages:[f.exactPassage]});await settle();
  await f.openDetail();let resolve;f.handle=(path,body)=>path.includes('offset=50')?new Promise(r=>resolve=r):f.respond(path,body);
  const pending=f.fire(r.reader.more);await settle();const read=f.calls.at(-1);f.$('annotation-color').value='green';await f.fire('annotation-color','change');await f.fire('annotation-form','submit');assert.equal(read.signal.aborted,true);assert.equal(r.reader.annotations.get('saved').color,'green');
  resolve({items:[{...f.annotation,color:'yellow'}],total:51,offset:50,limit:50});await pending;assert.equal(r.reader.annotations.get('saved').color,'green');assert.equal(r.reader.offset,50);assert.equal(r.reader.more.disabled,true);
  f.handle=(path,body)=>f.respond(path,body);f.items=Array.from({length:51},(_,i)=>f.highlight('other-'+i,1,3));await f.fire(r.reader.refresh);
  f.handle=()=>Promise.resolve({items:[],total:20,offset:50,limit:50});await f.fire(r.reader.more);assert.match(r.reader.highlightStatus.textContent,/Получено ранее: 50. Сейчас в списке: 20/);assert.match(r.reader.highlightStatus.textContent,/Список изменился/);assert.equal(r.reader.more.hidden,true);
});

test('transfer hooks export only the opened annotation and refresh the list after import',async()=>{
  let exported,imported;const f=fixture({transfer:{showExport:options=>exported=options,showImport:options=>imported=options}});
  f.items=[f.annotation,{...f.annotation,id:'second'}];await f.openDetail();await f.fire(f.button('Экспорт аннотации'));assert.equal(exported.workspace,'ws');assert.deepEqual(Array.from(exported.ids),[f.annotation.id]);
  await f.fire('annotation-back');await f.fire(f.button('Импорт аннотаций'));assert.equal(imported.workspace,'ws');const before=f.calls.length;await imported.onImported();assert.equal(f.calls.length,before+1);assert.match(f.calls.at(-1).path,/annotations\?/);f.module.close();await imported.onImported();assert.equal(f.calls.length,before+1);
});

test('reanchor hooks retain annotation and selection drafts and open the returned annotation explicitly',async()=>{
  let started,chosen,history,target;const reanchor={active:()=>target,start:options=>{started=options;target=options.annotation;},cancel:()=>target=null,preview:options=>chosen=options,showHistory:options=>history=options};
  const f=fixture({reanchor});await f.openDetail();f.$('annotation-note').value='Unsaved old note';await f.fire('annotation-note','input');await f.fire(f.button('Выбрать новый источник'));assert.equal(started.annotation.id,f.annotation.id);assert.equal(started.workspace,'ws');assert.equal(f.$('annotation-note').value,'Unsaved old note');
  await f.fire(f.button('История перепривязок'));assert.equal(history.id,f.annotation.id);f.module.close();const r=f.mount();f.select(r.pre,1,4);await f.fire(r.create);f.$('annotation-note').value='Selection draft';await f.fire('annotation-note','input');await f.fire(f.button('Перепривязать прежнюю цитату'));assert.equal(chosen.selection.quote,'😀Б');assert.equal(chosen.selection.start,1);assert.equal(chosen.selection.end,3);assert.equal(f.$('annotation-note').value,'Selection draft');
  const fresh={...f.annotation,id:'ann-'+'b'.repeat(32),revision:1,quote:'😀Б',note:'Saved inherited note'};f.annotation=fresh;await chosen.onApplied({annotation:fresh});assert.equal(f.$('annotation-note').value,'Saved inherited note');assert.match(f.calls.at(-1).path,new RegExp(fresh.id+'$'));
  f.module.close();await f.fire(r.create);assert.equal(f.$('annotation-note').value,'Selection draft');await f.fire(f.button('Отменить выбор прежней цитаты'));assert.equal(target,null);assert.equal(f.button('Перепривязать прежнюю цитату').hidden,true);
});

test('candidate-search completion opens the new annotation while preserving the old note draft and ignoring closed callbacks',async()=>{
  let started;const f=fixture({reanchor:{active:()=>null,start:options=>started=options}});await f.openDetail();const original={...f.annotation};f.$('annotation-note').value='Unsaved note before exact search';await f.fire('annotation-note','input');await f.fire(f.button('Выбрать новый источник'));assert.equal(typeof started.onApplied,'function');
  const fresh={...original,id:'ann-'+'c'.repeat(32),revision:1,note:'Saved note at the new source'};f.annotation=fresh;await started.onApplied({annotation:fresh});assert.equal(f.$('annotation-note').value,fresh.note);assert.match(f.calls.at(-1).path,new RegExp(fresh.id+'$'));
  f.module.close();const before=f.calls.length;await started.onApplied({annotation:fresh});assert.equal(f.calls.length,before);f.annotation=original;await f.openDetail();assert.equal(f.$('annotation-note').value,'Unsaved note before exact search');
});
