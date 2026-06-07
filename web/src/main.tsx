import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createBrowserRouter, RouterProvider } from 'react-router-dom'
import App from './App'
import ProjectPage from './ProjectPage'
import './index.css'

// The Go control plane serves index.html for any non-asset path (SPA fallback),
// so these client routes resolve on a fresh load too — the /demo Project page is
// a shareable, persistent URL (#10).
const router = createBrowserRouter([
  { path: '/', element: <App /> },
  { path: '/demo', element: <ProjectPage /> },
])

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
)
