import { useState, type MouseEvent } from 'react'
import { RefreshCw, Pin, PinOff } from 'lucide-react'
import type { Container } from '@/lib/types'
import { Badge, Button } from '@/components/ui'
import { cx, registryLabel, timeAgo } from '@/lib/utils'

interface Props {
  containers: Container[]
  isAdmin: boolean
  onSelect: (c: Container) => void
  onUpdate: (c: Container) => void
  onPin: (c: Container, pinned: boolean) => void
}

/** The "current → latest" summary, shared by the card and table layouts. */
function UpdateSummary({ c }: { c: Container }) {
  if (!c.update) {
    return (
      <span className="text-xs text-muted-foreground">{c.pinned ? 'Pinned' : 'Up to date'}</span>
    )
  }
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <div className="flex flex-wrap items-center gap-1.5 font-mono text-xs">
        <span className="text-muted-foreground">{c.update.current_tag}</span>
        <span className="text-muted-foreground">→</span>
        <span className="font-medium text-success">{c.update.latest_tag}</span>
      </div>
      <span className="text-[11px] text-muted-foreground">
        {c.update.versions_behind > 1 && `${c.update.versions_behind} versions behind · `}
        {timeAgo(c.update.checked_at)}
      </span>
      {c.managed && (
        <span className="text-[11px] text-muted-foreground">
          systemd: {c.systemd_unit || 'managed'}
        </span>
      )}
    </div>
  )
}

function UpdateButton({
  c,
  isAdmin,
  busy,
  onUpdate,
}: {
  c: Container
  isAdmin: boolean
  busy: boolean
  onUpdate: (c: Container) => void
}) {
  if (!isAdmin || !c.update) return null
  return (
    <Button
      size="sm"
      variant="primary"
      loading={busy}
      onClick={() => onUpdate(c)}
      title={
        c.managed
          ? `Update to ${c.update.latest_tag} — the systemd unit will recreate this container`
          : `Update to ${c.update.latest_tag}`
      }
    >
      <RefreshCw className="h-3.5 w-3.5" />
      Update
    </Button>
  )
}

function PinButton({
  c,
  isAdmin,
  onPin,
}: {
  c: Container
  isAdmin: boolean
  onPin: (c: Container, pinned: boolean) => void
}) {
  if (!isAdmin) return null
  return (
    <Button
      size="icon"
      variant="ghost"
      onClick={() => onPin(c, !c.pinned)}
      title={c.pinned ? 'Unpin' : 'Pin (ignore updates)'}
      aria-label={c.pinned ? 'Unpin' : 'Pin (ignore updates)'}
    >
      {c.pinned ? <PinOff className="h-4 w-4" /> : <Pin className="h-4 w-4" />}
    </Button>
  )
}

export default function ContainerTable({ containers, isAdmin, onSelect, onUpdate, onPin }: Props) {
  const [busyId, setBusyId] = useState<string | null>(null)

  const handleUpdate = (c: Container) => {
    setBusyId(c.id)
    onUpdate(c)
    setTimeout(() => setBusyId(null), 1500)
  }

  const stop = (e: MouseEvent) => e.stopPropagation()

  const statusDot = (c: Container) => (
    <span
      className={cx('h-2 w-2 shrink-0 rounded-full', c.running ? 'bg-success' : 'bg-muted-foreground/50')}
      title={c.running ? 'Running' : 'Stopped'}
    />
  )

  return (
    <>
      {/* Phones: a card per container. A six-column table is unreadable on a
          narrow screen, and scrolling it sideways hides the row actions. */}
      <div className="space-y-2 md:hidden">
        {containers.map((c) => (
          <div
            key={c.id}
            data-container-row
            onClick={() => onSelect(c)}
            className="cursor-pointer rounded-xl border border-border bg-card p-3.5 shadow-card transition-colors active:bg-card-hover"
          >
            <div className="flex items-start gap-2.5">
              <span className="mt-1.5">{statusDot(c)}</span>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate font-medium text-foreground">{c.name}</span>
                  {c.pinned && <Pin className="h-3 w-3 shrink-0 text-warning" />}
                </div>
                <div className="truncate font-mono text-[11px] text-muted-foreground">
                  {c.image_name}:{c.image_tag || 'latest'}
                </div>
              </div>
              <Badge variant="muted">{registryLabel(c.registry)}</Badge>
            </div>

            <div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
              <span className="truncate">{c.server_name}</span>
              {c.stack_name && (
                <>
                  <span aria-hidden="true">·</span>
                  <span className="truncate">{c.stack_name}</span>
                </>
              )}
              <span aria-hidden="true">·</span>
              <span>{c.running ? 'running' : c.state}</span>
            </div>

            <div className="mt-2.5 flex items-center justify-between gap-2 border-t border-border pt-2.5">
              <div className="min-w-0">
                <UpdateSummary c={c} />
              </div>
              <div className="flex shrink-0 items-center gap-1.5" onClick={stop}>
                <UpdateButton c={c} isAdmin={isAdmin} busy={busyId === c.id} onUpdate={handleUpdate} />
                <PinButton c={c} isAdmin={isAdmin} onPin={onPin} />
              </div>
            </div>
          </div>
        ))}
      </div>

      {/* Tablet and up: the table. */}
      <div className="hidden overflow-hidden rounded-xl border border-border bg-card shadow-card md:block">
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead>
              <tr className="border-b border-border text-xs uppercase tracking-wide text-muted-foreground">
                <th className="px-4 py-3 font-medium">Container</th>
                <th className="px-4 py-3 font-medium">Image</th>
                <th className="hidden px-4 py-3 font-medium lg:table-cell">Server</th>
                <th className="hidden px-4 py-3 font-medium xl:table-cell">Stack</th>
                <th className="px-4 py-3 font-medium">Update</th>
                <th className="px-4 py-3 text-right font-medium">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {containers.map((c) => (
                <tr
                  key={c.id}
                  data-container-row
                  onClick={() => onSelect(c)}
                  className="group cursor-pointer transition-colors hover:bg-card-hover"
                >
                  <td className="px-4 py-3">
                    <div className="flex items-center gap-2.5">
                      {statusDot(c)}
                      <div className="min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="truncate font-medium text-foreground">{c.name}</span>
                          {c.pinned && <Pin className="h-3 w-3 shrink-0 text-warning" />}
                        </div>
                        <div className="truncate font-mono text-xs text-muted-foreground">
                          {c.image_tag || 'latest'}
                        </div>
                      </div>
                    </div>
                  </td>
                  <td className="px-4 py-3">
                    <div className="max-w-[240px]">
                      <div className="truncate font-mono text-xs text-foreground">{c.image_name}</div>
                      <div className="mt-0.5">
                        <Badge variant="muted">{registryLabel(c.registry)}</Badge>
                      </div>
                    </div>
                  </td>
                  <td className="hidden px-4 py-3 text-muted-foreground lg:table-cell">{c.server_name}</td>
                  <td className="hidden px-4 py-3 text-muted-foreground xl:table-cell">
                    {c.stack_name || '—'}
                  </td>
                  <td className="px-4 py-3">
                    <UpdateSummary c={c} />
                  </td>
                  <td className="px-4 py-3">
                    <div className="flex items-center justify-end gap-1.5" onClick={stop}>
                      <UpdateButton c={c} isAdmin={isAdmin} busy={busyId === c.id} onUpdate={handleUpdate} />
                      <PinButton c={c} isAdmin={isAdmin} onPin={onPin} />
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </>
  )
}
