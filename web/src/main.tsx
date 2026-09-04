import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import { DialogsProvider } from './lib/dialogs'
import './styles.css'

const root = document.getElementById('root')
if (!root) throw new Error('#root is missing from index.html')

createRoot(root).render(
  <StrictMode>
    <DialogsProvider>
      <App />
    </DialogsProvider>
  </StrictMode>,
)
