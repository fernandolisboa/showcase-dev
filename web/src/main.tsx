import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createBrowserRouter, RouterProvider } from 'react-router-dom'
import App from './App'
import ProjectPage from './ProjectPage'
import PortfolioPage from './PortfolioPage'
import ProjectBySlugPage from './ProjectBySlugPage'
import './index.css'

// The Go control plane serves index.html for any non-asset path (SPA fallback),
// so these client routes resolve on a fresh load too — the /demo Project page, the
// /{username} Portfolio, and a Project's /{username}/{slug} page are shareable,
// persistent URLs (#10, #19, #51). React Router ranks the static /demo above the
// dynamic /:username regardless of array order, so /demo always wins; /:username/:slug
// is a distinct two-segment path; real server routes (/login, /api, …) never reach the
// SPA, and a reserved/unclaimed name or unknown slug 404s from the portfolio API.
const router = createBrowserRouter([
  { path: '/', element: <App /> },
  { path: '/demo', element: <ProjectPage /> },
  { path: '/:username', element: <PortfolioPage /> },
  { path: '/:username/:slug', element: <ProjectBySlugPage /> },
])

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
)
