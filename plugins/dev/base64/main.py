"""
QuickDock Base64 编码/解码插件
后端处理命令面板的 encode/decode 请求
"""
import sys
import json
import base64


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


def b64_encode(text: str) -> str:
    """将文本编码为 Base64"""
    try:
        return base64.b64encode(text.encode('utf-8')).decode('ascii')
    except Exception as e:
        raise ValueError(f'编码失败: {e}')


def b64_decode(text: str) -> str:
    """将 Base64 解码为文本"""
    try:
        # 清理可能的空白字符
        clean = text.strip()
        return base64.b64decode(clean).decode('utf-8')
    except Exception as e:
        raise ValueError(f'解码失败: {e}')


def handle_execute(params):
    command = params.get('command', '')
    input_data = params.get('input', {})
    text = input_data.get('text', input_data.get('input', ''))

    if command == 'encode':
        result = b64_encode(text)
        return {
            'input': text,
            'output': result,
            'action': 'encode'
        }
    elif command == 'decode':
        result = b64_decode(text)
        return {
            'input': text,
            'output': result,
            'action': 'decode'
        }
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
                'pluginId': 'com.quickdock.base64',
                'name': 'Base64 编码解码'
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
