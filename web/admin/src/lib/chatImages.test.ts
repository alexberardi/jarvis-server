import { describe, expect, it } from 'vitest'
import { fitWithin } from './chatImages'

describe('fitWithin (photos are sent at about 1024 px)', () => {
  it('scales the long side down and never up', () => {
    expect(fitWithin(4032, 3024)).toEqual({ width: 1024, height: 768 })
    expect(fitWithin(3024, 4032)).toEqual({ width: 768, height: 1024 })
    expect(fitWithin(800, 600)).toEqual({ width: 800, height: 600 })
    expect(fitWithin(10000, 1)).toEqual({ width: 1024, height: 1 })
  })
})
