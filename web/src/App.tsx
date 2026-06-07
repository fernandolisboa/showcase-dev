import { useEffect, useState } from 'react'

type Health = { status: string }

export default function App() {
  const [health, setHealth] = useState('checking…')

  useEffect(() => {
    let cancelled = false
    fetch('/healthz')
      .then((r) => (r.ok ? (r.json() as Promise<Health>) : Promise.reject(new Error(String(r.status)))))
      .then((h) => {
        if (!cancelled) setHealth(h.status)
      })
      .catch(() => {
        if (!cancelled) setHealth('unreachable')
      })
    return () => {
      cancelled = true
    }
  }, [])

  return (
    <main>
      <h1>Showcase</h1>
      <p>Control-plane scaffold is up.</p>
      <p>
        API health: <strong data-testid="health">{health}</strong>
      </p>
    </main>
  )
}
