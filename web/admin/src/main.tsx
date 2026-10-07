import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import { captureSetupToken } from './auth/setupToken'
import './index.css'

// Take the first-run setup token out of the address bar before anything renders (AD2).
captureSetupToken()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
