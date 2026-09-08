// 插件对话框桥接：将插件 iframe 内的原生 confirm/alert 替换为宿主的 toast 对话框。
// 通过 postMessage 与宿主通信（与 pluginExec 同源机制）。
//
// - qdConfirm(message): Promise<boolean>  —— 路由到宿主 toast.confirm
// - qdAlert(message):  Promise<void>      —— 路由到宿主 toast（success 样式）
// - qdPickFile(opts?): Promise<string|null> —— 原生文件选择（Wails Dialog）。
//     opts: { title?, filter?, pattern? }；取消/失败返回 null。iframe 内的
//     input[type=file] 受沙箱限制且引发宿主失焦问题，选文件一律走此桥。
// - qdPickFolder(opts?): Promise<string|null> —— 原生目录选择（Wails Dialog 目录模式）。
//     opts: { title? }；取消/失败返回 null。选目录一律走此桥，避免插件自行
//     spawn PowerShell/AppleScript/zenity 等系统对话框（脆弱且引发失焦问题）。
// - qdReadFile(path): Promise<{type:'text',content:string}|{type:'dataurl',content:string}|null>
//     读取 qdPickFile 选中的文件内容；文本→原文，图片/二进制→dataURL。失败/取消返回 null。
// - window.alert 被全局覆盖为 qdAlert（fire-and-forget，安全）
// - window.confirm 不强制覆盖（避免破坏插件的同步调用语义），使用确认框的插件应显式 await qdConfirm
// - Esc 键盘转发：iframe 抢占焦点后 keydown 不会跨 frame 传到父文档，父级的
//   document 级监听彻底失效（表现为「只能鼠标点返回」）。桥内把 Esc 命中后
//   postMessage 给宿主，由宿主关插件页/面板（plugins: 'plugin:esc'）。
//   两条让位规则，避免劫持插件自己的 Esc 语义：
//   a) e.defaultPrevented —— 插件已消费该 Esc（如关闭自家弹窗），宿主不再导航；
//   b) 焦点在可编辑元素（INPUT/TEXTAREA/SELECT/contentEditable）—— 视为插件表单内操作，不外传。

const BRIDGE_SCRIPT = `<script>(function(){if(window.__qdBridge)return;window.__qdBridge=true;var p={},s=0;window.addEventListener('message',function(e){var d=e.data;if(!d||!d.type)return;if(d.type==='plugin:confirm-result'){var c=p[d.id];if(c){delete p[d.id];c(d.ok)}}else if(d.type==='plugin:alert-result'){var a=p[d.id];if(a){delete p[d.id];a()}}else if(d.type==='plugin:pickfile-result'){var f=p[d.id];if(f){delete p[d.id];f(d.path||null)}}else if(d.type==='plugin:pickfolder-result'){var fo=p[d.id];if(fo){delete p[d.id];fo(d.path||null)}}else if(d.type==='plugin:readfile-result'){var rf=p[d.id];if(rf){delete p[d.id];rf((d&&d.payload)||null)}}else if(d.type==='plugin:theme'&&d.data&&d.data.theme){document.documentElement.setAttribute('data-theme',d.data.theme)}});function post(t,x){(window.parent||window).postMessage(Object.assign({type:t},x),'*')}window.addEventListener('keydown',function(e){if(e.key!=='Escape'||e.defaultPrevented)return;var t=e.target;if(t&&(t.tagName==='INPUT'||t.tagName==='TEXTAREA'||t.tagName==='SELECT'||t.isContentEditable))return;e.preventDefault();e.stopPropagation();post('plugin:esc',{})});window.qdConfirm=function(m){return new Promise(function(r){var id='c'+(++s);p[id]=r;post('plugin:confirm',{id:id,message:m})})};window.qdAlert=function(m){return new Promise(function(r){var id='a'+(++s);p[id]=r;post('plugin:alert',{id:id,message:m})})};window.qdPickFile=function(o){o=o||{};return new Promise(function(r){var id='f'+(++s);p[id]=r;post('plugin:pickfile',{id:id,title:o.title||'',filter:o.filter||'',pattern:o.pattern||''})})};window.qdPickFolder=function(o){o=o||{};return new Promise(function(r){var id='fo'+(++s);p[id]=r;post('plugin:pickfolder',{id:id,title:o.title||''})})};window.qdReadFile=function(path){return new Promise(function(r){if(!path){r(null);return};var id='r'+(++s);p[id]=r;post('plugin:readfile',{id:id,path:path})})};window.alert=function(m){window.qdAlert(m)}})()<\/script>`

// injectPluginBridge 将桥接脚本注入插件 HTML（插入到 <head> 之后；无 head 则前置）
export function injectPluginBridge(html: string): string {
  if (!html) return html
  if (/<head[^>]*>/i.test(html)) {
    return html.replace(/<head[^>]*>/i, (m) => m + BRIDGE_SCRIPT)
  }
  return BRIDGE_SCRIPT + html
}
