import { describe, expect, it } from 'vitest'
import { commandCatalog, formProblem, withRequiredArgs, type RoutineForm } from './commands'

const catalog = commandCatalog([
  { command_name: 'control_device', description: 'Control a device', parameters: [
    { name: 'device_name', type: 'str', required: true },
    { name: 'action', type: 'str', required: true },
    { name: 'brightness', type: 'int' },
  ] },
  { command_name: 'get_weather', description: 'Weather', parameters: [{ name: 'city', type: 'str', default_value: 'Boston', required: true }] },
  { command_name: 'off_one', enabled: false },
])

const form = (over: Partial<RoutineForm> = {}): RoutineForm => ({
  mode: 'advanced', name: 'Good morning', triggerPhrases: ['good morning'], utterance: '',
  steps: [{ command: 'get_weather', label: 'get_weather', args: [{ key: 'city', value: 'Boston' }] }],
  scheduled: false, scheduleType: 'cron', days: ['mon'], intervalMinutes: 60, targetNodeId: null, ...over,
})

describe('routine commands', () => {
  it('reads the catalog, filling core-command hints the node left out', () => {
    expect(Object.keys(catalog)).toEqual(['control_device', 'get_weather'])
    const p = catalog.control_device.parameters
    expect(p.find((x) => x.name === 'device_name')?.options_source).toBe('devices')
    expect(p.find((x) => x.name === 'action')?.enum_values).toContain('turn_on')
  })

  it('adds a chosen command’s required parameters with their defaults', () => {
    expect(withRequiredArgs({ command: 'get_weather', label: '', args: [] }, catalog.get_weather).args).toEqual([{ key: 'city', value: 'Boston' }])
  })

  it('says what is missing before a save', () => {
    const simpleOn = (id: string) => id === 'pi'
    expect(formProblem(form(), catalog, simpleOn)).toBeNull()
    expect(formProblem(form({ triggerPhrases: [] }), catalog, simpleOn)).toBe('Add at least one trigger phrase')
    expect(formProblem(form({ steps: [{ command: 'control_device', label: 'x', args: [{ key: 'device_name', value: 'Lamp' }] }] }), catalog, simpleOn)).toBe('Step 1: “action” is required')
    expect(formProblem(form({ steps: [{ command: 'get_weather', label: '', args: [{ key: 'city', value: 'a' }] }, { command: 'get_weather', label: '', args: [{ key: 'city', value: 'b' }] }] }), catalog, simpleOn)).toBe('Each step needs its own label')
    expect(formProblem(form({ mode: 'simple', utterance: 'lights on', scheduled: true, targetNodeId: 'old' }), catalog, simpleOn)).toBe('Schedule: choose a node that can run Simple routines')
    expect(formProblem(form({ scheduled: true, targetNodeId: 'pi', days: [] }), catalog, simpleOn)).toBe('Schedule: pick at least one day')
  })
})
