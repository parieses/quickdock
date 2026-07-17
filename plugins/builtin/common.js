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

  // 复制到剪贴板（优先 Clipboard API，失败降级）
  function copyText(text) {
    try {
      var p = navigator.clipboard.writeText(text);
      if (p && typeof p.catch === 'function') {
        p.catch(function () { fallbackCopy(text); });
      }
    } catch (e) {
      fallbackCopy(text);
    }
  }

  // 暴露为全局（兼容既有插件直接调用 escapeHtml(...) / copyText(...)）
  global.escapeHtml = escapeHtml;
  global.copyText = copyText;
  global.fallbackCopy = fallbackCopy;

  // 同时挂到命名空间，便于未来扩展而不污染全局
  global.QD = global.QD || {};
  global.QD.escapeHtml = escapeHtml;
  global.QD.copyText = copyText;
  global.QD.fallbackCopy = fallbackCopy;
})(typeof window !== 'undefined' ? window : this);
