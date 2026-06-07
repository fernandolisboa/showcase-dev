import { useState } from 'react'

type Session = { id: string; url: string }
type Status = 'idle' | 'starting' | 'error'

// navigate is injected so the click flow is testable; in the browser it sends the
// Guest to their live Session URL (the proxy shows the booting page, then the Demo).
type Props = { navigate?: (url: string) => void }

export default function ProjectPage({ navigate = (url) => (window.location.href = url) }: Props) {
  const [status, setStatus] = useState<Status>('idle')

  async function play() {
    setStatus('starting')
    try {
      const res = await fetch('/api/play', { method: 'POST' })
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
    </main>
  )
}
