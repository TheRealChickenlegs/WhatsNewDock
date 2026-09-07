import { useSearchParams } from 'react-router-dom'
import ContainerExplorer from '@/components/ContainerExplorer'

export default function Containers() {
  const [params] = useSearchParams()
  const key = [params.get('server'), params.get('stack'), params.get('updates')].join('|')
  return (
    <ContainerExplorer
      key={key}
      title="Containers"
      subtitle="A unified view of every container across all servers and stacks."
      initialServer={params.get('server') || ''}
      initialStack={params.get('stack') || ''}
      initialHasUpdate={params.get('updates') === '1'}
    />
  )
}
