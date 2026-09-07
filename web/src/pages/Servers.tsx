import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Server as ServerIcon, Plus, Trash2, Copy, Check, Cpu, MemoryStick } from 'lucide-react'
import { useFetch } from '@/lib/hooks'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import type { Container, Server } from '@/lib/types'
import { Badge, Button, Card, Input, Label, Loading, Modal, ConfirmDialog, EmptyState } from '@/components/ui'
import { formatBytes, timeAgo } from '@/lib/utils'

export default function Servers() {
  const { me } = useAuth()
  const isAdmin = me?.role === 'admin'
  const { data: servers, loading, refetch } = useFetch<Server[]>('/api/v1/servers', 15000)
  const { data: containers } = useFetch<Container[]>('/api/v1/containers')

  const [adding, setAdding] = useState(false)
  const [name, setName] = useState('')
  const [created, setCreated] = useState<{ name: string; token: string } | null>(null)
  const [copied, setCopied] = useState(false)
  const [deleting, setDeleting] = useState<Server | null>(null)

  const counts = useMemo(() => {
    const map: Record<string, number> = {}
    for (const c of containers || []) map[c.server_id] = (map[c.server_id] || 0) + 1
    return map
  }, [containers])

  const submit = async () => {
    if (!name.trim()) return
    const res = await api.post<{ id: string; name: string; token: string }>('/api/v1/servers', {
      name: name.trim(),
    })
    setCreated(res)
    setName('')
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
          <Button onClick={() => { setAdding(true); setCreated(null) }}>
            <Plus className="h-4 w-4" /> Add server
          </Button>
        )}
      </div>

      {(servers || []).length === 0 ? (
        <EmptyState
          title="No servers"
          hint="Add a remote server with an agent, or mount the Docker socket locally."
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
          {(servers || []).map((s) => (
            <Card key={s.id} className="p-5">
              <div className="flex items-start justify-between">
                <div className="flex items-center gap-3">
                  <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-primary/10 text-primary">
                    <ServerIcon className="h-5 w-5" />
                  </div>
                  <div>
                    <Link
                      to={`/servers/${s.id}`}
                      className="text-sm font-semibold text-foreground hover:text-primary"
                    >
                      {s.name}
                    </Link>
                    <div className="mt-0.5 flex items-center gap-2">
                      <Badge variant={s.online ? 'success' : 'muted'}>
                        {s.online ? 'online' : 'offline'}
                      </Badge>
                      {s.is_local && <Badge variant="primary">local</Badge>}
                    </div>
                  </div>
                </div>
                {isAdmin && !s.is_local && (
                  <Button size="icon" variant="ghost" onClick={() => setDeleting(s)}>
                    <Trash2 className="h-4 w-4 text-danger" />
                  </Button>
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
      <Modal open={adding} onClose={() => setAdding(false)} title="Add server">
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
            <div>
              <Label>Server name</Label>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. homelab-nas"
                onKeyDown={(e) => e.key === 'Enter' && submit()}
                autoFocus
              />
            </div>
            <Button className="w-full" onClick={submit}>
              Create server
            </Button>
          </div>
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
