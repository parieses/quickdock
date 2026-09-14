/* QuickDock 官网交互 —— 无依赖，纯原生 */
(function () {
  'use strict';

  /* 1. 移动端菜单 */
  var nav = document.getElementById('nav');
  var links = document.getElementById('navLinks');
  var toggle = document.getElementById('navToggle');

  if (toggle && links) {
    toggle.addEventListener('click', function () {
      var open = links.classList.toggle('open');
      toggle.setAttribute('aria-expanded', String(open));
    });
    links.addEventListener('click', function (e) {
      if (e.target.tagName === 'A') {
        links.classList.remove('open');
        toggle.setAttribute('aria-expanded', 'false');
      }
    });
  }

  /* 2. 顶栏滚动描边 */
  if (nav) {
    var onScroll = function () {
      nav.classList.toggle('scrolled', window.scrollY > 8);
    };
    onScroll();
    window.addEventListener('scroll', onScroll, { passive: true });
  }

  /* 3. 运行时分组 tabs */
  var tabs = document.getElementById('rtTabs');
  if (tabs) {
    tabs.addEventListener('click', function (e) {
      var btn = e.target.closest('.tab');
      if (!btn) return;
      tabs.querySelectorAll('.tab').forEach(function (t) {
        t.setAttribute('aria-selected', String(t === btn));
      });
      document.querySelectorAll('.panel').forEach(function (p) {
        p.hidden = p.id !== btn.dataset.panel;
      });
    });
  }

  /* 4. 代码块复制 */
  document.querySelectorAll('.copy').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var el = document.getElementById(btn.dataset.copy);
      if (!el) return;
      var text = el.innerText;
      var done = function () {
        var old = btn.textContent;
        btn.textContent = '已复制';
        setTimeout(function () { btn.textContent = old; }, 1600);
      };
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(done, function () {});
      } else {
        var ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        try { document.execCommand('copy'); done(); } catch (err) {}
        document.body.removeChild(ta);
      }
    });
  });

  /* 5. 使用说明侧栏 scrollspy */
  var spyLinks = Array.prototype.slice.call(document.querySelectorAll('#guideNav a'));
  var spyTargets = spyLinks
    .map(function (a) { return document.querySelector(a.getAttribute('href')); })
    .filter(Boolean);

  if (spyTargets.length && 'IntersectionObserver' in window) {
    var spy = new IntersectionObserver(function (entries) {
      entries.forEach(function (entry) {
        if (!entry.isIntersecting) return;
        spyLinks.forEach(function (a) {
          a.classList.toggle('active', a.getAttribute('href') === '#' + entry.target.id);
        });
      });
    }, { rootMargin: '-25% 0px -65% 0px', threshold: 0 });
    spyTargets.forEach(function (t) { spy.observe(t); });
  } else if (spyLinks.length) {
    spyLinks[0].classList.add('active');
  }

  /* 6. 滚动淡入 */
  var reveals = Array.prototype.slice.call(document.querySelectorAll('.reveal'));
  if (reveals.length && 'IntersectionObserver' in window) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (entry) {
        if (entry.isIntersecting) {
          entry.target.classList.add('in');
          io.unobserve(entry.target);
        }
      });
    }, { rootMargin: '0px 0px -10% 0px', threshold: 0.05 });
    reveals.forEach(function (el) { io.observe(el); });
  } else {
    reveals.forEach(function (el) { el.classList.add('in'); });
  }

  /* 7. 年份 */
  var y = document.getElementById('year');
  if (y) y.textContent = new Date().getFullYear();

  /* 8. 明暗主题：默认跟随系统，手动切换后记住选择 */
  var root = document.documentElement;
  var themeBtn = document.getElementById('themeToggle');
  var themeColor = document.querySelector('meta[name="theme-color"]');

  function applyTheme(t) {
    root.setAttribute('data-theme', t);
    if (themeBtn) themeBtn.setAttribute('aria-pressed', t === 'light' ? 'true' : 'false');
    if (themeColor) themeColor.setAttribute('content', t === 'light' ? '#ffffff' : '#08090a');
  }
  function store(k, v) {
    try { localStorage.setItem(k, v); } catch (e) {}
  }
  function read(k) {
    try { return localStorage.getItem(k); } catch (e) { return null; }
  }

  var savedTheme = read('qd-theme');
  applyTheme(savedTheme || (window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'));

  if (themeBtn) {
    themeBtn.addEventListener('click', function () {
      var next = root.getAttribute('data-theme') === 'light' ? 'dark' : 'light';
      applyTheme(next);
      store('qd-theme', next);
    });
  }
  if (window.matchMedia) {
    var mq = window.matchMedia('(prefers-color-scheme: light)');
    var onSys = function (e) { if (!read('qd-theme')) applyTheme(e.matches ? 'light' : 'dark'); };
    if (mq.addEventListener) mq.addEventListener('change', onSys);
  }

  /* 9. 中 / 英切换：中文就是 HTML 原文（默认语言 + 利于收录），英文来自 i18n.js。
        按「中文原文」查表，不往 HTML 里塞任何标记 —— 改文案只会退回中文，不会把页面弄坏。 */
  var DICT = (window.QD_I18N && window.QD_I18N.en) || {};
  var HEAD_DICT = (window.QD_I18N && window.QD_I18N.head) || {};
  var SKIP_TAGS = {
    SCRIPT: 1, STYLE: 1, PRE: 1, CODE: 1, KBD: 1, SAMP: 1,
    TEXTAREA: 1, SVG: 1, NOSCRIPT: 1
  };
  var iNodes = [];   // [文本节点, 原文, 前导空白, 后置空白, 归一化 key]

  function norm(s) { return s.replace(/\s+/g, ' ').trim(); }

  (function collect(el) {
    for (var n = el.firstChild; n; n = n.nextSibling) {
      if (n.nodeType === 3) {
        var raw = n.nodeValue;
        if (!raw || !/\S/.test(raw)) continue;
        var key = norm(raw);
        if (!DICT[key]) continue;             // 字典里没有就保持中文
        var ws = /^(\s*)([\s\S]*?)(\s*)$/.exec(raw);
        iNodes.push([n, raw, ws[1], ws[3], key]);
      } else if (n.nodeType === 1 && !SKIP_TAGS[n.nodeName]) {
        collect(n);
      }
    }
  })(document.documentElement);

  /* head 里的 meta 文案是属性不是文本节点，单独处理 */
  var iMetas = [];
  ['meta[name="description"]', 'meta[property="og:title"]', 'meta[property="og:description"]']
    .forEach(function (sel) {
      var el = document.querySelector(sel);
      if (!el) return;
      iMetas.push([el, el.getAttribute('content') || '',
        el.getAttribute('name') || el.getAttribute('property')]);
    });

  function applyLang(lang) {
    var en = lang === 'en';
    iNodes.forEach(function (it) {
      it[0].nodeValue = en ? it[2] + DICT[it[4]] + it[3] : it[1];
    });
    iMetas.forEach(function (it) {
      var v = en ? HEAD_DICT[it[2]] : it[1];
      if (v) it[0].setAttribute('content', v);
    });
    root.setAttribute('lang', en ? 'en' : 'zh-CN');
    var bZh = document.getElementById('langZh');
    var bEn = document.getElementById('langEn');
    if (bZh) bZh.setAttribute('aria-pressed', String(!en));
    if (bEn) bEn.setAttribute('aria-pressed', String(en));
  }

  applyLang(read('qd-lang') || 'zh');

  [['langZh', 'zh'], ['langEn', 'en']].forEach(function (pair) {
    var btn = document.getElementById(pair[0]);
    if (!btn) return;
    btn.addEventListener('click', function () {
      applyLang(pair[1]);
      store('qd-lang', pair[1]);
    });
  });
})();
