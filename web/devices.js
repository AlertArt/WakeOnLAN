// devices.js —— 设备列表页：列表渲染、唤醒状态机、编辑/删除弹层、退出登录。
// 只消费 docs/E1–E4-contract.md 已冻结的接口；字段原样使用，不改名、不重组。
// 唤醒流程：wake(202) → check(202) → 每 5 秒轮询 /api/status → 终态。
(function () {
  'use strict';

  var WOL = window.WOL;
  var request = WOL.request;
  var el = WOL.el;
  var statusMeta = WOL.statusMeta;

  var POLL_INTERVAL_MS = 5000;
  // 服务端上限 120 秒，客户端多留 10 秒；超限即停止轮询，交给"刷新"。
  var CHECK_LIMIT_MS = 130000;
  var TERMINAL = { online: true, timeout: true, failed: true };

  var PROBE_FIELD = {
    ping: { label: 'Ping 目标', placeholder: '192.168.1.10', hint: '填写设备在局域网内的 IPv4 地址。' },
    tcp: { label: 'TCP 目标', placeholder: '192.168.1.10:22', hint: '主机:端口，端口用于判断服务是否已经启动。' },
  };

  var dom = {
    list: document.getElementById('device-list'),
    empty: document.getElementById('empty'),
    notice: document.getElementById('notice'),
    add: document.getElementById('add-device'),
    refresh: document.getElementById('refresh'),
    logout: document.getElementById('logout'),
    editor: document.getElementById('editor'),
    editorForm: document.getElementById('editor-form'),
    editorTitle: document.getElementById('editor-title'),
    editorError: document.getElementById('editor-error'),
    editorSubmit: document.getElementById('editor-submit'),
    editorCancel: document.getElementById('editor-cancel'),
    confirm: document.getElementById('confirm'),
    confirmText: document.getElementById('confirm-text'),
    confirmOk: document.getElementById('confirm-ok'),
    confirmCancel: document.getElementById('confirm-cancel'),
  };

  var fields = {
    name: document.getElementById('f-name'),
    mac: document.getElementById('f-mac'),
    addr: document.getElementById('f-addr'),
    port: document.getElementById('f-port'),
    probeType: document.getElementById('f-probe-type'),
    probeValue: document.getElementById('f-probe-value'),
    probeValueField: document.getElementById('f-probe-value-field'),
    probeValueLabel: document.getElementById('f-probe-value-label'),
    probeValueHint: document.getElementById('f-probe-value-hint'),
  };

  var state = {
    devices: [],
    statuses: new Map(), // device_id -> /api/status 的单项
    phase: new Map(), // device_id -> 'sending' | 'checking'，只存在于本页面内存
    since: new Map(), // device_id -> 校验开始毫秒数
    notes: new Map(), // device_id -> 本地补充说明（不写入服务端）
    pollTimer: null,
    loaded: false,
    loading: false,
    editingId: null,
    deletingId: null,
  };

  var noticeTimer = null;

  function notice(text, kind) {
    clearTimeout(noticeTimer);
    if (!text) {
      dom.notice.hidden = true;
      return;
    }
    dom.notice.textContent = text;
    dom.notice.className = 'notice notice--' + (kind || 'info');
    dom.notice.hidden = false;
    if (kind !== 'error') {
      noticeTimer = setTimeout(function () {
        dom.notice.hidden = true;
      }, 6000);
    }
  }

  function findDevice(id) {
    return state.devices.find(function (dv) {
      return dv.id === id;
    });
  }

  function probeText(dv) {
    if (dv.probe_type === 'none') return '未配置';
    if (dv.probe_type === 'ping') return 'Ping ' + dv.probe_value;
    return 'TCP ' + dv.probe_value;
  }

  function fact(key, value, mono) {
    var row = el('div', 'fact');
    row.appendChild(el('span', 'fact-k', key));
    row.appendChild(el('span', 'fact-v' + (mono ? ' mono' : ''), value));
    return row;
  }

  function chip(phase, status) {
    var meta;
    if (phase === 'sending') meta = { label: '发送中', tone: 'busy' };
    else if (status && statusMeta[status.status]) meta = statusMeta[status.status];
    else meta = { label: '未校验', tone: 'muted' };

    var node = el('span', 'chip chip--' + meta.tone);
    node.appendChild(el('span', 'dot'));
    node.appendChild(el('span', null, meta.label));
    return node;
  }

  function messageOf(id, phase, status) {
    if (phase === 'sending') return '正在发送唤醒包…';
    if (status && status.message) return status.message;
    if (state.notes.has(id)) return state.notes.get(id);
    if (status && statusMeta[status.status]) return statusMeta[status.status].hint;
    return '';
  }

  function card(dv) {
    var status = state.statuses.get(dv.id);
    var phase = state.phase.get(dv.id);
    if (!phase && status && status.status === 'checking') phase = 'checking';

    var node = el('li', 'card');
    node.dataset.id = String(dv.id);

    var head = el('div', 'card-head');
    head.appendChild(el('h2', 'card-title', dv.name));
    head.appendChild(chip(phase, status));
    node.appendChild(head);

    var facts = el('div', 'facts');
    facts.appendChild(fact('MAC', dv.mac, true));
    facts.appendChild(fact('广播', dv.broadcast_addr + ':' + dv.broadcast_port, true));
    facts.appendChild(fact('校验', probeText(dv), true));
    facts.appendChild(fact('最近结论', status && status.checked_at ? WOL.formatTime(status.checked_at) : '—', false));
    node.appendChild(facts);

    var message = messageOf(dv.id, phase, status);
    if (message) node.appendChild(el('p', 'card-msg', message));

    var actions = el('div', 'card-actions');
    var wake = el('button', 'btn btn--primary', phase ? (phase === 'sending' ? '发送中…' : '校验中…') : '唤醒');
    wake.type = 'button';
    wake.dataset.act = 'wake';
    wake.disabled = Boolean(phase);
    actions.appendChild(wake);

    var edit = el('button', 'btn', '编辑');
    edit.type = 'button';
    edit.dataset.act = 'edit';
    actions.appendChild(edit);

    var remove = el('button', 'btn btn--danger', '删除');
    remove.type = 'button';
    remove.dataset.act = 'delete';
    actions.appendChild(remove);

    node.appendChild(actions);
    return node;
  }

  function render() {
    dom.list.textContent = '';
    var hasDevices = state.devices.length > 0;
    dom.empty.hidden = !(state.loaded && !hasDevices);
    if (!hasDevices) return;

    state.devices.forEach(function (dv) {
      dom.list.appendChild(card(dv));
    });
  }

  function startPolling() {
    if (state.pollTimer !== null) return;
    state.pollTimer = setInterval(poll, POLL_INTERVAL_MS);
  }

  function stopPolling() {
    if (state.pollTimer === null) return;
    clearInterval(state.pollTimer);
    state.pollTimer = null;
  }

  // 把 /api/status 的结果并入本地状态：终态即清理阶段标记，无任务时停掉定时器。
  function mergeStatuses(list) {
    list.forEach(function (item) {
      state.statuses.set(item.device_id, item);
      state.notes.delete(item.device_id);
      if (TERMINAL[item.status]) {
        state.phase.delete(item.device_id);
        state.since.delete(item.device_id);
      }
    });

    var now = Date.now();
    state.since.forEach(function (startedAt, id) {
      if (now - startedAt > CHECK_LIMIT_MS) {
        state.phase.delete(id);
        state.since.delete(id);
        state.notes.set(id, '等待超过 130 秒仍未拿到结论，请点「刷新」查看最新状态');
      }
    });

    if (state.since.size === 0) stopPolling();
  }

  async function poll() {
    var list;
    try {
      list = await request('GET', '/api/status');
    } catch (err) {
      // 401 已由 request 内部跳转；其它错误提示一次，下一轮继续，不中断轮询。
      notice('暂时无法获取校验结果，正在重试…', 'error');
      return;
    }
    mergeStatuses(list || []);
    render();
  }

  async function loadAll() {
    if (state.loading) return;
    state.loading = true;
    dom.refresh.disabled = true;
    try {
      var results = await Promise.all([request('GET', '/api/devices'), request('GET', '/api/status')]);
      state.devices = Array.isArray(results[0]) ? results[0] : [];
      state.statuses = new Map();
      (results[1] || []).forEach(function (item) {
        state.statuses.set(item.device_id, item);
        // 其它客户端发起的校验：本地也接着轮询，避免界面停在"校验中"不动。
        if (item.status === 'checking' && !state.since.has(item.device_id)) {
          state.since.set(item.device_id, Date.now());
        }
      });
      state.loaded = true;
      if (state.since.size > 0) startPolling();
    } catch (err) {
      notice(err.message, 'error');
    } finally {
      state.loading = false;
      dom.refresh.disabled = false;
      render();
    }
  }

  async function wake(id) {
    var dv = findDevice(id);
    if (!dv || state.phase.has(id)) return;

    state.notes.delete(id);
    state.statuses.delete(id);
    state.phase.set(id, 'sending');
    render();

    try {
      await request('POST', '/api/devices/' + id + '/wake');
    } catch (err) {
      state.phase.delete(id);
      render();
      notice(err.message, 'error');
      return;
    }

    // 202 只代表包已交给内核，不代表设备已开机：文案上必须与"已上线"区分。
    if (dv.probe_type === 'none') {
      state.phase.delete(id);
      state.notes.set(id, '唤醒包已发送；该设备未配置校验目标，无法自动确认是否开机');
      render();
      notice('唤醒包已发送。该设备未配置校验目标，无法自动确认是否开机。', 'info');
      return;
    }

    await startCheck(id);
  }

  async function startCheck(id) {
    state.phase.set(id, 'checking');
    state.since.set(id, Date.now());
    render();

    try {
      await request('POST', '/api/devices/' + id + '/check');
    } catch (err) {
      if (err.code === 'check_in_progress') {
        // 服务端已有任务在跑，继续等它的结果即可，不重复报错。
        notice('该设备正在校验中，继续等待本次结果。', 'info');
      } else {
        state.phase.delete(id);
        state.since.delete(id);
        render();
        notice('唤醒包已发送，但校验未能启动：' + err.message, 'error');
        return;
      }
    }

    startPolling();
    render();
  }

  function editorError(text) {
    if (!text) {
      dom.editorError.textContent = '';
      dom.editorError.hidden = true;
      return;
    }
    dom.editorError.textContent = text;
    dom.editorError.hidden = false;
  }

  function setEditorBusy(busy) {
    dom.editorSubmit.disabled = busy;
    dom.editorSubmit.textContent = busy ? '保存中…' : '保存';
  }

  function syncProbeField() {
    var type = fields.probeType.value;
    if (type === 'none') {
      fields.probeValueField.hidden = true;
      fields.probeValue.value = '';
      return;
    }
    var meta = PROBE_FIELD[type];
    fields.probeValueField.hidden = false;
    fields.probeValueLabel.textContent = meta.label;
    fields.probeValue.placeholder = meta.placeholder;
    fields.probeValueHint.textContent = meta.hint;
  }

  function openEditor(id) {
    var dv = id ? findDevice(id) : null;
    state.editingId = dv ? dv.id : null;

    dom.editorTitle.textContent = dv ? '编辑设备' : '添加设备';
    fields.name.value = dv ? dv.name : '';
    fields.mac.value = dv ? dv.mac : '';
    fields.addr.value = dv ? dv.broadcast_addr : '';
    fields.port.value = dv ? String(dv.broadcast_port) : '9';
    fields.probeType.value = dv ? dv.probe_type : 'none';
    fields.probeValue.value = dv ? dv.probe_value : '';
    syncProbeField();

    editorError('');
    setEditorBusy(false);
    dom.editor.showModal();
    fields.name.focus();
  }

  async function saveEditor(event) {
    event.preventDefault();
    if (dom.editorSubmit.disabled) return;

    var body = {
      name: fields.name.value.trim(),
      mac: fields.mac.value.trim(),
      broadcast_addr: fields.addr.value.trim(),
      broadcast_port: Number(fields.port.value),
      probe_type: fields.probeType.value,
      probe_value: fields.probeType.value === 'none' ? '' : fields.probeValue.value.trim(),
    };

    if (!body.name) return editorError('请填写设备名称');
    if (!body.mac) return editorError('请填写 MAC 地址');
    if (!body.broadcast_addr) return editorError('请填写广播地址，例如 192.168.1.255');
    if (!Number.isInteger(body.broadcast_port) || body.broadcast_port < 1 || body.broadcast_port > 65535) {
      return editorError('广播端口需为 1–65535 之间的整数');
    }
    if (body.probe_type !== 'none' && !body.probe_value) {
      return editorError('请填写校验目标，或将校验方式改为「不校验」');
    }

    var isEdit = Boolean(state.editingId);
    var target = state.editingId;

    editorError('');
    setEditorBusy(true);
    try {
      if (isEdit) await request('PUT', '/api/devices/' + target, body);
      else await request('POST', '/api/devices', body);
      state.editingId = null;
      dom.editor.close();
      notice(isEdit ? '设备已更新' : '设备已添加', 'ok');
      await loadAll();
    } catch (err) {
      editorError(err.message);
    } finally {
      setEditorBusy(false);
    }
  }

  function askDelete(id) {
    var dv = findDevice(id);
    if (!dv) return;
    state.deletingId = id;
    dom.confirmText.textContent = '删除「' + dv.name + '」后无法恢复，需要重新录入 MAC 与广播地址。';
    dom.confirm.showModal();
  }

  async function doDelete() {
    var id = state.deletingId;
    if (!id) return;
    dom.confirmOk.disabled = true;
    try {
      await request('DELETE', '/api/devices/' + id);
      dom.confirm.close();
      notice('设备已删除', 'ok');
      await loadAll();
    } catch (err) {
      dom.confirm.close();
      notice(err.message, 'error');
    } finally {
      dom.confirmOk.disabled = false;
    }
  }

  async function logout() {
    dom.logout.disabled = true;
    dom.logout.textContent = '退出中…';
    try {
      await request('POST', '/api/logout');
    } catch (err) {
      // 令牌可能已失效，本地清除后同样回登录页；401 时 request 已在跳转，避免二次覆盖。
      if (err.code === 'unauthorized' || err.code === 'token_expired') return;
    }
    WOL.token.clear();
    location.replace('login.html');
  }

  function bindEvents() {
    dom.list.addEventListener('click', function (event) {
      var btn = event.target.closest('button[data-act]');
      if (!btn || btn.disabled) return;
      var holder = btn.closest('[data-id]');
      if (!holder) return;

      var id = Number(holder.dataset.id);
      var act = btn.dataset.act;
      if (act === 'wake') wake(id);
      else if (act === 'edit') openEditor(id);
      else if (act === 'delete') askDelete(id);
    });

    dom.add.addEventListener('click', function () {
      openEditor(0);
    });
    dom.refresh.addEventListener('click', function () {
      notice('');
      loadAll();
    });
    dom.logout.addEventListener('click', logout);

    fields.probeType.addEventListener('change', syncProbeField);

    dom.editorForm.addEventListener('submit', saveEditor);
    dom.editorCancel.addEventListener('click', function () {
      dom.editor.close();
    });
    dom.editor.addEventListener('close', function () {
      state.editingId = null;
    });

    dom.confirmOk.addEventListener('click', doDelete);
    dom.confirmCancel.addEventListener('click', function () {
      dom.confirm.close();
    });
    dom.confirm.addEventListener('close', function () {
      state.deletingId = null;
    });
  }

  async function boot() {
    if (!WOL.token.read()) {
      WOL.toLogin();
      return;
    }
    bindEvents();
    await loadAll();
  }

  document.addEventListener('DOMContentLoaded', boot);
})();
