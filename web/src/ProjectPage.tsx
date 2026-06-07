import { useState } from 'react'

type Session = { id: string; url: string }
type Status = 'idle' | 'starting' | 'error' | 'capacity'

// navigate is injected so the click flow is testable; in the browser it sends the
// Guest to their live Session URL (the proxy shows the booting page, then the Demo).
type Props = { navigate?: (url: string) => void }

export default function ProjectPage({ navigate = (url) => (window.location.href = url) }: Props) {
  const [status, setStatus] = useState<Status>('idle')

  async function play() {
    setStatus('starting')
    try {
      const res = await fetch('/api/play', { method: 'POST' })
      // 503 = the platform is at its global capacity (ADR-0006); tell the Guest to
      // retry shortly rather than show a hard error — a slot frees as Sessions end.
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

  return (
    <main>
      <h1>Demo Project</h1>
      <p>A real, full-stack app — press play and it boots in an isolated environment just for you.</p>
      <button onClick={play} disabled={status === 'starting'}>
        {status === 'starting' ? 'Starting…' : '▶ Play'}
      </button>
      {status === 'error' && (
        <p role="alert">Could not start the demo. Please try again.</p>
      )}
      {status === 'capacity' && (
        <p role="alert">Too many demos are running right now. Please try again in a moment.</p>
      )}
    </main>
  )
}
