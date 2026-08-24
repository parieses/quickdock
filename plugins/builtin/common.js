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

  // 同时挂到命名空间，便于未来扩展而不污染全局
  global.QD = global.QD || {};
  global.QD.escapeHtml = escapeHtml;
  global.QD.copyText = copyText;
  global.QD.fallbackCopy = fallbackCopy;
  global.QD.i18n = createI18n;
}(typeof window !== 'undefined' ? window : this);
