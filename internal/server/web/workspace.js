// Workspace interactions. Native browser APIs only; bundled into the executable.
let latestStatus = { state: 'IDLE', tasks: [] };
let cachedTasks = [];
let currentTaskID = '';
let autoScroll = true;
let taskFilter = 'all';
let logEntries = [];
let logScope = '';
let pendingActions = new Set();
let drawerSnapshot = '';
let maaSnapshot = '';
let draggedTaskID = '';
let lastRenderedState = '';
let statusAvailable = false;
let logConnected = false;
let logLoadVersion = 0;
let confirmResolver = null;
const stateLabels = { IDLE: '待命', PREPARE: '准备中', RUNNING: '运行中', COOLING: '冷却中', STOPPING: '正在停止', WAITING_SCHEDULE: '等待执行时间' };

function escapeHtml(str) {
  if (!str) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

// ---- 访问密码：本机回环请求服务端免密放行；其他设备首次输入后缓存到 localStorage，之后自动携带 ----
const TOKEN_KEY = 'autonexus_token';
function getAuthToken() {
  try { return localStorage.getItem(TOKEN_KEY) || ''; } catch (e) { return ''; }
}

// ---- 界面主题：模式来自服务端配置 (auto/dark/light)，auto 经 matchMedia 跟随系统并同步原生标题栏 ----
function applyTheme() {
  const mode = (latestStatus && latestStatus.theme) || 'auto';
  let dark = mode === 'dark';
  if (mode !== 'dark' && mode !== 'light' && window.matchMedia) {
    dark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  }
  const resolved = dark ? 'dark' : 'light';
  try { document.documentElement.dataset.theme = resolved; } catch (e) {}
  if (window.__autonexusSetTheme) { try { window.__autonexusSetTheme(resolved); } catch (e) {} }
  return resolved;
}
if (window.matchMedia) {
  try { window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', applyTheme); } catch (e) { /* 旧浏览器无 addEventListener */ }
}
function showAuthGate() {
  try {
    if (document.getElementById('authGate')) return;
    const overlay = document.createElement('div');
    overlay.id = 'authGate';
    overlay.style.cssText = 'position:fixed;inset:0;z-index:9999;background:rgba(10,12,20,.97);display:flex;align-items:center;justify-content:center;padding:24px;';
    overlay.innerHTML = '<div style="width:min(92vw,380px);background:#161a26;border:1px solid #2a3042;border-radius:14px;padding:28px;color:#e5e7eb;text-align:center;">'
      + '<div style="font-size:18px;font-weight:600;margin-bottom:6px;">访问验证</div>'
      + '<div style="font-size:13px;color:#9aa3b5;margin-bottom:18px;line-height:1.7;">输入访问密码进入控制台</div>'
      + '<input id="authGateInput" type="password" placeholder="访问密码" autocomplete="current-password" style="width:100%;box-sizing:border-box;padding:10px 12px;border-radius:8px;border:1px solid #2a3042;background:#0d101a;color:#e5e7eb;font-size:14px;outline:none;">'
      + '<div id="authGateError" style="min-height:18px;font-size:12px;color:#f87171;margin:8px 0;"></div>'
      + '<button id="authGateSubmit" style="width:100%;padding:10px;border:none;border-radius:8px;background:#3b82f6;color:#fff;font-size:14px;cursor:pointer;">进入控制台</button>'
      + '</div>';
    (document.body || document.documentElement).appendChild(overlay);
    const input = document.getElementById('authGateInput');
    const btn = document.getElementById('authGateSubmit');
    const showError = msg => { const el = document.getElementById('authGateError'); if (el) el.textContent = msg; };
    async function submit() {
      const candidate = (input.value || '').trim();
      if (!candidate) return;
      btn.disabled = true;
      btn.textContent = '验证中...';
      try {
        // 用候选密码直接请求状态接口完成验证，通过后缓存到本地等效于配对
        const res = await fetch('/api/v1/status', { headers: { 'Authorization': 'Bearer ' + candidate } });
        if (res.ok) {
          try { localStorage.setItem(TOKEN_KEY, candidate); } catch (e) {}
          location.reload();
          return;
        }
        showError(res.status === 401 ? '密码不正确，请重试' : `验证失败 (${res.status})`);
      } catch (e) {
        showError('网络错误，请重试');
      }
      btn.disabled = false;
      btn.textContent = '进入控制台';
    }
    btn.addEventListener('click', submit);
    input.addEventListener('keydown', e => { if (e.key === 'Enter') submit(); });
    if (input.focus) input.focus();
  } catch (e) { /* 测试环境无完整 DOM */ }
}

function formatCommandLine(args) {
  if (!args || !args.length) return '';
  return args.map(arg => {
    if (arg === '') return '""';
    if (/[\s"]/.test(arg)) {
      let escaped = arg.replace(/"/g, '\\"');
      const trailing = escaped.match(/\\+$/);
      if (trailing) {
        escaped += trailing[0];
      }
      return `"${escaped}"`;
    }
    return arg;
  }).join(' ');
}

function stripWrappingQuotes(str) {
  if (!str) return '';
  str = str.trim();
  if ((str.startsWith('"') && str.endsWith('"')) || (str.startsWith("'") && str.endsWith("'"))) {
    return str.slice(1, -1).trim();
  }
  return str;
}

function parseCommandLine(cmdStr) {
  if (!cmdStr || !cmdStr.trim()) return [];
  const args = [];
  let current = '';
  let inQuote = false;
  let quoteChar = '';
  let hasToken = false;
  let i = 0;

  while (i < cmdStr.length) {
    const c = cmdStr[i];
    if (!inQuote && /\s/.test(c)) {
      if (hasToken) {
        args.push(current);
        current = '';
        hasToken = false;
      }
      i++;
      continue;
    }

    if (c === '\\') {
      let slashes = 0;
      while (i < cmdStr.length && cmdStr[i] === '\\') {
        slashes++;
        i++;
      }
      if (i < cmdStr.length && (cmdStr[i] === '"' || cmdStr[i] === "'") && (inQuote ? cmdStr[i] === quoteChar : true)) {
        current += '\\'.repeat(Math.floor(slashes / 2));
        if (slashes % 2 === 1) {
          current += cmdStr[i];
          hasToken = true;
          i++;
        }
      } else {
        current += '\\'.repeat(slashes);
        hasToken = true;
      }
      continue;
    }

    if (!inQuote && (c === '"' || c === "'")) {
      inQuote = true;
      quoteChar = c;
      hasToken = true;
      i++;
    } else if (inQuote && c === quoteChar) {
      inQuote = false;
      quoteChar = '';
      i++;
    } else {
      current += c;
      hasToken = true;
      i++;
    }
  }

  if (hasToken) {
    args.push(current);
  }
  return args;
}

function notifyUser(message, error = false) {
  const item = document.createElement('div');
  item.className = 'toast' + (error ? ' error' : '');
  item.setAttribute('role', error ? 'alert' : 'status');
  item.textContent = message;
  document.getElementById('toastRegion').appendChild(item);
  setTimeout(() => item.remove(), error ? 8000 : 3500);
}

function confirmAction(message) {
  if (confirmResolver) return Promise.resolve(false);
  document.getElementById('confirmMessage').textContent = message;
  document.getElementById('confirmModal').style.display = 'flex';
  return new Promise(resolve => { confirmResolver = resolve; });
}
function finishConfirmation(accepted) {
  document.getElementById('confirmModal').style.display = 'none';
  const resolve = confirmResolver;
  confirmResolver = null;
  resolve?.(accepted);
}

async function requestAPI(url, options = {}) {
  const token = getAuthToken();
  if (token) {
    options = Object.assign({}, options);
    options.headers = Object.assign({}, options.headers, { 'Authorization': 'Bearer ' + token });
  }
  const response = await fetch(url, options);
  let data;
  try { data = await response.json(); } catch { data = {}; }
  if (response.status === 401) showAuthGate();
  if (!response.ok) throw new Error(data.error || `请求失败 (${response.status})`);
  return data;
}

async function performAction(key, url, options, message) {
  if (pendingActions.has(key)) return;
  pendingActions.add(key);
  updateControls();
  try {
    await requestAPI(url, options);
    if (message) notifyUser(message);
    await fetchStatus();
  } catch (error) {
    notifyUser(error.message || '连接失败，请重试', true);
  } finally {
    pendingActions.delete(key);
    updateControls();
  }
}

function updateControls() {
  const busy = latestStatus.state !== 'IDLE';
  const tasks = latestStatus.tasks || [];
  const force = document.getElementById('chkForceRun').checked;
  const count = tasks.filter(t => t.enabled && (force || !t.is_completed_today)).length;
  const start = document.getElementById('btnStart');
  start.disabled = !statusAvailable || busy || !count || pendingActions.has('start');
  start.textContent = pendingActions.has('start') ? '正在启动…' : busy ? '队列执行中' : `启动队列 · ${count} 项`;
  start.title = busy ? '请等待当前调度结束' : count ? '按队列顺序执行' : '没有待执行的已启用任务';
  const stop = document.getElementById('btnStop');
  stop.disabled = !statusAvailable || !busy || latestStatus.state === 'STOPPING' || pendingActions.has('stop');
  stop.textContent = latestStatus.state === 'STOPPING' || pendingActions.has('stop') ? '正在停止…' : '停止队列';
  document.getElementById('runControl').dataset.state = latestStatus.state;
  document.getElementById('queueSummary').textContent = busy ? '' : `待执行 ${count} 项`;
  document.getElementById('runtimeDetails').hidden = !busy;
  const counts = {
    pending: tasks.filter(t => t.enabled && !t.is_completed_today).length,
    done: tasks.filter(t => t.is_completed_today).length,
    error: tasks.filter(t => t.last_success === false && !t.is_completed_today).length,
  };
  for (const [filter, label] of [['pending','待执行'],['done','已完成'],['error','异常']]) {
    const button = document.querySelector(`[data-filter="${filter}"]`);
    if (button) button.textContent = `${label} ${counts[filter]}`;
  }
  document.querySelectorAll('[data-idle-only]').forEach(button => {
    const id = button.closest('.task-card')?.dataset.taskId;
    const edge = button.dataset.action === 'up' && id === tasks[0]?.id || button.dataset.action === 'down' && id === tasks[tasks.length - 1]?.id;
    button.disabled = !statusAvailable || busy || pendingActions.size > 0 || edge;
  });
}

function updateConnectionIndicator() {
  const text = !statusAvailable ? '状态连接中断' : !logConnected ? '日志重连中' : '已连接';
  document.getElementById('wsText').textContent = text;
  document.getElementById('wsDot').className = statusAvailable && logConnected ? 'dot connected' : 'dot';
  const refresh = document.getElementById('btnRefreshLogs');
  if (refresh) refresh.hidden = !logScope && logConnected;
}

function renderStatus(data) {
  const previousStart = latestStatus.tasks?.find(task => task.id === logScope)?.last_start_time;
  latestStatus = data;
  statusAvailable = true;
  currentTaskID = data.current_task_id || '';

  const stateBadge = document.getElementById('stateBadge');
  if (stateBadge) {
    stateBadge.textContent = stateLabels[data.state] || data.state;
    stateBadge.className = `badge badge-${data.state}`;
  }

  const nameEl = document.getElementById('currentTaskName');
  if (nameEl) nameEl.textContent = data.current_task_name || '无运行中任务';

  const seconds = data.elapsed_seconds || 0;
  const elapsedEl = document.getElementById('elapsedTime');
  if (elapsedEl) elapsedEl.textContent = seconds >= 60 ? `${Math.floor(seconds / 60)}分${seconds % 60}秒` : `${seconds}秒`;

  const progEl = document.getElementById('progressText');
  if (progEl) {
    const idx = (data.tasks || []).findIndex(t => t.id === data.current_task_id);
    progEl.textContent = data.current_task_id && idx >= 0 ? `第 ${idx + 1} 项` : '—';
  }

  const cdBox = document.getElementById('cooldownBox');
  if (cdBox) {
    if (data.state === 'COOLING' && data.cooldown_remaining > 0) {
      cdBox.style.display = 'block';
      const cdSec = document.getElementById('cooldownSec');
      if (cdSec) cdSec.textContent = `${data.cooldown_remaining}s`;
    } else {
      cdBox.style.display = 'none';
    }
  }

  const waitBox = document.getElementById('waitScheduleBox');
  if (waitBox) {
    if (data.state === 'WAITING_SCHEDULE' && data.schedule_wait_remaining > 0) {
      waitBox.style.display = 'block';
      const targetEl = document.getElementById('waitTargetTime');
      if (targetEl) targetEl.textContent = data.schedule_wait_target || '04:00';
      const remSec = data.schedule_wait_remaining;
      const h = Math.floor(remSec / 3600);
      const m = Math.floor((remSec % 3600) / 60);
      const s = remSec % 60;
      let timeText = `${remSec}s`;
      if (h > 0) timeText = `${h}h ${m}m ${s}s`;
      else if (m > 0) timeText = `${m}m ${s}s`;
      const waitSec = document.getElementById('waitScheduleSec');
      if (waitSec) waitSec.textContent = timeText;
    } else {
      waitBox.style.display = 'none';
    }
  }

  const inpTime = document.getElementById('inpScheduleTime');
  const chkSched = document.getElementById('chkAutoSchedule');
  if (chkSched && inpTime && document.activeElement !== inpTime) {
    chkSched.checked = !!data.schedule_enabled;
    if (data.schedule_time) inpTime.value = data.schedule_time;
  }
  const tip = document.getElementById('nextScheduleTip');
  if (tip) {
    tip.textContent = data.schedule_enabled ? `下次: ${data.schedule_next_run || '-'}` : '未启用';
  }

  const tasksStr = JSON.stringify(data.tasks);
  if (tasksStr !== JSON.stringify(cachedTasks)) {
    cachedTasks = data.tasks || [];
    renderTaskList(cachedTasks);
  } else if (lastRenderedState !== `${data.state}:${data.current_task_id}`) {
    renderTaskList(cachedTasks);
  }
  lastRenderedState = `${data.state}:${data.current_task_id}`;

  updateControls();
  const nextStart = data.tasks?.find(task => task.id === logScope)?.last_start_time;
  if (logScope && previousStart !== nextStart) selectTaskLogs(logScope);
}

async function fetchStatus() {
  try {
    renderStatus(await requestAPI('/api/v1/status'));
    updateSecurityHint();
    applyTheme();
    updateConnectionIndicator();
  } catch {
    statusAvailable = false;
    updateConnectionIndicator();
    updateControls();
  }
};

function setTaskFilter(filter) {
  taskFilter = filter;
  document.querySelectorAll('[data-filter]').forEach(button => {
    button.classList.toggle('selected', button.dataset.filter === filter);
    button.setAttribute('aria-pressed', button.dataset.filter === filter);
  });
  renderTaskList(cachedTasks);
  document.getElementById('taskList').scrollTop = 0;
}

function renderTaskList(tasks) {
  const container = document.getElementById('taskList');
  const previousScroll = container.scrollTop;
  const openMenuID = container.querySelector('.task-more[open]')?.closest('.task-card')?.dataset.taskId;
  container.replaceChildren();
  document.getElementById('taskCount').textContent = `${tasks.length} 项任务`;
  const visible = tasks.map((task, index) => ({ task, index })).filter(({task}) => taskFilter === 'all' || (taskFilter === 'pending' && task.enabled && !task.is_completed_today) || (taskFilter === 'done' && task.is_completed_today) || (taskFilter === 'error' && task.last_success === false && !task.is_completed_today));
  if (!visible.length) {
    const empty = document.createElement('div');
    empty.className = 'empty-hint';
    empty.textContent = tasks.length ? '当前筛选下没有任务' : '还没有任务，点击「添加任务」选择一个程序开始。';
    container.appendChild(empty);
  }
  visible.forEach(({task: t, index}) => {
    const active = t.id === currentTaskID;
    const failed = t.last_success === false && !t.is_completed_today;
    const label = active ? stateLabels[latestStatus.state] || '执行中' : !t.enabled ? '未启用' : t.is_completed_today ? '今日已完成' : failed ? '执行失败' : '待执行';
    const card = document.createElement('article');
    card.className = `task-card${active ? ' active' : ''}${!t.enabled ? ' disabled' : ''}${logScope === t.id ? ' selected-log' : ''}`;
    card.dataset.taskId = t.id;
    card.innerHTML = `<div class="task-top">
      <span class="task-index" title="拖动调整队列顺序">${String(index + 1).padStart(2, '0')}</span>
      <div class="task-info"><div class="task-name" title="${escapeHtml(t.name || t.id)}">${escapeHtml(t.name || t.id)}</div><div class="task-sub">${t.is_maa ? 'MAA · ' + escapeHtml(t.maa_config_name || '默认预设') : '外部程序'} · ${t.last_duration ? '上次耗时 ' + escapeHtml(t.last_duration) : '尚未运行'}</div></div>
      <span class="task-state ${active ? 'running' : failed ? 'error' : t.is_completed_today ? 'done' : ''}">${label}</span>
      <label class="switch"><input type="checkbox" aria-label="参与队列：${escapeHtml(t.name || t.id)}" ${t.enabled ? 'checked' : ''}><span class="slider"></span></label>
    </div>
    ${failed ? `<details class="task-error"><summary>${escapeHtml(t.last_error || '执行异常，请查看任务日志')}</summary><p>${escapeHtml(t.last_error || '执行异常，请查看任务日志')}</p></details>` : ''}
    <div class="task-footer">
      <div class="task-actions"><button class="btn btn-secondary btn-sm" data-action="run" data-idle-only>运行</button><button class="btn btn-secondary btn-sm" data-action="config">配置</button><button class="btn btn-secondary btn-sm" data-action="logs">日志</button>
      <details class="task-more"><summary class="btn btn-secondary btn-sm" aria-label="更多任务操作">•••</summary><div class="more-menu"><div class="task-record">上次启动<br>${escapeHtml(t.last_start_time || '尚无记录')}</div><button data-action="edit">启动设置</button><button data-action="up" data-idle-only ${index === 0 ? 'disabled' : ''}>上移一位</button><button data-action="down" data-idle-only ${index === tasks.length - 1 ? 'disabled' : ''}>下移一位</button><button data-action="delete" style="color:var(--pill-err-text)">删除任务</button></div></details></div></div>`;
    card.querySelector('input').onchange = async event => {
      const checkbox = event.target;
      checkbox.disabled = true;
      await performAction(`toggle:${t.id}`, `/api/v1/tasks/${encodeURIComponent(t.id)}/toggle`, { method:'POST' }, '队列参与状态已更新');
      checkbox.checked = !!cachedTasks.find(task => task.id === t.id)?.enabled;
      checkbox.disabled = false;
    };
    const actions = { run: () => runSingleTask(t.id), config: () => t.is_maa ? openMAAConfigModal(t.id) : openTaskModal(t), logs: () => selectTaskLogs(t.id), edit: () => openTaskModal(t), up: () => moveTask(index,-1), down: () => moveTask(index,1), delete: () => deleteTaskByIndex(index) };
    const menu = card.querySelector('.task-more');
    menu.open = openMenuID === t.id;
    menu.addEventListener('toggle', () => positionMoreMenu(menu));
    card.querySelectorAll('[data-action]').forEach(button => { button.onclick = () => { menu.open = false; actions[button.dataset.action](); }; });
    // Dragging starts on the handle only, preserving text selection and controls.
    const handle = card.querySelector('.task-index');
    handle.draggable = latestStatus.state === 'IDLE' && taskFilter === 'all';
    handle.ondragstart = event => { draggedTaskID = t.id; event.dataTransfer.setData('text/plain', t.id); event.dataTransfer.effectAllowed = 'move'; card.classList.add('dragging'); };
    handle.ondragend = () => { draggedTaskID = ''; card.classList.remove('dragging'); };
    card.ondragover = event => { if (draggedTaskID && latestStatus.state === 'IDLE' && taskFilter === 'all') event.preventDefault(); };
    card.ondrop = event => {
      event.preventDefault();
      const from = cachedTasks.findIndex(task => task.id === draggedTaskID);
      const to = cachedTasks.findIndex(task => task.id === t.id);
      if (from >= 0 && to >= 0 && from !== to && latestStatus.state === 'IDLE') moveTask(from,to-from);
    };
    container.appendChild(card);
  });
  updateControls();
  container.scrollTop = previousScroll;
  container.querySelectorAll('[data-action=up]').forEach(button => { if (button.closest('.task-card').dataset.taskId === tasks[0]?.id) button.disabled = true; });
  container.querySelectorAll('[data-action=down]').forEach(button => { if (button.closest('.task-card').dataset.taskId === tasks[tasks.length-1]?.id) button.disabled = true; });
};

// Fixed menus stay clear of the independently scrolling task list's edges.
function positionMoreMenu(details) {
  if (!details.open) return;
  const menu = details.querySelector('.more-menu');
  const anchor = details.querySelector('summary').getBoundingClientRect();
  menu.style.position = 'fixed';
  menu.style.left = `${Math.max(8, Math.min(anchor.right - menu.offsetWidth, window.innerWidth - menu.offsetWidth - 8))}px`;
  const below = window.innerHeight - anchor.bottom - 8;
  menu.style.top = `${Math.max(8, below >= menu.offsetHeight ? anchor.bottom + 4 : anchor.top - menu.offsetHeight - 4)}px`;
}

function switchMobileView(view) {
  document.getElementById('workspace').dataset.view = view;
  document.querySelectorAll('[data-mobile-view]').forEach(button => { button.classList.toggle('selected',button.dataset.mobileView === view); button.setAttribute('aria-pressed',button.dataset.mobileView === view); });
  if (window.innerWidth <= 760) {
    if (view === 'logs') renderWorkspaceLogs();
    else document.querySelector('main').scrollTop = 0;
  }
}

function entryMatches(entry) {
  const query = document.getElementById('logSearch').value.toLowerCase();
  const stream = document.getElementById('logStream').value;
  return (!stream || entry.stream === stream) && (!query || `${entry.message || ''} ${entry.task_id || ''}`.toLowerCase().includes(query));
}

function createLogRow(entry) {
  const row = document.createElement('div');
  row.className = `log-line stream-${['stdout','stderr','system'].includes(entry.stream) ? entry.stream : 'stdout'}`;
  const time = document.createElement('span');
  time.className = 'log-time'; time.textContent = entry.timestamp || '';
  row.append(time,document.createTextNode((entry.task_id ? `[${entry.task_id}] ` : '') + (entry.message || '')));
  return row;
}

function renderWorkspaceLogs() {
  const body = document.getElementById('terminalBody');
  const mobile = window.innerWidth <= 760;
  const scroller = mobile ? document.querySelector('main') : body;
  const oldScroll = scroller.scrollTop;
  body.replaceChildren();
  const entries = logScope ? scopedLogEntries : logEntries;
  const filtered = entries.filter(entryMatches);
  const fragment = document.createDocumentFragment();
  filtered.forEach(entry => fragment.appendChild(createLogRow(entry)));
  body.appendChild(fragment);
  if (!filtered.length) {
    const hint = document.createElement('div'); hint.className = 'empty-hint';
    hint.textContent = entries.length ? '没有匹配的日志' : '暂无日志，运行任务后将在这里显示。'; body.appendChild(hint);
  }
  document.getElementById('logCount').textContent = `${filtered.length} / ${entries.length} 行${logScope ? ' · 最近一次运行 · 最多 1200 行' : ' · 最多保留 1200 行'}`;
  if (!mobile || document.getElementById('workspace').dataset.view === 'logs') {
    scroller.scrollTop = autoScroll ? scroller.scrollHeight : oldScroll;
  }
}
let scopedLogEntries = [];
function appendLog(entry) {
  logEntries.push(entry);
  if (logEntries.length > 1200) logEntries.shift();
  if (logScope && entry.task_id === logScope) {
    // A new run replaces the previous run's history in the task log endpoint.
    scopedLogEntries.push(entry);
    if (scopedLogEntries.length > 1200) scopedLogEntries.shift();
  }
  if (!logScope || entry.task_id === logScope) scheduleLogRender();
};
let logRenderPending = false;
function scheduleLogRender() {
  if (logRenderPending) return;
  logRenderPending = true;
  requestAnimationFrame(() => { logRenderPending = false; renderWorkspaceLogs(); });
}

async function selectTaskLogs(id = '') {
  const version = ++logLoadVersion;
  logScope = id;
  document.querySelectorAll('.task-card').forEach(card => {
    card.classList.toggle('selected-log', !!id && card.dataset.taskId === id);
  });
  updateConnectionIndicator();
  document.getElementById('logTaskSelect').value = id;
  const task = cachedTasks.find(t => t.id === id);
  document.getElementById('logTitle').textContent = task ? task.name || task.id : '实时日志';
  switchMobileView('logs');
  scopedLogEntries = [];
  renderWorkspaceLogs();
  if (!id) return;
  try {
    const data = await requestAPI(`/api/v1/tasks/${encodeURIComponent(id)}/logs`);
    if (version !== logLoadVersion) return;
    scopedLogEntries = (data.logs || []).slice(-1200);
    renderWorkspaceLogs();
  } catch (error) { if (version === logLoadVersion) notifyUser(error.message,true); }
}
const openTaskLogModal = selectTaskLogs;
function clearLogs() { if(logScope) scopedLogEntries=[]; else logEntries=[]; renderWorkspaceLogs(); };
function updateLogFollowControls() {
  const button = document.getElementById('btnAutoScroll');
  button.textContent = autoScroll ? '跟随最新' : '已暂停';
  button.setAttribute('aria-pressed', String(autoScroll));
  document.getElementById('btnResumeLogs').hidden = autoScroll;
}
function resumeLogFollow() { autoScroll = true; updateLogFollowControls(); renderWorkspaceLogs(); }
function toggleAutoScroll() { autoScroll = !autoScroll; updateLogFollowControls(); if(autoScroll) renderWorkspaceLogs(); }
async function copyWorkspaceLogs() {
  const entries = (logScope ? scopedLogEntries : logEntries).filter(entryMatches);
  try { await navigator.clipboard.writeText(entries.map(entry => `[${entry.timestamp || ''}] ${entry.message || ''}`).join('\n')); notifyUser('已复制当前筛选日志'); }
  catch { notifyUser('复制失败，请选中文本后手动复制',true); }
}

const startQueue = () => performAction('start',`/api/v1/queue/start?force=${document.getElementById('chkForceRun').checked}`,{method:'POST'},'已提交队列启动请求');
const stopQueue = () => performAction('stop','/api/v1/queue/stop',{method:'POST'},'已提交停止请求');
const skipWait = () => performAction('skip','/api/v1/queue/skip-wait',{method:'POST'},'已跳过等待');
const runSingleTask = id => performAction('run',`/api/v1/tasks/${encodeURIComponent(id)}/run`,{method:'POST'},'已提交任务启动请求');
async function moveTask(index, offset) {
  const target=index+offset;
  if(target<0 || target>=cachedTasks.length || latestStatus.state!=='IDLE') return;
  const tasks=[...cachedTasks]; tasks.splice(target,0,tasks.splice(index,1)[0]);
  await performAction('reorder','/api/v1/tasks/reorder',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({task_ids:tasks.map(t=>t.id)})},'队列顺序已更新');
};
async function updateScheduleConfig() {
  const controls = [document.getElementById('chkAutoSchedule'),document.getElementById('inpScheduleTime')];
  controls.forEach(input=>input.disabled=true);
  await performAction('schedule','/api/v1/schedule',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({schedule_enabled:controls[0].checked,schedule_time:controls[1].value || '04:05'})},'每日调度已保存');
  controls.forEach(input=>input.disabled=false);
};

async function updateAuthToken() {
  const input = document.getElementById('inpAuthToken');
  const token = (input.value || '').trim();
  if (token && token.length < 6) { notifyUser('访问密码至少需要 6 位', true); return; }
  if (!token) { notifyUser('请输入新密码；如需取消密码请点「清除」', true); return; }
  input.disabled = true;
  try {
    await requestAPI('/api/v1/auth/token', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token }) });
    // 本机立即切换到新密码，避免下一次请求 401
    try { localStorage.setItem(TOKEN_KEY, token); } catch (e) {}
    input.value = '';
    notifyUser('访问密码已更新，其他设备需重新验证');
    await fetchStatus();
  } catch (error) {
    notifyUser(error.message || '保存失败，请重试', true);
  } finally {
    input.disabled = false;
    updateSecurityHint();
  }
};

async function clearAuthToken() {
  if (!(await confirmAction('确定清除访问密码？清除后局域网内所有设备都可直接访问本控制台。'))) return;
  try {
    await requestAPI('/api/v1/auth/token', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token: '' }) });
    try { localStorage.removeItem(TOKEN_KEY); } catch (e) {}
    notifyUser('访问密码已清除');
    await fetchStatus();
  } catch (error) {
    notifyUser(error.message || '操作失败，请重试', true);
  }
  updateSecurityHint();
};

function openSettingsModal() {
  const chkLAN = document.getElementById('chkLANAccess');
  if (chkLAN) chkLAN.checked = !!latestStatus.lan_enabled;
  const selTheme = document.getElementById('selTheme');
  if (selTheme) selTheme.value = (latestStatus && latestStatus.theme) || 'auto';
  updateSecurityHint();
  document.getElementById('settingsModal').style.display = 'flex';
};
function closeSettingsModal() {
  document.getElementById('settingsModal').style.display = 'none';
};

async function updateLANAccess() {
  const chk = document.getElementById('chkLANAccess');
  chk.disabled = true;
  await performAction('lan', '/api/v1/settings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ lan_enabled: chk.checked }) }, '局域网访问配置已保存');
  chk.disabled = false;
  updateSecurityHint();
};

async function updateThemeSetting() {
  const sel = document.getElementById('selTheme');
  sel.disabled = true;
  await performAction('theme', '/api/v1/settings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ theme: sel.value }) }, '界面主题已保存');
  sel.disabled = false;
  applyTheme();
};

// 局域网开启但未设密码时在设置中显示红字风险提醒
function updateSecurityHint() {
  const warn = document.getElementById('authWarnLine');
  if (!warn) return;
  const lanOn = !!(latestStatus && latestStatus.lan_enabled);
  const hasPassword = !!(latestStatus && latestStatus.auth_token_set);
  warn.style.display = lanOn && !hasPassword ? 'block' : 'none';
  const hint = document.getElementById('authSetHint');
  if (hint) hint.textContent = hasPassword ? '已设置访问密码' : '未设置访问密码';
  const clearBtn = document.getElementById('btnClearToken');
  if (clearBtn) clearBtn.style.display = hasPassword ? '' : 'none';
}

function formSnapshot(id) {
  return JSON.stringify([...document.querySelectorAll(`#${id} input, #${id} select`)].map(input=>[input.id,input.type==='checkbox'?input.checked:input.value]));
}

function openTaskModal(task = null) {
  const modal = document.getElementById('taskModal');
  const title = document.getElementById('modalTitle');
  const mode = document.getElementById('taskEditMode');
  const idInput = document.getElementById('taskIdInput');

  if (task) {
    title.innerText = '编辑任务配置';
    mode.value = 'edit';
    idInput.value = task.id;
    idInput.disabled = true;
    document.getElementById('taskNameInput').value = task.name || '';
    document.getElementById('taskExeInput').value = task.executable || '';
    document.getElementById('taskArgsInput').value = formatCommandLine(task.args);
    document.getElementById('taskDirInput').value = task.working_dir || '';
    document.getElementById('taskGamesInput').value = (task.game_process_names || []).join(', ');
    document.getElementById('taskTimeoutInput').value = task.timeout_seconds ?? 1800;
    document.getElementById('taskNoLogInput').value = task.no_log_timeout_seconds ?? 0;
    document.getElementById('taskCooldownInput').value = task.cooldown_seconds ?? 5;
    document.getElementById('taskRefreshInput').value = task.refresh_time || '04:00';
  } else {
    title.innerText = '添加任务';
    mode.value = 'create';
    idInput.value = 'task_' + Date.now().toString(36);
    idInput.disabled = false;
    document.getElementById('taskNameInput').value = '';
    document.getElementById('taskExeInput').value = '';
    document.getElementById('taskArgsInput').value = '';
    document.getElementById('taskDirInput').value = '';
    document.getElementById('taskGamesInput').value = '';
    document.getElementById('taskTimeoutInput').value = 1800;
    document.getElementById('taskNoLogInput').value = 0;
    document.getElementById('taskCooldownInput').value = 5;
    document.getElementById('taskRefreshInput').value = '04:00';
  }

  document.getElementById('advancedSettings').open = false;
  modal.style.display = 'flex';
  drawerSnapshot = formSnapshot('taskModal');
  document.getElementById('taskNameInput').focus();
}

async function closeTaskModal(force = false) {
  if (!force && drawerSnapshot !== formSnapshot('taskModal') && !await confirmAction('有尚未保存的修改，确定放弃吗？')) return;
  document.getElementById('taskModal').style.display = 'none';
}

async function saveTaskFromModal() {
  if (pendingActions.has('saveTask')) return;
  const name = document.getElementById('taskNameInput');
  const exe = document.getElementById('taskExeInput');
  if (!name.value.trim() || !exe.value.trim()) {
    notifyUser('请填写任务名称并选择执行程序', true);
    (!name.value.trim() ? name : exe).focus();
    return;
  }
  for (const id of ['taskTimeoutInput', 'taskNoLogInput', 'taskCooldownInput']) {
    const input = document.getElementById(id);
    if (input.value !== '' && (!Number.isInteger(Number(input.value)) || Number(input.value) < 0)) {
      notifyUser('时间参数请填写大于等于 0 的整数', true);
      input.focus();
      return;
    }
  }

  const mode = document.getElementById('taskEditMode').value;
  const id = document.getElementById('taskIdInput').value.trim();
  const dir = document.getElementById('taskDirInput').value.trim();
  const gamesRaw = document.getElementById('taskGamesInput').value;
  const games = gamesRaw ? gamesRaw.split(/[,，]/).map(s => s.trim()).filter(Boolean) : [];
  const timeout = parseInt(document.getElementById('taskTimeoutInput').value, 10);
  const noLog = parseInt(document.getElementById('taskNoLogInput').value, 10);
  const cooldown = parseInt(document.getElementById('taskCooldownInput').value, 10);
  const refreshTime = document.getElementById('taskRefreshInput').value.trim();
  const argsRaw = document.getElementById('taskArgsInput').value.trim();
  const args = parseCommandLine(argsRaw);

  let isEnabled = true;
  let lastStartTime = '';
  let lastDuration = '';
  let lastSuccess = null;
  let lastError = '';
  let maaConfigId = '';
  let maaConfigName = '';

  if (mode === 'edit') {
    const existing = cachedTasks.find(t => t.id === id);
    if (existing) {
      if (typeof existing.enabled === 'boolean') {
        isEnabled = existing.enabled;
      }
      lastStartTime = existing.last_start_time;
      lastDuration = existing.last_duration;
      lastSuccess = existing.last_success;
      lastError = existing.last_error;
      const pathChanged = (existing.executable !== exe.value.trim() || (existing.working_dir || '') !== dir);
      if (!pathChanged) {
        maaConfigId = existing.maa_config_id || '';
        maaConfigName = existing.maa_config_name || '';
      }
    }
  }

  const payload = {
    id: id,
    name: name.value.trim() || id || '未命名任务',
    enabled: isEnabled,
    executable: exe.value.trim(),
    args: args,
    working_dir: dir,
    game_process_names: games,
    timeout_seconds: isNaN(timeout) ? 1800 : timeout,
    no_log_timeout_seconds: isNaN(noLog) ? 0 : noLog,
    cooldown_seconds: isNaN(cooldown) ? 5 : cooldown,
    refresh_time: refreshTime || '04:00',
    last_start_time: lastStartTime,
    last_duration: lastDuration,
    last_success: lastSuccess,
    last_error: lastError,
    maa_config_id: maaConfigId,
    maa_config_name: maaConfigName,
  };

  pendingActions.add('saveTask');
  const button = document.getElementById('btnSaveTask');
  button.disabled = true;
  button.textContent = '正在保存…';

  try {
    const url = mode === 'edit' ? `/api/v1/tasks/${encodeURIComponent(id)}` : '/api/v1/tasks';
    const method = mode === 'edit' ? 'PUT' : 'POST';
    await requestAPI(url, {
      method: method,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    notifyUser('任务已成功保存');
    await closeTaskModal(true);
    await fetchStatus();
  } catch (e) {
    notifyUser(e.message || '保存失败', true);
  } finally {
    pendingActions.delete('saveTask');
    button.disabled = false;
    button.textContent = '保存任务';
  }
}

async function deleteTaskByIndex(index) {
  const task = cachedTasks[index];
  if (!task) return;
  if (await confirmAction(`确定要删除任务 [${task.name || task.id}] 吗？`)) {
    await performAction(`delete:${task.id}`, `/api/v1/tasks/${encodeURIComponent(task.id)}`, { method: 'DELETE' }, '任务已删除');
  }
}

let currentMAATaskID = '';
let currentMAAData = null;
let currentSelectedProfileID = '';
let maaLoadVersion = 0;

async function openMAAConfigModal(taskID, configID = '') {
  currentMAATaskID = taskID;
  const modal = document.getElementById('maaConfigModal');
  const container = document.getElementById('maaTaskListContainer');
  container.innerHTML = '<div style="color:var(--text-muted); padding:30px; text-align:center;">正在检测并加载自动化配置...</div>';
  modal.style.display = 'flex';
  await loadMAAConfig(taskID, configID);
}

async function closeMAAConfigModal(force = false) {
  if (!force && maaSnapshot && maaSnapshot !== formSnapshot('maaConfigModal') && !await confirmAction('有尚未保存的自动化配置，确定放弃吗？')) return;
  currentMAATaskID = '';
  currentMAAData = null;
  currentSelectedProfileID = '';
  maaSnapshot = '';
  document.getElementById('maaConfigModal').style.display = 'none';
}

async function loadMAAConfig(taskID, configID = '') {
  const version = ++maaLoadVersion;
  currentMAAData = null; // Bug 5: 切换或加载新预设期间立即清空旧数据
  const btnSave = document.getElementById('btnSaveMAA');
  if (btnSave) {
    btnSave.disabled = true; // Bug 5: 加载期间严禁保存
    btnSave.textContent = '正在载入预设…';
  }
  const select = document.getElementById('maaProfileSelect');
  if (select) select.disabled = true;

  const container = document.getElementById('maaTaskListContainer');
  container.innerHTML = '<div style="color:var(--text-muted); padding:30px; text-align:center;">正在载入预设任务状态...</div>';

  try {
    let url = `/api/v1/tasks/${encodeURIComponent(taskID)}/maa`;
    if (configID) {
      url += `?config_id=${encodeURIComponent(configID)}`;
    }
    const data = await requestAPI(url);
    if (version !== maaLoadVersion) return; // 丢弃过期请求，防并发时序颠倒

    currentMAAData = data;
    currentSelectedProfileID = data.active_config_id || '';

    if (!data.is_maa) {
      container.innerHTML = '<div style="color:#f87171; padding:30px; text-align:center;">未检测到 MAA 框架 interface.json 声明文件</div>';
      return;
    }

    document.getElementById('maaModalTitle').innerText = `${data.project_name || 'MAA 自动化'} - 任务配置`;
    document.getElementById('maaModalBadge').innerText = data.version ? `${data.version}` : 'MAA/MXU';

    const profileBar = document.getElementById('maaProfileBar');
    const profileSelect = document.getElementById('maaProfileSelect');
    profileSelect.innerHTML = '';

    if (data.profiles && data.profiles.length > 0) {
      profileBar.style.display = 'flex';
      data.profiles.forEach(p => {
        const opt = document.createElement('option');
        opt.value = p.id;
        opt.innerText = p.name + (p.is_active ? ' (当前磁盘生效)' : '');
        if (p.id === data.active_config_id) {
          opt.selected = true;
        }
        profileSelect.appendChild(opt);
      });
    } else {
      profileBar.style.display = 'none';
    }

    document.getElementById('maaModalSubTitle').innerText = `已识别 ${data.tasks ? data.tasks.length : 0} 个子任务模块 · 本地配置文件: ${data.config_type === 'mxu' ? 'MXU (config/mxu-*.json)' : data.config_type}`;

    renderMAATasks(data.tasks || []);
    maaSnapshot = formSnapshot('maaConfigModal');
    if (btnSave) {
      btnSave.disabled = false;
      btnSave.textContent = '保存，下次运行生效';
    }
  } catch (e) {
    if (version !== maaLoadVersion) return;
    container.innerHTML = `<div style="color:#f87171; padding:30px; text-align:center;">加载失败: ${escapeHtml(e.message || String(e))}</div>`;
    if (btnSave) {
      btnSave.disabled = true;
      btnSave.textContent = '加载失败';
    }
  } finally {
    if (version === maaLoadVersion && select) select.disabled = false;
  }
}

async function onMAAProfileChange(id) {
  const select = document.getElementById('maaProfileSelect');
  if (maaSnapshot && maaSnapshot !== formSnapshot('maaConfigModal') && !await confirmAction('切换预设将放弃未保存的修改，继续吗？')) {
    if (select) select.value = currentSelectedProfileID;
    return;
  }
  if (select) select.value = id;
  currentSelectedProfileID = id;
  await loadMAAConfig(currentMAATaskID, id);
}

function renderMAATasks(tasks) {
  const container = document.getElementById('maaTaskListContainer');
  container.innerHTML = '';

  if (tasks.length === 0) {
    container.innerHTML = '<div style="color:var(--text-muted); padding:30px; text-align:center;">未找到可配置的子任务项</div>';
    return;
  }

  tasks.forEach((t, idx) => {
    const item = document.createElement('div');
    item.style.background = '#0f172a';
    item.style.border = '1px solid #334155';
    item.style.borderRadius = '8px';
    item.style.padding = '10px 14px';
    item.style.display = 'flex';
    item.style.flexDirection = 'column';
    item.style.gap = '8px';

    const safeLabel = escapeHtml(t.label || t.name);
    const safeDesc = escapeHtml(t.description || '无任务说明');
    const safeName = escapeHtml(t.name);

    let html = `
      <div style="display:flex; justify-content:space-between; align-items:center; gap:12px;">
        <div style="flex:1;">
          <div style="display:flex; align-items:center; gap:8px;">
            <span style="font-weight:700; color:#f1f5f9; font-size:0.95rem;">${safeLabel}</span>
            <span style="font-size:0.75rem; color:#64748b; font-family:monospace;">(${safeName})</span>
          </div>
          <div style="font-size:0.8rem; color:#94a3b8; margin-top:4px; line-height:1.4;">${safeDesc}</div>
        </div>
        <div>
          <label class="switch">
            <input type="checkbox" id="chk_maa_task_${idx}" ${t.enabled ? 'checked' : ''}>
            <span class="slider"></span>
          </label>
        </div>
      </div>
    `;

    // 渲染子选项 (Sub-options: switch, select, input)
    if (t.sub_options && t.sub_options.length > 0) {
      html += `<div style="margin-top:6px; background:#070d19; border:1px solid #1e293b; border-radius:6px; padding:6px 12px; display:flex; flex-direction:column; gap:6px;">`;
      t.sub_options.forEach((sub, sIdx) => {
        const subLabel = escapeHtml(sub.label || sub.name);
        const subDesc = escapeHtml(sub.description || '');
        let controlHtml = '';
        if (sub.type === 'select') {
          const cases = sub.cases || [];
          controlHtml = `<select id="sel_maa_sub_${idx}_${sIdx}" style="background:#1e293b; color:#f1f5f9; border:1px solid #334155; border-radius:4px; padding:3px 8px; font-size:0.8rem; max-width:200px; outline:none;">` +
            cases.map(c => `<option value="${escapeHtml(c.name)}" ${c.name === String(sub.value) ? 'selected' : ''}>${escapeHtml(c.label || c.name)}</option>`).join('') +
            `</select>`;
        } else if (sub.type === 'input') {
          controlHtml = `<input type="text" id="ipt_maa_sub_${idx}_${sIdx}" value="${escapeHtml(sub.value || '')}" placeholder="请输入..." style="background:#1e293b; color:#f1f5f9; border:1px solid #334155; border-radius:4px; padding:3px 8px; font-size:0.8rem; max-width:200px; outline:none;">`;
        } else {
          controlHtml = `
            <label class="switch" style="transform: scale(0.85); transform-origin: right center;">
              <input type="checkbox" id="chk_maa_sub_${idx}_${sIdx}" ${sub.value ? 'checked' : ''}>
              <span class="slider"></span>
            </label>
          `;
        }
        html += `
          <div style="display:flex; justify-content:space-between; align-items:center; gap:10px; padding:4px 0; border-bottom:${sIdx === t.sub_options.length - 1 ? 'none' : '1px solid rgba(255,255,255,0.04)'};">
            <div style="flex:1;">
              <span style="font-size:0.82rem; color:#e2e8f0; font-weight:600;">${subLabel}</span>
              ${subDesc ? `<div style="font-size:0.74rem; color:#64748b; margin-top:2px;">${subDesc}</div>` : ''}
            </div>
            <div>
              ${controlHtml}
            </div>
          </div>
        `;
      });
      html += `</div>`;
    }

    item.innerHTML = html;
    container.appendChild(item);
  });
}

function maaToggleAll(enabled) {
  if (!currentMAAData || !currentMAAData.tasks) return;
  currentMAAData.tasks.forEach((_, idx) => {
    const chk = document.getElementById(`chk_maa_task_${idx}`);
    if (chk) chk.checked = enabled;
  });
}

async function saveMAAConfig() {
  if (!currentMAATaskID || !currentMAAData || !currentMAAData.tasks) return;
  // Bug 5 校验：当前预设 ID 必须与加载的数据一致，防止未完成加载时将旧状态保存到新预设
  if (currentSelectedProfileID !== (currentMAAData.active_config_id || '')) return;

  const btn = document.getElementById('btnSaveMAA');
  const oldText = btn.innerText;
  btn.innerText = '正在保存…';
  btn.disabled = true;

  const updatedTasks = currentMAAData.tasks.map((t, idx) => {
    const chk = document.getElementById(`chk_maa_task_${idx}`);
    const subOptions = (t.sub_options || []).map((sub, sIdx) => {
      let val = sub.value;
      if (sub.type === 'select') {
        const sel = document.getElementById(`sel_maa_sub_${idx}_${sIdx}`);
        if (sel) val = sel.value;
      } else if (sub.type === 'input') {
        const ipt = document.getElementById(`ipt_maa_sub_${idx}_${sIdx}`);
        if (ipt) val = ipt.value;
      } else {
        const subChk = document.getElementById(`chk_maa_sub_${idx}_${sIdx}`);
        if (subChk) val = subChk.checked;
      }
      return {
        name: sub.name,
        type: sub.type,
        value: val,
        input_key: sub.input_key || '',
      };
    });
    return {
      name: t.name,
      enabled: chk ? chk.checked : t.enabled,
      sub_options: subOptions
    };
  });

  const payload = {
    config_id: currentSelectedProfileID,
    tasks: updatedTasks
  };

  try {
    await requestAPI(`/api/v1/tasks/${encodeURIComponent(currentMAATaskID)}/maa`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    btn.innerText = '✅ 保存并生效成功';
    maaSnapshot = '';
    setTimeout(async () => {
      await closeMAAConfigModal(true);
      btn.innerText = oldText;
      btn.disabled = false;
      await fetchStatus();
    }, 600);
  } catch (e) {
    notifyUser('保存失败: ' + (e.message || String(e)), true);
    btn.innerText = oldText;
    btn.disabled = false;
  }
}

window.addEventListener('DOMContentLoaded',()=>{
  document.getElementById('chkForceRun').addEventListener('change',updateControls);
  document.getElementById('terminalBody').addEventListener('scroll',event=>{
    const body=event.target;
    if(body.scrollHeight-body.scrollTop-body.clientHeight>40 && autoScroll) { autoScroll=false;updateLogFollowControls(); }
  });
  document.querySelector('main').addEventListener('scroll',event=>{
    if(window.innerWidth > 760 || document.getElementById('workspace').dataset.view !== 'logs') return;
    const body = event.target;
    if(body.scrollHeight-body.scrollTop-body.clientHeight>40 && autoScroll) { autoScroll=false;updateLogFollowControls(); }
  },{passive:true});
  document.querySelectorAll('.task-more').forEach(menu => menu.addEventListener('toggle',()=>positionMoreMenu(menu)));
  const closeMenus = () => document.querySelectorAll('.task-more[open]').forEach(menu => { menu.open = false; });
  document.getElementById('taskList').addEventListener('scroll', closeMenus, { passive: true });
  window.addEventListener('resize', () => { closeMenus(); scheduleLogRender(); });
  document.addEventListener('click', event => {
    document.querySelectorAll('.task-more[open]').forEach(menu => { if(!menu.contains(event.target)) menu.open=false; });
  });
  document.addEventListener('keydown',event=>{
    if(event.key!=='Escape') return;
    for(const [id,close] of [['confirmModal',()=>finishConfirmation(false)],['settingsModal',closeSettingsModal],['fileBrowserModal',closeFileBrowser],['maaConfigModal',closeMAAConfigModal],['taskModal',closeTaskModal]]) {
      if(document.getElementById(id).style.display==='flex') { close();break; }
    }
  });
  document.querySelectorAll('.modal-title > span[onclick], .modal-title > div > span[onclick]').forEach(element=>{
    const button=document.createElement('button');button.className='close-button';button.textContent='×';button.setAttribute('aria-label','关闭面板');button.onclick=()=>element.click();element.replaceWith(button);
  });
  document.querySelectorAll('.form-group').forEach(group => {
    const label = group.querySelector('label.form-label');
    const input = group.querySelector('input');
    if (label && input) label.htmlFor = input.id;
  });
  // Keep keyboard focus in the top dialog, including the nested file browser.
  let priorFocus = null;
  const modalOrder = ['taskModal','maaConfigModal','fileBrowserModal','settingsModal','confirmModal'];
  function activeModal() { return modalOrder.map(id=>document.getElementById(id)).filter(modal=>modal.style.display==='flex').pop(); }
  modalOrder.forEach(id=>{
    const modal = document.getElementById(id);
    modal.setAttribute('role','dialog'); modal.setAttribute('aria-modal','true');
    modal.setAttribute('aria-label',id==='confirmModal'?'确认操作':id==='settingsModal'?'设置':id==='fileBrowserModal'?'选择文件或目录':id==='maaConfigModal'?'自动化配置':'任务配置');
    new MutationObserver(()=>{
      const active = activeModal();
      document.querySelector('main').inert = !!active;
      document.querySelector('header').inert = !!active;
      document.body.style.overflow = active ? 'hidden' : '';
      if(active && !active.contains(document.activeElement)) {
        priorFocus = document.activeElement;
        active.querySelector('button,input:not([type=hidden]),select')?.focus();
      } else if(!active && priorFocus?.isConnected) { priorFocus.focus(); priorFocus=null; }
    }).observe(modal,{attributes:true,attributeFilter:['style']});
  });
  document.addEventListener('keydown',event=>{
    const modal=activeModal();
    if(event.key!=='Tab' || !modal) return;
    const focusable=[...modal.querySelectorAll('button:not(:disabled),input:not(:disabled):not([type=hidden]),select:not(:disabled),summary,[tabindex="0"]')].filter(element=>element.getClientRects().length);
    const first=focusable[0],last=focusable[focusable.length-1];
    if(event.shiftKey && document.activeElement===first) {event.preventDefault();last?.focus();}
    else if(!event.shiftKey && document.activeElement===last) {event.preventDefault();first?.focus();}
  });
  const observer=new MutationObserver(()=>{
    const select=document.getElementById('logTaskSelect');
    const value=logScope;
    select.replaceChildren(new Option('全部任务',''),...cachedTasks.map(t=>new Option(t.name || t.id,t.id)));
    select.value=value;
    if(value && !cachedTasks.some(t=>t.id===value)) selectTaskLogs('');
  });
  observer.observe(document.getElementById('taskList'),{childList:true});
  updateControls();
  renderWorkspaceLogs();
});
