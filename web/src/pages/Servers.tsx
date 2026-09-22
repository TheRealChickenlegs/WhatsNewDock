import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  Server as ServerIcon,
  Plus,
  Trash2,
  Copy,
  Check,
  Cpu,
  MemoryStick,
  Pencil,
  Radio,
  Network,
  Boxes,
} from 'lucide-react'
import { useFetch } from '@/lib/hooks'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import type { Container, Server } from '@/lib/types'
import {
  Badge,
  Button,
  Card,
  Input,
  Label,
  Loading,
  Modal,
  ConfirmDialog,
  EmptyState,
} from '@/components/ui'
import ServerNameEdit from '@/components/ServerNameEdit'
import DirectEndpointForm, { type EndpointValues } from '@/components/DirectEndpointForm'
import { cx, formatBytes, formatVersion, timeAgo } from '@/lib/utils'

type AddMode = 'agent' | 'direct'

/** Swarm role, and — for a manager — whether service updates are opted in. */
function SwarmBadge({ server }: { server: Server }) {
  const role = server.swarm_role
  if (!role || role === 'none') return null
  if (role === 'manager') {
    return (
      <Badge
        variant="primary"
        title={
          server.swarm_services
            ? 'Swarm manager — service updates are enabled'
            : 'Swarm manager — service updates are opt-in: set SERVICES=1 (with POST=1) on your socket proxy'
        }
      >
        <Boxes className="mr-1 h-3 w-3" />
        swarm manager{server.swarm_services ? '' : ' · updates off'}
      </Badge>
    )
  }
  return (
    <Badge variant="default" title="Swarm worker — tasks are visible, but updates need a manager node">
      <Boxes className="mr-1 h-3 w-3" />
      swarm worker
    </Badge>
  )
}

function ServerKindBadge({ server }: { server: Server }) {
  const kind = server.is_local ? 'local' : server.kind || 'agent'
  if (kind === 'local') return <Badge variant="primary">local</Badge>
  if (kind === 'direct') {
    return (
      <Badge variant="default">
        <Network className="mr-1 h-3 w-3" /> direct
      </Badge>
    )
  }
  return (
    <Badge variant="default">
      <Radio className="mr-1 h-3 w-3" /> agent
    </Badge>
  )
}

export default function Servers() {
  const { me } = useAuth()
  const isAdmin = me?.role === 'admin'
  const { data: servers, loading, refetch } = useFetch<Server[]>('/api/v1/servers', 15000)
  const { data: containers } = useFetch<Container[]>('/api/v1/containers')

  const [adding, setAdding] = useState(false)
  const [mode, setMode] = useState<AddMode>('agent')
  const [name, setName] = useState('')
  const [addError, setAddError] = useState('')
  const [created, setCreated] = useState<{ name: string; token: string } | null>(null)
  const [copied, setCopied] = useState(false)
  const [deleting, setDeleting] = useState<Server | null>(null)
  const [editingEndpoint, setEditingEndpoint] = useState<Server | null>(null)

  const counts = useMemo(() => {
    const map: Record<string, number> = {}
    for (const c of containers || []) map[c.server_id] = (map[c.server_id] || 0) + 1
    return map
  }, [containers])

  const openAdd = (m: AddMode) => {
    setMode(m)
    setCreated(null)
    setAddError('')
    setName('')
    setAdding(true)
  }

  const submitAgent = async () => {
    if (!name.trim()) return
    setAddError('')
    try {
      const res = await api.post<{ id: string; name: string; token: string }>('/api/v1/servers', {
        name: name.trim(),
        kind: 'agent',
      })
      setCreated(res)
      setName('')
      refetch()
    } catch (e) {
      setAddError(e instanceof Error ? e.message : 'Could not create the server')
    }
  }

  const submitDirect = async (values: EndpointValues) => {
    await api.post('/api/v1/servers', { name: name.trim(), kind: 'direct', ...values })
    setAdding(false)
    setName('')
    refetch()
  }

  const saveEndpoint = async (values: EndpointValues) => {
    if (!editingEndpoint) return
    await api.patch(`/api/v1/servers/${editingEndpoint.id}`, {
      name: editingEndpoint.name,
      ...values,
    })
    setEditingEndpoint(null)
    refetch()
  }

  const copyToken = async () => {
    if (!created) return
    try {
      await navigator.clipboard.writeText(created.token)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      /* clipboard unavailable */
    }
  }

  const doDelete = async () => {
    if (!deleting) return
    await api.del(`/api/v1/servers/${deleting.id}`)
    setDeleting(null)
    refetch()
  }

  if (loading && !servers) return <Loading />

  return (
    <div className="space-y-5">
      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-xl font-semibold text-foreground">Servers</h1>
          <p className="mt-0.5 text-sm text-muted-foreground">
            Docker hosts monitored by this deployment.
          </p>
        </div>
        {isAdmin && (
          <Button onClick={() => openAdd('agent')}>
            <Plus className="h-4 w-4" /> Add server
          </Button>
        )}
      </div>

      {(servers || []).length === 0 ? (
        <EmptyState
          title="No servers"
          hint="Add a host with an agent or a direct Docker API endpoint, or mount the Docker socket locally."
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
          {(servers || []).map((s) => (
            <Card key={s.id} className="p-5">
              <div className="flex items-start justify-between gap-2">
                <div className="flex min-w-0 items-center gap-3">
                  <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">
                    <ServerIcon className="h-5 w-5" />
                  </div>
                  <div className="min-w-0">
                    {isAdmin ? (
                      <ServerNameEdit
                        id={s.id}
                        name={s.name}
                        onSaved={() => refetch()}
                        renderName={(n) => (
                          <Link
                            to={`/servers/${s.id}`}
                            className="inline-flex min-h-[28px] max-w-full items-center truncate text-sm font-semibold text-foreground hover:text-primary"
                          >
                            {n}
                          </Link>
                        )}
                      />
                    ) : (
                      <Link
                        to={`/servers/${s.id}`}
                        className="inline-flex min-h-[28px] max-w-full items-center truncate text-sm font-semibold text-foreground hover:text-primary"
                      >
                        {s.name}
                      </Link>
                    )}
                    <div className="mt-0.5 flex flex-wrap items-center gap-1.5">
                      <Badge variant={s.online ? 'success' : 'muted'}>
                        {s.online ? 'online' : 'offline'}
                      </Badge>
                      <ServerKindBadge server={s} />
                      <SwarmBadge server={s} />
                    </div>
                  </div>
                </div>
                {isAdmin && !s.is_local && (
                  <div className="flex shrink-0 items-center">
                    {s.kind === 'direct' && (
                      <Button
                        size="icon"
                        variant="ghost"
                        onClick={() => setEditingEndpoint(s)}
                        title="Edit endpoint"
                      >
                        <Pencil className="h-4 w-4 text-muted-foreground" />
                      </Button>
                    )}
                    <Button size="icon" variant="ghost" onClick={() => setDeleting(s)}>
                      <Trash2 className="h-4 w-4 text-danger" />
                    </Button>
                  </div>
                )}
              </div>

              <div className="mt-4 grid grid-cols-2 gap-2 text-xs text-muted-foreground">
                <div className="flex items-center gap-1.5">
                  <Cpu className="h-3.5 w-3.5" /> {s.cpus || '—'} CPUs
                </div>
                <div className="flex items-center gap-1.5">
                  <MemoryStick className="h-3.5 w-3.5" /> {formatBytes(s.memory_bytes)}
                </div>
              </div>

              <div className="mt-3 space-y-1 border-t border-border pt-3 text-xs text-muted-foreground">
                <div className="flex justify-between">
                  <span>Containers</span>
                  <span className="font-medium text-foreground">{counts[s.id] || 0}</span>
                </div>
                <div className="flex justify-between">
                  <span>Docker</span>
                  <span className="font-mono text-foreground">{s.docker_version || '—'}</span>
                </div>
                <div className="flex justify-between">
                  <span>OS / Arch</span>
                  <span className="text-foreground">
                    {s.os || '—'} {s.arch ? `/ ${s.arch}` : ''}
                  </span>
                </div>
                {s.kind === 'direct' && (
                  <div className="flex justify-between gap-3">
                    <span>Endpoint</span>
                    <span className="truncate font-mono text-foreground" title={s.docker_host}>
                      {s.docker_host || '—'}
                      {s.tls ? ' · tls' : ''}
                    </span>
                  </div>
                )}
                {s.kind === 'agent' && (
                  <div className="flex items-center justify-between gap-2">
                    <span>Agent</span>
                    <span
                      className={cx(
                        'truncate font-mono',
                        s.agent_build === 'current'
                          ? 'text-success'
                          : s.agent_build === 'behind'
                            ? 'text-danger'
                            : 'text-muted-foreground',
                      )}
                      title={
                        s.agent_build === 'behind'
                          ? 'Behind this deployment — it will be updated with the next self-update'
                          : s.agent_build === 'current'
                            ? 'Running the same build as this deployment'
                            : 'This agent has not reported its build yet'
                      }
                    >
                      {s.agent_version ? formatVersion(s.agent_version) : 'unknown'}
                    </span>
                  </div>
                )}
                <div className="flex justify-between">
                  <span>Last seen</span>
                  <span>{timeAgo(s.last_seen)}</span>
                </div>
              </div>
            </Card>
          ))}
        </div>
      )}

      {/* Add server modal */}
      <Modal
        open={adding}
        onClose={() => setAdding(false)}
        title={mode === 'direct' ? 'Add direct endpoint' : 'Add server'}
      >
        {created ? (
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">
              Server <span className="font-medium text-foreground">{created.name}</span> created.
              Copy the agent token — it will only be shown once.
            </p>
            <div className="flex items-center gap-2">
              <code className="flex-1 break-all rounded-lg border border-border bg-background px-3 py-2 font-mono text-xs text-foreground">
                {created.token}
              </code>
              <Button variant="secondary" onClick={copyToken}>
                {copied ? <Check className="h-4 w-4 text-success" /> : <Copy className="h-4 w-4" />}
                {copied ? 'Copied' : 'Copy'}
              </Button>
            </div>
            <div>
              <Label>Agent run command</Label>
              <pre className="overflow-x-auto rounded-lg border border-border bg-background p-3 font-mono text-[11px] leading-relaxed text-foreground">
{`docker run -d --name whatsnewdock-agent \\
  -v /var/run/docker.sock:/var/run/docker.sock:ro \\
  -e WND_MODE=agent \\
  -e WND_AGENT_SERVER_URL=https://<server-url> \\
  -e WND_AGENT_TOKEN=${created.token} \\
  ghcr.io/<owner>/whatsnewdock:latest`}
              </pre>
            </div>
            <Button className="w-full" onClick={() => setAdding(false)}>
              Done
            </Button>
          </div>
        ) : (
          <div className="space-y-4">
            <div className="grid grid-cols-2 gap-1 rounded-lg border border-border p-1">
              {(['agent', 'direct'] as AddMode[]).map((m) => (
                <button
                  key={m}
                  onClick={() => setMode(m)}
                  className={`rounded-md px-3 py-1.5 text-xs font-medium transition-colors ${
                    mode === m
                      ? 'bg-primary text-primary-foreground'
                      : 'text-muted-foreground hover:text-foreground'
                  }`}
                >
                  {m === 'agent' ? 'Agent' : 'Direct endpoint'}
                </button>
              ))}
            </div>
            <p className="text-[11px] leading-relaxed text-muted-foreground">
              {mode === 'agent'
                ? 'A small agent runs next to Docker and reports in. Works across NAT and firewalls.'
                : 'This server talks to the remote Docker API itself — no agent to install. The endpoint must be reachable from here.'}
            </p>

            <div>
              <Label>Server name</Label>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. homelab-nas"
                onKeyDown={(e) => e.key === 'Enter' && mode === 'agent' && submitAgent()}
                autoFocus
              />
            </div>

            {addError && <p className="text-xs text-danger">{addError}</p>}

            {mode === 'agent' ? (
              <Button className="w-full" onClick={submitAgent}>
                Create server
              </Button>
            ) : name.trim() ? (
              <DirectEndpointForm submitLabel="Add endpoint" onSubmit={submitDirect} />
            ) : (
              <p className="text-xs text-muted-foreground">Give the server a name to continue.</p>
            )}
          </div>
        )}
      </Modal>

      {/* Edit direct endpoint modal */}
      <Modal
        open={!!editingEndpoint}
        onClose={() => setEditingEndpoint(null)}
        title={`Endpoint for ${editingEndpoint?.name ?? ''}`}
      >
        {editingEndpoint && (
          <DirectEndpointForm
            editing
            initialTls={editingEndpoint.tls}
            initial={{
              docker_host: editingEndpoint.docker_host ?? '',
              tls_ca: '',
              tls_cert: '',
              tls_key: '',
            }}
            submitLabel="Save endpoint"
            onSubmit={saveEndpoint}
            onCancel={() => setEditingEndpoint(null)}
          />
        )}
      </Modal>

      <ConfirmDialog
        open={!!deleting}
        onClose={() => setDeleting(null)}
        onConfirm={doDelete}
        title="Remove server"
        message={
          <>
            Remove <span className="font-medium text-foreground">{deleting?.name}</span> and all its
            containers, stacks and updates?
          </>
        }
        confirmLabel="Remove"
        danger
      />
    </div>
  )
}
