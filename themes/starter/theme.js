const send = message => parent.postMessage({ api_version: 1, ...message }, '*')
let signature = ''
window.addEventListener('pointerdown', () => send({ type: 'activity' }))
window.addEventListener('keydown', () => send({ type: 'activity' }))
window.addEventListener('message', event => {
  if (event.source !== parent || event.data?.api_version !== 1) return
  const state = event.data
  if (state.type === 'result') {
    document.querySelector('#result').textContent = state.result.error || 'Done'
    return
  }
  if (state.type !== 'state') return
  document.querySelector('#clock').textContent = new Date(state.clock.timestamp).toLocaleTimeString('en-AU', { timeZone: state.clock.timezone, hour: '2-digit', minute: '2-digit' })
  document.querySelector('#voice').textContent = `Jarvis: ${state.display.voice}`
  const next = JSON.stringify(state.devices)
  if (next === signature) return
  signature = next
  const container = document.querySelector('#devices')
  container.replaceChildren()
  for (const device of state.devices.slice(0, 48)) {
    const card = document.createElement('article')
    const title = document.createElement('h2')
    title.textContent = device.name
    card.append(title)
    const status = document.createElement('p')
    status.textContent = `${device.room_name || 'No room'}: ${device.state || 'Unknown'}`
    card.append(status)
    for (const action of device.supported_actions || []) {
      const button = document.createElement('button')
      button.textContent = action.button_text
      button.onclick = () => send({ type: 'device.action', request_id: crypto.randomUUID(), device_id: device.id, action: action.button_action })
      card.append(button)
    }
    container.append(card)
  }
})
send({ type: 'ready' })
