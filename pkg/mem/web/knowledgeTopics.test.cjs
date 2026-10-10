const test = require('node:test');
const assert = require('node:assert/strict');
const { build, positions } = require('./knowledgeTopics.js');
const { names, project } = require('./knowledgeTopics.js');
const node = (id, kind = 'claim', label = id, origin = 'generated') => ({ id, kind, label, origin, status: 'draft' });
const edge = (from, to, kind = 'about') => ({ id: from + to + kind, from, to, kind, status: 'draft' });

test('a common source cannot swallow independent semantic topics', () => {
  const nodes = [node('pdf', 'document', 'Manual', 'source'), node('page', 'section', 'Page', 'source'), node('a', 'topic', 'Pumps'), node('b', 'topic', 'Pipes'), node('c'), node('d'), node('unlinked')];
  const edges = [edge('pdf', 'page', 'contains'), edge('c', 'a'), edge('d', 'b'), ...nodes.slice(2).map(n => edge(n.id, 'pdf', 'derived_from'))];
  const before = JSON.stringify({ nodes, edges }), result = build(nodes, edges);
  assert.equal(result.owner.get('c').id, 'a'); assert.equal(result.owner.get('d').id, 'b');
  assert.equal(result.owner.get('unlinked').category, 'unlinked');
  assert.deepEqual(result.owner.get('pdf').members.map(n => n.id), ['pdf', 'page']);
  assert.equal(result.semanticEdges, 2); assert.equal(result.sourceEdges, 6);
  assert.equal(JSON.stringify({ nodes, edges }), before);
});

test('identical topic labels share a presentation group but conflicting claims stay intact', () => {
  const nodes = [node('a', 'topic', 'Насосы'), node('b', 'topic', ' НАСОСЫ '), { ...node('c', 'constraint'), body: 'не менее 3', evidence: [{ citation_id: 'x' }] }, { ...node('d', 'constraint'), body: 'не более 2', evidence: [{ citation_id: 'y' }] }];
  const result = build(nodes, [edge('c', 'a'), edge('d', 'b'), edge('c', 'd', 'contradicts')]);
  assert.equal(result.groups.length, 1); assert.equal(result.groups[0].members.length, 4);
  assert.equal(result.groups[0].members.find(n => n.id === 'c'), nodes[2]);
  assert.equal(result.groups[0].members.find(n => n.id === 'd'), nodes[3]);
});

test('links between topics keep real edge identities, without inferring new relations', () => {
  const nodes = [node('a', 'topic'), node('b', 'topic'), node('c'), node('d')];
  const edges = [edge('c', 'a'), edge('d', 'b'), edge('c', 'd', 'depends_on'), { ...edge('a', 'b', 'causes'), status: 'rejected' }, edge('missing', 'a')];
  const result = build(nodes, edges);
  assert.equal(result.links.length, 1); assert.deepEqual(result.links[0].edges, [edges[2]]);
  assert.deepEqual(build([...nodes].reverse(), [...edges].reverse()).groups.map(g => [g.id, g.members.map(n => n.id).sort()]), result.groups.map(g => [g.id, g.members.map(n => n.id).sort()]));
});

test('all source-only, disconnected, and empty graphs remain navigable', () => {
  for (const nodes of [[], [node('a')], [node('a', 'section', 'text', 'source'), node('b', 'document')], [node('a'), node('b')]]) {
    const result = build(nodes, []), ids = result.groups.flatMap(g => g.members.map(n => n.id));
    assert.equal(new Set(ids).size, nodes.length); assert.equal(ids.length, nodes.length);
  }
});

test('focus layout wraps rows and keeps coordinates finite for large groups and narrow screens', () => {
  const nodes = Array.from({ length: 611 }, (_, i) => node(String(i)));
  for (const width of [320, 900, 1500]) {
    const result = positions(nodes, width);
    assert.equal(result.length, nodes.length);
    assert.equal(new Set(result.map(p => p.x + ':' + p.y)).size, nodes.length);
    assert.ok(result.every(p => Number.isFinite(p.x) && Number.isFinite(p.y) && p.x >= 0 && p.y >= 0 && p.x + 264 <= width));
  }
});

test('private corpus replay assigns each node exactly once without changing evidence', { skip: !process.env.MEM_TOPICS_GRAPH }, () => {
  const graph = JSON.parse(require('node:fs').readFileSync(process.env.MEM_TOPICS_GRAPH, 'utf8'));
  const before = JSON.stringify(graph), result = build(graph.nodes, graph.edges);
  const ids = result.groups.flatMap(g => g.members.map(n => n.id));
  assert.equal(new Set(ids).size, graph.nodes.length); assert.equal(ids.length, graph.nodes.length);
  assert.equal(JSON.stringify(graph), before);
  assert.ok(result.groups.filter(g => g.category === 'topic').every(g => !g.members.some(n => n.origin === 'source' || n.kind === 'document')));
  console.log(JSON.stringify({ nodes: graph.nodes.length, edges: graph.edges.length, groups: result.groups.length, thematic: result.groups.filter(g => g.category === 'topic').length, semanticEdges: result.semanticEdges, sourceEdges: result.sourceEdges, unlinked: result.groups.find(g => g.category === 'unlinked')?.members.length }));
});

test('four empty mentions become one subject, while definition and claims remain distinct', () => {
  const evidence = [{ document_id: 'manual', document_revision: 'v1', page: 2, excerpt: 'Внутренний противопожарный водопровод (ВПВ).' }];
  const nodes = [1,2,3,4].map(i => ({ ...node('t'+i,'topic','ВПВ'), body:i%2?'':'ВПВ', evidence }));
  nodes.push({ ...node('definition','definition','ВПВ'), body:'Совокупность трубопроводов.', evidence });
  nodes.push({ ...node('claim'), body:'не менее 3', evidence }, { ...node('other'), body:'не более 2', evidence });
  const before = JSON.stringify(nodes), titles = names(nodes), result = project(nodes, titles);
  assert.equal(result.nodes.length,4); assert.equal(result.mentions.get('t1').length,4);
  assert.equal(titles.get('t1'),'Внутренний противопожарный водопровод (ВПВ)');
  assert.equal(titles.get('definition'),'Определение: Внутренний противопожарный водопровод (ВПВ)');
  for (const id of ['definition','claim','other']) assert.equal(result.representative.get(id),id);
  assert.equal(JSON.stringify(nodes),before);
  // Every original relation can be routed to the subject while retaining its identity and evidence.
  for (const id of ['t1','t2','t3','t4']) assert.equal(result.representative.get(id),'t1');
});

test('abbreviations are scoped to source/revision and ambiguous meanings are not collapsed', () => {
  const a = { ...node('a','topic','АС'), evidence:[{document_id:'one',document_revision:'r1',excerpt:'Автоматическая система (АС).'}] };
  const b = { ...node('b','topic','АС'), evidence:[{document_id:'two',document_revision:'r1',excerpt:'Аварийная сигнализация (АС).'}] };
  const c = { ...a,id:'c',evidence:[...a.evidence,{...a.evidence[0],excerpt:'Аварийная сигнализация (АС).'}] };
  assert.equal(names([a,b]).get('a'),'Автоматическая система (АС)');
  assert.equal(names([a,b]).get('b'),'Аварийная сигнализация (АС)');
  assert.equal(names([a,c]).get('a'),'АС');
  assert.equal(project([a,c]).nodes.length,2);
  assert.equal(project([a,b]).nodes.length,2);
  assert.equal(project([{...a,evidence:[]},{...a,id:'d',evidence:[]}]).nodes.length,2);
  const revised = {...a,id:'new',evidence:a.evidence.map(e=>({...e,document_revision:'r2'}))};
  assert.equal(project([a,revised]).nodes.length,2);
});

test('content-bearing topics, different statuses and normative nodes never collapse by title', () => {
  const evidence = [{document_id:'d',document_revision:'v1'}];
  const nodes = [
    {...node('a','topic','Насосы'),evidence}, {...node('b','topic','Насосы'),body:'Установка с резервным насосом',evidence},
    {...node('c','topic','Насосы'),status:'rejected',evidence}, {...node('d','constraint','Насосы'),body:'Требование 1',evidence},
    {...node('e','constraint','Насосы'),body:'Требование 2',evidence}
  ];
  assert.equal(project(nodes).nodes.length,nodes.length);
});

test('real VPV cluster has five informative objects and four original topic mentions', { skip: !process.env.MEM_TOPICS_GRAPH }, () => {
  const graph = JSON.parse(require('node:fs').readFileSync(process.env.MEM_TOPICS_GRAPH, 'utf8'));
  const cluster = build(graph.nodes,graph.edges).groups.find(g=>g.label==='ВПВ');
  const result = project(cluster.members,names(graph.nodes));
  assert.equal(cluster.members.length,8); assert.equal(result.nodes.length,5);
  assert.equal(result.nodes.filter(n=>n.kind==='topic').length,1);
  assert.equal(result.mentions.get(result.nodes.find(n=>n.kind==='topic').id).length,4);
  assert.equal(new Set(result.nodes.map(n=>result.labels.get(n.id))).size,5);
});
