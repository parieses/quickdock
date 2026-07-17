/**
 * 计算稿纸 — Goja 后端
 * 提供 SQLite 持久化存储
 */
function handleInitialize(params) {
  // 创建稿纸表
  api.db.exec(
    'CREATE TABLE IF NOT EXISTS sheets (' +
    'id TEXT PRIMARY KEY,' +
    'name TEXT NOT NULL,' +
    'data TEXT NOT NULL,' +
    'created_at TEXT,' +
    'updated_at TEXT' +
    ')'
  )
  api.log('数据库就绪')
  return { status: 'ready', version: '0.2.0' }
}

function handleExecute(params) {
  var command = params.command || ''
  var input = params.input || {}

  // open-calc: 仅打开前端窗口，无需后端操作
  if (command === 'open-calc') {
    return { status: 'ok', frontendOnly: true }
  }

  if (command === 'list-sheets') {
    var rows = api.db.query('SELECT id, name, created_at, updated_at FROM sheets ORDER BY updated_at DESC')
    return { sheets: rows || [] }
  }

  if (command === 'save-sheet') {
    var s = input.sheet || {}
    if (!s.id || !s.name) return { error: '缺少 id 或 name' }
    var now = new Date().toISOString()
    var data = JSON.stringify({ lines: s.lines || [], pinned: !!s.pinned })
    api.db.exec(
      "INSERT INTO sheets (id, name, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET name=excluded.name, data=excluded.data, updated_at=excluded.updated_at",
      s.id, s.name, data, now, now
    )
    return { saved: true }
  }

  if (command === 'load-sheet') {
    var id = input.id || ''
    if (!id) return { error: '缺少 id' }
    var rows = api.db.query("SELECT * FROM sheets WHERE id=?", id)
    if (rows && rows.length > 0) {
      var r = rows[0]
      var d = JSON.parse(r.data || '{}')
      return { sheet: { id: r.id, name: r.name, lines: d.lines || [], pinned: !!d.pinned, createdAt: r.created_at, updatedAt: r.updated_at } }
    }
    return { sheet: null }
  }

  if (command === 'delete-sheet') {
    var id = input.id || ''
    if (!id) return { error: '缺少 id' }
    api.db.exec("DELETE FROM sheets WHERE id=?", id)
    return { deleted: true }
  }

  if (command === 'search-sheets') {
    var q = input.query || ''
    var rows = api.db.query(
      "SELECT id, name, created_at, updated_at FROM sheets WHERE name LIKE ? ORDER BY updated_at DESC",
      "%" + q + "%"
    )
    return { sheets: rows || [] }
  }

  throw new Error('未知命令: ' + command)
}
