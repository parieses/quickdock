/* 站点配置。当前版本只在这里写一次，页面上的 [data-ver] / [data-ver-date]
   由本文件填充。改版本号请用 `python tools/bump.py <版本> <日期>`，它会顺带
   处理更新日志的升降级（见 README「发版时要改的地方」）。 */
window.QD_SITE = {
  version: 'v0.0.35',
  released: '2026-10-31'
};

(function () {
  'use strict';

  function fill() {
    var s = window.QD_SITE;
    var vers = document.querySelectorAll('[data-ver]');
    for (var i = 0; i < vers.length; i++) vers[i].textContent = s.version;
    var dates = document.querySelectorAll('[data-ver-date]');
    for (var j = 0; j < dates.length; j++) dates[j].textContent = s.released;
  }

  // defer 加载时 DOM 已解析完，直接填；万一被提前加载就等 DOMContentLoaded
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', fill);
  } else {
    fill();
  }
})();
