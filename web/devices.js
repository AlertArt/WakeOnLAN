// devices.js —— 设备列表页：列表渲染、唤醒状态机、编辑/删除弹层、退出登录。
// 只消费 docs/E1–E4-contract.md 已冻结的接口；字段原样使用，不改名、不重组。
// 唤醒流程：wake(202) → check(202) → 每 5 秒轮询 /api/status → 终态。
// 关机/重启流程：POST /commands(201) → 每 5 秒轮询该设备最近一条指令 → done/failed/expired。
(function () {
  'use strict';

  var WOL = window.WOL;
  var request = WOL.request;
  var el = WOL.el;
  var statusMeta = WOL.statusMeta;

  var POLL_INTERVAL_MS = 5000;
  // 服务端上限 120 秒，客户端多留 10 秒；超限即停止轮询，交给"刷新"。
  var CHECK_LIMIT_MS = 130000;
  // 心跳设备的等待窗口：客户端每 60 秒上报一次，服务端 180 秒判离线，
  // 这里多留 20 秒余量，覆盖开机 + 客户端启动的时间。
  var HEARTBEAT_WAIT_MS = 200000;
  var TERMINAL = { online: true, timeout: true, failed: true };
  // 指令是异步执行的：下发后至少等一个心跳周期，这里给足 5 分钟。
  var COMMAND_WAIT_MS = 300000;
  var COMMAND_LABEL = {
    shutdown: '关机',
    restart: '重启',
    enable_rdp: '启用远程桌面',
    disable_rdp: '关闭远程桌面',
  };
  var COMMAND_TEXT = {
    pending: '等待客户端取走（下一个心跳周期）',
    delivered: '客户端已接收，正在执行',
    done: '已执行',
    failed: '执行失败',
    expired: '已过期未送达（设备当时可能已离线）',
  };

  var PROBE_FIELD = {
    ping: { label: 'Ping 目标', placeholder: '192.168.1.10', hint: '填写设备在局域网内的 IPv4 地址。' },
    tcp: { label: 'TCP 目标', placeholder: '192.168.1.10:22', hint: '主机:端口，端口用于判断服务是否已经启动。' },
    heartbeat: {
      label: '心跳密钥',
      placeholder: '至少 16 位，例如 hb-7f3c9a2d5e8b1f4c',
      hint: '目标机上的客户端用这把密钥上报心跳；每 60 秒一次，超过 3 分钟没有心跳即判离线。',
    },
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
    confirmTitle: document.getElementById('confirm-title'),
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
    watch: new Map(), // device_id -> 等待心跳的截止毫秒数
    notes: new Map(), // device_id -> 本地补充说明（不写入服务端）
    pollTimer: null,
    loaded: false,
    loading: false,
    editingId: null,
    deletingId: null,
    commands: new Map(), // device_id -> 最近一条指令（服务端返回的形状）
    commandWatch: new Map(), // device_id -> 停止轮询的截止毫秒数
    confirmMode: null, // 'delete' | 'power' | 'rdp' | 'rdp-toggle'
    powerRequest: null, // { id, type }
    rdpRequest: null, // { id, enable }
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
    if (dv.probe_type === 'heartbeat') return '心跳上报';
    return 'TCP ' + dv.probe_value;
  }

  function fact(key, value, mono) {
    var row = el('div', 'fact');
    row.appendChild(el('span', 'fact-k', key));
    row.appendChild(el('span', 'fact-v' + (mono ? ' mono' : ''), value));
    return row;
  }

  function chip(phase, status, dv) {
    var meta;
    if (phase === 'sending') meta = { label: '发送中', tone: 'busy' };
    // 心跳设备没有"校验中"这一说，未收到心跳时显示「等待心跳」，别让它看起来像从未配置。
    else if (dv && dv.probe_type === 'heartbeat' && (!status || status.status === 'pending')) {
      meta = { label: '等待心跳', tone: 'muted' };
    } else if (status && statusMeta[status.status]) meta = statusMeta[status.status];
    else meta = { label: '未校验', tone: 'muted' };

    var node = el('span', 'chip chip--' + meta.tone);
    node.appendChild(el('span', 'dot'));
    node.appendChild(el('span', null, meta.label));
    return node;
  }

  // ---------- F11 M2：远程桌面 ----------

  // 状态 chip：未启用时用 muted，避免和"离线"抢注意力。
  function rdpChip(status) {
    var on = Boolean(status && status.rdp_enabled);
    var node = el('span', 'chip chip--' + (on ? 'ok' : 'muted') + ' chip--rdp');
    node.appendChild(el('span', 'dot'));
    node.appendChild(el('span', null, on ? 'RDP 已启用' : 'RDP 未启用'));
    if (!on) node.title = '在目标机上点「启用远程桌面」，或下发 enable_rdp 指令';
    return node;
  }

  function askRdp(id) {
    var dv = findDevice(id);
    if (!dv) return;

    var status = state.statuses.get(id);
    if (!status || status.status !== 'online') {
      notice('设备当前不在线，无法建立远程桌面', 'error');
      return;
    }

    var on = Boolean(status.rdp_enabled);
    state.rdpRequest = { id: id, enable: !on };
    state.confirmMode = on ? 'rdp' : 'rdp-toggle';

    if (on) {
      dom.confirmTitle.textContent = '打开远程桌面';
      dom.confirmText.textContent = '将为「' + dv.name + '」创建一次性会话：票据 ' +
        '60 秒内有效且只能用一次，过期需要重新点击。进入桌面后需要输入 Windows 账号口令。';
      dom.confirmOk.textContent = '打开';
    } else {
      dom.confirmTitle.textContent = '启用远程桌面';
      dom.confirmText.textContent = '将向「' + dv.name + '」下发启用指令：客户端会打开 RDP 开关、' +
        '按规则名放行防火墙并启用 NLA。客户端需以管理员身份运行，否则会失败并回执原因。';
      dom.confirmOk.textContent = '下发指令';
    }
    dom.confirm.showModal();
  }

  async function doRdpSession() {
    var req = state.rdpRequest;
    if (!req) return;

    dom.confirmOk.disabled = true;
    // 必须在用户手势的**同步**上下文里开窗：一旦经过 await，浏览器就把
    // window.open 当作非手势弹窗拦截。先开 about:blank 占位、拿到票据后再
    // 导航，是绕开拦截的标准做法。也不能用 'noopener' 特性——按规范它会让
    // window.open 返回 null，窗口其实开了，提示却永远报"被拦截"。
    var win = window.open('about:blank', '_blank');
    if (win) win.opener = null;
    try {
      var resp = await request('POST', '/api/devices/' + req.id + '/rdp-session');
      dom.confirm.close();
      // 票据一次性：先开窗口再提示，避免用户复制到已经失效的地址。
      if (!win || win.closed) {
        notice('浏览器拦截了新标签页：请允许本站弹窗后重试（票据 ' + resp.expires_in + ' 秒内有效）', 'error');
      } else {
        win.location.replace(resp.url);
        notice('已打开远程桌面，票据 ' + resp.expires_in + ' 秒内有效，过期请重新点击', 'ok');
      }
    } catch (err) {
      dom.confirm.close();
      if (win) win.close(); // 请求失败时把刚开的空白页收掉
      notice(err.message, 'error');
    } finally {
      dom.confirmOk.disabled = false;
    }
  }

  async function doRdpToggle() {
    var req = state.rdpRequest;
    if (!req) return;

    dom.confirmOk.disabled = true;
    try {
      var cmd = await request('POST', '/api/devices/' + req.id + '/commands', {
        type: req.enable ? 'enable_rdp' : 'disable_rdp',
      });
      dom.confirm.close();
      state.commands.set(req.id, cmd);
      state.commandWatch.set(req.id, Date.now() + COMMAND_WAIT_MS);
      startPolling();
      notice((req.enable ? '启用' : '关闭') + '远程桌面指令已下发，等待客户端执行…', 'ok');
      render();
    } catch (err) {
      dom.confirm.close();
      notice(err.message, 'error');
    } finally {
      dom.confirmOk.disabled = false;
    }
  }

  function askRdpOff(id) {
    var dv = findDevice(id);
    if (!dv) return;

    var status = state.statuses.get(id);
    if (!status || status.status !== 'online') {
      notice('设备当前不在线，无法下发关闭指令', 'error');
      return;
    }

    state.rdpRequest = { id: id, enable: false };
    state.confirmMode = 'rdp-toggle';
    dom.confirmTitle.textContent = '关闭远程桌面';
    dom.confirmText.textContent = '将向「' + dv.name + '」下发关闭指令：3389 会停止监听，' +
      '已经打开的会话不受影响。之后需要重新启用才能再用远程桌面。';
    dom.confirmOk.textContent = '确认关闭';
    dom.confirm.showModal();
  }

  function commandNote(id) {
    var cmd = state.commands.get(id);
    if (!cmd) return '';
    var label = COMMAND_LABEL[cmd.type] || cmd.type;
    var text = COMMAND_TEXT[cmd.status] || cmd.status;
    // 失败时把客户端回执里的原因带上，否则用户只能看到"执行失败"四个字。
    if (cmd.message && cmd.status === 'failed') text += '：' + cmd.message;
    return label + '指令：' + text;
  }

  function messageOf(id, phase, status) {
    if (phase === 'sending') return '正在发送唤醒包…';

    // 指令进行中优先展示指令状态：这时候"校验超时"之类的旧结论只会让人困惑。
    var cmd = state.commands.get(id);
    if (cmd) {
      if (cmd.status === 'pending' || cmd.status === 'delivered' || cmd.status === 'failed') {
        return commandNote(id);
      }
      // 已执行只保留 10 分钟：机器关掉后本来就会显示心跳超时，太久远的结果别再占着位置。
      if (cmd.status === 'done' && cmd.finished_at &&
        Date.now() - Date.parse(cmd.finished_at) < 600000) {
        return commandNote(id);
      }
    }

    if (status && status.message) return status.message;
    if (state.notes.has(id)) return state.notes.get(id);
    if (status && statusMeta[status.status]) return statusMeta[status.status].hint;
    return '';
  }

  // ---------- F11 M1：心跳指标条 ----------

  function fmtSpeed(bps) {
    if (typeof bps !== 'number' || bps < 1) return null;
    if (bps >= 1e6) return (bps / 1e6).toFixed(1) + 'M/s';
    if (bps >= 1e3) return Math.round(bps / 1e3) + 'K/s';
    return Math.round(bps) + 'B/s';
  }

  function fmtUptime(sec) {
    if (typeof sec !== 'number' || sec < 60) return null;
    var d = Math.floor(sec / 86400);
    var h = Math.floor((sec % 86400) / 3600);
    var m = Math.floor((sec % 3600) / 60);
    if (d > 0) return d + '天' + h + '时';
    if (h > 0) return h + '时' + m + '分';
    return m + '分';
  }

  // 客户端上报过指标才出现；离线时整条变灰并悬浮显示最后上报时间。
  function metricsBar(status) {
    var m = status && status.metrics;
    if (!m) return null;

    var bits = [];
    if (typeof m.cpu === 'number' && m.cpu >= 1) bits.push('CPU ' + Math.round(m.cpu) + '%');
    if (m.mem_total > 0 && m.mem_used <= m.mem_total) {
      bits.push('内存 ' + Math.round(m.mem_used / m.mem_total * 100) + '%');
    }
    if (m.disk_total > 0 && m.disk_used <= m.disk_total) {
      bits.push('硬盘 ' + Math.round(m.disk_used / m.disk_total * 100) + '%');
    }
    var down = fmtSpeed(m.net_down);
    var up = fmtSpeed(m.net_up);
    if (down) bits.push('↓' + down);
    if (up) bits.push('↑' + up);
    var alive = fmtUptime(m.uptime_s);
    if (alive) bits.push('运行 ' + alive);
    if (m.ip) bits.push(m.ip);
    if (bits.length === 0) return null;

    var online = status.status === 'online';
    var node = el('div', 'card-metrics' + (online ? '' : ' card-metrics--stale'));
    node.textContent = bits.join(' · ');
    if (!online && status.metrics_at) {
      node.title = '最后上报：' + WOL.formatTime(status.metrics_at);
    }
    return node;
  }

  function card(dv) {
    var status = state.statuses.get(dv.id);
    var phase = state.phase.get(dv.id);
    if (!phase && status && status.status === 'checking') phase = 'checking';

    var node = el('li', 'card');
    node.dataset.id = String(dv.id);

    var head = el('div', 'card-head');
    head.appendChild(el('h2', 'card-title', dv.name));
    head.appendChild(chip(phase, status, dv));
    // 远程桌面状态只对心跳设备有意义：其它校验方式没有下发通道，也就打不开 RDP。
    if (dv.probe_type === 'heartbeat') head.appendChild(rdpChip(status));
    node.appendChild(head);

    var facts = el('div', 'facts');
    facts.appendChild(fact('MAC', dv.mac, true));
    facts.appendChild(fact('广播', dv.broadcast_addr + ':' + dv.broadcast_port, true));
    facts.appendChild(fact('校验', probeText(dv), true));
    facts.appendChild(fact('最近结论', status && status.checked_at ? WOL.formatTime(status.checked_at) : '—', false));
    node.appendChild(facts);

    var metrics = metricsBar(status);
    if (metrics) node.appendChild(metrics);

    var message = messageOf(dv.id, phase, status);
    if (message) node.appendChild(el('p', 'card-msg', message));

    var actions = el('div', 'card-actions');
    var wake = el('button', 'btn btn--primary', phase ? (phase === 'sending' ? '发送中…' : '校验中…') : '唤醒');
    wake.type = 'button';
    wake.dataset.act = 'wake';
    wake.disabled = Boolean(phase);
    actions.appendChild(wake);

    // 只有心跳设备有下发通道，其余设备不显示关机/重启，避免点了必然失败。
    if (dv.probe_type === 'heartbeat') {
      var rdpOn = Boolean(status && status.rdp_enabled);
      var online = Boolean(status && status.status === 'online');
      var rdp = el('button', 'btn', rdpOn ? '远程桌面' : '启用远程桌面');
      rdp.type = 'button';
      rdp.dataset.act = 'rdp';
      rdp.disabled = !online || Boolean(phase);
      if (!online) rdp.title = '设备离线，无法建立远程桌面';
      else if (!rdpOn) rdp.title = '目标机尚未启用远程桌面，点击下发启用指令';
      actions.appendChild(rdp);

      if (rdpOn) {
        var rdpOff = el('button', 'btn btn--ghost', '关闭 RDP');
        rdpOff.type = 'button';
        rdpOff.dataset.act = 'rdp-off';
        rdpOff.disabled = !online || Boolean(phase);
        rdpOff.title = '关闭目标机的远程桌面（不影响已打开的会话）';
        actions.appendChild(rdpOff);
      }

      var restart = el('button', 'btn', '重启');
      restart.type = 'button';
      restart.dataset.act = 'restart';
      actions.appendChild(restart);

      var shutdown = el('button', 'btn btn--danger', '关机');
      shutdown.type = 'button';
      shutdown.dataset.act = 'shutdown';
      actions.appendChild(shutdown);
    }

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

    // 心跳设备：等到 online 即收工；窗口内一直是 pending/checking 就提示客户端可能没起来。
    state.watch.forEach(function (deadline, id) {
      var latest = state.statuses.get(id);
      if (latest && latest.status === 'online') {
        state.watch.delete(id);
        return;
      }
      if (now > deadline) {
        state.watch.delete(id);
        state.notes.set(id, '3 分钟内未收到心跳，请确认目标机已开机且心跳客户端正在运行');
      }
    });

    if (state.since.size === 0 && state.watch.size === 0) stopPolling();
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
    await refreshWatchedCommands();
    render();
  }

  // 只轮询还在等待中的指令，终态即停止，避免长期空转。
  async function refreshWatchedCommands() {
    var now = Date.now();
    var ids = Array.from(state.commandWatch.keys());
    for (var i = 0; i < ids.length; i++) {
      var id = ids[i];
      if (now > state.commandWatch.get(id)) {
        state.commandWatch.delete(id);
        continue;
      }
      try {
        var list = await request('GET', '/api/devices/' + id + '/commands?limit=1');
        var cmd = list && list[0];
        if (!cmd) continue;
        state.commands.set(id, cmd);
        if (cmd.status === 'done' || cmd.status === 'failed' || cmd.status === 'expired') {
          state.commandWatch.delete(id);
        }
      } catch (err) {
        // 单条失败不影响其它设备，下一轮继续。
      }
    }

    if (state.commandWatch.size === 0 && state.watch.size === 0 && state.phase.size === 0) {
      stopPolling();
    }
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

    // 心跳设备不能主动探测，只能等开机后的客户端自己上报。
    if (dv.probe_type === 'heartbeat') {
      state.phase.delete(id);
      state.notes.set(id, '唤醒包已发送；正在等待设备心跳上报（最长 3 分钟）');
      startHeartbeatWatch(id);
      render();
      notice('唤醒包已发送，等待设备心跳上报中…', 'info');
      return;
    }

    await startCheck(id);
  }

  // 只等 online，不等 timeout：刚唤醒的机器在客户端起来之前，服务端本来就该报超时。
  function startHeartbeatWatch(id) {
    state.watch.set(id, Date.now() + HEARTBEAT_WAIT_MS);
    startPolling();
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
    if (body.probe_type === 'heartbeat' && body.probe_value.length < 16) {
      return editorError('心跳密钥至少 16 位，建议用一串随机字符');
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

  function askPower(id, type) {
    var dv = findDevice(id);
    if (!dv) return;

    // 客户端离线时服务端会拒绝，这里先拦一次，省一次往返。
    var status = state.statuses.get(id);
    if (!status || status.status !== 'online') {
      notice('设备当前不在线，无法下发' + COMMAND_LABEL[type] + '指令', 'error');
      return;
    }

    state.confirmMode = 'power';
    state.powerRequest = { id: id, type: type };
    dom.confirmTitle.textContent = COMMAND_LABEL[type] + '设备';
    dom.confirmText.textContent =
      '将向「' + dv.name + '」下发' + COMMAND_LABEL[type] + '指令：客户端会在 60 秒后执行，' +
      '期间可在目标机执行 shutdown /a 撤销。确定继续？';
    dom.confirmOk.textContent = '确认' + COMMAND_LABEL[type];
    dom.confirm.showModal();
  }

  async function doPower() {
    var req = state.powerRequest;
    if (!req) return;

    dom.confirmOk.disabled = true;
    try {
      var cmd = await request('POST', '/api/devices/' + req.id + '/commands', {
        type: req.type,
        delay_seconds: 60,
      });
      dom.confirm.close();
      state.commands.set(req.id, cmd);
      state.commandWatch.set(req.id, Date.now() + COMMAND_WAIT_MS);
      startPolling();
      notice(COMMAND_LABEL[req.type] + '指令已下发，等待客户端执行…', 'ok');
      render();
    } catch (err) {
      dom.confirm.close();
      notice(err.message, 'error');
    } finally {
      dom.confirmOk.disabled = false;
    }
  }

  function askDelete(id) {
    var dv = findDevice(id);
    if (!dv) return;
    state.confirmMode = 'delete';
    state.deletingId = id;
    dom.confirmTitle.textContent = '删除设备';
    dom.confirmText.textContent = '删除「' + dv.name + '」后无法恢复，需要重新录入 MAC 与广播地址。';
    dom.confirmOk.textContent = '删除';
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
      else if (act === 'shutdown') askPower(id, 'shutdown');
      else if (act === 'restart') askPower(id, 'restart');
      else if (act === 'rdp') askRdp(id);
      else if (act === 'rdp-off') askRdpOff(id);
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

    dom.confirmOk.addEventListener('click', function () {
      if (state.confirmMode === 'power') doPower();
      else if (state.confirmMode === 'rdp') doRdpSession();
      else if (state.confirmMode === 'rdp-toggle') doRdpToggle();
      else doDelete();
    });
    dom.confirmCancel.addEventListener('click', function () {
      dom.confirm.close();
    });
    dom.confirm.addEventListener('close', function () {
      state.deletingId = null;
      state.powerRequest = null;
      state.rdpRequest = null;
      state.confirmMode = null;
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
