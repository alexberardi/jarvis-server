// A simulated node: the real jarvis-agent pairing with a code, headless (scripts/sandbox
// node-sim). It keeps running as the node until the sandbox goes down.
import { execFile } from 'node:child_process'
import path from 'node:path'
import { promisify } from 'node:util'
import { env } from './env'

const sandbox = path.resolve(import.meta.dirname, '../../../scripts/sandbox')

/**
 * startSimNode starts one named nodeName and returns the pairing code it shows. Once the code
 * is approved, the agent says yes to joining and comes online. The first call builds the agent.
 */
export async function startSimNode(nodeName: string): Promise<string> {
  const { stdout } = await promisify(execFile)(sandbox, ['node-sim', '--name', env('JARVIS_SANDBOX'), '--node-name', nodeName], {
    timeout: 90_000,
  })
  const code = stdout.trim()
  if (!/^[A-Z0-9]{4}-[A-Z0-9]{4}$/.test(code)) throw new Error(`node-sim printed no code: ${stdout}`)
  return code
}
