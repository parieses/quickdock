/** 计算稿纸 — 前端核心引擎 */
const $ = s => document.querySelector(s)

// ---- postMessage 桥接（调用 goja 后端）----
let _nextId = 1, _pending = {}
function pluginExec(command, input) {
  return new Promise((resolve, reject) => {
    const id = _nextId++; _pending[id] = { resolve, reject }
    window.parent.postMessage({ type: 'plugin:execute', id, command, input }, '*')
  })
}
window.addEventListener('message', (e) => {
  const p = _pending[e.data?.id]
  if (e.data?.type === 'plugin:result' && p) {
    if (e.data.error) p.reject(new Error(e.data.error)); else p.resolve(e.data.data)
    delete _pending[e.data.id]
  }
  // 从命令面板传入的计算文本
  if (e.data?.type === 'plugin:init' && e.data?.data?.text) {
    if (typeof app !== 'undefined' && app && app.activeSheet) {
      commitEdit()
      const l = app._add(e.data.data.text)
      if (l) startEdit(l.id)
    }
  }
})

// ---- 内置轻量求值器 ----
class CalcEngine {
  static FUNCS = { sqrt:Math.sqrt, sin:Math.sin, cos:Math.cos, tan:Math.tan, asin:Math.asin, acos:Math.acos, atan:Math.atan, log:Math.log10, ln:Math.log, log2:Math.log2, exp:Math.exp, abs:Math.abs, round:Math.round, floor:Math.floor, ceil:Math.ceil, min:Math.min, max:Math.max, pow:Math.pow }
  static eval(expr, lv={}, vars={}) {
    expr = expr.replace(/,(?=\d)/g, '') // 去掉千位分隔逗号
    expr = expr.replace(/#(\d{3,})/g, (_, id) => { if (lv[id]===undefined) throw new Error('行 #'+id+' 无值'); return lv[id] })
    for (const [k,v] of Object.entries(vars)) expr = expr.replace(new RegExp('\\b'+k+'\\b','g'), v)
    return CalcEngine._parse(expr)
  }
  static _parse(s) {
    let pos = 0; s = s.trim(); if (!s) return 0
    const sk = () => { while (pos < s.length && s[pos] === ' ') pos++ }
    const pk = () => pos < s.length ? s[pos] : '\0'
    const ex = (c) => { sk(); if (pk() !== c) throw new Error('期望 "'+c+'" 在 '+pos); pos++ }
    function pn() { const st=pos; let d=false; while (pos < s.length) { const c=s[pos]; if (c>='0'&&c<='9')pos++; else if (c==='.'&&!d){d=true;pos++} else break } if (pos===st) throw new Error('非法字符 '+s[st]); return parseFloat(s.slice(st,pos)) }
    function pi() {
      sk(); const ch = pk()
      if (ch==='#') { pos++; while (pos<s.length && s[pos]>='0'&&s[pos]<='9') pos++; throw new Error('行号引用内部错误') }
      if ((ch>='a'&&ch<='z')||(ch>='A'&&ch<='Z')||ch==='_') {
        const st=pos; while (pos<s.length && /[a-zA-Z0-9_]/.test(s[pos])) pos++; const name=s.slice(st,pos); sk()
        if (pk()==='(') { pos++; const args=[]; sk(); if (pk()!==')') { args.push(pe()); while (pk()===',') { pos++; args.push(pe()) } } ex(')'); const fn=CalcEngine.FUNCS[name]; if (!fn) throw new Error('未知函数: '+name); return fn(...args) }
        throw new Error('未知标识符: '+name)
      } return null
    }
    function pa() {
      sk(); const ch=pk()
      if (ch==='-') { const n=pos+1; if (n<s.length && /[0-9.]/.test(s[n])) return pn(); pos++; if (pk()==='('){pos++;const v=pe();ex(')');return -v}; const id=pi(); if (id!==null) return -id; throw new Error('一元负号后缺表达式') }
      if (ch==='(') { pos++; const v=pe(); ex(')'); return v }
      if (/[0-9.]/.test(ch)) return pn(); const id=pi(); if (id!==null) return id
      throw new Error('非法字符 "'+ch+'" 在 '+pos)
    }
    function pp() { let l=pa(); sk(); while (pk()==='^') { pos++; const r=pp(); l=Math.pow(l,r); sk() } return l }
    function pm() { let l=pp(); sk(); while (pos<s.length) { const c=pk(); if (c!=='*'&&c!=='/'&&c!=='%') break; pos++; const r=pp(); if (c==='*') l*=r; else if (c==='/'){if(r===0) throw new Error('除以零'); l/=r} else l%=r; sk() } return l }
    function pe() { let l=pm(); sk(); while (pos<s.length) { const c=pk(); if (c!=='+'&&c!=='-') break; pos++; const r=pm(); if (c==='+') l+=r; else l-=r; sk() } return l }
    const r=pe(); sk(); if (pos<s.length) throw new Error('非法字符 "'+s[pos]+'" 在 '+pos); return r
  }
}

// ---- 工具函数 ----
function fmtNum(v) {
  if (v===Infinity||v===-Infinity) return v>0?'∞':'-∞'
  if (isNaN(v)) return 'NaN'
  if (Number.isInteger(v)&&Math.abs(v)<1e15) return v.toLocaleString('en-US')
  let raw = v.toPrecision(12)
  if (raw.includes('.')&&!raw.includes('e')) raw = raw.replace(/\.?0+$/,'')
  const p = raw.split('.'); p[0] = p[0].replace(/\B(?=(\d{3})+(?!\d))/g,','); return p.join('.')
}
function splitComment(raw) {
  let d=0
  for (let i=0;i<raw.length;i++) {
    if (raw[i]==='(') d++; else if (raw[i]===')') d--
    else if (d===0 && raw[i]==='/' && i+1<raw.length && raw[i+1]==='/') return { expr: raw.slice(0,i).trimEnd(), comment: raw.slice(i+2).trim() }
    else if (d===0 && raw[i]==='#' && (i===0||raw[i-1]===' ')) return { expr: raw.slice(0,i).trimEnd(), comment: raw.slice(i+1).trim() }
  }
  return { expr: raw.trim(), comment: '' }
}
function isVarDef(raw) { const m = raw.trim().match(/^([a-zA-Z_]\w*)\s*=\s*(.+)$/); return m ? { name:m[1], expr:m[2] } : null }

// ---- 稿纸数据模型 ----
const STORAGE_KEY = 'calc_sheets_plugin'

class CalcSheetApp {
  constructor() {
    this.sheets = []; this.activeId = null
    this.undoStack = []; this.redoStack = []
    this.editingId = null
  }

  /** 首次渲染（构造之后调用，确保 DOM 和全局 app 就绪） */
  init() {
    this._load()
    if (!this.activeId && this.sheets.length > 0) this.activeId = this.sheets[0].id
    if (this.sheets.length === 0) this._create('计算稿纸 1')
    this._bindEvents()
    renderAll(this)
  }

  _load() {
    // 先尝试从后端加载
    pluginExec('list-sheets', {}).then(r => {
      if (r && r.sheets && r.sheets.length > 0) {
        // 合并后端数据与本地数据
        const local = JSON.parse(localStorage.getItem(STORAGE_KEY) || '{"sheets":[],"activeId":null}')
        const localIds = new Set(local.sheets.map(s => s.id))
        let merged = local.sheets.slice()
        for (let i = 0; i < r.sheets.length; i++) {
          const rs = r.sheets[i]
          if (!localIds.has(rs.id)) {
            // 从后端加载完整数据
            pluginExec('load-sheet', { id: rs.id }).then(r2 => {
              if (r2 && r2.sheet) {
                merged.push(r2.sheet)
                this.sheets = merged
                this._save()
              }
            }).catch(() => {})
          }
        }
        // 合并已有的 sheets 数据（lines 在本地更完整）
        this.sheets = merged
        if (r.sheets.length > local.sheets.length && !this.activeId) {
          this.activeId = r.sheets[0].id
        }
      }
    }).catch(() => {
      // 后端不可用，回退 LocalStorage
      this._loadLocal()
    })
    // 同时加载本地数据（保证启动速度）
    this._loadLocal()
  }
  _loadLocal() {
    try { const d=localStorage.getItem(STORAGE_KEY); if (d) { const p=JSON.parse(d); this.sheets=p.sheets||[]; this.activeId=p.activeId||null } } catch(e) {}
  }
  _save() {
    try { localStorage.setItem(STORAGE_KEY, JSON.stringify({ sheets:this.sheets, activeId:this.activeId })) } catch(e) {}
    this._syncBackend()
  }
  _syncBackend() {
    const s = this.activeSheet; if (!s) return
    pluginExec('save-sheet', { sheet: { id:s.id, name:s.name, lines:s.lines, pinned:s.pinned } }).catch(() => {})
  }

  get activeSheet() { return this.sheets.find(s=>s.id===this.activeId)||null }
  get sortedSheets() {
    const p=this.sheets.filter(s=>s.pinned), o=this.sheets.filter(s=>!s.pinned)
    p.sort((a,b)=>b.updatedAt.localeCompare(a.updatedAt)); o.sort((a,b)=>b.updatedAt.localeCompare(a.updatedAt))
    return [...p,...o]
  }

  _create(name) {
    const s = { id: Date.now().toString(36)+Math.random().toString(36).slice(2,6), name: name||'未命名', lines:[], createdAt:new Date().toISOString(), updatedAt:new Date().toISOString(), pinned:false }
    this.sheets.push(s); this.activeId=s.id; this._save(); return s
  }
  _delete(id) {
    const i=this.sheets.findIndex(s=>s.id===id); if(i<0) return
    this.sheets.splice(i,1); if(this.activeId===id) this.activeId=this.sheets[0]?.id||null
    this._save(); pluginExec('delete-sheet',{id}).catch(()=>{}); renderAll(this)
  }
  _select(id) {
    if(id===this.activeId) return
    commitEdit(); this.activeId=id; this.undoStack=[]; this.redoStack=[]
    renderAll(this)
  }

  _pushUndo() { const s=this.activeSheet; if(!s) return; this.undoStack.push(JSON.parse(JSON.stringify(s.lines))); if(this.undoStack.length>50) this.undoStack.shift(); this.redoStack=[] }
  _undo() { const s=this.activeSheet; if(!s||!this.undoStack.length) return; this.redoStack.push(JSON.parse(JSON.stringify(s.lines))); s.lines=this.undoStack.pop(); this._recalc() }
  _redo() { const s=this.activeSheet; if(!s||!this.redoStack.length) return; this.undoStack.push(JSON.parse(JSON.stringify(s.lines))); s.lines=this.redoStack.pop(); this._recalc() }
  _pad(n) { return String(n).padStart(3,'0') }

  _add(raw='', indent=0) {
    const s=this.activeSheet; if(!s||s.lines.length>=1000) return null
    this._pushUndo()
    const l = { id:this._pad(s.lines.length+1), raw, remark:'', result:null, error:null, indentLevel:indent, dependencies:[] }
    s.lines.push(l); this._recalc(); return l
  }
  _insertAfter(id, raw='') {
    const s=this.activeSheet; if(!s) return null
    const i=s.lines.findIndex(l=>l.id===id); if(i<0) return this._add(raw)
    this._pushUndo()
    const l = { id:this._pad(s.lines.length+1), raw, remark:'', result:null, error:null, indentLevel:0, dependencies:[] }
    s.lines.splice(i+1,0,l); this._renum(); this._recalc(); return l
  }
  _renum() { const s=this.activeSheet; if(!s) return; s.lines.forEach((l,i)=>l.id=this._pad(i+1)) }
  _clear() { const s=this.activeSheet; if(!s||!s.lines.length) return; this._pushUndo(); s.lines=[]; this._recalc() }
  _update(id, raw) { const s=this.activeSheet; if(!s) return false; const l=s.lines.find(x=>x.id===id); if(!l||l.raw===raw) return false; this._pushUndo(); l.raw=raw; this._recalc(); return true }

  _recalc() {
    const s=this.activeSheet; if(!s) return; const vl={},vr={}
    for (let r=0;r<3;r++) for (const l of s.lines) { const vd=isVarDef(l.raw); if(!vd) continue; try { const v=CalcEngine.eval(vd.expr,vl,vr); vr[vd.name]=v; vl[l.id]=v; l.result=v; l.error=null } catch(e) { l.error=e.message; l.result=null } }
    for (const l of s.lines) {
      if (isVarDef(l.raw)) continue
      if (!l.raw.trim()) { l.result=null; l.error=null; l.dependencies=[]; continue }
      const {expr}=splitComment(l.raw); if (!expr) { l.result=null; l.error=null; l.dependencies=[]; continue }
      l.dependencies=[...expr.matchAll(/#(\d{3,})/g)].map(m=>m[1])
      try { const v=CalcEngine.eval(expr,vl,vr); l.result=v; l.error=null; vl[l.id]=v }
      catch(e) { l.error=e.message; l.result=null; delete vl[l.id] }
    }
    s.updatedAt=new Date().toISOString(); this._save()
  }


  _bindEvents() {
    const self = this
    // 全局键盘
    document.addEventListener('keydown', (e) => {
      const ctrl = e.ctrlKey||e.metaKey
      if (ctrl && e.key==='z') { e.preventDefault(); if(e.shiftKey) self._redo(); else self._undo(); renderAll(self) }
      else if (ctrl && e.key==='y') { e.preventDefault(); self._redo(); renderAll(self) }
      else if (e.key==='Enter' && self.editingId===null) { const s=self.activeSheet; if(s&&s.lines.length>0) startEdit(s.lines[s.lines.length-1].id) }
    })
    // 容器点击
    $('#linesContainer').addEventListener('click', (e) => {
      if (e.target.closest('.line-row')||e.target.closest('.empty-hint')) return
      commitEdit(); const l=self._add(''); if(l) startEdit(l.id)
    })
    $('#emptyHint').addEventListener('click', () => { commitEdit(); const l=self._add(''); if(l) startEdit(l.id) })
    // 输入区键盘
    $('#linesContainer').addEventListener('keydown', onEditKeydown)
    // 工具栏
    $('#btnNew').addEventListener('click', () => { const n=prompt('稿纸名称:','新建稿纸'); if(n){commitEdit();self._create(n);renderAll(self)} })
    $('#btnRename').addEventListener('click', () => { const s=self.activeSheet; if(!s) return; const n=prompt('新名称:',s.name); if(n&&n!==s.name){s.name=n;self._save();renderAll(self)}})
    $('#btnSave').addEventListener('click', () => { self._save(); const b=$('#btnSave'); b.innerHTML='✓ 已保存'; setTimeout(()=>{b.innerHTML='💾 保存'},1500) })
    $('#btnDel').addEventListener('click', async () => { const s=self.activeSheet; if(!s) return; if(!(await qdConfirm('删除稿纸"'+s.name+'"？'))) return; commitEdit(); self._delete(s.id) })
    $('#btnClear').addEventListener('click', async () => { if(!self.activeSheet||!self.activeSheet.lines.length) return; if(!(await qdConfirm('清空所有行？'))) return; commitEdit(); self._clear(); renderAll(self) })
    $('#btnUndo').addEventListener('click', () => { self._undo(); renderAll(self) })
    $('#btnRedo').addEventListener('click', () => { self._redo(); renderAll(self) })
    $('#sheetSelect').addEventListener('change', (e) => { commitEdit(); self._select(e.target.value) })
  }
}

// ---- 渲染函数（接受 ctx = app，不依赖全局 app） ----
function renderSheetSelect(ctx) {
  const sel = $('#sheetSelect'); if(!sel) return; sel.innerHTML = ''
  for (const s of ctx.sortedSheets) {
    const o = document.createElement('option'); o.value=s.id; o.textContent=(s.pinned?'📌 ':'')+s.name
    if (s.id===ctx.activeId) o.selected=true; sel.appendChild(o)
  }
}

function renderTable(ctx) {
  const ct = $('#linesContainer'); if(!ct) return
  // 只移除 .line-row，不碰 #emptyHint
  ct.querySelectorAll('.line-row').forEach(el => el.remove())
  const s = ctx.activeSheet
  const eh = $('#emptyHint')
  if (!s || !s.lines.length) {
    if (eh) eh.style.display = 'flex'
    const lc = $('#lineCount'); if(lc) lc.textContent = '0 行'
    return
  }
  if (eh) eh.style.display = 'none'
  const lc = $('#lineCount'); if(lc) lc.textContent = s.lines.length+' 行'

  for (const l of s.lines) {
    const row = document.createElement('div'); row.className='line-row'+(ctx.editingId===l.id?' editing':''); row.dataset.id=l.id
    // 行号列
    const cl = document.createElement('span'); cl.className='cl'
    const num = document.createElement('span'); num.className='line-num'; num.textContent='#'+l.id; cl.appendChild(num)
    if (l.dependencies.length>0) { const d=document.createElement('span'); d.className='dep-dot'; d.title='↳ '+l.dependencies.map(d=>'#'+d).join(', '); cl.appendChild(d) }
    row.appendChild(cl)
    // 备注列
    const cm = document.createElement('span'); cm.className='cm'
    const rminp = document.createElement('input'); rminp.className='remark-input'; rminp.type='text'; rminp.placeholder='备注'; rminp.value=l.remark||''
    cm.appendChild(rminp)
    row.appendChild(cm)
    // 算式列
    const ci = document.createElement('span'); ci.className='ci'
    if (ctx.editingId===l.id) {
      const inp=document.createElement('input'); inp.className='line-input'; inp.type='text'; inp.value=l.raw; inp.spellcheck=false; ci.appendChild(inp)
      setTimeout(()=>{inp.focus();inp.setSelectionRange(inp.value.length,inp.value.length)},0)
    } else {
      const t=document.createElement('span'); t.className='line-text'
      let txt=''; if (l.indentLevel>0) txt='\u00A0'.repeat(l.indentLevel*2)
      txt+=l.raw||'↵ 点击编辑'; t.textContent=txt
      if (!l.raw) t.style.color='var(--text3)'; ci.appendChild(t)
    }
    row.appendChild(ci)
    // 结果列
    const cr = document.createElement('span'); cr.className='cr'; cr.title='点击复制结果'
    if (l.error) { const e=document.createElement('span'); e.className='result-error'; e.textContent='⚠ '+l.error; cr.appendChild(e) }
    else if (l.result!==null) { const v=document.createElement('span'); v.className='result-value'; v.textContent='= '+fmtNum(l.result); cr.appendChild(v) }
    row.appendChild(cr)
    // 事件绑定
    if (ctx.editingId!==l.id) ci.addEventListener('click',(e)=>{e.stopPropagation();startEdit(l.id)})
    cr.addEventListener('click',()=>{if(l.result!==null){copyText(fmtNum(l.result));cr.style.opacity='0.5';setTimeout(()=>cr.style.opacity='1',200)}})
    // 备注变更自动保存
    rminp.addEventListener('change', () => { if(l.remark!==rminp.value){const s=ctx.activeSheet;if(s){l.remark=rminp.value;s.updatedAt=new Date().toISOString();ctx._save()}} })
    ct.appendChild(row)
  }
}

function renderAll(ctx) { renderSheetSelect(ctx); renderTable(ctx) }

// ---- 编辑交互 ----
function startEdit(id) {
  const s = app.activeSheet; if (!s) return
  const l = s.lines.find(x=>x.id===id); if (!l) return
  app.editingId = id; renderAll(app)
}

function commitEdit() {
  if (app.editingId===null) return
  const row = document.querySelector('.line-row[data-id="'+app.editingId+'"] .line-input')
  const val = row ? row.value : ''
  app._update(app.editingId, val||'')
  app.editingId = null
  renderAll(app)
}

function onEditKeydown(e) {
  const s=app.activeSheet; if (!s||app.editingId===null) return
  const idx=s.lines.findIndex(l=>l.id===app.editingId)
  const inp = document.querySelector('.line-row[data-id="'+app.editingId+'"] .line-input'); if (!inp) return
  if (e.key==='Enter'&&!e.shiftKey) {
    e.preventDefault(); const val=inp.value
    if (val.trim()) { app._update(app.editingId,val); const pl=s.lines.find(l=>l.id===app.editingId); const ind=pl?.indentLevel||0; const nl=app._insertAfter(app.editingId,''); if(nl){nl.indentLevel=ind;startEdit(nl.id)}}
    else if (idx<s.lines.length-1) startEdit(s.lines[idx+1].id)
  } else if (e.key==='Tab') { e.preventDefault(); const st=inp.selectionStart||0,en=inp.selectionEnd||0; inp.value=inp.value.slice(0,st)+'\t'+inp.value.slice(en); setTimeout(()=>inp.setSelectionRange(st+1,st+1),0) }
  else if (e.key==='ArrowUp') { e.preventDefault(); if (idx>0) { commitEdit(); startEdit(s.lines[idx-1].id) } }
  else if (e.key==='ArrowDown') { e.preventDefault(); if (idx<s.lines.length-1) { commitEdit(); startEdit(s.lines[idx+1].id) } }
  else if (e.key==='Escape') { commitEdit() }
}

// ---- 启动 ----
const app = new CalcSheetApp()
app._commit = commitEdit
app.init()
