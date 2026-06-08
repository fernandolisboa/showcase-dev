import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import PlayButton from './PlayButton'

type Project = { id: string; name: string }
type Portfolio = { owner: { username: string }; projects: Project[] }
type Load = 'loading' | 'ready' | 'notfound' | 'error'

// PortfolioPage is the public showcase at /{username} (AC4): it lists the Owner's
// published Projects, each with a Play button that spins up that Project. A reserved
// or unclaimed username 404s (the server never lets a reserved name be claimed).
export default function PortfolioPage() {
  const { username } = useParams()
  const [load, setLoad] = useState<Load>('loading')
  const [data, setData] = useState<Portfolio | null>(null)

  useEffect(() => {
    let cancelled = false
    fetch(`/api/portfolio/${encodeURIComponent(username ?? '')}`)
      .then(async (res) => {
        if (cancelled) return
        if (res.status === 404) return setLoad('notfound')
        if (!res.ok) return setLoad('error')
        setData((await res.json()) as Portfolio)
        setLoad('ready')
      })
      .catch(() => {
        if (!cancelled) setLoad('error')
      })
    return () => {
      cancelled = true
    }
  }, [username])

  if (load === 'loading') return <main><p>Loading…</p></main>
  if (load === 'notfound')
    return (
      <main>
        <h1>Not found</h1>
        <p>There’s no showcase for “{username}”.</p>
      </main>
    )
  if (load === 'error' || !data)
    return (
      <main>
        <p role="alert">Something went wrong. Please try again.</p>
      </main>
    )

  return (
    <main>
      <h1>{data.owner.username}</h1>
      {data.projects.length === 0 ? (
        <p>No published projects yet.</p>
      ) : (
        <ul>
          {data.projects.map((p) => (
            <li key={p.id}>
              <h2>{p.name}</h2>
              <PlayButton projectId={p.id} label={`▶ Play ${p.name}`} />
            </li>
          ))}
        </ul>
      )}
    </main>
  )
}
