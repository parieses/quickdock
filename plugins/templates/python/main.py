"""
QuickDock 插件 - Python 模板
通过 stdin/stdout 使用 JSON-RPC 2.0 协议通信
"""
import sys
import json


def respond(req_id, result):
    """发送成功响应到 stdout"""
    resp = json.dumps({
        'jsonrpc': '2.0',
        'id': req_id,
        'result': result
    })
    sys.stdout.write(resp + '\n')
    sys.stdout.flush()


def respond_error(req_id, code, message):
    """发送错误响应到 stdout"""
    resp = json.dumps({
        'jsonrpc': '2.0',
        'id': req_id,
        'error': {'code': code, 'message': message}
    })
    sys.stdout.write(resp + '\n')
    sys.stdout.flush()


def handle_execute(params):
    """处理插件命令执行"""
    command = params.get('command', '')
    input_data = params.get('input', {})

    if command == 'hello':
        name = input_data.get('name', 'World')
        return {'message': f'Hello, {name}! 👋'}
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
                'pluginId': 'com.quickdock.my-python-plugin'
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
