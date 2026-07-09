const readline = require('readline')

const rl = readline.createInterface({ input: process.stdin })

rl.on('line', (line) => {
  try {
    const req = JSON.parse(line)

    switch (req.method) {
      case 'initialize':
        respond(req.id, {
          status: 'ready',
          pluginId: 'com.quickdock.my-node-plugin'
        })
        break

      case 'plugin.execute':
        const { command, input } = req.params
        switch (command) {
          case 'hello':
            const name = (input && input.name) || 'World'
            respond(req.id, {
              message: `Hello, ${name}! 👋`
            })
            break
          default:
            respondError(req.id, -10001, `Unknown command: ${command}`)
        }
        break

      case 'shutdown':
        respond(req.id, { status: 'bye' })
        process.exit(0)
    }
  } catch (e) {
    // JSON 解析失败，忽略该行
  }
})

function respond(id, result) {
  process.stdout.write(JSON.stringify({
    jsonrpc: '2.0', id, result
  }) + '\n')
}

function respondError(id, code, message) {
  process.stdout.write(JSON.stringify({
    jsonrpc: '2.0', id,
    error: { code, message }
  }) + '\n')
}
