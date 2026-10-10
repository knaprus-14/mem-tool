const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const template = fs.readFileSync(path.join(__dirname, 'knowledgeMapHTMLSuffix.html'), 'utf8');

class Element {
  constructor(tag) {
    this.tag = tag;
    this.children = [];
    this.value = '';
    this.dataset = {};
    this.listeners = {};
  }
  append(...children) { this.children.push(...children); }
  addEventListener(event, handler) { this.listeners[event] = handler; }
  querySelectorAll(tag) {
    return this.children.flatMap(child => [
      ...(child.tag === tag ? [child] : []), ...child.querySelectorAll(tag)
    ]);
  }
}

function setup(latest = null) {
  const requests = [];
  const shown = {
    evidence_state: 'current', evidence: [{}], ready_for_approval: true,
    content_digest: 'editor-snapshot', evidence_digest: 'evidence-snapshot',
    review_digest: 'reviewed-snapshot'
  };
  const context = vm.createContext({
    document: { createElement: tag => new Element(tag) },
    writable: true,
    workspace: { session_token: 'test-session' },
    reviewFor: () => shown,
    latestReviewFor: () => latest,
    field: () => new Element('div'),
    localStorage: { getItem: () => 'Reviewer', setItem: () => {} },
    setTimeout: () => {},
    fetch: async (url, options) => {
      requests.push({ url, options, payload: JSON.parse(options.body) });
      return { ok: true, json: async () => ({}) };
    }
  });
  const reviewStart = template.indexOf('const reviewActionLabels=');
  const reviewEnd = template.indexOf('function setNodeFocus(', reviewStart);
  const createStart = template.indexOf('function workspaceCreateAction(');
  const createEnd = template.indexOf('function currentSelectionRequest(', createStart);
  assert.ok(reviewStart >= 0 && reviewEnd > reviewStart && createStart >= 0 && createEnd > createStart);
  vm.runInContext(template.slice(reviewStart, reviewEnd) + template.slice(createStart, createEnd), context);
  return { context, requests, shown };
}

test('review forms send the displayed content pin for every decision', async () => {
  for (const action of ['approve', 'reject', 'reopen', 'undo']) {
    const status = action === 'reopen' ? 'rejected' : action === 'undo' ? 'active' : 'draft';
    const latest = action === 'undo' ? {
      id: 9, action: 'approve', new_status: 'active', evidence_digest: 'evidence-snapshot',
      review_digest: 'reviewed-snapshot'
    } : null;
    const { context, requests } = setup(latest);
    const section = context.reviewAction('node', { id: 'n1', status });
    const form = section.querySelectorAll('form')[0];
    assert.ok(form, action);
    form.querySelectorAll('textarea')[0].value = 'Reason';
    const button = form.querySelectorAll('button').find(b => b.dataset.action === action);
    assert.ok(button, action);
    context.reviewFor = () => ({ review_digest: 'changed-after-render' });
    await form.listeners.submit({ preventDefault() {}, submitter: button });
    assert.equal(requests.length, 1, action);
    assert.equal(requests[0].url, `/api/review/${action}`);
    assert.equal(requests[0].options.headers['X-Mem-Session'], 'test-session');
    assert.equal(requests[0].payload.expected_review_digest, 'reviewed-snapshot');
    assert.equal(requests[0].payload.expected_evidence_digest, 'evidence-snapshot');
    if (action === 'undo') assert.equal(requests[0].payload.expected_review_id, 9);
  }
});

test('workspace creation sends only fields accepted by its strict Go request contract', async () => {
  const { context, requests } = setup();
  const section = context.workspaceCreateAction({ id: 'n1', status: 'draft' });
  const form = section.querySelectorAll('form')[0];
  form.querySelectorAll('input')[0].value = 'New note';
  form.querySelectorAll('select')[0].value = 'note';
  form.querySelectorAll('textarea')[0].value = 'Note body';
  await form.listeners.submit({ preventDefault() {} });
  assert.equal(requests.length, 1);
  assert.equal(requests[0].url, '/api/workspace/create');
  const go = fs.readFileSync(path.join(__dirname, '..', 'graph_workspace.go'), 'utf8');
  const request = go.match(/type KnowledgeWorkspaceCreateRequest struct \{([\s\S]*?)\n\}/);
  assert.ok(request);
  const accepted = new Set([...request[1].matchAll(/json:"([^",]+)/g)].map(match => match[1]));
  for (const name of Object.keys(requests[0].payload)) {
    assert.ok(accepted.has(name), `unknown field would cause HTTP 400: ${name}`);
  }
  assert.equal(requests[0].payload.expected_parent_content_digest, 'editor-snapshot');
  assert.equal(requests[0].payload.expected_evidence_digest, 'evidence-snapshot');
  assert.equal(requests[0].payload.label, 'New note');
});
