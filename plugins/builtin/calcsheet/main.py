"""
QuickDock 计算稿纸插件 — Python 后端
功能：多行表达式求值、变量引用、行号引用、稿纸持久化（SQLite）
通信方式：stdin/stdout JSON-RPC 2.0
"""
import sys
import json
import math
import re
import os
import time
import sqlite3
import threading

DATA_DIR = os.path.join(os.path.expanduser("~"), ".quickdock")
os.makedirs(DATA_DIR, exist_ok=True)
DB_PATH = os.path.join(DATA_DIR, "calc-sheets.db")

# ---- SQLite 初始化 ----
_local = threading.local()

def get_db():
    """获取线程本地的数据库连接"""
    if not hasattr(_local, 'conn') or _local.conn is None:
        _local.conn = sqlite3.connect(DB_PATH)
        _local.conn.row_factory = sqlite3.Row
        _local.conn.execute("PRAGMA journal_mode=WAL")
        _local.conn.execute("PRAGMA synchronous=NORMAL")
        _init_db(_local.conn)
    return _local.conn

def _init_db(conn):
    conn.execute("""
        CREATE TABLE IF NOT EXISTS sheets (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL,
            data TEXT NOT NULL,
            created_at TEXT NOT NULL,
            updated_at TEXT NOT NULL,
            pinned INTEGER DEFAULT 0
        )
    """)
    conn.commit()

def db_list_sheets():
    conn = get_db()
    rows = conn.execute(
        "SELECT data FROM sheets ORDER BY pinned DESC, updated_at DESC"
    ).fetchall()
    result = []
    for row in rows:
        try:
            result.append(json.loads(row['data']))
        except:
            continue
    return result

def db_save_sheet(sheet_data):
    conn = get_db()
    sheet_id = sheet_data.get('id', '')
    name = sheet_data.get('name', '未命名')
    created_at = sheet_data.get('createdAt', time.strftime('%Y-%m-%dT%H:%M:%S'))
    updated_at = sheet_data.get('updatedAt', time.strftime('%Y-%m-%dT%H:%M:%S'))
    pinned = 1 if sheet_data.get('pinned') else 0
    data_json = json.dumps(sheet_data, ensure_ascii=False)
    conn.execute(
        "INSERT OR REPLACE INTO sheets (id, name, data, created_at, updated_at, pinned) VALUES (?, ?, ?, ?, ?, ?)",
        (sheet_id, name, data_json, created_at, updated_at, pinned)
    )
    conn.commit()
    return {"path": DB_PATH, "name": name, "id": sheet_id}

def db_delete_sheet(sheet_id):
    conn = get_db()
    conn.execute("DELETE FROM sheets WHERE id = ?", (sheet_id,))
    conn.commit()
    return {"deleted": True, "id": sheet_id}

def db_search_sheets(keyword):
    conn = get_db()
    pattern = f"%{keyword}%"
    rows = conn.execute(
        "SELECT data FROM sheets WHERE name LIKE ? OR data LIKE ? ORDER BY pinned DESC, updated_at DESC",
        (pattern, pattern)
    ).fetchall()
    result = []
    for row in rows:
        try:
            result.append(json.loads(row['data']))
        except:
            continue
    return result

# ---- 表达式求值 ----
SAFE_FUNCS = {
    'abs': abs, 'round': round, 'min': min, 'max': max,
    'sqrt': math.sqrt, 'pow': math.pow,
    'sin': math.sin, 'cos': math.cos, 'tan': math.tan,
    'asin': math.asin, 'acos': math.acos, 'atan': math.atan,
    'log': math.log10, 'ln': math.log, 'log2': math.log2, 'exp': math.exp,
    'ceil': math.ceil, 'floor': math.floor, 'trunc': math.trunc,
    'radians': math.radians, 'degrees': math.degrees,
    'pi': math.pi, 'e': math.e, 'tau': math.tau,
}

SAFE_ENV = {
    '__builtins__': {},
    'True': True, 'False': False, 'None': None,
    **SAFE_FUNCS,
}

def safe_eval(expr):
    expr = expr.strip()
    expr = expr.replace('^', '**').replace('×', '*').replace('÷', '/').replace('π', 'pi').replace('∞', '1e999')
    if not re.match(r'^[\d\s\+\-\*\/\(\)\.,eEpiInfNaN\:\=a-z\^A-Z_]+$', expr):
        raise ValueError('表达式包含不允许的字符')
    for kw in ['import', 'exec', 'eval', 'compile', 'open', '__', 'getattr', 'setattr', 'delattr', 'globals', 'locals']:
        if kw in expr.lower():
            raise ValueError(f'不安全的关键词: {kw}')
    return eval(expr, SAFE_ENV)

def format_result(value):
    if isinstance(value, float):
        if value == math.inf: return '∞'
        if value == -math.inf: return '-∞'
        if math.isnan(value): return 'NaN'
        if value == int(value) and abs(value) < 1e15: return format(int(value), ',')
        return '{:,.10g}'.format(value)
    if isinstance(value, int): return format(value, ',')
    return str(value)

def eval_single_line(expression, line_values, variables):
    def replace_ref(m):
        ref_id = m.group(1)
        val = line_values.get(ref_id)
        if val is None: raise ValueError(f'引用行 #{ref_id} 不存在或尚未计算')
        return str(val)
    expr = re.sub(r'#(\d{3,})', replace_ref, expression)
    for var_name, var_val in variables.items():
        expr = re.sub(r'\b' + re.escape(var_name) + r'\b', str(var_val), expr)
    return safe_eval(expr)

def eval_lines(lines_data):
    line_values = {}
    variables = {}
    results = []
    for line in lines_data:
        line_id = line['id']
        raw = line.get('raw', '').strip()
        if not raw:
            results.append({"id": line_id, "result": None, "error": None, "dependencies": []})
            continue
        expr_raw = raw
        for sep in [' // ', ' #']:
            if sep in raw:
                expr_raw = raw[:raw.index(sep)].strip()
                break
        var_match = re.match(r'^([a-zA-Z_]\w*)\s*=\s*(.+)$', expr_raw)
        if var_match:
            var_name, rhs = var_match.group(1), var_match.group(2).strip()
            try:
                val = eval_single_line(rhs, line_values, variables)
                variables[var_name] = val
                line_values[line_id] = val
                results.append({"id": line_id, "result": val, "error": None, "dependencies": []})
            except Exception as e:
                results.append({"id": line_id, "result": None, "error": str(e), "dependencies": []})
            continue
        try:
            deps = [m.group(1) for m in re.finditer(r'#(\d{3,})', expr_raw)]
            val = eval_single_line(expr_raw, line_values, variables)
            line_values[line_id] = val
            results.append({"id": line_id, "result": val, "error": None, "dependencies": deps})
        except Exception as e:
            results.append({"id": line_id, "result": None, "error": str(e), "dependencies": []})
    return results

# ---- JSON-RPC 处理 ----
def respond(req_id, result):
    resp = json.dumps({'jsonrpc': '2.0', 'id': req_id, 'result': result})
    sys.stdout.write(resp + '\n')
    sys.stdout.flush()

def respond_error(req_id, code, message):
    resp = json.dumps({'jsonrpc': '2.0', 'id': req_id, 'error': {'code': code, 'message': message}})
    sys.stdout.write(resp + '\n')
    sys.stdout.flush()

def handle_execute(params):
    command = params.get('command', '')
    input_data = params.get('input', {})
    if command == 'eval':
        expr = input_data.get('expression', '') or input_data.get('text', '')
        if not expr: return {'error': '请输入表达式'}
        try:
            expr = re.sub(r',(?=\d)', '', expr)
            raw = safe_eval(expr)
            return {'expression': expr, 'result': format_result(raw), 'raw': raw}
        except Exception as e:
            return {'expression': expr, 'error': str(e)}
    elif command == 'eval-batch':
        return {'results': eval_lines(input_data.get('lines', []))}
    elif command == 'list-sheets':
        return {'sheets': db_list_sheets()}
    elif command == 'save-sheet':
        return db_save_sheet(input_data.get('sheet', {}))
    elif command == 'delete-sheet':
        return db_delete_sheet(input_data.get('id', ''))
    elif command == 'new-sheet':
        now = time.strftime('%Y-%m-%dT%H:%M:%S')
        sheet = {'id': str(int(time.time() * 1e6)), 'name': '新建稿纸', 'lines': [],
                 'createdAt': now, 'updatedAt': now, 'pinned': False}
        db_save_sheet(sheet)
        return {'sheet': sheet, 'message': '已创建新稿纸'}
    elif command == 'search-sheets':
        return {'sheets': db_search_sheets(input_data.get('keyword', ''))}
    elif command == 'open-calc':
        return {'opened': True, 'message': '打开计算稿纸'}
    else:
        raise ValueError(f'Unknown command: {command}')

def main():
    # 预初始化数据库
    get_db()
    for line in sys.stdin:
        line = line.strip()
        if not line: continue
        try: req = json.loads(line)
        except json.JSONDecodeError: continue
        req_id = req.get('id', 0)
        method = req.get('method', '')
        params_raw = req.get('params', '{}')
        params = json.loads(params_raw) if isinstance(params_raw, str) else params_raw
        if method == 'initialize':
            respond(req_id, {'status': 'ready', 'pluginId': 'com.quickdock.calcsheet', 'name': '计算稿纸', 'version': '2.1.0'})
        elif method == 'plugin.execute':
            try:
                result = handle_execute(params)
                respond(req_id, result)
            except Exception as e:
                respond_error(req_id, -1, str(e))
        elif method == 'shutdown':
            if req_id != 0: respond(req_id, {'status': 'bye'})
            sys.exit(0)

if __name__ == '__main__':
    main()
