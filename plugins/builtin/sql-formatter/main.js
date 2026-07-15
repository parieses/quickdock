/**
 * SQL 格式化 — Goja 后端
 * 关键字大写、子句换行、缩进对齐
 * 支持 MySQL / PostgreSQL 常见语法
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.1.0' }
}

function handleExecute(params) {
  var input = params.input || {}
  var text = (input.text || '').trim()
  if (!text) return { error: '请输入 SQL 语句' }

  var result = formatSQL(text)
  return {
    text: result,
    display: result
  }
}

// ---- SQL 关键字（按优先级排序）----

var MAJOR_KEYWORDS = [
  'SELECT', 'FROM', 'WHERE', 'AND', 'OR', 'SET',
  'INSERT INTO', 'VALUES', 'UPDATE', 'DELETE FROM',
  'CREATE TABLE', 'CREATE INDEX', 'CREATE VIEW', 'CREATE DATABASE',
  'ALTER TABLE', 'DROP TABLE', 'DROP INDEX', 'DROP VIEW',
  'ORDER BY', 'GROUP BY', 'HAVING',
  'LIMIT', 'OFFSET',
  'JOIN', 'INNER JOIN', 'LEFT JOIN', 'RIGHT JOIN', 'FULL JOIN', 'CROSS JOIN',
  'ON', 'USING',
  'UNION', 'UNION ALL', 'INTERSECT', 'EXCEPT',
  'IN', 'EXISTS', 'NOT', 'BETWEEN', 'LIKE', 'IS NULL', 'IS NOT NULL',
  'AS', 'DISTINCT', 'ALL',
  'CASE', 'WHEN', 'THEN', 'ELSE', 'END',
  'WITH', 'RECURSIVE',
  'RETURNING', 'ON CONFLICT', 'DO UPDATE', 'DO NOTHING',
  'TRUNCATE', 'MERGE', 'MATCHED',
  'BEGIN', 'COMMIT', 'ROLLBACK',
  'DECLARE', 'SET',
  'SHOW', 'DESCRIBE', 'EXPLAIN', 'ANALYZE',
  'INDEX', 'UNIQUE', 'PRIMARY KEY', 'FOREIGN KEY', 'REFERENCES',
  'DEFAULT', 'CHECK', 'CONSTRAINT',
  'CASCADE', 'RESTRICT',
  'ASC', 'DESC',
  'NULLS FIRST', 'NULLS LAST',
  'WINDOW', 'PARTITION BY', 'OVER',
  'FETCH', 'NEXT', 'ROWS', 'ONLY',
  'FOR UPDATE', 'FOR SHARE', 'OF', 'SKIP LOCKED', 'NOWAIT'
]

// 大写化关键字
function uppercaseKeywords(sql) {
  var upper = sql.toUpperCase()
  var result = sql
  // 从长到短替换，避免部分匹配
  var sorted = MAJOR_KEYWORDS.slice().sort(function(a,b){ return b.length - a.length })
  for (var i = 0; i < sorted.length; i++) {
    var kw = sorted[i]
    var re = new RegExp('\\b' + escapeRegExp(kw) + '\\b', 'gi')
    result = result.replace(re, kw)
  }
  return result
}

function escapeRegExp(str) {
  return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

// ---- 核心格式化 ----

function formatSQL(sql) {
  // 1. 关键字大写
  sql = uppercaseKeywords(sql)

  // 2. 在主要子句前加换行
  var clauses = [
    'SELECT', 'FROM', 'WHERE', 'ORDER BY', 'GROUP BY', 'HAVING',
    'LIMIT', 'OFFSET', 'SET', 'VALUES', 'INSERT INTO', 'UPDATE',
    'DELETE FROM', 'CREATE TABLE', 'ALTER TABLE', 'DROP TABLE',
    'INNER JOIN', 'LEFT JOIN', 'RIGHT JOIN', 'FULL JOIN', 'CROSS JOIN', 'JOIN',
    'UNION', 'UNION ALL', 'INTERSECT', 'EXCEPT',
    'ON', 'USING', 'RETURNING',
    'WITH', 'RECURSIVE',
    'BEGIN', 'COMMIT', 'ROLLBACK',
    'FOR UPDATE', 'FOR SHARE',
    'WINDOW'
  ]

  for (var i = 0; i < clauses.length; i++) {
    var re = new RegExp('\\b' + escapeRegExp(clauses[i]) + '\\b', 'gi')
    sql = sql.replace(re, '\n' + clauses[i])
  }

  // 3. AND / OR 缩进
  sql = sql.replace(/\nAND\b/gi,   '\n  AND')
  sql = sql.replace(/\nOR\b/gi,    '\n  OR')

  // 4. 清理多余空行和首尾空白
  sql = sql.replace(/\n{2,}/g, '\n')
  sql = sql.replace(/^\n+/, '')
  sql = sql.replace(/\n+$/, '')

  // 5. 逗号前加换行（在 SELECT 子句中每列一行）
  // 只在 SELECT / GROUP BY / ORDER BY 子句内处理逗号
  var lines = sql.split('\n')
  var result = []
  for (var j = 0; j < lines.length; j++) {
    var line = lines[j]
    var trimmed = line.trim()

    // 检测是否在 SELECT / 逗号子句中
    if (/^(SELECT|\\s*[,])/.test(trimmed) ||
        (result.length > 0 && /^\s*[,]/.test(line) && !/^\s*(FROM|WHERE|ORDER BY|GROUP BY|HAVING|LIMIT|OFFSET|JOIN|AND|OR)/.test(trimmed))) {
      // 替换逗号为逗号+换行+缩进
      var indent = /^\s*/.exec(line)[0]
      // 避免在函数调用内（如 COUNT(a,b)）的逗号换行
      var depth = 0
      var chars = ''
      for (var k = 0; k < line.length; k++) {
        var ch = line[k]
        if (ch === '(') depth++
        if (ch === ')') depth--
        if (ch === ',' && depth === 0) {
          chars += ',\n  ' + indent
        } else {
          chars += ch
        }
      }
      result.push(chars)
    } else {
      result.push(line)
    }
  }

  sql = result.join('\n')

  // 6. 清理首尾
  sql = sql.trim()

  // 如果没变化（可能不是 SQL），原样返回
  if (sql === sql.trim() && sql.indexOf('\n') === -1 && sql.length > 50) {
    // 至少来个关键字大写
    return uppercaseKeywords(sql.trim())
  }

  return sql
}
