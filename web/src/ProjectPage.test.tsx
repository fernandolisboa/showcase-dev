import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import ProjectPage from './ProjectPage'

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn(() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ id: 'abc', url: 'http://s-abc.run.localhost' }),
      } as Response),
    ),
  )
})

afterEach(() => vi.unstubAllGlobals())

test('play starts a session and navigates the Guest to it', async () => {
  const navigate = vi.fn()
  render(<ProjectPage navigate={navigate} />)

  fireEvent.click(screen.getByRole('button', { name: /play/i }))

  await waitFor(() => expect(navigate).toHaveBeenCalledWith('http://s-abc.run.localhost'))
  expect(fetch).toHaveBeenCalledWith('/api/play', { method: 'POST' })
})

test('shows an error and does not navigate when start fails', async () => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 500 } as Response)))
  const navigate = vi.fn()
  render(<ProjectPage navigate={navigate} />)

  fireEvent.click(screen.getByRole('button', { name: /play/i }))

  expect(await screen.findByRole('alert')).toBeInTheDocument()
  expect(navigate).not.toHaveBeenCalled()
})
