// Artifact component checks only. The builder runs in a VM with captured writes;
// synthetic overlays never replace the actual report or declare real acceptance.
const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const file=path.join(__dirname,'build-coverage.cjs'),source=fs.readFileSync(file,'utf8');
function build(edits={}){
  const writes=new Map(),proxy={...fs,readFileSync(name,...args){const base=path.basename(name);if(base in edits){const value=typeof edits[base]==='string'?edits[base]:JSON.stringify(edits[base]);return args[0]?value:Buffer.from(value);}return fs.readFileSync(name,...args);},writeFileSync(name,data){writes.set(path.basename(name),data);}};
  vm.runInNewContext(source,{require:id=>id==='node:fs'?proxy:require(id),__dirname,process:{argv:['node',file]},console:{log(){}},Buffer},{filename:file});
  return {coverage:JSON.parse(writes.get('coverage.json')),html:writes.get('COVERAGE_RU.html'),writes};
}
const read=name=>JSON.parse(fs.readFileSync(path.join(__dirname,name),'utf8'));
const syntheticVerified=()=>({id:'test-only-verified',feature_id:'TRU09',release:'test-only',state:'verified',title:'Synthetic builder check',summary:'Not a release claim',status_override:'delivered',remaining:['Synthetic remaining'],evidence:['pkg/mem/document_annotation_reanchor.go:1'],checks:[{kind:'fixture',status:'passed',result:'Synthetic; not written to the actual report'}]});

test('120 original contracts survive normalization and deterministic rebuild',()=>{
  const a=build(),b=build();assert.equal(a.writes.get('coverage.json'),b.writes.get('coverage.json'));assert.equal(a.html,b.html);
  const original=JSON.parse(fs.readFileSync(path.resolve(__dirname,'../..','2026-10-09/mindmap-research/features.json'),'utf8'));
  assert.equal(a.coverage.features.length,120);assert.equal(new Set(a.coverage.features.map(f=>f.id)).size,120);
  for(const f of a.coverage.features){assert.deepEqual(f.original,original.find(s=>s.id===f.id));assert.equal(f.current.whole_feature_tested,false);}
  assert.equal(a.coverage.milestones.length,7);assert.equal(a.coverage.summary.accepted_milestones,0);
  assert.match(a.coverage.meta.verification.client_component_checks,/PASS/);assert(!a.html.includes('<script src='));assert(!a.html.includes('fetch('));
});

test('verified overlay changes only explicit status and remaining, preserving alpha3 baseline',()=>{
  const before=build().coverage,previous=before.features.find(f=>f.id==='TRU09');
  const overlay=read('coverage-deltas.json');overlay.entries.push(syntheticVerified());
  const {coverage}=build({'coverage-deltas.json':overlay}),f=coverage.features.find(f=>f.id==='TRU09');
  assert.deepEqual(f.audit_alpha3,previous.audit_alpha3);assert.equal(f.current.status,'delivered');assert.equal(f.current.status_basis,'test-only-verified');assert.equal(f.current.whole_feature_tested,false);assert.deepEqual(f.current.remaining,['Synthetic remaining']);
  const expected={...before.summary.current_statuses};if(previous.current.status!=='delivered'){expected[previous.current.status]--;expected.delivered=(expected.delivered||0)+1;if(!expected[previous.current.status])delete expected[previous.current.status];}assert.deepEqual(coverage.summary.current_statuses,expected);
});

test('empty overlay reproduces alpha3 and rejects unverified promotion',()=>{
  const clean={schema_version:1,updated:'2026-10-10',entries:[]};const {coverage}=build({'coverage-deltas.json':clean});assert.deepEqual(coverage.summary.current_statuses,coverage.summary.alpha3_statuses);assert.equal(coverage.release_deltas.length,0);
  const overlay=read('coverage-deltas.json');overlay.entries.push({...syntheticVerified(),state:'in_progress'});assert.throws(()=>build({'coverage-deltas.json':overlay}),/override requires verified/);
});

test('verified means supplied evidence and a passed scoped check',()=>{
  for(const field of ['evidence','checks']){const overlay=read('coverage-deltas.json'),entry=syntheticVerified();entry[field]=[];overlay.entries.push(entry);assert.throws(()=>build({'coverage-deltas.json':overlay}),/Verified requires evidence/);}
  const overlay=read('coverage-deltas.json');overlay.entries.push(syntheticVerified(),syntheticVerified());assert.throws(()=>build({'coverage-deltas.json':overlay}),/Duplicate\/missing delta ID/);
});

test('missing and duplicated audit mapping fail closed',()=>{
  const audit=read('audit-kno-tru-ext-ai.json');audit.features.pop();assert.throws(()=>build({'audit-kno-tru-ext-ai.json':audit}),/Expected40/);
  const duplicate=read('audit-kno-tru-ext-ai.json');duplicate.features[1]=duplicate.features[0];assert.throws(()=>build({'audit-kno-tru-ext-ai.json':duplicate}),/Duplicate audit ID/);
});

test('changed acceptance and broken evidence links are rejected',()=>{
  const audit=read('audit-kno-tru-ext-ai.json');audit.features[0].original_acceptance+=' changed';assert.throws(()=>build({'audit-kno-tru-ext-ai.json':audit}),/Acceptance differs/);
  for(const ref of ['pkg/mem/graph_job_retention.go:999999','missing-review-evidence.txt:1','../../outside.txt:1']){const overlay=read('coverage-deltas.json');overlay.entries.push({...syntheticVerified(),evidence:[ref]});assert.throws(()=>build({'coverage-deltas.json':overlay}));}
});
