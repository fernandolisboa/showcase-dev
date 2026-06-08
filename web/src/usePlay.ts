import { useState } from 'react'

export type Session = { id: string; url: string }
export type PlayStatus = 'idle' | 'starting' | 'error' | 'capacity'

// usePlay drives the spin-up state machine. navigate is injected for testability; in
// the browser it sends the Guest to their live Session URL (the proxy shows the
// booting page, then the Demo). Call play() with no id for the default demo (sent
// with no body), or play(projectId) to spin up a specific published Project.
export function usePlay(navigate: (url: string) => void = (url) => (window.location.href = url)) {
  const [status, setStatus] = useState<PlayStatus>('idle')

  async function play(projectId?: string) {
    setStatus('starting')
    try {
      const init: RequestInit = projectId
        ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ projectId }) }
        : { method: 'POST' }
      const res = await fetch('/api/play', init)
      // 503 = at the global capacity cap (ADR-0006); tell the Guest to retry shortly
      // rather than show a hard error — a slot frees as Sessions end.
      if (res.status === 503) {
        setStatus('capacity')
        return
      }
      if (!res.ok) throw new Error(String(res.status))
      const session = (await res.json()) as Session
      navigate(session.url)
    } catch {
      setStatus('error')
    }
  }

  return { status, play }
}
