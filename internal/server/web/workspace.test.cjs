const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

function workspace(fetch = async () => ({ ok: true, json: async () => ({}) }), extraContext = {}) {
  const elements = new Map();
  const { document: documentOverrides, ...globals } = extraContext;
  const context = vm.createContext({
    fetch,
    renderStatus() {}, openTaskModal() {}, saveTaskFromModal() {},
    closeMAAConfigModal() {}, loadMAAConfig() {}, onMAAProfileChange() {},
    window: { addEventListener() {} },
    setTimeout() {},
    prompt: () => null,
    document: Object.assign({
      getElementById(id) {
        if (!elements.has(id)) elements.set(id, { style: { display: 'flex' }, dataset: {}, textContent: '', setAttribute(name,value) { this[name] = value; }, appendChild() {} });
        return elements.get(id);
      },
      createElement() { return { className: '', dataset: {}, textContent: '', setAttribute() {}, appendChild() {}, remove() {} }; },
      querySelector(selector) { return this.getElementById(selector); },
      querySelectorAll() { return []; },
    }, documentOverrides || {}),
    ...globals,
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

test('idle control bar shows runnable count and hides empty runtime details', () => {
  const { context, elements } = workspace();
  vm.runInContext(`statusAvailable=true; latestStatus={state:'IDLE',tasks:[
    {id:'a',enabled:true,is_completed_today:false},
    {id:'b',enabled:true,is_completed_today:true},
    {id:'c',enabled:false,is_completed_today:false}
  ]}; updateControls();`, context);
  assert.equal(elements.get('queueSummary').textContent, '待执行 1 项');
  assert.equal(elements.get('runtimeDetails').hidden, true);
  assert.equal(elements.get('btnStart').disabled, false);
  elements.get('chkForceRun').checked = true;
  context.updateControls();
  assert.equal(elements.get('queueSummary').textContent, '待执行 2 项');
});

test('running control bar exposes runtime details and prevents duplicate start', () => {
  const { context, elements } = workspace();
  vm.runInContext("statusAvailable=true; latestStatus={state:'RUNNING',tasks:[{id:'a',enabled:true}]}; updateControls();", context);
  assert.equal(elements.get('runControl').dataset.state, 'RUNNING');
  assert.equal(elements.get('runtimeDetails').hidden, false);
  assert.equal(elements.get('queueSummary').textContent, '');
  assert.equal(elements.get('btnStart').disabled, true);
  assert.equal(elements.get('btnStop').disabled, false);
});

test('runtime labels distinguish single-task and total elapsed time', () => {
  const { context, elements } = workspace();
  context.updateElapsedLabels(3661, 7322);
  assert.equal(elements.get('elapsedTime').textContent, '单项 1小时1分1秒');
  assert.equal(elements.get('totalElapsedTime').textContent, '总计 2小时2分2秒');
  assert.equal(context.compactStartTime('2026-11-04 09:08:07'), '11-04 09:08:07');
  assert.equal(context.compactStartTime(''), '未运行');
  assert.equal(context.taskSummaryLine({ is_maa:false }), '外部程序');
  assert.equal(context.taskSummaryLine({ is_maa:false, last_duration:'2分' }), '外部程序 · 上次耗时 2分');
  assert.equal(context.taskStartLabel('2026-11-04 09:08:07'), '上次启动 11-04 09:08:07');
  assert.equal(context.taskStartLabel(''), '未运行');
});

test('connection indicator distinguishes status failure from log reconnection', () => {
  const { context, elements } = workspace();
  context.updateConnectionIndicator();
  assert.equal(elements.get('wsText').textContent, '状态连接中断');
  vm.runInContext('statusAvailable=true; updateConnectionIndicator();', context);
  assert.equal(elements.get('wsText').textContent, '日志重连中');
  vm.runInContext('logConnected=true; updateConnectionIndicator();', context);
  assert.equal(elements.get('wsText').textContent, '已连接');
  assert.equal(elements.get('btnRefreshLogs').hidden, true);
});

test('paused log following exposes a resume action and accurate pressed state', () => {
  const { context, elements } = workspace();
  vm.runInContext('autoScroll=false; updateLogFollowControls();', context);
  assert.equal(elements.get('btnResumeLogs').hidden, false);
  assert.equal(elements.get('btnAutoScroll')['aria-pressed'], 'false');
  vm.runInContext('autoScroll=true; updateLogFollowControls();', context);
  assert.equal(elements.get('btnResumeLogs').hidden, true);
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

test('updateLANAccess restores the switch when saving fails', async () => {
  let calls = 0;
  const { context } = workspace(async () => {
    calls++;
    if (calls === 1) return { ok: false, status: 500, json: async () => ({ error: '落盘失败' }) };
    return { ok: true, json: async () => ({}) };
  });
  const chk = context.document.getElementById('chkLANAccess');
  chk.checked = true;
  await context.updateLANAccess();
  assert.equal(chk.checked, false, 'switch should roll back to the server value');
  assert.equal(chk.disabled, false, 'switch should be re-enabled');
  assert.equal(calls, 1, 'status should not be re-fetched after a failed save');
});

test('updateLANAccess keeps the new value when saving succeeds', async () => {
  const { context } = workspace(async () => ({ ok: true, json: async () => ({}) }));
  const chk = context.document.getElementById('chkLANAccess');
  chk.checked = true;
  await context.updateLANAccess();
  assert.equal(chk.checked, true, 'switch should keep the saved value');
  assert.equal(chk.disabled, false);
});

test('updateThemeSetting restores the selection when saving fails', async () => {
  let calls = 0;
  const { context } = workspace(async () => {
    calls++;
    if (calls === 1) return { ok: false, status: 500, json: async () => ({ error: '落盘失败' }) };
    return { ok: true, json: async () => ({}) };
  });
  const sel = context.document.getElementById('selTheme');
  sel.value = 'auto';
  sel.value = 'light';
  await context.updateThemeSetting();
  assert.equal(sel.value, 'auto', 'selection should roll back to the server theme');
  assert.equal(sel.disabled, false, 'select should be re-enabled');
  assert.equal(calls, 1, 'status should not be re-fetched after a failed save');
});

const maaView = tasks => ({
  is_maa: true,
  project_name: '示例项目',
  version: 'v1.0',
  config_type: 'mxu',
  active_config_id: 'p1',
  profiles: [{ id: 'p1', name: 'P', is_active: true }],
  binding_stale: false,
  binding_id: '',
  binding_name: '',
  tasks,
});

test('MAA binding_stale renders the stale binding warning and hides it otherwise', async () => {
  let stale = true;
  const { context, elements } = workspace(async () => ({
    ok: true,
    json: async () => ({ ...maaView([]), binding_stale: stale, binding_id: 'old-id', binding_name: '旧预设' }),
  }));
  await context.loadMAAConfig('task1');
  const warn = elements.get('maaBindingWarn');
  assert.equal(warn.style.display, 'block', 'warning shows while binding_stale is true');
  assert.ok(warn.textContent.includes('绑定的预设已失效'), 'warning text mentions the stale binding');
  assert.ok(warn.textContent.includes('请重新选择并保存'), 'warning text tells the user what to do');
  stale = false;
  await context.loadMAAConfig('task1');
  assert.equal(warn.style.display, 'none', 'warning hides once the binding is healthy');
});

test('sub option visibility follows depends_on and show_when against the parent value', () => {
  const { context } = workspace();
  const visible = context.isSubOptionVisible;
  // select 父选项：当前 case 名命中 show_when 任一项即显示
  assert.equal(visible({ name: 'x', depends_on: 'p', show_when: ['CaseA'] }, [{ name: 'p', type: 'select', value: 'CaseA' }]), true);
  assert.equal(visible({ name: 'x', depends_on: 'p', show_when: ['CaseA'] }, [{ name: 'p', type: 'select', value: 'CaseB' }]), false);
  // switch 父选项：show_when 的 case 名解析为真值集合后对照布尔值
  assert.equal(visible({ name: 'x', depends_on: 'p', show_when: ['true'] }, [{ name: 'p', type: 'switch', value: true }]), true);
  assert.equal(visible({ name: 'x', depends_on: 'p', show_when: ['true'] }, [{ name: 'p', type: 'switch', value: false }]), false);
  assert.equal(visible({ name: 'x', depends_on: 'p', show_when: ['false', 'CaseX'] }, [{ name: 'p', type: 'switch', value: false }]), true);
  assert.equal(visible({ name: 'x', depends_on: 'p', show_when: ['0'] }, [{ name: 'p', type: 'switch', value: false }]), true);
  // 无依赖、缺 show_when、父选项缺失：兜底显示，避免值不可达
  assert.equal(visible({ name: 'x' }, [{ name: 'p', type: 'switch', value: false }]), true);
  assert.equal(visible({ name: 'x', depends_on: 'p' }, [{ name: 'p', type: 'switch', value: false }]), true);
  assert.equal(visible({ name: 'x', depends_on: 'missing', show_when: ['true'] }, []), true);
});

test('MAA save payload carries each task entry_id and keeps hidden dependent values', async () => {
  const calls = [];
  const { context, elements } = workspace(async (url, options) => {
    calls.push({ url, options: options || {} });
    return { ok: true, json: async () => ({}) };
  });
  vm.runInContext(`currentMAATaskID='task1'; currentSelectedProfileID='p1'; currentMAAData={active_config_id:'p1',tasks:[
    {name:'制衣',label:'制衣',enabled:true,entry_id:'entry-a',sub_options:[
      {name:'攻击方法',type:'switch',value:true},
      {name:'攻击详情',type:'select',depends_on:'攻击方法',show_when:['true'],value:'CaseA',cases:[{name:'CaseA',label:'A'},{name:'CaseB',label:'B'}]}
    ]},
    {name:'战斗',label:'战斗',enabled:false,entry_id:'',sub_options:[]}
  ]};`, context);
  const byId = id => context.document.getElementById(id);
  byId('chk_maa_task_0').checked = true;
  byId('chk_maa_task_1').checked = false;
  byId('chk_maa_sub_0_0').checked = true;
  byId('sel_maa_sub_0_1').value = 'CaseB';
  await context.saveMAAConfig();
  const post = calls.find(c => c.url === '/api/v1/tasks/task1/maa');
  assert.ok(post, 'save request issued');
  const body = JSON.parse(post.options.body);
  assert.equal(body.tasks[0].entry_id, 'entry-a', 'entry tasks carry their entry_id');
  assert.equal(body.tasks[1].entry_id, '', 'pi/mfaa tasks carry an empty entry_id');
  assert.equal(body.tasks[0].sub_options[0].value, true);
  assert.equal(body.tasks[0].sub_options[1].value, 'CaseB', 'dependent option submits its current value even when hidden');
});

test('renaming an entry PUTs the alias endpoint and reloads the panel', async () => {
  const calls = [];
  const { context } = workspace(async (url, options) => {
    calls.push({ url, method: (options && options.method) || 'GET', options: options || {} });
    if ((options && options.method) === 'PUT') return { ok: true, json: async () => ({ message: 'ok', display_name: '制衣-上衣' }) };
    return { ok: true, json: async () => maaView([{ name: '制衣', label: '制衣', entry_id: 'entry-a', display_name: '', enabled: true, sub_options: [] }]) };
  });
  context.prompt = () => '制衣-上衣';
  vm.runInContext("currentMAATaskID='task1'; currentSelectedProfileID='p1'; currentMAAData=" + JSON.stringify(maaView([{ name: '制衣', label: '制衣', entry_id: 'entry-a', display_name: '', enabled: true, sub_options: [] }])) + ';', context);
  await context.renameMAAEntry('entry-a');
  const put = calls.find(c => c.url === '/api/v1/tasks/task1/maa/task-entries/entry-a/alias');
  assert.ok(put, 'alias PUT issued');
  assert.equal(put.method, 'PUT');
  assert.deepEqual(JSON.parse(put.options.body), { display_name: '制衣-上衣', config_id: 'p1' });
  assert.ok(calls.some(c => c.url === '/api/v1/tasks/task1/maa?config_id=p1'), 'panel reloaded after rename');
});

test('cancelling the rename prompt sends no request', async () => {
  let calls = 0;
  const { context } = workspace(async () => {
    calls++;
    return { ok: true, json: async () => ({}) };
  });
  context.prompt = () => null;
  vm.runInContext("currentMAATaskID='task1'; currentMAAData=" + JSON.stringify(maaView([{ name: '制衣', entry_id: 'entry-a', enabled: true, sub_options: [] }])) + ';', context);
  await context.renameMAAEntry('entry-a');
  assert.equal(calls, 0, 'no request after cancelling the prompt');
});

test('duplicating an entry POSTs entry_id, reloads and locates the new entry', async () => {
  const before = [{ name: '制衣', label: '制衣', entry_id: 'entry-a', display_name: '', enabled: true, sub_options: [] }];
  const after = before.concat([{ name: '制衣', label: '制衣', entry_id: 'entry-b', display_name: '', enabled: true, sub_options: [] }]);
  const calls = [];
  const { context, elements } = workspace(async (url, options) => {
    const method = (options && options.method) || 'GET';
    calls.push({ url, method, options: options || {} });
    if (url.endsWith('/task-entries/duplicate')) return { ok: true, json: async () => ({ message: '已复制', entry_id: 'entry-b' }) };
    const duplicated = calls.some(c => c.method === 'POST');
    return { ok: true, json: async () => maaView(duplicated ? after : before) };
  });
  vm.runInContext("currentMAATaskID='task1'; currentSelectedProfileID='p1'; currentMAAData=" + JSON.stringify(maaView(before)) + ';', context);
  await context.duplicateMAAEntry('entry-a');
  const post = calls.find(c => c.url === '/api/v1/tasks/task1/maa/task-entries/duplicate');
  assert.ok(post, 'duplicate POST issued');
  assert.equal(post.method, 'POST');
  assert.deepEqual(JSON.parse(post.options.body), { entry_id: 'entry-a', config_id: 'p1' });
  const reload = calls.find(c => c.url === '/api/v1/tasks/task1/maa?config_id=p1' && calls.indexOf(c) > calls.indexOf(post));
  assert.ok(reload, 'panel reloaded after duplicate');
  assert.equal(elements.get('maa_task_item_1').style.outline, '2px solid #3b82f6', 'new entry located and highlighted');
});

test('isSubOptionVisible mirrors backend truthiness for switch case names', () => {
  const { context } = workspace();
  const parent = { name: '作战指示', type: 'switch', value: false };
  const sub = { name: '攻击方法', depends_on: '作战指示', show_when: ['Yes'] };
  assert.equal(context.isSubOptionVisible(sub, [parent, sub]), false, 'parent off + show_when Yes should hide');
  parent.value = true;
  assert.equal(context.isSubOptionVisible(sub, [parent, sub]), true, 'parent on + show_when Yes should show');
  sub.show_when = ['No'];
  assert.equal(context.isSubOptionVisible(sub, [parent, sub]), false, 'parent on + show_when No should hide');
  parent.value = false;
  assert.equal(context.isSubOptionVisible(sub, [parent, sub]), true, 'parent off + show_when No should show');
  sub.show_when = ['开启'];
  assert.equal(context.isSubOptionVisible(sub, [parent, sub]), false, 'Chinese truthy case hides when parent off');
  sub.show_when = ['关闭'];
  assert.equal(context.isSubOptionVisible(sub, [parent, sub]), true, 'Chinese falsy case shows when parent off');
});

function maaPanelFetchRecorder(calls) {
  return async (url, options) => {
    calls.push({ url, options });
    if (options && options.method === 'POST') {
      return { ok: true, json: async () => ({ message: '已复制任务条目', entry_id: 'zzzzzzz' }) };
    }
    return { ok: true, json: async () => ({ is_maa: true, project_name: 'P', version: '1', config_type: 'mxu', active_config_id: 'i1', profiles: [{ id: 'i1', name: 'A', is_active: true }], tasks: [] }) };
  };
}

test('duplicate keeps unsaved edits safe: asks first, skips request on decline', async () => {
  const calls = [];
  const fakeInputs = [{ id: 'chk_maa_task_0', type: 'checkbox', checked: false, value: '' }];
  const { context } = workspace(maaPanelFetchRecorder(calls), {
    document: { querySelectorAll: (sel) => (String(sel).includes('maaConfigModal') ? fakeInputs : []) },
  });
  vm.runInContext("currentMAATaskID='t1'; currentSelectedProfileID='i1'; maaSnapshot='snap';", context);
  fakeInputs[0].checked = true; // 模拟用户在面板上做出未保存修改
  const pending = context.duplicateMAAEntry('e1');
  context.finishConfirmation(false);
  await pending;
  assert.equal(calls.filter(c => c.options && c.options.method === 'POST').length, 0, 'declined guard must not send duplicate request');
  assert.equal(calls.length, 0, 'no request at all should leave the panel untouched');
});
test('duplicate after confirmation sends the currently selected profile id', async () => {
  const calls = [];
  const fakeInputs = [{ id: 'chk_maa_task_0', type: 'checkbox', checked: false, value: '' }];
  const { context } = workspace(maaPanelFetchRecorder(calls), {
    document: { querySelectorAll: (sel) => (String(sel).includes('maaConfigModal') ? fakeInputs : []) },
  });
  vm.runInContext("currentMAATaskID='t1'; currentSelectedProfileID='i1'; maaSnapshot='snap';", context);
  fakeInputs[0].checked = true;
  const pending = context.duplicateMAAEntry('e1');
  context.finishConfirmation(true);
  await pending;
  const post = calls.find(c => c.options && c.options.method === 'POST');
  assert.ok(post, 'duplicate request should be sent after confirmation');
  assert.deepEqual(JSON.parse(post.options.body), { entry_id: 'e1', config_id: 'i1' });
});

test('rename sends the currently selected profile id', async () => {
  const calls = [];
  const { context } = workspace(maaPanelFetchRecorder(calls), {
    prompt: () => '制衣-上衣',
    document: { querySelectorAll: (sel) => (String(sel).includes('maaConfigModal') ? [{ id: 'x', type: 'text', checked: false, value: '' }] : []) },
  });
  vm.runInContext("currentMAATaskID='t1'; currentSelectedProfileID='i1';", context);
  await context.renameMAAEntry('e1');
  const put = calls.find(c => c.options && c.options.method === 'PUT');
  assert.ok(put, 'alias request should be sent');
  assert.deepEqual(JSON.parse(put.options.body), { display_name: '制衣-上衣', config_id: 'i1' });
});

test('isSubOptionVisible hides descendants through multi-level chains', () => {
  const { context } = workspace();
  const a = { name: 'A', type: 'switch', value: false };
  const b = { name: 'B', depends_on: 'A', show_when: ['Yes'], type: 'switch', value: true };
  const c = { name: 'C', depends_on: 'B', show_when: ['Yes'], type: 'switch', value: true };
  const opts = [a, b, c];
  assert.equal(context.isSubOptionVisible(b, opts), false, 'B hidden while A off');
  assert.equal(context.isSubOptionVisible(c, opts), false, 'C hidden through hidden ancestor B even with B on');
  a.value = true;
  assert.equal(context.isSubOptionVisible(b, opts), true, 'B shown once A on');
  assert.equal(context.isSubOptionVisible(c, opts), true, 'C shown when the whole chain is visible');
});

test('isSubOptionVisible survives cyclic dependencies without hanging', () => {
  const { context } = workspace();
  const x = { name: 'X', depends_on: 'Y', show_when: ['Yes'], type: 'switch', value: true };
  const y = { name: 'Y', depends_on: 'X', show_when: ['Yes'], type: 'switch', value: true };
  let result;
  assert.doesNotThrow(() => { result = context.isSubOptionVisible(x, [x, y]); });
  assert.equal(result, true, 'cycle falls back to the direct condition');
});

test('refreshMAADependents preserves three-level metadata, values, and stable cycle visibility', () => {
  const rows = new Map();
  const controls = new Map();
  const { context } = workspace(async () => ({ ok: true, json: async () => ({}) }), {
    document: {
      getElementById(id) {
        if (rows.has(id)) return rows.get(id);
        if (controls.has(id)) return controls.get(id);
        return { style: { display: 'flex' }, dataset: {}, textContent: '', value: '', checked: false, setAttribute() {}, appendChild() {} };
      },
    },
  });
  const row = id => {
    const value = { style: { display: 'unset' } };
    rows.set(id, value);
    return value;
  };
  const checkbox = (id, checked) => {
    const value = { checked };
    controls.set(id, value);
    return value;
  };
  const select = (id, value) => {
    const control = { value };
    controls.set(id, control);
    return control;
  };
  vm.runInContext(`currentMAAData={tasks:[{sub_options:[
    {name:'A',type:'switch',value:false},
    {name:'B',type:'switch',depends_on:'A',show_when:['Yes'],value:true},
    {name:'C',type:'select',depends_on:'B',show_when:['Yes'],value:'keep-me'},
    {name:'X',type:'switch',depends_on:'Y',show_when:['Yes'],value:true},
    {name:'Y',type:'switch',depends_on:'X',show_when:['Yes'],value:true}
  ]}]};`, context);
  const a = checkbox('chk_maa_sub_0_0', false);
  const b = checkbox('chk_maa_sub_0_1', true);
  const c = select('sel_maa_sub_0_2', 'live-value');
  const x = checkbox('chk_maa_sub_0_3', true);
  const y = checkbox('chk_maa_sub_0_4', true);
  const bRow = row('maa_sub_row_0_1');
  const cRow = row('maa_sub_row_0_2');
  const xRow = row('maa_sub_row_0_3');
  const yRow = row('maa_sub_row_0_4');

  context.refreshMAADependents(0);
  assert.equal(bRow.style.display, 'none', 'B hides while A is off');
  assert.equal(cRow.style.display, 'none', 'C remains hidden through hidden B');
  assert.equal(xRow.style.display, 'flex', 'cyclic X/Y resolves by the existing direct-condition fallback');
  assert.equal(yRow.style.display, 'flex', 'cyclic Y/X remains stable');

  a.checked = true;
  context.refreshMAADependents(0);
  assert.equal(bRow.style.display, 'flex', 'B appears after A turns on');
  assert.equal(cRow.style.display, 'flex', 'C appears once its full ancestor chain is visible');

  a.checked = false;
  context.refreshMAADependents(0);
  assert.equal(bRow.style.display, 'none');
  assert.equal(cRow.style.display, 'none');
  const liveValues = vm.runInContext('maaLiveSubOptions(0,currentMAAData.tasks[0])', context);
  assert.equal(liveValues[1].value, true, 'hidden B keeps its live value');
  assert.equal(liveValues[2].value, 'live-value', 'hidden C keeps its live value');
  context.refreshMAADependents(0);
  assert.equal(xRow.style.display, 'flex');
  assert.equal(yRow.style.display, 'flex');
});

test('duplicate ignores re-entrant clicks while a request is in flight', async () => {
  const calls = [];
  const { context } = workspace(maaPanelFetchRecorder(calls), {
    document: { querySelectorAll: (sel) => (String(sel).includes('maaConfigModal') ? [{ id: 'x', type: 'checkbox', checked: false, value: '' }] : []) },
  });
  vm.runInContext("currentMAATaskID='t1'; currentSelectedProfileID='i1'; maaSnapshot='snap';", context);
  const p1 = context.duplicateMAAEntry('e1');
  context.finishConfirmation(true);
  const p2 = context.duplicateMAAEntry('e1');
  context.finishConfirmation(true);
  await Promise.all([p1, p2]);
  const posts = calls.filter(c => c.options && c.options.method === 'POST');
  assert.equal(posts.length, 1, 'only one duplicate request despite re-entrant click');
  assert.deepEqual(JSON.parse(posts[0].options.body), { entry_id: 'e1', config_id: 'i1' });
});
