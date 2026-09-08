import { useParams, Link } from 'react-router-dom'
import { ArrowLeft, Cpu, MemoryStick, Server as ServerIcon } from 'lucide-react'
import { useFetch } from '@/lib/hooks'
import { useAuth } from '@/context/AuthContext'
import type { Server, Stack } from '@/lib/types'
import { Badge, Card, Loading } from '@/components/ui'
import ServerNameEdit from '@/components/ServerNameEdit'
import { formatBytes, timeAgo } from '@/lib/utils'
import ContainerExplorer from '@/components/ContainerExplorer'

export default function ServerDetail() {
  const { id } = useParams<{ id: string }>()
  const { data: server, loading, refetch } = useFetch<Server>(id ? `/api/v1/servers/${id}` : null)
  const { data: stacks } = useFetch<Stack[]>(id ? `/api/v1/stacks?server=${id}` : null)
  const { me } = useAuth()
  const isAdmin = me?.role === 'admin'

  if (loading && !server) return <Loading />

  return (
    <div className="space-y-6">
      <Link
        to="/servers"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="h-3.5 w-3.5" /> All servers
      </Link>

      <div className="flex flex-wrap items-center gap-4">
        <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-primary/10 text-primary">
          <ServerIcon className="h-6 w-6" />
        </div>
        <div>
          <div className="flex items-center gap-2">
            {isAdmin && server ? (
              <ServerNameEdit
                id={server.id}
                name={server.name}
                onSaved={() => refetch()}
                renderName={(n) => <h1 className="text-xl font-semibold text-foreground">{n}</h1>}
              />
            ) : (
              <h1 className="text-xl font-semibold text-foreground">{server?.name}</h1>
            )}
            {server?.online ? <Badge variant="success">online</Badge> : <Badge variant="muted">offline</Badge>}
            {server?.is_local && <Badge variant="primary">local</Badge>}
          </div>
          <p className="mt-0.5 text-sm text-muted-foreground">
            Docker {server?.docker_version || '—'} · {server?.os || '—'} / {server?.arch || '—'} · last
            seen {timeAgo(server?.last_seen || '')}
          </p>
        </div>
        <div className="ml-auto flex gap-3 text-xs text-muted-foreground">
          <span className="inline-flex items-center gap-1.5">
            <Cpu className="h-4 w-4" /> {server?.cpus || '—'} CPUs
          </span>
          <span className="inline-flex items-center gap-1.5">
            <MemoryStick className="h-4 w-4" /> {formatBytes(server?.memory_bytes || 0)}
          </span>
        </div>
      </div>

      {(stacks || []).length > 0 && (
        <div>
          <h2 className="mb-2 text-sm font-semibold text-foreground">Stacks</h2>
          <div className="flex flex-wrap gap-2">
            {(stacks || []).map((s) => (
              <Link
                key={s.id}
                to={`/containers?server=${id}&stack=${s.id}`}
                className="group"
              >
                <Card className="flex items-center gap-2 px-3 py-2 transition-colors hover:bg-card-hover">
                  <Badge variant={s.kind === 'swarm' ? 'primary' : 'default'}>{s.kind}</Badge>
                  <span className="text-sm font-medium text-foreground group-hover:text-primary">
                    {s.name}
                  </span>
                  <span className="text-xs text-muted-foreground">{s.count}</span>
                </Card>
              </Link>
            ))}
          </div>
        </div>
      )}

      <ContainerExplorer
        title="Containers"
        subtitle={`Running on ${server?.name || 'this server'}.`}
        initialServer={id || ''}
        showFilters={false}
      />
    </div>
  )
}
