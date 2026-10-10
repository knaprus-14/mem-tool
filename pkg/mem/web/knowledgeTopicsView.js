// Runs inside the map editor; shares its selection, filters and evidence inspector.
let topicsPage = 0, topicsPageKey = '', topicsTopology = null;
let knowledgeNodeNames = new Map(), topicProjection = null, topicMentionsExpanded = false;
const knowledgeLabel = node => knowledgeNodeNames.get(node?.id) || node?.label || node?.id || '';
function refreshTopicProjection() {
  topicProjection = KnowledgeTopics.project(nodes.filter(n => n._visible), knowledgeNodeNames);
}
function graphNode(id) {
  return nodeById.get(!topicMentionsExpanded && activeFocus?.cluster_id ? topicProjection?.representative.get(id) || id : id);
}
function visibleTopicNodes(items) {
  return topicMentionsExpanded ? items : KnowledgeTopics.project(items, knowledgeNodeNames).nodes;
}
function toggleTopicMentions() {
  topicMentionsExpanded = !topicMentionsExpanded; applyFilters();
  if (activeFocus?.cluster_id && representation === 'graph') arrangeClusterFocus(activeFocus.cluster_id);
}
function mentionsButton() {
  return topicAction(topicMentionsExpanded ? 'Свернуть повторы темы' : 'Все исходные записи', toggleTopicMentions);
}
function openTopicNode(node) {
  const mentions = !topicMentionsExpanded && topicProjection?.mentions.get(node.id);
  if (!mentions || mentions.length < 2) { selectObject('node', node); return; }
  selected = null; objectDetails.textContent = '';
  objectDetails.append(topicElement('h2', '', knowledgeLabel(node)), topicElement('p', '', 'Упоминания одной темы: ' + mentions.length + '. Выберите запись, чтобы открыть её цитаты и действия.'));
  for (const mention of [...mentions].sort((a, b) => (a.evidence?.[0]?.page || 0) - (b.evidence?.[0]?.page || 0) || a.id.localeCompare(b.id))) {
    const pages = [...new Set((mention.evidence || []).map(a => a.page).filter(Boolean))].sort((a,b)=>a-b);
    objectDetails.append(topicAction('Упоминание темы' + (pages.length ? ' · стр. ' + pages.join(', ') : '') + ' · ' + (badgeLabels[mention.status] || mention.status), () => selectObject('node', mention)));
  }
  setDetailsOpen(true); updateGraphContext();
}
function updateTopicGraphHeading() {
  const heading = $('topicGraphHeading'), cluster = clusterByID.get(activeFocus?.cluster_id);
  const visible = nodes.filter(n => n._visible), shown = visibleTopicNodes(visible);
  heading.classList.toggle('hidden', representation !== 'graph' || !cluster); heading.textContent = '';
  if (!cluster || representation !== 'graph') return;
  const title = topicElement('div', '');
  title.append(topicElement('strong', '', cluster.label), topicElement('small', '', shown.length + ' элементов · ' + visible.length + ' исходных записей'));
  heading.append(title, mentionsButton());
}
function graphLabel(node) {
  const text = knowledgeLabel(node), label = node._label;
  if (!label) return;
  label.setAttribute('text-anchor', node._subjectFocus ? 'end' : 'start');
  label.textContent = '';
  if (!activeFocus?.cluster_id) { label.textContent = shortGraphLabel(text); return; }
  const words = text.split(/\s+/u), lines = []; let line = '';
  for (const word of words) { if (line && line.length + word.length > 34) { lines.push(line); line = word; } else line += (line ? ' ' : '') + word; }
  if (line) lines.push(line);
  for (const [i, value] of lines.slice(0,3).entries()) { const span = make('tspan', {x:node._subjectFocus?-20:20,dy:i?16:0}); span.textContent = value + (i===2 && lines.length>3?'…':''); label.append(span); }
}
const topicsSearch = $('topicsSearch');
topicsSearch.addEventListener('input', () => { search.value = topicsSearch.value; applyFilters(); });
function topicElement(tag, className, text) {
  const element = document.createElement(tag); element.className = className;
  if (text !== undefined) element.textContent = text;
  return element;
}
function topicAction(label, action) {
  const button = topicElement('button', 'topic-action', label);
  button.type = 'button'; button.onclick = action; return button;
}
function updateTopicsView() {
  const surface = $('topicsContent'), scrollSurface = $('topicsView');
  if (representation !== 'topics' || !topicsTopology) return;
  if (topicsSearch.value !== search.value) topicsSearch.value = search.value;
  const focused = activeFocus?.cluster_id ? clusterByID.get(activeFocus.cluster_id) : null;
  const scoped = focused || activeFocus?.node_id;
  const key = JSON.stringify([activeFocus, search.value, [...checked('statusFilters')], [...checked('kindFilters')], [...checked('evidenceFilters')], [...checked('relationFilters')]]);
  if (key !== topicsPageKey) { topicsPage = 0; topicsPageKey = key; scrollSurface.scrollTop = 0; }
  surface.textContent = '';
  const header = topicElement('div', 'topics-heading'), heading = topicElement('div', '');
  heading.append(topicElement('div', 'topics-eyebrow', scoped ? 'ЗНАНИЯ ПО ТЕМЕ' : 'ОБЗОР КАРТЫ'), topicElement('h1', '', focused?.label || (scoped ? 'Выбранная область' : 'Темы и связи')));
  header.append(heading);
  if (scoped) header.append(topicAction('← Все темы', clearScope), topicAction('Открыть граф темы', () => setRepresentation('graph')));
  else header.append(topicAction('Подробный граф', () => setRepresentation('graph')));
  surface.append(header);
  const visible = nodes.filter(n => n._visible);
  if (scoped) header.append(mentionsButton());
  const visibleIDs = new Set(visible.map(n => n.id));
  const available = clusters.map(c => ({ ...c, members: c.members.filter(n => visibleIDs.has(n.id)) })).filter(c => c.members.length);
  const themeCount = available.filter(c => c.category !== 'source' && c.category !== 'unlinked').length;
  const visibleEdges = edges.filter(e => e._visible);
  const semanticCount = visibleEdges.filter(e => !KnowledgeTopics.provenance(e, nodeById)).length;
  const stats = topicElement('div', 'topics-stats');
  for (const [number, label] of [[scoped ? visibleTopicNodes(visible).length : themeCount, scoped ? 'элементов в теме' : 'тематических групп'], [semanticCount, 'связей между знаниями'], [visibleEdges.length - semanticCount, 'связей с источниками']]) {
    const item = topicElement('div', ''); item.append(topicElement('strong', '', number), topicElement('span', '', label)); stats.append(item);
  }
  surface.append(stats);
  if (!scoped) {
    const shortcuts = topicElement('div', 'topics-related');
    for (const group of available.filter(c => c.category === 'source' || c.category === 'unlinked')) {
      shortcuts.append(topicAction(group.label + ' · ' + group.members.length, () => setClusterFocus(group.id)));
    }
    surface.append(shortcuts);
  }
  surface.append(topicElement('p', 'topics-help', focused?.category === 'source' ? 'Документы и исходные фрагменты. Выберите карточку, чтобы открыть текст и сведения об источнике.' : focused?.category === 'unlinked' ? 'У этих узлов нет тематических связей. Их содержание сохранено; принадлежность к теме ещё нужно определить.' : 'Группы построены по названиям тем и существующим связям. Одинаковое название объединяет карточки для просмотра. Утверждения и цитаты остаются отдельными; извлечённые знания требуют проверки.'));
  if (focused) {
    const related = topicElement('div', 'topics-related');
    for (const link of topicsTopology.links.filter(l => l.from === focused.id || l.to === focused.id)) {
      const other = clusterByID.get(link.from === focused.id ? link.to : link.from);
      // Cross-topic endpoints are outside the focus by design. Apply object filters, not focus membership.
      const applicable = link.edges.filter(e => checked('relationFilters').has(e.kind) && [e.from, e.to].every(id => {
        const node = nodeById.get(id); return checked('statusFilters').has(node.status) && checked('kindFilters').has(node.kind) && checked('evidenceFilters').has(evidenceState('node', id));
      }));
      if (!applicable.length || !other) continue;
      const group = topicElement('span', 'topics-link');
      group.append(topicAction(other.label, () => setClusterFocus(other.id)), topicAction(applicable.length + ' связ.', () => selectObject('edge', applicable[0])));
      related.append(group);
    }
    if (related.children.length) { surface.append(topicElement('h2', '', 'Связанные темы'), related); }
  }
  const items = scoped ? visibleTopicNodes(visible).sort((a, b) => (semanticKindPriority[a.kind] ?? 5) - (semanticKindPriority[b.kind] ?? 5) || knowledgeLabel(a).localeCompare(knowledgeLabel(b), 'ru')) : available;
  const pageSize = 24, pages = Math.max(1, Math.ceil(items.length / pageSize));
  topicsPage = Math.max(0, Math.min(pages - 1, topicsPage));
  const grid = topicElement('div', 'topics-grid' + (scoped ? ' topics-objects' : ''));
  for (const item of items.slice(topicsPage * pageSize, (topicsPage + 1) * pageSize)) {
    const card = topicElement('article', 'topic-card');
    if (scoped) {
      card.style.setProperty('--topic-color', palette[item.kind] || 'var(--accent)');
      card.append(topicElement('div', 'topics-eyebrow', (badgeLabels[item.kind] || item.kind) + ' · ' + (badgeLabels[item.status] || item.status)));
      card.append(topicAction(knowledgeLabel(item), () => openTopicNode(item)));
      if (item.body && item.body !== item.label) card.append(topicElement('p', 'topic-preview', item.body));
      const mentions = !topicMentionsExpanded && topicProjection?.mentions.get(item.id);
      card.append(topicElement('div', 'topic-meta', mentions?.length > 1 ? 'Упоминаний темы: ' + mentions.length : 'Источников: ' + (item.evidence || []).length), topicAction(mentions?.length > 1 ? 'Упоминания и источники →' : 'Содержание и источники →', () => openTopicNode(item)));
    } else {
      card.style.setProperty('--topic-color', item.color);
      card.append(topicElement('div', 'topics-eyebrow', item.category === 'source' ? 'ИСТОЧНИКИ' : item.category === 'unlinked' ? 'НУЖНА ПРОВЕРКА' : item.category === 'related' ? 'ГРУППА СВЯЗАННЫХ УЗЛОВ' : 'ТЕМА'));
      card.append(topicAction(item.label, () => setClusterFocus(item.id)));
      const counts = new Map(); for (const node of item.members) counts.set(node.kind, (counts.get(node.kind) || 0) + 1);
      card.append(topicElement('div', 'topic-meta', [...counts].map(([kind, count]) => (badgeLabels[kind] || kind) + ': ' + count).join(' · ')));
      const samples = item.members.filter(n => !['topic', 'document', 'section'].includes(n.kind)).slice(0, 2);
      for (const sample of samples) card.append(topicElement('p', 'topic-sample', sample.label));
      card.append(topicAction('Открыть · ' + item.members.length + ' узл. →', () => setClusterFocus(item.id)));
    }
    grid.append(card);
  }
  surface.append(grid);
  if (!items.length) surface.append(topicElement('p', 'topics-help', 'Нет знаний для выбранных фильтров.'));
  const paging = topicElement('nav', 'topics-paging'); paging.setAttribute('aria-label', 'Страницы тем');
  const previous = topicAction('← Назад', () => { topicsPage--; updateTopicsView(); scrollSurface.scrollTop = 0; }); previous.disabled = topicsPage === 0;
  const next = topicAction('Далее →', () => { topicsPage++; updateTopicsView(); scrollSurface.scrollTop = 0; }); next.disabled = topicsPage >= pages - 1;
  paging.append(previous, topicElement('span', '', 'Страница ' + (topicsPage + 1) + ' из ' + pages + ' · показано ' + Math.min(items.length, topicsPage * pageSize + 1) + '–' + Math.min(items.length, (topicsPage + 1) * pageSize) + ' из ' + items.length), next);
  surface.append(paging);
}
