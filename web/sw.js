// sw.js —— 手写 Service Worker，无框架（docs/E6-contract.md 第三节）。
// 策略：
//   预缓存：壳资源（两个页面 + 样式脚本 + 清单 + 图标）
//   同源静态：缓存优先，未命中走网络并回填（仅同源 GET 且 resp.ok）
//   导航请求：网络优先，断网回退缓存中的 index.html
//   /api/   ：不拦截、不缓存 —— 接口数据永远来自网络，断网时不得展示旧数据
// 禁止：离线队列、后台同步、推送（6-5 反向项，出现即扩大开发）。
'use strict';

var CACHE = 'wol-static-v1';

// 壳资源。注意：这里绝不能出现任何 /api/ 路径。
var SHELL = [
  './',
  './index.html',
  './login.html',
  './app.css',
  './app.js',
  './login.js',
  './devices.js',
  './manifest.json',
  './icons/icon-192.png',
  './icons/icon-512.png',
  './icons/icon-maskable-512.png',
  './icons/apple-touch-icon.png',
];

self.addEventListener('install', function (event) {
  event.waitUntil(
    caches
      .open(CACHE)
      .then(function (cache) {
        return cache.addAll(SHELL);
      })
      .then(function () {
        return self.skipWaiting();
      })
  );
});

self.addEventListener('activate', function (event) {
  event.waitUntil(
    caches
      .keys()
      .then(function (keys) {
        return Promise.all(
          keys
            .filter(function (key) {
              return key !== CACHE;
            })
            .map(function (key) {
              return caches.delete(key);
            })
        );
      })
      .then(function () {
        return self.clients.claim();
      })
  );
});

self.addEventListener('fetch', function (event) {
  var request = event.request;
  if (request.method !== 'GET') return;

  var url = new URL(request.url);
  if (url.origin !== self.location.origin) return; // 跨域不碰

  // /api/ 一律走浏览器网络栈：不 respondWith，也不写缓存。
  if (url.pathname.indexOf('/api/') === 0) return;

  // 导航请求：网络优先，断网回退缓存壳（index 内部会按令牌状态引导到登录页）。
  if (request.mode === 'navigate') {
    event.respondWith(
      fetch(request).catch(function () {
        return caches.match('./index.html').then(function (page) {
          return (
            page ||
            new Response('网络已断开，且本地没有可用的页面缓存', {
              status: 503,
              headers: { 'Content-Type': 'text/plain; charset=utf-8' },
            })
          );
        });
      })
    );
    return;
  }

  // 其余同源静态资源：缓存优先，未命中走网络并回填。
  event.respondWith(
    caches.match(request).then(function (cached) {
      if (cached) return cached;
      return fetch(request).then(function (response) {
        if (response && response.ok && response.type === 'basic') {
          var copy = response.clone();
          caches.open(CACHE).then(function (cache) {
            cache.put(request, copy);
          });
        }
        return response;
      });
    })
  );
});
