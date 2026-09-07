import { useNavigate } from 'react-router-dom'
import { Layers } from 'lucide-react'
import { useFetch } from '@/lib/hooks'
import type { Server, Stack } from '@/lib/types'
import { Badge, Card, Loading, EmptyState } from '@/components/ui'

export default function Stacks() {
  const navigate = useNavigate()
  const { data: stacks, loading } = useFetch<Stack[]>('/api/v1/stacks', 20000)
  const { data: servers } = useFetch<Server[]>('/api/v1/servers')

  const serverName = (id: string) => (servers || []).find((s) => s.id === id)?.name || id

  if (loading && !stacks) return <Loading />

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-semibold text-foreground">Stacks</h1>
        <p className="mt-0.5 text-sm text-muted-foreground">
          Compose projects and Swarm stacks across all servers.
        </p>
      </div>

      {(stacks || []).length === 0 ? (
        <EmptyState
          title="No stacks"
          hint="Stacks are detected from Compose project labels or Swarm stack namespaces."
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
          {(stacks || []).map((s) => (
            <Card
              key={s.id}
              className="cursor-pointer p-5 transition-colors hover:bg-card-hover"
              onClick={() => navigate(`/containers?server=${s.server_id}&stack=${s.id}`)}
            >
              <div className="flex items-center gap-3">
                <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-warning/10 text-warning">
                  <Layers className="h-5 w-5" />
                </div>
                <div className="min-w-0">
                  <div className="truncate text-sm font-semibold text-foreground">{s.name}</div>
                  <div className="truncate text-xs text-muted-foreground">{serverName(s.server_id)}</div>
                </div>
              </div>
              <div className="mt-4 flex items-center gap-2 border-t border-border pt-3">
                <Badge variant={s.kind === 'swarm' ? 'primary' : 'default'}>{s.kind}</Badge>
                <span className="text-xs text-muted-foreground">
                  {s.count} container{s.count === 1 ? '' : 's'}
                </span>
              </div>
            </Card>
          ))}
        </div>
      )}
    </div>
  )
}
