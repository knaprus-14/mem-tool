/* Presentation groups only: stored nodes, claims, evidence and edges are never merged. */
(function (root) {
  'use strict';
  const normalize = value => String(value || '').normalize('NFC').replace(/\s+/gu, ' ').trim().toLocaleLowerCase('ru');
  const sourceNode = node => node && (node.origin === 'source' || node.kind === 'document');
  function provenance(edge, byID) {
    return edge.kind === 'derived_from' || sourceNode(byID.get(edge.from)) || sourceNode(byID.get(edge.to));
  }
  const documentKeys = node => [...new Set((node.evidence || []).map(a => (a.document_id || a.source_path || '') + '\0' + (a.document_revision || '')))].filter(k => k !== '\0').sort();
  function names(nodes) {
    // Expand only explicit "long name (ABC)" occurrences in source text. Never use a glossary guessed from an acronym.
    const definitions = new Map(), scanned = new Set();
    for (const node of nodes) for (const anchor of node.evidence || []) {
      const document = documentKeys({ evidence: [anchor] })[0];
      const text = node.origin === 'source' && node.kind === 'section' ? node.body : anchor.excerpt;
      if (!document || !text || scanned.has(document + '\0' + text)) continue;
      scanned.add(document + '\0' + text);
      for (const match of text.matchAll(/\(([A-ZА-ЯЁ]{2,8})\)/gu)) {
        const acronym = match[1], before = text.slice(Math.max(0, match.index - 220), match.index);
        const words = [...before.matchAll(/[\p{L}]+(?:-[\p{L}]+)*/gu)].slice(-acronym.length);
        if (words.length !== acronym.length || words.map(w => w[0][0].toLocaleUpperCase('ru')).join('') !== acronym) continue;
        const phrase = before.slice(words[0].index).trim();
        if (!/^[\p{L}\s-]+$/u.test(phrase)) continue;
        const key = document + '\0' + acronym;
        if (!definitions.has(key)) definitions.set(key, new Map());
        definitions.get(key).set(normalize(phrase), phrase[0].toLocaleUpperCase('ru') + phrase.slice(1) + ' (' + acronym + ')');
      }
    }
    const result = new Map();
    for (const node of nodes) {
      const label = String(node.label || '').trim(), candidates = new Set();
      if (/^[A-ZА-ЯЁ]{2,8}$/u.test(label)) for (const document of documentKeys(node)) for (const value of definitions.get(document + '\0' + label)?.values() || []) candidates.add(value);
      const title = candidates.size === 1 ? [...candidates][0] : label || node.id;
      result.set(node.id, node.kind === 'definition' ? 'Определение: ' + title : title);
    }
    return result;
  }
  function project(nodes, labels = names(nodes)) {
    const representative = new Map(), mentions = new Map(), buckets = new Map();
    for (const node of [...nodes].sort((a, b) => a.id.localeCompare(b.id))) {
      const docs = documentKeys(node), body = normalize(node.body);
      // Only empty subject markers in the same document/revision may collapse. Content-bearing nodes stay separate.
      const acronym = /^[A-ZА-ЯЁ]{2,8}$/u.test(String(node.label || '').trim());
      const foldable = node.kind === 'topic' && node.origin === 'generated' && docs.length === 1 && (!acronym || labels.get(node.id) !== String(node.label).trim()) && (!body || body === normalize(node.label));
      const key = foldable ? JSON.stringify([normalize(node.label), docs, node.status]) : node.id;
      const id = buckets.get(key) || node.id;
      buckets.set(key, id); representative.set(node.id, id);
      if (!mentions.has(id)) mentions.set(id, []);
      mentions.get(id).push(node);
    }
    return { representative, mentions, nodes: nodes.filter(n => representative.get(n.id) === n.id), labels };
  }
  function build(nodes, edges) {
    const byID = new Map(nodes.map(n => [n.id, n])), adjacency = new Map(nodes.map(n => [n.id, new Set()]));
    const ordered = [...nodes].sort((a, b) => a.id.localeCompare(b.id));
    for (const edge of edges) {
      if (!byID.has(edge.from) || !byID.has(edge.to) || provenance(edge, byID) || edge.status === 'rejected') continue;
      adjacency.get(edge.from).add(edge.to);
      adjacency.get(edge.to).add(edge.from);
    }
    const groups = [], owner = new Map(), topicLabels = new Map(), queue = [];
    for (const node of ordered) {
      if (sourceNode(node) || !['topic', 'section'].includes(node.kind) || node.status === 'rejected') continue;
      const key = normalize(node.label) || node.id;
      let group = topicLabels.get(key);
      if (!group) {
        group = { id: node.id, label: node.label || node.id, category: 'topic', members: [] };
        topicLabels.set(key, group); groups.push(group);
      }
      owner.set(node.id, group); queue.push(node.id);
    }
    // Multi-source BFS over semantic edges; document hubs never compete with topic seeds.
    for (let i = 0; i < queue.length; i++) {
      const id = queue[i];
      for (const next of [...adjacency.get(id)].sort()) {
        if (owner.has(next)) continue;
        owner.set(next, owner.get(id)); queue.push(next);
      }
    }
    let unlinked;
    for (const node of ordered) {
      if (sourceNode(node) || owner.has(node.id)) continue;
      if (!adjacency.get(node.id).size) {
        if (!unlinked) { unlinked = { id: node.id, label: 'Без тематических связей', category: 'unlinked', members: [] }; groups.push(unlinked); }
        owner.set(node.id, unlinked); continue;
      }
      const component = [node.id]; owner.set(node.id, null);
      for (let i = 0; i < component.length; i++) for (const next of adjacency.get(component[i])) {
        if (!owner.has(next)) { owner.set(next, null); component.push(next); }
      }
      component.sort((a, b) => adjacency.get(b).size - adjacency.get(a).size || a.localeCompare(b));
      const seed = byID.get(component[0]), group = { id: seed.id, label: seed.label || seed.id, category: 'related', members: [] };
      groups.push(group); for (const id of component) owner.set(id, group);
    }
    const sources = ordered.filter(sourceNode);
    if (sources.length) {
      const root = sources.find(n => n.origin === 'source' && n.kind === 'document') || sources[0];
      const group = { id: root.id, label: 'Источники и фрагменты', category: 'source', members: [] };
      groups.push(group); for (const node of sources) owner.set(node.id, group);
    }
    for (const node of nodes) owner.get(node.id).members.push(node);
    groups.sort((a, b) => (a.category === 'source') - (b.category === 'source') || (a.category === 'unlinked') - (b.category === 'unlinked') || b.members.length - a.members.length || a.label.localeCompare(b.label, 'ru') || a.id.localeCompare(b.id));
    const links = new Map(); let semanticEdges = 0, sourceEdges = 0;
    for (const edge of edges) {
      if (!byID.has(edge.from) || !byID.has(edge.to) || edge.status === 'rejected') continue;
      if (provenance(edge, byID)) { sourceEdges++; continue; }
      semanticEdges++;
      const from = owner.get(edge.from).id, to = owner.get(edge.to).id;
      if (from === to) continue;
      const key = JSON.stringify([from, to].sort());
      if (!links.has(key)) links.set(key, { from, to, edges: [] });
      links.get(key).edges.push(edge);
    }
    return { groups, owner, links: [...links.values()], semanticEdges, sourceEdges };
  }
  // Wrapping rows avoid the unbounded vertical lanes used by the old focus view.
  function positions(nodes, width) {
    const subjects = nodes.filter(n => n.kind === 'topic');
    if (subjects.length === 1 && nodes.length > 1 && nodes.length <= 16) {
      const root = subjects[0], children = nodes.filter(n => n.id !== root.id);
      return [{ id: root.id, x: 310, y: 60 + (children.length - 1) * 48, subject: true }, ...children.map((node, i) => ({ id: node.id, x: 660, y: 60 + i * 96 }))];
    }
    const columns = Math.max(1, Math.floor(Math.max(300, width - 60) / 300));
    return nodes.map((node, i) => ({ id: node.id, x: 36 + (i % columns) * 300, y: 60 + Math.floor(i / columns) * 74 }));
  }
  const api = { build, provenance, positions, names, project };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.KnowledgeTopics = api;
})(globalThis);
