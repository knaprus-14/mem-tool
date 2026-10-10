const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm'),path=require('node:path');
const graph=fs.readFileSync(path.join(__dirname,'../../pkg/mem/web/knowledgeMapHTMLSuffix.html'),'utf8');
const helper=graph.slice(graph.indexOf('function annotationBacklinksAction('),graph.indexOf('function setInspectorTab('));
const source=fs.readFileSync(path.join(__dirname,'assets/app.js'),'utf8');
const boot=source.slice(source.indexOf('  async function openAnnotationLink(){'),source.indexOf('  async function loadWorkspaces('));
function link(workspace,id){const ctx={workspace,location:{href:'http://127.0.0.1:8872/graph/base/',origin:'http://127.0.0.1:8872'},URL,URLSearchParams,document:{createElement:tag=>({tag,children:[],append(...c){this.children.push(...c);}})}};vm.createContext(ctx);vm.runInContext(helper,ctx);return ctx.annotationBacklinksAction({id});}
test('graph annotation navigation is absent from portable pages and rejects external base URLs',()=>{
  for(const workspace of [null,{}, {application_url:'https://example.com/#workspace='+'a'.repeat(32)},{application_url:'/other#workspace='+'a'.repeat(32)},{application_url:'/#workspace=invalid'}])assert.equal(link(workspace,'n'),null);
});
test('graph link opens the correct workspace and exact node, without reusing an annotation link',()=>{
  const nodeID='manual-node-abc',w='a'.repeat(32),box=link({application_url:'/#workspace='+w+'&annotation=old'},nodeID),a=box.children[0];
  const u=new URL(a.href),p=new URLSearchParams(u.hash.slice(1));assert.equal(p.get('workspace'),w);assert.equal(p.get('knowledge_node'),nodeID);assert.equal(p.has('annotation'),false);assert.equal(a.target,'_blank');assert.match(a.rel,/noopener/);
});
async function open(hash,selected,authenticated=true,offline=false){const calls=[],ctx={location:{hash},state:{selected,session:{authenticated}},current:()=>({offline}),URLSearchParams,annotations:{show:id=>calls.push(['annotation',id])},annotationKnowledge:{showNode:input=>{calls.push(['knowledge',input.workspace,input.nodeID]);}}};vm.createContext(ctx);vm.runInContext(boot,ctx);await ctx.openAnnotationLink();return calls;}
test('annotation deep link is scoped to selected authorized workspace',async()=>{
  const id='ann-'+'b'.repeat(32),h='#workspace=alpha&annotation='+id;
  assert.deepEqual(await open(h,'alpha'),[['annotation',id]]);
  assert.deepEqual(await open(h,'beta'),[]);assert.deepEqual(await open(h,'alpha',false),[]);assert.deepEqual(await open(h,'alpha',true,true),[]);
  assert.deepEqual(await open('#workspace=alpha&annotation=../../private','alpha'),[]);
});
test('graph reverse deep link uses bounded node and retains workspace identity',async()=>{
  assert.deepEqual(await open('#workspace=alpha&knowledge_node=manual-node-1','alpha'),[['knowledge','alpha','manual-node-1']]);
  assert.deepEqual(await open('#workspace=alpha&knowledge_node='+encodeURIComponent('n'.repeat(257)),'alpha'),[]);
});
