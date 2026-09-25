import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App.tsx'
import { LocaleProvider } from './LocaleProvider.tsx'
import './styles.css'

const root = document.getElementById('root')
if (root === null) {
  throw new Error('index.html is missing the #root element')
}

createRoot(root).render(
  <StrictMode>
    <LocaleProvider>
      <App />
    </LocaleProvider>
  </StrictMode>,
)
