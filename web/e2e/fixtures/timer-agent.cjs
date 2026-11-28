// A deterministic ACP peer for timer E2E. It doesn't contact a model service.
const { randomUUID } = require('node:crypto')
const readline = require('node:readline')

if (process.argv.includes('--version')) {
  process.stdout.write('quartet-timer-e2e 1.0.0\n')
  process.exit(0)
}

const sessions = new Set()
const configOptions = [{
  id: 'model', name: 'Model', category: 'model', type: 'select', currentValue: 'timer-test-model',
  options: [{ value: 'timer-test-model', name: 'Timer test model' }],
}]
const send = message => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...message }) + '\n')

readline.createInterface({ input: process.stdin }).on('line', line => {
  const { id, method, params } = JSON.parse(line)
  if (id === undefined) return
  switch (method) {
    case 'initialize':
      send({ id, result: { protocolVersion: 1, agentCapabilities: {}, authMethods: [] } })
      break
    case 'session/new': {
      const sessionId = randomUUID()
      sessions.add(sessionId)
      send({ id, result: { sessionId, configOptions } })
      break
    }
    case 'session/set_config_option':
      send({ id, result: { configOptions } })
      break
    case 'session/prompt': {
      if (!sessions.has(params.sessionId)) {
        send({ id, error: { code: -32602, message: 'Unknown fixture session' } })
        break
      }
      const input = params.prompt.filter(item => item.type === 'text').map(item => item.text).join(' ')
      send({
        method: 'session/update',
        params: { sessionId: params.sessionId, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'Timer received: ' + input } } },
      })
      setTimeout(() => send({ id, result: { stopReason: 'end_turn' } }), 150)
      break
    }
    default:
      send({ id, error: { code: -32601, message: 'Method unavailable in fixture: ' + method } })
  }
})
