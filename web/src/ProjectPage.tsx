import PlayButton from './PlayButton'

// navigate is forwarded to PlayButton so the click flow stays testable.
type Props = { navigate?: (url: string) => void }

export default function ProjectPage({ navigate }: Props) {
  return (
    <main>
      <h1>Demo Project</h1>
      <p>A real, full-stack app — press play and it boots in an isolated environment just for you.</p>
      <PlayButton navigate={navigate} />
    </main>
  )
}
