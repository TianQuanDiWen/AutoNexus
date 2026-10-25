const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

function workspace(fetch = async () => ({ ok: true, json: async () => ({}) })) {
  const elements = new Map();
  const context = vm.createContext({
    fetch,
    renderStatus() {}, openTaskModal() {}, saveTaskFromModal() {},
    closeMAAConfigModal() {}, loadMAAConfig() {}, onMAAProfileChange() {},
    window: { addEventListener() {} },
    document: {
      getElementById(id) {
        if (!elements.has(id)) elements.set(id, { style: { display: 'flex' }, textContent: '' });
        return elements.get(id);
      },
      querySelectorAll() { return []; },
    },
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'workspace.js'), 'utf8'), context);
  return { context, elements };
}

test('API errors preserve the backend reason', async () => {
  const { context } = workspace(async () => ({ ok: false, status: 400, json: async () => ({ error: '当前引擎正在运行中' }) }));
  await assert.rejects(context.requestAPI('/api/v1/queue/start'), /当前引擎正在运行中/);
});

test('non-JSON API failures show the HTTP status', async () => {
  const { context } = workspace(async () => ({ ok: false, status: 503, json: async () => { throw new Error('bad JSON'); } }));
  await assert.rejects(context.requestAPI('/api/v1/status'), /503/);
});

test('cancel keeps the unsaved task drawer open', async () => {
  const { context, elements } = workspace();
  const pending = context.closeTaskModal();
  assert.equal(elements.get('confirmModal').style.display, 'flex');
  context.finishConfirmation(false);
  await pending;
  assert.equal(context.document.getElementById('taskModal').style.display, 'flex');
});

test('confirm discards changes and closes the drawer', async () => {
  const { context, elements } = workspace();
  const pending = context.closeTaskModal();
  context.finishConfirmation(true);
  await pending;
  assert.equal(elements.get('taskModal').style.display, 'none');
});

test('successful save closes without prompting for discarded changes', async () => {
  const { context, elements } = workspace();
  await context.closeTaskModal(true);
  assert.equal(elements.get('taskModal').style.display, 'none');
  assert.equal(elements.has('confirmModal'), false);
});

test('repeated confirmation requests do not replace the active decision', async () => {
  const { context, elements } = workspace();
  const original = context.confirmAction('保留原操作');
  assert.equal(await context.confirmAction('另一个操作'), false);
  assert.equal(elements.get('confirmMessage').textContent, '保留原操作');
  context.finishConfirmation(true);
  assert.equal(await original, true);
});

test('parseCommandLine parses quotes and spaces without splitting path arguments', () => {
  const { context } = workspace();
  const input = '--config "D:\\Game Data\\config.json" --flag';
  const args = context.parseCommandLine(input);
  assert.deepEqual([...args], ['--config', 'D:\\Game Data\\config.json', '--flag']);
});

test('formatCommandLine and parseCommandLine roundtrip preserve arguments with spaces and quotes', () => {
  const { context } = workspace();
  const cases = [
    ['--config', 'D:\\Game Data\\config.json'],
    ['--dir', 'C:\\Program Files\\My App\\', '--verbose'],
    ['--message', 'hello "world" test'],
    ['singleToken', 'anotherToken'],
    [''],
    ['arg1', '', 'arg3'],
  ];

  for (const original of cases) {
    const formatted = context.formatCommandLine(original);
    const parsed = context.parseCommandLine(formatted);
    assert.deepEqual([...parsed], original, `Roundtrip failed for: ${JSON.stringify(original)} -> formatted as: ${formatted}`);
  }
});
