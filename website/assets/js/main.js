/* QuickDock 官网交互，无依赖。
   只有四件事：主题切换、顶栏分隔线、滚动入场、说明页目录高亮。 */
(function () {
  'use strict';

  /* ---------- 1. 主题 ---------- */
  var root = document.documentElement;
  var themeBtn = document.getElementById('themeBtn');
  var THEME_KEY = 'qd-theme';

  // 中英两页共用这份脚本，按钮的 title 跟着 <html lang> 走
  var isEn = (root.getAttribute('lang') || '').indexOf('en') === 0;
  var LABEL = isEn
    ? { light: 'Switch to light theme', dark: 'Switch to dark theme' }
    : { light: '切换到浅色主题', dark: '切换到深色主题' };

  function applyTheme(name) {
    root.setAttribute('data-theme', name);
    var meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute('content', name === 'light' ? '#f6f4ef' : '#080c15');
    if (themeBtn) {
      var text = name === 'light' ? LABEL.dark : LABEL.light;
      themeBtn.setAttribute('aria-label', text);
      themeBtn.title = text;
    }
  }

  // 先读存储，没有就跟随系统；再没有就深色（产品本身的默认）
  var saved = null;
  try {
    saved = localStorage.getItem(THEME_KEY);
  } catch (e) {
    /* 隐私模式下读不到，用系统偏好兜底 */
  }

  if (saved === 'light' || saved === 'dark') {
    applyTheme(saved);
  } else if (window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches) {
    applyTheme('light');
  }

  if (themeBtn) {
    themeBtn.addEventListener('click', function () {
      var next = root.getAttribute('data-theme') === 'light' ? 'dark' : 'light';
      applyTheme(next);
      try {
        localStorage.setItem(THEME_KEY, next);
      } catch (e) {
        /* 存不了就算了，本次会话内仍然生效 */
      }
    });
  }

  /* ---------- 2. 顶栏：滚动后出现分隔线 ---------- */
  var bar = document.getElementById('top-bar');
  if (bar) {
    var ticking = false;
    var onScroll = function () {
      if (ticking) return;
      ticking = true;
      window.requestAnimationFrame(function () {
        bar.classList.toggle('scrolled', window.scrollY > 8);
        ticking = false;
      });
    };
    window.addEventListener('scroll', onScroll, { passive: true });
    onScroll();
  }

  /* ---------- 3. 滚动入场 ---------- */
  var rises = document.querySelectorAll('.rise');
  if (rises.length) {
    if (!('IntersectionObserver' in window)) {
      // 老浏览器直接显示，不要因为动画把内容藏起来
      for (var i = 0; i < rises.length; i++) rises[i].classList.add('in');
    } else {
      var io = new IntersectionObserver(
        function (entries) {
          entries.forEach(function (entry) {
            if (entry.isIntersecting) {
              entry.target.classList.add('in');
              io.unobserve(entry.target);
            }
          });
        },
        { rootMargin: '0px 0px -8% 0px', threshold: 0.06 }
      );
      for (var j = 0; j < rises.length; j++) io.observe(rises[j]);
    }
  }

  /* ---------- 4. 说明页：目录跟随滚动高亮 ---------- */
  var docNav = document.getElementById('docNav');
  if (docNav) {
    var links = Array.prototype.slice.call(docNav.querySelectorAll('a'));
    var secs = links
      .map(function (a) {
        return document.getElementById(a.getAttribute('href').slice(1));
      })
      .filter(Boolean);

    var setActive = function (id) {
      links.forEach(function (a) {
        a.classList.toggle('on', a.getAttribute('href') === '#' + id);
      });
    };

    if ('IntersectionObserver' in window && secs.length) {
      var current = secs[0].id;
      var spy = new IntersectionObserver(
        function (entries) {
          entries.forEach(function (entry) {
            if (entry.isIntersecting) current = entry.target.id;
          });
          setActive(current);
        },
        // 用一条接近顶部的窄带做判定，避免同时命中多节
        { rootMargin: '-88px 0px -72% 0px', threshold: 0 }
      );
      secs.forEach(function (s) {
        spy.observe(s);
      });
      setActive(current);
    }

    // 移动端目录横向滚动时，把当前项带进视野（不用 scrollIntoView，避免带动整页）
    docNav.addEventListener('click', function (e) {
      var a = e.target.closest ? e.target.closest('a') : null;
      if (!a) return;
      var el = docNav;
      if (el.scrollWidth > el.clientWidth) {
        var left = a.offsetLeft - (el.clientWidth - a.offsetWidth) / 2;
        el.scrollTo({ left: Math.max(0, left), behavior: 'smooth' });
      }
    });
  }
})();
