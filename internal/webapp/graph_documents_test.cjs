const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
function fixture(){
 const elements=new Map(),calls=[],timers=[];
 class Element {
  constructor(tag,text){this.tag=tag;this.textContent=text||'';this.children=[];this.events={};this.dataset={};this.value='';this.hidden=false;}
  set id(id){this._id=id;elements.set(id,this);}get id(){return this._id;}
  append(...els){for(const el of els){el.parentElement=this;this.children.push(el);}}
  replaceChildren(...els){this.children=[];this.append(...els);}
  addEventListener(name,fn){this.events[name]=fn;}
  setAttribute(){}
  querySelectorAll(tag){return this.children.flatMap(c=>[...(c.tag===tag?[c]:[]),...c.querySelectorAll(tag)]);}
  focus(){}
 }
 const main=new Element('main'),state={selected:'ws',revision:1,view:'graph'},node=(tag,text)=>new Element(tag,text);
 const f={state,calls,documents:[{document_id:'doc-a',name:'Manual.pdf',chunks:3}],jobs:[],operations:[],posts:0,uploads:0,answerModel:"test-chat"};
 const context=vm.createContext({document:{querySelector:()=>main,activeElement:null},setTimeout:fn=>{timers.push(fn);return timers.length;},clearTimeout(){},crypto:{randomUUID:()=> 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa'}});
 vm.runInContext(fs.readFileSync(__dirname+'/assets/graph-documents.js','utf8'),context);
 f.ui=context.installMemGraphDocuments({state,$:id=>elements.get(id),node,run:fn=>fn,showUpload:()=>f.uploads++,loadView:async()=>{},api:async(path,body)=>{
  calls.push({path,body});if(path.endsWith('/settings'))return {revision:'config-1',answer_model:f.answerModel};
  if(path.includes('/library?')){if(f.libraryResponse)return f.libraryResponse;return {documents:f.documents,total:f.documents.length};}
  if(path.endsWith('/jobs'))return f.jobs;
  if(path.endsWith('/coverage'))return {summary:{chunks_with_text:3,processed_chunks:0,covered_chunks:0,extracted_nodes:0,extracted_relations:0,failed_physical_pages:0}};
  if(path.endsWith('/maintenance')&&body){f.posts++;if(f.postError)throw Error('Connection lost');return {};}
  if(path.endsWith('/maintenance'))return f.operations;
  return {};
 }});
 f.el=id=>elements.get(id);f.poll=async()=>{await timers.at(-1)();};
 f.submit=async()=>{f.el('graph-start').parentElement.events.submit({preventDefault(){}});await new Promise(setImmediate);};
 f.click=async id=>{await f.el(id).events.click();};return f;
}
test('selected document submits durable full extraction with explicit source and pinned settings',async()=>{
 const f=fixture();await f.ui.load(1,{id:'ws'});assert.equal(f.el('graph-document').value,'doc-a');await f.submit();
 const body=f.calls.find(c=>c.body?.kind==='graph-extract').body;
 assert.equal(body.document,'doc-a');assert.equal(body.all_chunks,true);assert.equal(body.batches,16);assert.equal(body.config_revision,'config-1');
 await f.click('graph-upload');assert.equal(f.uploads,1);
});
test('no document cannot submit and importing a file refreshes the selection',async()=>{
 const f=fixture();f.documents=[];f.jobs=[{id:'upload',filename:'Manual.pdf',status:'running'}];await f.ui.load(1,{id:'ws'});
 assert.equal(f.el('graph-start').disabled,true);await f.submit();assert.equal(f.posts,0);
 f.documents=[{document_id:'uploaded',name:'Manual.pdf',chunks:3}];f.jobs[0].status='succeeded';await f.poll();
 assert.equal(f.el('graph-document').value,'uploaded');assert.equal(f.el('graph-start').disabled,false);
});
test('lost submission response retries the same request after background import refresh',async()=>{
 const f=fixture();f.postError=true;await f.ui.load(1,{id:'ws'});await f.submit();
 f.jobs=[{id:'another',status:'running',filename:'Another.pdf'}];await f.poll();f.postError=false;await f.submit();
 const submitted=f.calls.filter(c=>c.body?.kind==='graph-extract');assert.equal(submitted.length,2);assert.deepEqual(submitted[0].body,submitted[1].body);
});
test('late library response cannot select a source in another workspace',async()=>{
 const f=fixture();let resolve;f.libraryResponse=new Promise(r=>resolve=r);const loading=f.ui.load(1,{id:'ws'});
 await new Promise(setImmediate);f.state.selected='other';f.state.revision++;f.ui.hide();resolve({documents:f.documents,total:1});await loading;
 assert.equal(f.el('graph-documents').hidden,true);assert.equal(f.state.graphDocument,undefined);
 assert.equal(f.calls.some(c=>c.path.endsWith('/coverage')),false);
});
test('cancel and resume call the persisted operation, never start a replacement',async()=>{
 const f=fixture();f.operations=[{id:'op-a',kind:'graph-extract',document:'doc-a',all_chunks:true,status:'cancelled',current:1,total:3}];await f.ui.load(1,{id:'ws'});
 const b=f.el('graph-tasks').querySelectorAll('button').find(b=>b.dataset.graphAction==='op-a-retry');await b.events.click();
 assert.ok(f.calls.some(c=>c.path==='workspaces/ws/maintenance/op-a/retry'));assert.equal(f.posts,0);
});

test('missing or embedding-only model permits source graph and links settings',async()=>{
 for(const model of ['', ' bge-m3:latest ']){
  const f=fixture();f.answerModel=model;await f.ui.load(1,{id:'ws'});
  assert.equal(f.el('graph-start').disabled,false);await f.submit();assert.equal(f.posts,1);
  assert.match(f.el('graph-model-notice').textContent,/Без модели будет сохранена структура/);
  await f.click('graph-configure');assert.equal(f.state.view,'settings');
 }
});
