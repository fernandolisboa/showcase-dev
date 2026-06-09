import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import PlayButton from './PlayButton'

type Project = { id: string; name: string; slug: string }
type Load = 'loading' | 'ready' | 'notfound' | 'error'

// ProjectBySlugPage is the public, shareable page for a single published Project at
// /{username}/{slug} (#51). It resolves the slug to the Project via the portfolio API and
// offers a Play button. The server keeps /api/play strictly id-based, so the resolved
// Project's UUID — not the slug — drives the spin-up. An unknown or unpublished slug 404s.
export default function ProjectBySlugPage() {
  const { username, slug } = useParams()
  const [load, setLoad] = useState<Load>('loading')
  const [project, setProject] = useState<Project | null>(null)

  useEffect(() => {
    let cancelled = false
    fetch(`/api/portfolio/${encodeURIComponent(username ?? '')}/${encodeURIComponent(slug ?? '')}`)
      .then(async (res) => {
        if (cancelled) return
        if (res.status === 404) return setLoad('notfound')
        if (!res.ok) return setLoad('error')
        setProject((await res.json()) as Project)
        setLoad('ready')
      })
      .catch(() => {
        if (!cancelled) setLoad('error')
      })
    return () => {
      cancelled = true
    }
  }, [username, slug])

  if (load === 'loading')
    return (
      <main>
        <p>Loading…</p>
      </main>
    )
  if (load === 'notfound')
    return (
      <main>
        <h1>Not found</h1>
        <p>
          There’s no project “{slug}” for “{username}”.
        </p>
      </main>
    )
  if (load === 'error' || !project)
    return (
      <main>
        <p role="alert">Something went wrong. Please try again.</p>
      </main>
    )

  return (
    <main>
      <h1>{project.name}</h1>
      <PlayButton projectId={project.id} label={`▶ Play ${project.name}`} />
    </main>
  )
}
