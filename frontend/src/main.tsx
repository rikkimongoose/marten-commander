import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import { ViewerWindow } from './ViewerWindow'

const viewerID = new URLSearchParams(window.location.search).get('viewer')

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <React.StrictMode>
    {viewerID ? <ViewerWindow viewerID={viewerID} /> : <App />}
  </React.StrictMode>,
)
