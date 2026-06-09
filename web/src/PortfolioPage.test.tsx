import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, expect, test, vi } from 'vitest'
import PortfolioPage from './PortfolioPage'

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path=":username" element={<PortfolioPage />} />
      </Routes>
    </MemoryRouter>,
  )
}

afterEach(() => vi.unstubAllGlobals())

test('lists published projects and plays one by id', async () => {
  const fetchMock = vi.fn((url: string) => {
    if (url === '/api/portfolio/alice') {
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () =>
          Promise.resolve({
            owner: { username: 'alice' },
            projects: [
              { id: 'p1', name: 'Blog', slug: 'blog' },
              { id: 'p2', name: 'Draft', slug: '' },
            ],
          }),
      } as Response)
    }
    // The play POST — return 503 so no real navigation happens in jsdom.
    return Promise.resolve({ ok: false, status: 503 } as Response)
  })
  vi.stubGlobal('fetch', fetchMock)

  renderAt('/alice')

  expect(await screen.findByRole('heading', { name: 'alice' })).toBeInTheDocument()
  // A Project with a slug links to its shareable /{username}/{slug} page (#51); one
  // without a slug renders plain text (no link).
  expect(screen.getByRole('link', { name: 'Blog' })).toHaveAttribute('href', '/alice/blog')
  expect(screen.getByText('Draft')).toBeInTheDocument()
  expect(screen.queryByRole('link', { name: 'Draft' })).toBeNull()

  fireEvent.click(screen.getByRole('button', { name: /play blog/i }))
  await waitFor(() =>
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/play',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ projectId: 'p1' }) }),
    ),
  )
})

test('shows a friendly message when the user has nothing published', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ owner: { username: 'bob' }, projects: [] }),
      } as Response),
    ),
  )
  renderAt('/bob')
  expect(await screen.findByText(/no published projects yet/i)).toBeInTheDocument()
})

test('shows not found for an unknown or reserved username', async () => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 404 } as Response)))
  renderAt('/ghost')
  expect(await screen.findByText(/not found/i)).toBeInTheDocument()
})
