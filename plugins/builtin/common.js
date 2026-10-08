/*
 * QuickDock 插件公共 JS — 由后端在插件前端页面中自动注入（见 services/plugin.go GetPluginFrontendPage）。
 * 提供各插件共享的纯前端工具函数，避免每个插件重复实现。
 * 既挂载为全局函数（escapeHtml / copyText / fallbackCopy），也挂载到 window.QD 命名空间。
 */
(function (global) {
  'use strict';

  // HTML 转义：防止注入，用于把用户文本安全插入 innerHTML
  function escapeHtml(str) {
    return String(str)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  // 剪贴板降级方案（兼容 iframe sandbox / 非安全上下文）
  function fallbackCopy(text) {
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); } catch (e) { /* 忽略 */ }
    document.body.removeChild(ta);
  }

  // 本地复制：优先 navigator.clipboard（安全上下文可用时），失败降级 execCommand。
  // 仅作为 plugin:copy 桥不可用（旧宿主 / 超时）时的兜底。
  function localCopy(text) {
    try {
      var p = navigator.clipboard.writeText(text);
      if (p && typeof p.catch === 'function') {
        p.catch(function () { fallbackCopy(text); });
        return;
      }
      return;
    } catch (e) { /* 继续降级 */ }
    fallbackCopy(text);
  }

  // 复制到剪贴板：优先走宿主 plugin:copy 桥——QuickDock 插件运行在 opaque-origin iframe /
  // WebView2 沙箱内，navigator.clipboard 与 execCommand 常静默失败，只有宿主的 CopyText 能可靠写入。
  // 协议：post {type:'plugin:copy', id, text} → 宿主回 {type:'plugin:copy-result', id, ok}。
  var __qdCopySeq = 0;
  function copyViaHost(text, cb) {
    var s = String(text == null ? '' : text);
    // 不在 iframe 内（独立窗口/直接打开）→ 无宿主桥，本地降级
    if (!window.parent || window.parent === window) { localCopy(s); cb && cb(false); return; }
    var id = 'qdc_' + (++__qdCopySeq);
    var settled = false;
    function onMsg(e) {
      var d = e && e.data;
      if (d && d.type === 'plugin:copy-result' && d.id === id) {
        settled = true;
        window.removeEventListener('message', onMsg);
        if (!d.ok) localCopy(s); // 宿主写剪贴板失败时兜底
        cb && cb(!!d.ok);
      }
    }
    window.addEventListener('message', onMsg);
    try {
      window.parent.postMessage({ type: 'plugin:copy', id: id, text: s }, '*');
    } catch (e) {
      window.removeEventListener('message', onMsg);
      localCopy(s); cb && cb(false); return;
    }
    // 400ms 无回复（旧宿主未接桥）→ 本地降级
    setTimeout(function () {
      if (settled) return;
      settled = true;
      window.removeEventListener('message', onMsg);
      localCopy(s); cb && cb(false);
    }, 400);
  }

  // 对外统一入口（保持既有全局/命名空间签名，向后兼容）。
  function copyText(text) { copyViaHost(text, null); }

  // ---- 插件页面 i18n ----
  // 宿主已注入：<html lang="zh-CN|en-US"> + 转发 plugin:theme{theme,locale} 消息热更新 lang。
  // QD.i18n(langPack) 示例：
  //   var i18n = QD.i18n({
  //     'zh-CN': { merge: '合并 PDF', pick: '选择文件' },
  //     'en-US': { merge: 'Merge PDF', pick: 'Select Files' }
  //   });
  //   console.log(i18n.t('merge'));          // 按当前语言取文案
  //   i18n.onChange(function () { render(); }); // 宿主切语言时回调，重渲染界面
  // 语言键匹配：精确 locale（zh-CN）→ 主语言（zh）→ 首个语言包 → ''。
  function currentLocale() {
    var l = document.documentElement.getAttribute('lang') || '';
    return l;
  }
  function createI18n(langPack) {
    var listeners = [];
    var packKeys = langPack ? Object.keys(langPack) : [];
    function t(key) {
      if (!langPack) return key;
      var loc = currentLocale();
      if (loc && langPack[loc] && langPack[loc][key] !== undefined) return langPack[loc][key];
      var primary = loc.split('-')[0];
      if (primary && langPack[primary] && langPack[primary][key] !== undefined) return langPack[primary][key];
      if (packKeys.length > 0 && langPack[packKeys[0]][key] !== undefined) return langPack[packKeys[0]][key];
      return key;
    }
    function onChange(fn) {
      listeners.push(fn);
      window.addEventListener('message', function (e) {
        var d = e.data;
        if (d && d.type === 'plugin:theme' && d.data && (d.data.locale || d.data.theme)) {
          var dl = d.data.locale;
          if (dl) document.documentElement.setAttribute('lang', dl);
          for (var i = 0; i < listeners.length; i++) { try { listeners[i](); } catch (e2) { /* 忽略插件回调错误 */ } }
        }
      });
    }
    return { t: t, onChange: onChange, locale: currentLocale };
  }

  // 暴露为全局（兼容既有插件直接调用 escapeHtml(...) / copyText(...)）
  global.escapeHtml = escapeHtml;
  global.copyText = copyText;
  global.fallbackCopy = fallbackCopy;
  global.copyViaHost = copyViaHost;

  // qdCopy：Promise 版复制，resolve(true/false) 表示宿主桥是否写入成功，便于需要反馈的插件复用。
  global.qdCopy = function (text) {
    return new Promise(function (resolve) { copyViaHost(text, function (ok) { resolve(!!ok); }); });
  };

  // 同时挂到命名空间，便于未来扩展而不污染全局
  global.QD = global.QD || {};
  global.QD.escapeHtml = escapeHtml;
  global.QD.copyText = copyText;
  global.QD.fallbackCopy = fallbackCopy;
  global.QD.copyViaHost = copyViaHost;
  global.QD.localCopy = localCopy;
  global.QD.qdCopy = global.qdCopy;
  global.QD.i18n = createI18n;
}(typeof window !== 'undefined' ? window : this));
