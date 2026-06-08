import { usePlay } from './usePlay'

type Props = {
  // projectId selects a specific published Project; omit it for the default demo.
  projectId?: string
  // navigate is injected for testability (defaults to a real browser navigation).
  navigate?: (url: string) => void
  label?: string
}

// PlayButton is the shared spin-up control: the demo page uses it with no projectId
// (the default fixture), and each Portfolio card passes its Project's id. It owns its
// own status so one card starting doesn't disable the others.
export default function PlayButton({ projectId, navigate, label = '▶ Play' }: Props) {
  const { status, play } = usePlay(navigate)
  return (
    <>
      <button onClick={() => play(projectId)} disabled={status === 'starting'}>
        {status === 'starting' ? 'Starting…' : label}
      </button>
      {status === 'error' && <p role="alert">Could not start the demo. Please try again.</p>}
      {status === 'capacity' && (
        <p role="alert">Too many demos are running right now. Please try again in a moment.</p>
      )}
    </>
  )
}
