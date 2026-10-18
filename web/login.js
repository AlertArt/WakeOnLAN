// login.js —— 登录页。只做三件事：提示过期、提交口令、成功后进入列表页。
// 不申请注册/找回入口，不做"记住我"（令牌已由 app.js 统一保存）。
(function () {
  'use strict';

  var WOL = window.WOL;
  var form = document.getElementById('login-form');
  var input = document.getElementById('password');
  var submit = document.getElementById('submit');
  var errorBox = document.getElementById('form-error');

  function showError(text) {
    errorBox.textContent = text;
    errorBox.hidden = false;
  }

  function clearError() {
    errorBox.textContent = '';
    errorBox.hidden = true;
  }

  function setBusy(busy) {
    submit.disabled = busy;
    submit.textContent = busy ? '登录中…' : '登录';
  }

  // 被过期踢回来时：清掉旧令牌，给出一次说明，不再自动跳转，杜绝跳转循环（5-3）。
  var reason = new URLSearchParams(location.search).get('reason');
  if (reason === 'expired') {
    WOL.token.clear();
    showError('登录已过期，请重新输入口令');
  } else if (WOL.token.read()) {
    // 已有令牌时直接进列表页；若它其实已失效，列表页会带 reason=expired 跳回来，此时令牌已被清除。
    location.replace('index.html');
  }

  form.addEventListener('submit', async function (event) {
    event.preventDefault();
    if (submit.disabled) return;

    var password = input.value;
    if (!password) {
      showError('请输入口令');
      input.focus();
      return;
    }

    clearError();
    setBusy(true);
    try {
      var data = await WOL.request('POST', '/api/login', { password: password });
      WOL.token.write(data.token);
      location.replace('index.html');
    } catch (err) {
      showError(err.message);
      setBusy(false);
      input.select();
    }
  });
})();
