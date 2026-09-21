import { useEffect } from 'react'
import { RefreshCw, Pin, PinOff } from 'lucide-react'
import type { Container, ReleasesResponse } from '@/lib/types'
import { useFetch } from '@/lib/hooks'
import { Badge, Button, Modal, Loading } from '@/components/ui'
import ChangelogView from '@/components/ChangelogView'
import { formatDate, registryLabel, shortId } from '@/lib/utils'

interface Props {
  container: Container | null
  isAdmin: boolean
  onClose: () => void
  onUpdate: (c: Container) => void
  onPin: (c: Container, pinned: boolean) => void
}

export default function ContainerDetail({ container, isAdmin, onClose, onUpdate, onPin }: Props) {
  const id = container?.id || null
  const { data, loading, refetch } = useFetch<ReleasesResponse>(
    id ? `/api/v1/containers/${id}/releases` : null,
  )

  useEffect(() => {
    if (id) refetch()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id])

  const c = data?.container || container
  const releases = data?.releases || []
  const update = data?.update || container?.update || null

  return (
    <Modal open={!!container} onClose={onClose} title={container?.name || 'Container'} wide>
      {!c ? (
        <Loading />
      ) : (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-[260px_minmax(0,1fr)]">
          {/* Details column */}
          <div className="min-w-0 space-y-4">
            <div className="space-y-2.5 text-sm">
              <InfoRow label="Server" value={c.server_name} />
              <InfoRow label="Stack" value={c.stack_name || '—'} />
              <InfoRow label="Image" value={c.image_name} mono />
              <InfoRow label="Tag" value={c.image_tag || 'latest'} mono />
              <InfoRow label="Registry" value={registryLabel(c.registry)} />
              <InfoRow label="Status" value={c.status} />
              <InfoRow label="Restart policy" value={c.restart_policy || '—'} />
              {c.swarm_service_id && (
                <>
                  <InfoRow label="Swarm service" value={c.swarm_service_name || c.swarm_service_id} />
                  {c.swarm_node_id && (
                    <InfoRow label="Node" value={shortId(c.swarm_node_id)} />
                  )}
                  {c.swarm_task_id && <InfoRow label="Task" value={shortId(c.swarm_task_id)} />}
                </>
              )}
              {c.managed && !c.swarm_service_id && (
                <div>
                  <div className="text-xs font-medium text-muted-foreground">Managed by</div>
                  <div className="mt-1">
                    <Badge variant="warning">
                      <span className="font-mono">{c.systemd_unit || 'systemd'}</span>
                    </Badge>
                  </div>
                </div>
              )}
              <InfoRow label="Created" value={formatDate(c.created_at)} />
              {c.ports && c.ports.length > 0 && (
                <div>
                  <div className="text-xs font-medium text-muted-foreground">Ports</div>
                  <div className="mt-1 flex flex-wrap gap-1">
                    {c.ports.map((p) => (
                      <Badge key={p} variant="muted">
                        <span className="font-mono">{p}</span>
                      </Badge>
                    ))}
                  </div>
                </div>
              )}
              <div>
                <div className="text-xs font-medium text-muted-foreground">Container ID</div>
                <div className="font-mono text-xs text-foreground">{shortId(c.docker_id)}</div>
              </div>
            </div>

            {isAdmin && (
              <div className="flex flex-col gap-2 border-t border-border pt-4">
                {update ? (
                  <Button onClick={() => onUpdate(c)}>
                    <RefreshCw className="h-4 w-4" />
                    Update to {update.latest_tag}
                  </Button>
                ) : (
                  <p className="text-xs text-muted-foreground">No update available.</p>
                )}
                {c.swarm_service_id && (
                  <p className="rounded-lg border border-primary/30 bg-primary/10 px-3 py-2 text-xs leading-relaxed text-foreground">
                    This is a task of the swarm service{' '}
                    <span className="font-mono">{c.swarm_service_name || c.swarm_service_id}</span>.
                    Updating rolls out <strong>every replica</strong> through the orchestrator — we
                    never recreate a task behind swarm's back. Swarm updates are opt-in: your socket
                    proxy needs <span className="font-mono">SERVICES=1</span> (with{' '}
                    <span className="font-mono">POST=1</span>), and the host must be a manager.
                  </p>
                )}
                {c.managed && !c.swarm_service_id && (
                  <p className="rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-xs leading-relaxed text-foreground">
                    This container belongs to{' '}
                    <span className="font-mono">{c.systemd_unit || 'a systemd unit'}</span>. The
                    update pulls the new image and then stops the container, so the unit recreates it
                    on the new image — a <span className="font-mono">Restart=</span> policy on the
                    unit is required, and the container must be running.
                  </p>
                )}
                <Button variant="ghost" onClick={() => onPin(c, !c.pinned)}>
                  {c.pinned ? (
                    <>
                      <PinOff className="h-4 w-4" /> Unpin container
                    </>
                  ) : (
                    <>
                      <Pin className="h-4 w-4" /> Pin (ignore updates)
                    </>
                  )}
                </Button>
              </div>
            )}
          </div>

          {/* Changelog column */}
          <div className="min-w-0">
            <div className="mb-3 flex items-center justify-between">
              <h3 className="text-sm font-semibold text-foreground">Changelog</h3>
              {loading && <Loading label="" />}
            </div>
            <ChangelogView
              releases={releases}
              currentTag={update?.current_tag || c.image_tag}
              latestTag={update?.latest_tag}
              versionsBehind={update?.versions_behind || 0}
              sourceUrl={update?.source_url}
            />
          </div>
        </div>
      )}
    </Modal>
  )
}

function InfoRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-3">
      <span className="shrink-0 text-xs text-muted-foreground">{label}</span>
      <span className={`text-right text-xs text-foreground ${mono ? 'font-mono break-all' : ''}`}>
        {value}
      </span>
    </div>
  )
}
