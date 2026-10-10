const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),path=require('node:path');
const context=vm.createContext({});vm.runInContext(fs.readFileSync(path.join(__dirname,'knowledgeSourcePreview.js'),'utf8'),context);
test('formula previews include constraints and damaged text without guessing math',()=>{
 assert.equal(context.formulaLike({kind:'constraint',body:'расстояние по формуле: ± ± L= 2 [ 2 Rk'}),true);
 assert.equal(context.formulaLike({kind:'formula'}),true);
 assert.equal(context.formulaLike({kind:'claim',body:'L = √(R² - H²)'}),true);
 assert.equal(context.formulaLike({kind:'constraint',body:'Установить пожарный кран'}),false);
 assert.equal(context.pdfEvidence({anchor:{source_path:'SP10.PDF',page:15}}),true);
 assert.equal(context.pdfEvidence({anchor:{source_path:'SP10.PDF',page:0}}),false);
});
test('regions cannot cross page bounds or follow a different citation or revision',()=>{
 for(const crop of [[-1,0,1,1],[0,0,1.1,1],[0,0,0,1],[0,NaN,1,1],null])assert.equal(context.validSourceCrop(crop),false);
 assert.equal(context.validSourceCrop([.2,.8,.9,.94]),true);
 const a={document_id:'d',document_revision:'r1',page:15,citation_id:'c1',evidence_hash:'h1'};
 for(const change of [{document_revision:'r2'},{page:16},{citation_id:'c2'},{evidence_hash:'h2'}])assert.notEqual(context.sourceCropKey(a),context.sourceCropKey({...a,...change}));
});

test('PDF image, PDF link and text context carry the selected revision and evidence pins',()=>{
 const anchor={citation_id:'cite-example',document_revision:'sha256:revision',chunk_hash:'sha256:chunk',evidence_hash:'sha256:excerpt',source_path:'C:/private/source.pdf'};
 for(const format of ['', 'png', 'text']){
  const url=new URL(context.knowledgeSourceQuery(anchor,format),'http://localhost');
  assert.equal(url.searchParams.get('citation'),anchor.citation_id);
  assert.equal(url.searchParams.get('revision'),anchor.document_revision);
  assert.equal(url.searchParams.get('chunk_hash'),anchor.chunk_hash);
  assert.equal(url.searchParams.get('evidence_hash'),anchor.evidence_hash);
  assert.equal(url.searchParams.get('format')||'',format);
  assert.equal(url.searchParams.has('path'),false);
 }
 const html=fs.readFileSync(path.join(__dirname,'knowledgeMapHTMLSuffix.html'),'utf8');
 assert.match(html,/link\.href=mapURL\(knowledgeSourceQuery\(a\)\)/);
 assert.match(html,/fetch\(mapURL\(knowledgeSourceQuery\(a,'text'\)\)/);
 assert.match(fs.readFileSync(path.join(__dirname,'knowledgeSourcePreview.js'),'utf8'),/fetch\(mapURL\(knowledgeSourceQuery\(a,'png'\)\)/);
});
