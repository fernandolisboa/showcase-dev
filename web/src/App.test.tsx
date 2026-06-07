import { render, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import App from './App'

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn(() =>
      Promise.resolve({ ok: true, json: () => Promise.resolve({ status: 'ok' }) } as Response),
    ),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

test('renders the heading', async () => {
  render(<App />)
  // Await a settled state so the fetch-driven update flushes inside act().
  expect(await screen.findByRole('heading', { name: 'Showcase' })).toBeInTheDocument()
  await screen.findByText('ok')
})

test('shows API health once resolved', async () => {
  render(<App />)
  expect(await screen.findByTestId('health')).toHaveTextContent('ok')
})
