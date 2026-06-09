import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, expect, test, vi } from 'vitest'
import ProjectBySlugPage from './ProjectBySlugPage'

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path=":username/:slug" element={<ProjectBySlugPage />} />
      </Routes>
    </MemoryRouter>,
  )
}

afterEach(() => vi.unstubAllGlobals())

test('resolves a slug to a published project and plays it by id', async () => {
  const fetchMock = vi.fn((url: string) => {
    if (url === '/api/portfolio/alice/blog') {
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ id: 'p1', name: 'Blog', slug: 'blog' }),
      } as Response)
    }
    // The play POST — 503 so no real navigation happens in jsdom; the slug never reaches it.
    return Promise.resolve({ ok: false, status: 503 } as Response)
  })
  vi.stubGlobal('fetch', fetchMock)

  renderAt('/alice/blog')
  expect(await screen.findByRole('heading', { name: 'Blog' })).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: /play blog/i }))
  await waitFor(() =>
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/play',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ projectId: 'p1' }) }),
    ),
  )
})

test('shows not found for an unknown or unpublished slug', async () => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 404 } as Response)))
  renderAt('/alice/ghost')
  expect(await screen.findByText(/not found/i)).toBeInTheDocument()
})
