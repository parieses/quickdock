"""
QuickDock 快速计算插件
功能：计算数学表达式、常数查询
通信方式：stdin/stdout JSON-RPC 2.0
"""
import sys
import json
import math
import re


def respond(req_id, result):
    resp = json.dumps({'jsonrpc': '2.0', 'id': req_id, 'result': result})
    sys.stdout.write(resp + '\n')
    sys.stdout.flush()


def respond_error(req_id, code, message):
    resp = json.dumps({
        'jsonrpc': '2.0', 'id': req_id,
        'error': {'code': code, 'message': message}
    })
    sys.stdout.write(resp + '\n')
    sys.stdout.flush()


# 安全的内置函数白名单
SAFE_FUNCS = {
    'abs': abs, 'round': round, 'min': min, 'max': max,
    'sqrt': math.sqrt, 'pow': math.pow, 'sin': math.sin, 'cos': math.cos,
    'tan': math.tan, 'asin': math.asin, 'acos': math.acos, 'atan': math.atan,
    'log': math.log, 'log10': math.log10, 'log2': math.log2,
    'ceil': math.ceil, 'floor': math.floor, 'trunc': math.trunc,
    'radians': math.radians, 'degrees': math.degrees,
    'pi': math.pi, 'e': math.e, 'tau': math.tau, 'inf': math.inf,
}

# 安全 eval 的局部环境
SAFE_ENV = {
    '__builtins__': {},
    'True': True, 'False': False, 'None': None,
    **SAFE_FUNCS,
}


def safe_eval(expr):
    """安全计算数学表达式"""
    expr = expr.strip()

    # 替换常见的数学符号
    expr = expr.replace('^', '**')
    expr = expr.replace('×', '*')
    expr = expr.replace('÷', '/')
    expr = expr.replace('π', 'pi')
    expr = expr.replace('∞', 'inf')

    # 仅允许安全的字符
    if not re.match(r'^[\d\s\+\-\*\/\(\)\.,eEpiInfNaN\:\=a-z\^A-Z_]+$', expr):
        raise ValueError('表达式包含不允许的字符')

    # 检查是否包含危险关键字
    forbidden = ['import', 'exec', 'eval', 'compile', 'open', 'os.', 'sys.',
                 '__', 'getattr', 'setattr', 'delattr', 'globals', 'locals']
    for kw in forbidden:
        if kw in expr.lower():
            raise ValueError(f'不允许使用 {kw}')

    # eval 计算结果
    result = eval(expr, SAFE_ENV)
    return result


def format_result(value):
    """格式化计算结果"""
    if isinstance(value, float):
        if value == math.inf:
            return '∞'
        if value == -math.inf:
            return '-∞'
        if math.isnan(value):
            return 'NaN'
        if value == int(value) and abs(value) < 1e15:
            return str(int(value))
        # 限制小数位数
        return f'{value:.10g}'
    return str(value)


def handle_execute(params):
    command = params.get('command', '')
    input_data = params.get('input', {})

    if command == 'eval':
        expr = input_data.get('expression', '')
        if not expr:
            # 尝试从 input 中获取 text（命令面板传递）
            expr = input_data.get('text', '')
        if not expr:
            return {'error': '请输入表达式', 'examples': [
                '2+2', 'sqrt(16)', 'sin(30)', 'pi*2', '2^10'
            ]}

        try:
            raw = safe_eval(expr)
            formatted = format_result(raw)
            return {
                'expression': expr,
                'result': formatted,
                'raw': raw
            }
        except Exception as e:
            return {'expression': expr, 'error': str(e)}

    elif command == 'calc-pi':
        return {'expression': 'π', 'result': format_result(math.pi)}
    elif command == 'calc-e':
        return {'expression': 'e', 'result': format_result(math.e)}
    else:
        raise ValueError(f'Unknown command: {command}')


def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue

        try:
            req = json.loads(line)
        except json.JSONDecodeError:
            continue

        req_id = req.get('id', 0)
        method = req.get('method', '')
        params_raw = req.get('params', '{}')
        params = json.loads(params_raw) if isinstance(params_raw, str) else params_raw

        if method == 'initialize':
            respond(req_id, {
                'status': 'ready',
                'pluginId': 'com.quickdock.calculator',
                'name': '快速计算'
            })

        elif method == 'plugin.execute':
            try:
                result = handle_execute(params)
                respond(req_id, result)
            except Exception as e:
                respond_error(req_id, -1, str(e))

        elif method == 'shutdown':
            respond(req_id, {'status': 'bye'})
            sys.exit(0)


if __name__ == '__main__':
    main()
