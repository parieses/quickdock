/**
 * Base64 编码解码插件 — 纯前端实现
 * 
 * 所有编码/解码操作在浏览器内完成，无需后端调用。
 * 同时提供 postMessage 通信桥，可与 QuickDock 主程序交互。
 */

(function() {
  'use strict';

  // ---- DOM 引用 ----
  const inputText   = document.getElementById('inputText');
  const outputText  = document.getElementById('outputText');
  const btnEncode   = document.getElementById('btnEncode');
  const btnDecode   = document.getElementById('btnDecode');
  const btnAction   = document.getElementById('btnAction');
  const btnClear    = document.getElementById('btnClear');
  const btnSwap     = document.getElementById('btnSwap');
  const btnCopy     = document.getElementById('btnCopy');
  const actionLabel = document.getElementById('actionLabel');
  const inputLabel  = document.getElementById('inputLabel');
  const outputLabel = document.getElementById('outputLabel');
  const statusBar   = document.getElementById('statusBar');

  // ---- 状态 ----
  let currentMode = 'encode'; // 'encode' | 'decode'

  // ---- Base64 核心函数 ----

  /** UTF-8 字符串 → Base64 */
  function btoaUTF8(str) {
    // 先 encodeURIComponent 再转 byte，避免中文乱码
    const bytes = new TextEncoder().encode(str);
    return btoa(String.fromCharCode(...bytes));
  }

  /** Base64 → UTF-8 字符串 */
  function atobUTF8(b64) {
    const binary = atob(b64.replace(/\s/g, ''));
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) {
      bytes[i] = binary.charCodeAt(i);
    }
    return new TextDecoder('utf-8').decode(bytes);
  }

  /** 编码操作 */
  function encode(text) {
    if (!text) throw new Error('输入不能为空');
    return btoaUTF8(text);
  }

  /** 解码操作 */
  function decode(text) {
    if (!text) throw new Error('输入不能为空');
    // 移除可能的空白字符
    const clean = text.replace(/\s/g, '');
    // 校验是否为合法 Base64
    if (!/^[A-Za-z0-9+/]*={0,2}$/.test(clean)) {
      throw new Error('输入不是有效的 Base64 编码');
    }
    return atobUTF8(clean);
  }

  // ---- UI 操作 ----

  function setMode(mode) {
    currentMode = mode;
    btnEncode.classList.toggle('active', mode === 'encode');
    btnDecode.classList.toggle('active', mode === 'decode');

    if (mode === 'encode') {
      inputLabel.textContent  = '输入文本（UTF-8）';
      outputLabel.textContent = 'Base64 编码结果';
      actionLabel.textContent = '编码';
      inputText.placeholder   = '在此输入要编码的文本...';
    } else {
      inputLabel.textContent  = '输入 Base64';
      outputLabel.textContent = '解码结果（UTF-8）';
      actionLabel.textContent = '解码';
      inputText.placeholder   = '在此输入 Base64 编码...';
    }

    setStatus('', '');
  }

  function doConvert() {
    const text = inputText.value;
    try {
      let result;
      if (currentMode === 'encode') {
        result = encode(text);
        setStatus(`✓ 编码完成，长度: ${result.length} 字符`, 'success');
      } else {
        result = decode(text);
        setStatus(`✓ 解码完成，长度: ${result.length} 字符`, 'success');
      }
      outputText.value = result;
    } catch (e) {
      outputText.value = '';
      setStatus(`✗ ${e.message}`, 'error');
    }
  }

  function clearAll() {
    inputText.value = '';
    outputText.value = '';
    setStatus('已清空', '');
    inputText.focus();
  }

  function swapInputOutput() {
    const tmp = inputText.value;
    inputText.value = outputText.value;
    outputText.value = '';
    // 自动切换模式
    if (currentMode === 'encode') {
      setMode('decode');
    } else {
      setMode('encode');
    }
    setStatus('已交换输入输出', 'info');
  }

  async function copyResult() {
    if (!outputText.value) {
      setStatus('没有可复制的内容', 'error');
      return;
    }
    try {
      await navigator.clipboard.writeText(outputText.value);
      setStatus('✓ 已复制到剪贴板', 'success');
    } catch (e) {
      // fallback
      outputText.select();
      document.execCommand('copy');
      setStatus('✓ 已复制到剪贴板', 'success');
    }
  }

  function setStatus(msg, type) {
    statusBar.textContent = msg;
    statusBar.className = 'b64-status' + (type ? ' ' + type : '');
  }

  // ---- 实时转换（输入时自动计算） ----
  let debounceTimer = null;
  function onInputChange() {
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => {
      if (inputText.value.trim()) {
        doConvert();
      } else {
        outputText.value = '';
        setStatus('', '');
      }
    }, 300);
  }

  // ---- 键盘快捷键 ----
  function onKeydown(e) {
    // Ctrl+Enter 或 Cmd+Enter 执行转换
    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
      e.preventDefault();
      doConvert();
    }
    // Escape 清空
    if (e.key === 'Escape') {
      clearAll();
    }
    // Tab 在输入输出之间切换焦点
    if (e.key === 'Tab' && !e.shiftKey) {
      if (document.activeElement === inputText) {
        e.preventDefault();
        outputText.focus();
      }
    }
  }

  // ---- 事件绑定 ----
  function init() {
    btnEncode.addEventListener('click', () => setMode('encode'));
    btnDecode.addEventListener('click', () => setMode('decode'));
    btnAction.addEventListener('click', doConvert);
    btnClear.addEventListener('click', clearAll);
    btnSwap.addEventListener('click', swapInputOutput);
    btnCopy.addEventListener('click', copyResult);
    inputText.addEventListener('input', onInputChange);

    // 键盘快捷键
    document.addEventListener('keydown', onKeydown);

    // 自动聚焦输入框
    inputText.focus();

    // 设置初始状态
    setMode('encode');
    setStatus('就绪 — 输入文本将自动编码', 'info');
  }

  // ---- 页面加载时初始化 ----
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

  // ---- postMessage 通信桥（与 QuickDock 主程序交互） ----
  function sendToHost(method, params) {
    window.parent.postMessage({
      type: 'plugin:call',
      pluginId: 'com.quickdock.base64',
      command: method,
      input: params
    }, '*');
  }

  // 监听主程序返回结果
  window.addEventListener('message', (event) => {
    if (event.data.type === 'plugin:result') {
      const result = event.data.result;
      if (result && result.output) {
        outputText.value = result.output;
        if (result.action === 'encode') {
          setMode('encode');
        } else if (result.action === 'decode') {
          setMode('decode');
        }
        setStatus(`✓ ${result.action === 'encode' ? '编码' : '解码'}完成`, 'success');
      }
    }
  });

  // 暴露 API 方便调试
  window.__base64Plugin = {
    encode,
    decode,
    setMode,
    doConvert,
    clearAll,
    swapInputOutput,
    copyResult,
    sendToHost,
  };
})();
