'use strict';
// Deliberately small Markdown preview. It never parses HTML, fetches resources,
// rewrites the stored source, or treats a matching quotation as a verified fact.
// Limits apply to the preview only; callers must retain the original edit value.
function renderMemMarkdown(host, text, {onObjectLink} = {}) {
  const MAX_INPUT = 131072, MAX_LINES = 4096, MAX_NODES = 4096;
  const MAX_INLINE = 8192, MAX_DEPTH = 4, MAX_WORK = 1048576;
  if (typeof text !== 'string') throw new TypeError('Markdown text must be a string');
  const doc = host.ownerDocument || document;
  const inputCodeUnits = text.length;
  let end = Math.min(text.length, MAX_INPUT);
  if (end < text.length && /[\uD800-\uDBFF]/.test(text[end - 1]) && /[\uDC00-\uDFFF]/.test(text[end])) end--;
  const source = text.slice(0, end), truncated = end < text.length;
  let nodes = 0, work = 0, formattingLimited = false;
  const exhausted = {};
  function charge(amount = 1) { work += amount; if (work > MAX_WORK) throw exhausted; }
  function element(tag) { if (++nodes > MAX_NODES) throw exhausted; return doc.createElement(tag); }
  function literal(parent, value) {
    if (!value) return;
    if (++nodes > MAX_NODES) throw exhausted;
    parent.append(doc.createTextNode(value));
  }
  function find(value, needle, start) {
    // Charge for a worst-case scan, not just successful matches. Malicious runs
    // of unmatched delimiters therefore cannot trigger unbounded rescanning.
    charge(value.length - start);
    return value.indexOf(needle, start);
  }
  function destination(raw) {
    if (raw.length > 2048 || /[\s\\<>"'\u0000-\u001f\u007f-\u009f\u200b-\u200f\u202a-\u202e\u2060-\u206f]/u.test(raw)) return null;
    const local = raw.match(/^mem:\/\/map\/([A-Za-z0-9_-]{1,128})\/node\/([A-Za-z0-9_-]{1,128})$/);
    if (local) return {mapID: local[1], nodeID: local[2]};
    // Do not decode a scheme or a local object ID. Only an explicit HTTP(S)
    // authority is eligible; paths, credentials and other schemes stay literal.
    if (!/^https?:\/\/[^/?#]+/i.test(raw) || /[()]/.test(raw)) return null;
    let decoded, url;
    try { decoded = decodeURIComponent(raw); url = new URL(raw); } catch (_) { return null; }
    if (/[\\\u0000-\u001f\u007f-\u009f\u200b-\u200f\u202a-\u202e\u2060-\u206f]/u.test(decoded) || /%[0-9a-f]{2}/i.test(decoded)) return null;
    if (!['http:', 'https:'].includes(url.protocol) || !url.hostname || url.username || url.password) return null;
    return {href: url.href};
  }
  function inline(parent, value, depth = 0) {
    // Raw HTML is displayed as authored, including any Markdown-looking text
    // inside tags. No HTML fragment is ever handed to a browser parser.
    if (/<[A-Za-z/!?]/.test(value)) { literal(parent, value); return; }
    if (depth >= MAX_DEPTH || value.length > MAX_INLINE) {
      formattingLimited = true; literal(parent, value); return;
    }
    let i = 0, plain = 0;
    const flush = stop => { literal(parent, value.slice(plain, stop)); };
    while (i < value.length) {
      charge();
      const ch = value[i];
      // Backslash escapes are supported for the small set of inline delimiters.
      if (ch === '\\' && /[\\`*_[\]$]/.test(value[i + 1] || '')) {
        flush(i); literal(parent, value[i + 1]); i += 2; plain = i; continue;
      }
      if (ch === '`' || ch === '$') {
        let width = 1;
        while (value[i + width] === ch) { width++; charge(); }
        const marker = ch.repeat(width), close = find(value, marker, i + width);
        if (close > i + width) {
          flush(i);
          if (ch === '`') {
            const code = element('code'); literal(code, value.slice(i + width, close)); parent.append(code);
          } else {
            // Math remains original text, including delimiters; no typesetting.
            literal(parent, value.slice(i, close + width));
          }
          i = close + width; plain = i; continue;
        }
        i += width; continue;
      }
      const image = ch === '!' && value[i + 1] === '[';
      if (ch === '[' || image) {
        const open = i + (image ? 1 : 0), middle = find(value, '](', open + 1);
        if (middle !== -1) {
          const close = find(value, ')', middle + 2);
          if (close !== -1) {
            const label = value.slice(open + 1, middle), raw = value.slice(middle + 2, close);
            const target = !image && label && !/[\[\]\n]/.test(label) ? destination(raw) : null;
            if (target?.href || (target?.mapID && typeof onObjectLink === 'function')) {
              flush(i);
              if (target.href) {
                const link = element('a'); link.href = target.href; link.target = '_blank'; link.rel = 'noopener noreferrer';
                literal(link, label); parent.append(link);
              } else {
                const button = element('button'); button.type = 'button'; button.className = 'mem-markdown-object';
                literal(button, label);
                button.addEventListener('click', () => {
                  // A detached preview must not navigate through its old callback.
                  if (host.contains(button)) onObjectLink({mapID: target.mapID, nodeID: target.nodeID});
                });
                parent.append(button);
              }
              i = close + 1; plain = i; continue;
            }
            // Keep unsupported/unsafe links and images intact, without reparsing
            // their labels as another link or interpreting their contents.
            i = close + 1; continue;
          }
        }
      }
      if (ch === '*' || ch === '_') {
        let width = value[i + 1] === ch ? 2 : 1;
        if (value[i + width] === ch) { while (value[i] === ch) { i++; charge(); } continue; }
        // Underscores inside identifiers/formulae are literal.
        const word = c => /[\p{L}\p{N}_]/u.test(c || '');
        if (!/\s/.test(value[i + width] || ' ') && !(ch === '_' && word(value[i - 1]))) {
          const marker = ch.repeat(width), close = find(value, marker, i + width);
          if (close > i + width && !/\s/.test(value[close - 1]) && !(ch === '_' && word(value[close + width]))) {
            flush(i); const span = element(width === 2 ? 'strong' : 'em');
            inline(span, value.slice(i + width, close), depth + 1); parent.append(span);
            i = close + width; plain = i; continue;
          }
        }
      }
      i++;
    }
    flush(value.length);
  }
  const lines = source.replace(/\r\n?/g, '\n').split('\n');
  const fence = line => line.match(/^ {0,3}(`{3,}|~{3,})([^\n]*)$/);
  const heading = line => line.match(/^ {0,3}(#{1,6})[ \t]+(.+)$/);
  const quote = line => /^ {0,3}> ?/.test(line);
  // This preview supports flat lists. An indented list is kept literal rather
  // than silently flattening an authored parent/child relationship.
  const list = line => line.match(/^(?:([-+*])|([0-9]{1,9})[.)])[ \t]+(.+)$/);
  const indented = line => /^(?: {4}|\t| +(?:[-+*]|[0-9]{1,9}[.)])[ \t])/.test(line);
  const special = line => fence(line) || heading(line) || quote(line) || list(line) || indented(line);
  const blocks = [];
  try {
    if (lines.length > MAX_LINES) throw exhausted;
    for (let i = 0; i < lines.length;) {
      charge(); const line = lines[i];
      if (!line.trim()) { i++; continue; }
      const fenced = fence(line), titled = heading(line), item = list(line);
      if (indented(line)) {
        const chunks = [];
        while (i < lines.length && indented(lines[i])) { chunks.push(lines[i++]); charge(); }
        const pre = element('pre'); pre.className = 'mem-markdown-literal'; literal(pre, chunks.join('\n')); blocks.push(pre);
      } else if (fenced) {
        const marker = fenced[1], chunks = []; i++;
        while (i < lines.length) {
          const closing = lines[i].trim();
          if (closing.length >= marker.length && [...closing].every(c => c === marker[0])) { i++; break; }
          chunks.push(lines[i++]); charge();
        }
        const pre = element('pre'), code = element('code'); literal(code, chunks.join('\n')); pre.append(code); blocks.push(pre);
      } else if (titled) {
        const h = element('h' + titled[1].length); inline(h, titled[2]); blocks.push(h); i++;
      } else if (quote(line)) {
        const chunks = [];
        while (i < lines.length && quote(lines[i])) { chunks.push(lines[i++].replace(/^ {0,3}> ?/, '')); charge(); }
        const block = element('blockquote'); inline(block, chunks.join('\n')); blocks.push(block);
      } else if (item) {
        const ordered = !!item[2], group = element(ordered ? 'ol' : 'ul');
        if (ordered) group.setAttribute('start', item[2]);
        while (i < lines.length) {
          const next = list(lines[i]); if (!next || !!next[2] !== ordered) break;
          const li = element('li'); if (ordered) li.setAttribute('value', next[2]);
          inline(li, next[3]); group.append(li); i++; charge();
        }
        blocks.push(group);
      } else {
        const chunks = [line]; i++;
        while (i < lines.length && lines[i].trim() && !special(lines[i])) { chunks.push(lines[i++]); charge(); }
        const p = element('p'); inline(p, chunks.join('\n')); blocks.push(p);
      }
    }
  } catch (error) {
    if (error !== exhausted) throw error;
    formattingLimited = true;
    // Complexity fallback preserves the entire bounded source, including all
    // syntax; it does not silently omit the tail of a partly rendered document.
    const pre = doc.createElement('pre'); pre.className = 'mem-markdown-literal'; pre.textContent = source;
    blocks.splice(0, blocks.length, pre);
  }
  if (truncated || formattingLimited) {
    const notice = doc.createElement('p'); notice.className = 'mem-markdown-notice'; notice.setAttribute('role', 'note');
    notice.textContent = truncated
      ? 'Предпросмотр сокращён из-за размера. Полный исходный текст сохранён и доступен в редакторе.'
      : 'Часть разметки показана обычным текстом из-за ограничений предпросмотра. Исходный текст сохранён.';
    blocks.push(notice);
  }
  host.classList.add('mem-markdown'); host.replaceChildren(...blocks);
  return {truncated, formattingLimited, inputCodeUnits, renderedCodeUnits: source.length};
}
