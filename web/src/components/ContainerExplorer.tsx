import { useMemo, useState } from 'react'
import { Search, X } from 'lucide-react'
import { useFetch } from '@/lib/hooks'
import { useContainerActions } from '@/hooks/useContainerActions'
import type { Container, Server, Stack } from '@/lib/types'
import { Button, Input, Select, Loading, EmptyState, Badge } from '@/components/ui'
import ContainerTable from './ContainerTable'

interface Props {
  title: string
  subtitle?: string
  showFilters?: boolean
  initialServer?: string
  initialStack?: string
  initialHasUpdate?: boolean
  alwaysUpdates?: boolean
  refreshMs?: number
  emptyHint?: string
}

export default function ContainerExplorer({
  title,
  subtitle,
  showFilters = true,
  initialServer = '',
  initialStack = '',
  initialHasUpdate = false,
  alwaysUpdates = false,
  refreshMs = 20000,
  emptyHint,
}: Props) {
  const [search, setSearch] = useState('')
  const [server, setServer] = useState(initialServer)
  const [stack, setStack] = useState(initialStack)
  const [state, setState] = useState('')
  const [hasUpdate, setHasUpdate] = useState(initialHasUpdate || alwaysUpdates)

  const path = useMemo(() => {
    const params = new URLSearchParams()
    if (search) params.set('search', search)
    if (server) params.set('server', server)
    if (stack) params.set('stack', stack)
    if (state) params.set('state', state)
    if (hasUpdate || alwaysUpdates) params.set('has_update', 'true')
    const qs = params.toString()
    return `/api/v1/containers${qs ? `?${qs}` : ''}`
  }, [search, server, stack, state, hasUpdate, alwaysUpdates])

  const { data: containers, loading, refetch } = useFetch<Container[]>(path, refreshMs)
  const { data: servers } = useFetch<Server[]>('/api/v1/servers')
  const { data: stacks } = useFetch<Stack[]>('/api/v1/stacks')

  const filteredStacks = useMemo(
    () => (stacks || []).filter((s) => !server || s.server_id === server),
    [stacks, server],
  )

  const { isAdmin, feedbackEl, selected, setSelected, requestUpdate, pin, detail, confirmDialog } =
    useContainerActions(refetch)

  const list = containers || []

  return (
    <div className="space-y-4">
      <div className="flex items-end justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold text-foreground">{title}</h1>
          {subtitle && <p className="mt-0.5 text-sm text-muted-foreground">{subtitle}</p>}
        </div>
        {feedbackEl}
      </div>

      {showFilters && (
        <div className="flex flex-wrap items-center gap-2 rounded-xl border border-border bg-card p-3">
          <div className="relative min-w-[220px] flex-1">
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              className="pl-9"
              placeholder="Search name or image…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </div>
          <Select value={server} onChange={(e) => { setServer(e.target.value); setStack('') }}>
            <option value="">All servers</option>
            {(servers || []).map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </Select>
          <Select value={stack} onChange={(e) => setStack(e.target.value)}>
            <option value="">All stacks</option>
            {filteredStacks.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </Select>
          <Select value={state} onChange={(e) => setState(e.target.value)}>
            <option value="">Any state</option>
            <option value="running">Running</option>
            <option value="stopped">Stopped</option>
          </Select>
          {!alwaysUpdates && (
            <Button
              variant={hasUpdate ? 'primary' : 'secondary'}
              onClick={() => setHasUpdate((v) => !v)}
            >
              Updates only
            </Button>
          )}
          {(search || server || stack || state || hasUpdate) && (
            <Button
              variant="ghost"
              size="icon"
              title="Clear filters"
              onClick={() => {
                setSearch('')
                setServer('')
                setStack('')
                setState('')
                setHasUpdate(false)
              }}
            >
              <X className="h-4 w-4" />
            </Button>
          )}
          <Badge variant="muted">{list.length} container{list.length === 1 ? '' : 's'}</Badge>
        </div>
      )}

      {loading && !containers ? (
        <Loading />
      ) : list.length === 0 ? (
        <EmptyState
          title="No containers found"
          hint={emptyHint || 'Try adjusting your filters, or wait for the first agent report.'}
        />
      ) : (
        <ContainerTable
          containers={list}
          isAdmin={isAdmin}
          onSelect={setSelected}
          onUpdate={requestUpdate}
          onPin={pin}
        />
      )}

      {detail}
      {confirmDialog}
    </div>
  )
}
