// Logical DOM component tests. No browser layout or E2E coverage is implied.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs'), vm = require('node:vm');
const source = fs.readFileSync(__dirname + '/assets/safe-markdown.js', 'utf8');
function fixture() {
  const created = [];
  const doc = {createElement: tag => node(tag), createTextNode: text => node('#text', text)};
  function node(tag, own = '') {
    const classes = new Set();
    const e = {tag, own, ownerDocument: doc, children: [], parentNode: null, attrs: {}, events: {},
      classList: {add: name => classes.add(name), contains: name => classes.has(name)},
      get textContent() { return this.own + this.children.map(child => child.textContent).join(''); },
      set textContent(text) { this.own = String(text); this.replaceChildren(); },
      set innerHTML(_) { throw Error('HTML parsing must never be used'); },
      insertAdjacentHTML() { throw Error('HTML parsing must never be used'); },
      append(...children) { for (const child of children) { assert.equal(typeof child, 'object'); child.parentNode = this; this.children.push(child); } },
      replaceChildren(...children) { for (const child of this.children) child.parentNode = null; this.children = []; this.append(...children); },
      setAttribute(key, value) { this.attrs[key] = String(value); },
      addEventListener(key, fn) { (this.events[key] ??= []).push(fn); },
      contains(child) { for (let n = child; n; n = n.parentNode) if (n === this) return true; return false; },
      click() { for (const fn of this.events.click || []) fn(); }
    };
    created.push(e); return e;
  }
  const context = vm.createContext({document: doc, URL}); vm.runInContext(source, context);
  const host = node('div');
  const descendants = parent => parent.children.flatMap(child => [child, ...descendants(child)]);
  return {host, created, render: (value, options) => context.renderMemMarkdown(host, value, options),
    all: tag => descendants(host).filter(e => e.tag === tag), descendants: () => descendants(host)};
}
test('headings paragraphs quotes and ordered/unordered lists preserve logical content and order', () => {
  const f = fixture(), text = '# Заголовок 😀\n\nПервый абзац\nпродолжение.\n\n- Один\n- Два\n\n3. Три\n7. Семь\n\n> Цитата\n> вторая строка\n\n## Итог';
  const result = f.render(text);
  assert.equal(result.truncated, false); assert.equal(result.formattingLimited, false);
  assert.deepEqual(f.host.children.map(e => e.tag), ['h1', 'p', 'ul', 'ol', 'blockquote', 'h2']);
  assert.equal(f.all('h1')[0].textContent, 'Заголовок 😀');
  assert.equal(f.all('p')[0].textContent, 'Первый абзац\nпродолжение.');
  assert.deepEqual(f.all('li').map(e => e.textContent), ['Один', 'Два', 'Три', 'Семь']);
  assert.equal(f.all('ol')[0].attrs.start, '3'); assert.equal(f.all('li')[3].attrs.value, '7');
  assert.equal(f.all('blockquote')[0].textContent, 'Цитата\nвторая строка');
  assert(f.host.classList.contains('mem-markdown')); assert.equal(result.inputCodeUnits, text.length);
});
test('inline code strong emphasis and escaped markers render without altering Unicode text', () => {
  const f = fixture();
  f.render('**важно 🧭** и *курсив* и __сильно__ и _наклон_ и `x < 5` и \\*буквально\\*.');
  assert.deepEqual(f.all('strong').map(e => e.textContent), ['важно 🧭', 'сильно']);
  assert.deepEqual(f.all('em').map(e => e.textContent), ['курсив', 'наклон']);
  assert.equal(f.all('code')[0].textContent, 'x < 5');
  assert.equal(f.host.textContent, 'важно 🧭 и курсив и сильно и наклон и x < 5 и *буквально*.');
});
test('code fences and math remain literal without links markup evaluation or language injection', () => {
  const f = fixture();
  f.render('```html onclick="alert(1)"\n<script>alert(1)</script>\n**bold** [x](https://example.org)\n```\n\n~~~tex\nE = mc^2; x_i_j\n~~~\n\n$x_i * y*$ и $$a_b_c$$ и x_i_j');
  assert.equal(f.all('pre').length, 2); assert.equal(f.all('script').length, 0); assert.equal(f.all('a').length, 0);
  assert.equal(f.all('code')[0].textContent, '<script>alert(1)</script>\n**bold** [x](https://example.org)');
  assert.equal(f.all('code')[1].textContent, 'E = mc^2; x_i_j');
  assert.equal(f.all('p')[0].textContent, '$x_i * y*$ и $$a_b_c$$ и x_i_j');
  assert.equal(f.all('em').length, 0); assert.equal(f.all('strong').length, 0);
  assert(f.descendants().every(e => !Object.keys(e.attrs).some(k => k.startsWith('on'))));
});
test('raw HTML images and unsupported syntax stay visible inert text', () => {
  const f = fixture(), raw = '<img src=x onerror=alert(1)> <script>alert(1)</script>\n\n![alt](https://example.org/x.png)\n\n| A | B |\n|---|---|\n\n[x](javascript:alert(1))';
  f.render(raw);
  for (const tag of ['img', 'script', 'iframe', 'style', 'table', 'a', 'button']) assert.equal(f.all(tag).length, 0, tag);
  assert.match(f.host.textContent, /<img src=x onerror=alert\(1\)>/); assert.match(f.host.textContent, /!\[alt\]\(https:\/\/example.org\/x.png\)/);
  assert.match(f.host.textContent, /\[x\]\(javascript:alert\(1\)\)/); assert.match(f.host.textContent, /\| A \| B \|/);
});
test('unsupported nested lists indented code and raw HTML preserve their authored markup', () => {
  const f = fixture(); f.render('- Parent\n  - **nested literal**\n    code *not emphasis*\n\n<b>**literal HTML**</b>');
  assert.equal(f.all('li').length, 1); assert.equal(f.all('li')[0].textContent, 'Parent');
  assert.equal(f.all('pre')[0].textContent, '  - **nested literal**\n    code *not emphasis*');
  assert.equal(f.all('p')[0].textContent, '<b>**literal HTML**</b>'); assert.equal(f.all('strong').length, 0); assert.equal(f.all('em').length, 0);
});
test('only explicit safe HTTP(S) links navigate and receive opener protection', () => {
  const f = fixture(); f.render('[Документ](https://example.org/a%20b?q=one&x=2#part) и [HTTP](HTTP://example.org/a)');
  assert.equal(f.all('a').length, 2);
  for (const link of f.all('a')) { assert.equal(link.target, '_blank'); assert.equal(link.rel, 'noopener noreferrer'); }
  assert.equal(f.all('a')[0].href, 'https://example.org/a%20b?q=one&x=2#part'); assert.equal(f.all('a')[0].textContent, 'Документ');
  assert.equal(f.all('a')[1].href, 'http://example.org/a');
});
test('unsafe protocols controls ambiguous encodings credentials and paths stay literal', () => {
  const urls = ['javascript:alert(1)', 'data:text/html,<svg/onload=alert(1)>', 'file:///C:/secret', 'vbscript:msgbox(1)', '//example.org', '/api/logout', '../secret', 'C:/secret', '#node',
    'https:example.org', 'https:///example.org', 'https://user:pass@example.org', 'https://example.org\\@evil.org', 'https://example.org/%0aevil', 'https://example.org/%00', 'https://example.org/%5cfoo',
    'https://example.org/%250a', 'https://example.org/%25250a', 'https://example.org/%FF', 'https://example.org/%', 'https://example.org/\tpath', 'https://example.org/\u202eevil', 'https://example.org/%E2%80%AEevil',
    'java&#115;cript:alert(1)', 'https&#58;//example.org', 'https%3a%2f%2fexample.org', 'https://example.org/\u0001x'];
  for (const url of urls) { const f = fixture(), raw = '[x](' + url + ')'; f.render(raw); assert.equal(f.all('a').length, 0, url); assert.equal(f.all('button').length, 0, url); assert.equal(f.host.textContent, raw, url); }
});
test('validated local references only call the supplied callback on an explicit current-preview click', () => {
  const f = fixture(), calls = [], mapID = 'mm-' + 'a'.repeat(32), nodeID = 'mmn-' + 'b'.repeat(32);
  const raw = '[Узел](mem://map/' + mapID + '/node/' + nodeID + ')';
  f.render(raw, {onObjectLink: ref => calls.push({...ref})}); assert.deepEqual(calls, []); assert.equal(f.all('a').length, 0);
  const button = f.all('button')[0]; assert.equal(button.type, 'button'); assert.equal(button.textContent, 'Узел'); assert.equal(button.href, undefined);
  button.click(); assert.deepEqual(calls, [{mapID, nodeID}]);
  f.render('new preview'); button.click(); assert.equal(calls.length, 1);
  f.render(raw); assert.equal(f.host.textContent, raw); assert.equal(f.all('button').length, 0);
});
test('local reference validation rejects encoded paths extra fields empty or oversized IDs', () => {
  const urls = ['mem://map/a/node/', 'mem://map/a/node/b/extra', 'mem://map/a/node/b?delete=1', 'mem://map/a/node/b#x', 'mem://map/../node/b', 'mem://map/a%2fb/node/c', 'mem://map/a/node/%252e%252e',
    'mem://map/a/node/b%00', 'mem://user@map/a/node/b', 'mem://map/a/edge/b', 'mem://map/а/node/b', 'MEM://map/a/node/b', 'mem://map/' + 'a'.repeat(129) + '/node/b'];
  for (const url of urls) { const f = fixture(), raw = '[target](' + url + ')'; f.render(raw, {onObjectLink: () => assert.fail('Unsafe callback')}); assert.equal(f.all('button').length, 0, url); assert.equal(f.all('a').length, 0, url); assert.equal(f.host.textContent, raw); }
});
test('oversized input visibly truncates at a Unicode boundary and keeps preview bounded', () => {
  const f = fixture(), raw = 'a'.repeat(131071) + '😀' + 'tail'; const result = f.render(raw);
  assert.equal(result.truncated, true); assert.equal(result.renderedCodeUnits, 131071); assert.equal(result.inputCodeUnits, raw.length);
  assert.equal(f.all('p')[0].textContent, 'a'.repeat(131071)); assert.equal(f.host.textContent.includes('\ud83d'), false);
  assert.match(f.host.textContent, /Полный исходный текст сохранён/); assert(f.descendants().length < 20); assert.equal(raw.slice(-6), '😀tail');
});
test('line node and scan budgets fall back to the entire bounded literal source', () => {
  const hostile = ['x\n'.repeat(4097), '- **a** *b*\n'.repeat(1100), '['.repeat(4000) + 'end'];
  for (const raw of hostile) { const f = fixture(), result = f.render(raw); assert.equal(result.truncated, false); assert.equal(result.formattingLimited, true); assert.equal(f.all('pre').length, 1); assert.equal(f.all('pre')[0].textContent, raw); assert(f.descendants().length <= 4); assert(f.created.length <= 4101); }
});
test('deep inline formatting and long lines stay bounded and visible', () => {
  const f = fixture(), raw = '**bold _italic **nested _four **five** four_ nested** italic_ bold**';
  const result = f.render(raw); assert.equal(result.truncated, false); assert.match(f.host.textContent, /five/); assert(f.descendants().length < 50);
  const long = '😀'.repeat(5000) + ' $formula$'; const next = f.render(long); assert.equal(next.formattingLimited, true); assert.equal(f.all('p')[0].textContent, long);
});
test('rerender replaces old content and source input type is explicit', () => {
  const f = fixture(); f.render('**old**'); f.render('new'); assert.equal(f.host.textContent, 'new'); assert.equal(f.all('strong').length, 0);
  f.render(''); assert.equal(f.host.children.length, 0); assert.throws(() => f.render({toString() { throw Error('do not coerce'); }}), /must be a string/);
});
