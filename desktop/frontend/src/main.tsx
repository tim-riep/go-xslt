import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import './styles/index.css'
import { languageReady } from './state/prefs'

// The texts of the language in effect are one chunk per language, loaded on
// demand; the first render waits for it so no English flashes before it.
languageReady.then(() => {
  ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
    <React.StrictMode>
      <App />
    </React.StrictMode>,
  )
})
