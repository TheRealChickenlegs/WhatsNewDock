import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Layers, X } from 'lucide-react'
import { useFetch } from '@/lib/hooks'
import type { Container, Server, Stack } from '@/lib/types'
import { Badge, Button, Card, Loading, EmptyState, Select } from '@/components/ui'

export default function Stacks() {
  const navigate = useNavigate()
  const { data: stacks, loading } = useFetch<Stack[]>('/api/v1/stacks', 20000)
  const { data: servers } = useFetch<Server[]>('/api/v1/servers')
  const { data: containers } = useFetch<Container[]>('/api/v1/containers', 20000)
  const [server, setServer] = useState('')

  const serverName = (id: string) => (servers || []).find((s) => s.id === id)?.name || id

  const list = useMemo(
    () => (stacks || []).filter((s) => !server || s.server_id === server),
    [stacks, server],
  )

  // Only offer servers that actually have stacks, so the list stays useful on a
  // large deployment.
  const serverOptions = useMemo(() => {
    const seen = new Set((stacks || []).map((s) => s.server_id))
    return (servers || []).filter((s) => seen.has(s.id))
  }, [stacks, servers])

  // Swarm stacks are built from services, and every service is a set of task
  // containers we already have — so the hierarchy is derived here rather than
  // needing the services API (which stays opt-in).
  const servicesByStack = useMemo(() => {
    const map: Record<string, { name: string; tasks: number; running: number }[]> = {}
    for (const c of containers || []) {
      if (!c.swarm_service_name || !c.stack_id) continue
      const list = (map[c.stack_id] ||= [])
      const found = list.find((s) => s.name === c.swarm_service_name)
      if (found) {
        found.tasks += 1
        if (c.running) found.running += 1
      } else {
        list.push({ name: c.swarm_service_name, tasks: 1, running: c.running ? 1 : 0 })
      }
    }
    for (const list of Object.values(map)) list.sort((a, b) => a.name.localeCompare(b.name))
    return map
  }, [containers])

  if (loading && !stacks) return <Loading />

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-semibold text-foreground">Stacks</h1>
        <p className="mt-0.5 text-sm text-muted-foreground">
          Compose projects and Swarm stacks across all servers.
        </p>
      </div>

      {(stacks || []).length > 0 && (
        <div className="flex flex-wrap items-center gap-2 rounded-xl border border-border bg-card p-3">
          <Select
            className="min-w-0 max-w-full flex-1 sm:w-56 sm:flex-none"
            value={server}
            onChange={(e) => setServer(e.target.value)}
            aria-label="Filter by server"
          >
            <option value="">All servers</option>
            {serverOptions.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </Select>
          {server && (
            <Button
              variant="ghost"
              size="icon"
              title="Clear filter"
              aria-label="Clear server filter"
              onClick={() => setServer('')}
            >
              <X className="h-4 w-4" />
            </Button>
          )}
          <Badge variant="muted">
            {list.length} stack{list.length === 1 ? '' : 's'}
            {server ? ` on ${serverName(server)}` : ''}
          </Badge>
        </div>
      )}

      {list.length === 0 ? (
        <EmptyState
          title={server ? 'No stacks on this server' : 'No stacks'}
          hint={
            server
              ? 'This server has no Compose projects or Swarm stacks right now.'
              : 'Stacks are detected from Compose project labels or Swarm stack namespaces.'
          }
          action={
            server ? (
              <Button variant="secondary" onClick={() => setServer('')}>
                Show all servers
              </Button>
            ) : undefined
          }
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
          {list.map((s) => (
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
              <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-border pt-3">
                <Badge variant={s.kind === 'swarm' ? 'primary' : 'default'}>{s.kind}</Badge>
                <span className="text-xs text-muted-foreground">
                  {s.count} {s.kind === 'swarm' ? 'task' : 'container'}
                  {s.count === 1 ? '' : 's'}
                </span>
                {(servicesByStack[s.id] || []).length > 0 && (
                  <span className="text-xs text-muted-foreground">
                    · {servicesByStack[s.id].length} service
                    {servicesByStack[s.id].length === 1 ? '' : 's'}
                  </span>
                )}
              </div>

              {(servicesByStack[s.id] || []).length > 0 && (
                <div className="mt-2.5 flex flex-wrap gap-1.5">
                  {servicesByStack[s.id].map((svc) => (
                    <span
                      key={svc.name}
                      title={`${svc.running}/${svc.tasks} tasks running`}
                      className="inline-flex items-center gap-1.5 rounded-md border border-border bg-background px-2 py-1 text-[11px] text-foreground"
                    >
                      <span className="font-mono">{svc.name}</span>
                      <span
                        className={
                          svc.running === svc.tasks
                            ? 'text-success'
                            : 'text-warning'
                        }
                      >
                        {svc.running}/{svc.tasks}
                      </span>
                    </span>
                  ))}
                </div>
              )}
            </Card>
          ))}
        </div>
      )}
    </div>
  )
}
