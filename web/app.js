// app.js —— 两页共用：令牌存取、请求封装、错误文案、状态文案、时间格式化。
// 约束见 docs/E5-contract.md：
//   1. 令牌只存 localStorage["wol.token"] 一处，不写 Cookie / sessionStorage；
//   2. 只消费已冻结的接口，不改字段名、不改造数据结构；
//   3. 401 只跳转一次，不弹窗、不自动重试。
// 无第三方依赖，无构建步骤，浏览器直接执行。
(function () {
  'use strict';

  var TOKEN_KEY = 'wol.token';
  var LOGIN_PAGE = 'login.html';
  var REQUEST_TIMEOUT_MS = 15000;

  function ApiError(code, message, status) {
    this.name = 'ApiError';
    this.message = message;
    this.code = code;
    this.status = status || 0;
  }
  ApiError.prototype = Object.create(Error.prototype);
  ApiError.prototype.constructor = ApiError;

  // 隐私模式下 localStorage 可能抛错；存取失败不阻断页面渲染（E5 验收 5-2 不允许白屏）。
  var token = {
    read: function () {
      try {
        return localStorage.getItem(TOKEN_KEY) || '';
      } catch (err) {
        return '';
      }
    },
    write: function (value) {
      try {
        localStorage.setItem(TOKEN_KEY, value);
      } catch (err) {
        /* 写入失败时登录态无法保持，用户会看到登录页，属可接受降级 */
      }
    },
    clear: function () {
      try {
        localStorage.removeItem(TOKEN_KEY);
      } catch (err) {
        /* 同上 */
      }
    },
  };

  // 防止 401 连锁跳转：同一页面生命周期内只跳一次。
  var redirecting = false;

  function toLogin(reason) {
    if (redirecting) return;
    redirecting = true;
    token.clear();
    location.replace(LOGIN_PAGE + (reason ? '?reason=' + encodeURIComponent(reason) : ''));
  }

  var ERROR_TEXT = {
    invalid_credentials: '口令错误，请重新输入',
    too_many_attempts: '尝试次数过多，请约 10 分钟后再试',
    not_found: '设备不存在，可能已被删除，请刷新列表',
    duplicate_mac: '该 MAC 已被其他设备使用，请检查后重试',
    limit_exceeded: '设备数量已达上限，请先删除不再使用的设备',
    check_in_progress: '该设备正在校验中，请等待本次结果',
    send_failed: '唤醒包发送失败，请检查广播地址与当前网络',
    network: '无法连接服务，请确认服务正在运行、手机与主机在同一局域网，然后重试',
    bad_response: '服务返回了无法识别的数据，请稍后重试',
  };

  function messageFor(code, data, status) {
    if (ERROR_TEXT[code]) return ERROR_TEXT[code];
    // 字段级校验错误由服务端给出具体说明，直接透传，前端不另造一套文案。
    if (status === 400 && data && data.message) return data.message;
    if (status >= 500) return '服务暂时不可用，请稍后重试';
    if (data && data.message) return data.message;
    return '操作未完成，请稍后重试';
  }

  // 状态取值与 docs/E3-contract.md 的五态一一对应，禁止在此新增取值。
  var statusMeta = {
    pending: { label: '待校验', tone: 'muted', hint: '等待校验' },
    checking: { label: '校验中', tone: 'busy', hint: '正在确认设备是否上线' },
    online: { label: '已上线', tone: 'ok', hint: '设备已响应' },
    timeout: { label: '超时未响应', tone: 'warn', hint: '120 秒内未检测到设备上线' },
    failed: { label: '校验失败', tone: 'bad', hint: '校验未能完成' },
  };

  async function request(method, path, body) {
    var headers = {};
    var payload;
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      payload = JSON.stringify(body);
    }
    var current = token.read();
    if (current) headers['Authorization'] = 'Bearer ' + current;

    var controller = new AbortController();
    var timer = setTimeout(function () {
      controller.abort();
    }, REQUEST_TIMEOUT_MS);

    var resp;
    try {
      resp = await fetch(path, {
        method: method,
        headers: headers,
        body: payload,
        cache: 'no-store',
        signal: controller.signal,
      });
    } catch (err) {
      // 断网、超时、服务未启动都归到这里，文案统一（E5 验收 5-2）。
      throw new ApiError('network', ERROR_TEXT.network, 0);
    } finally {
      clearTimeout(timer);
    }

    if (resp.status === 204) return null;

    var text = await resp.text();
    var data = null;
    if (text) {
      try {
        data = JSON.parse(text);
      } catch (err) {
        throw new ApiError('bad_response', ERROR_TEXT.bad_response, resp.status);
      }
    }

    if (!resp.ok) {
      var code = data && data.error ? data.error : 'http_' + resp.status;
      // 口令错误也是 401，但不能当成登录过期处理，否则登录页会自己跳自己。
      if (resp.status === 401 && (code === 'token_expired' || code === 'unauthorized')) {
        toLogin('expired');
      }
      throw new ApiError(code, messageFor(code, data, resp.status), resp.status);
    }
    return data;
  }

  function pad2(n) {
    return n < 10 ? '0' + n : String(n);
  }

  function formatTime(iso) {
    if (!iso) return '—';
    var d = new Date(iso);
    if (isNaN(d.getTime())) return '—';
    return (
      d.getFullYear() +
      '-' +
      pad2(d.getMonth() + 1) +
      '-' +
      pad2(d.getDate()) +
      ' ' +
      pad2(d.getHours()) +
      ':' +
      pad2(d.getMinutes()) +
      ':' +
      pad2(d.getSeconds())
    );
  }

  // 用 DOM API 构造节点，不拼 innerHTML：设备名来自用户输入，拼接会产生 XSS。
  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  window.WOL = {
    ApiError: ApiError,
    token: token,
    toLogin: toLogin,
    request: request,
    statusMeta: statusMeta,
    formatTime: formatTime,
    el: el,
  };
})();
