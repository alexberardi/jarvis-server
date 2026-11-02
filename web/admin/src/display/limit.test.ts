import { expect, it } from 'vitest'
import { createLimiter } from './limit'

it('never runs more than max at once, and runs every task', async () => {
  const limit = createLimiter(4)
  let running = 0, most = 0
  const done = await Promise.all(Array.from({ length: 30 }, (_, i) => limit(async () => {
    running++; most = Math.max(most, running)
    await new Promise(r => setTimeout(r, i % 3))
    running--
    return i
  })))
  expect(most).toBe(4)
  expect(done).toEqual(Array.from({ length: 30 }, (_, i) => i))
})
