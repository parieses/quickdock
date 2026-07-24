// 插件对话框桥接：将插件 iframe 内的原生 confirm/alert 替换为宿主的 toast 对话框。
// 通过 postMessage 与宿主通信（与 pluginExec 同源机制）。
//
// - qdConfirm(message): Promise<boolean>  —— 路由到宿主 toast.confirm
// - qdAlert(message):  Promise<void>      —— 路由到宿主 toast（success 样式）
// - window.alert 被全局覆盖为 qdAlert（fire-and-forget，安全）
// - window.confirm 不强制覆盖（避免破坏插件的同步调用语义），使用确认框的插件应显式 await qdConfirm

const BRIDGE_SCRIPT = `<script>(function(){if(window.__qdBridge)return;window.__qdBridge=true;var p={},s=0;window.addEventListener('message',function(e){var d=e.data;if(!d||!d.type)return;if(d.type==='plugin:confirm-result'){var c=p[d.id];if(c){delete p[d.id];c(d.ok)}}else if(d.type==='plugin:alert-result'){var a=p[d.id];if(a){delete p[d.id];a()}}});function post(t,x){(window.parent||window).postMessage(Object.assign({type:t},x),'*')}window.qdConfirm=function(m){return new Promise(function(r){var id='c'+(++s);p[id]=r;post('plugin:confirm',{id:id,message:m})})};window.qdAlert=function(m){return new Promise(function(r){var id='a'+(++s);p[id]=r;post('plugin:alert',{id:id,message:m})})};window.alert=function(m){window.qdAlert(m)}})</script>`

// injectPluginBridge 将桥接脚本注入插件 HTML（插入到 <head> 之后；无 head 则前置）
export function injectPluginBridge(html: string): string {
  if (!html) return html
  if (/<head[^>]*>/i.test(html)) {
    return html.replace(/<head[^>]*>/i, (m) => m + BRIDGE_SCRIPT)
  }
  return BRIDGE_SCRIPT + html
}
