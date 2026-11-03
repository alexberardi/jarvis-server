// createLimiter runs at most max tasks at once; the rest wait their turn in order. A finished
// task hands its slot straight to the next waiter, so the count can never overshoot.
export function createLimiter(max: number) {
  let active = 0
  const waiting: (() => void)[] = []
  return async function run<T>(task: () => Promise<T>): Promise<T> {
    if (active >= max) await new Promise<void>(resolve => waiting.push(resolve))
    else active++
    try {
      return await task()
    } finally {
      const next = waiting.shift()
      if (next) next()
      else active--
    }
  }
}
