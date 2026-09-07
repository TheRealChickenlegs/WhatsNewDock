import { useState } from 'react'
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

export default function ContainerTable({ containers, isAdmin, onSelect, onUpdate, onPin }: Props) {
  const [busyId, setBusyId] = useState<string | null>(null)

  const handleUpdate = (c: Container) => {
    setBusyId(c.id)
    onUpdate(c)
    setTimeout(() => setBusyId(null), 1500)
  }

  return (
    <div className="overflow-hidden rounded-xl border border-border bg-card shadow-card">
      <div className="overflow-x-auto">
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="border-b border-border text-xs uppercase tracking-wide text-muted-foreground">
              <th className="px-4 py-3 font-medium">Container</th>
              <th className="px-4 py-3 font-medium">Image</th>
              <th className="hidden px-4 py-3 font-medium lg:table-cell">Server</th>
              <th className="hidden px-4 py-3 font-medium md:table-cell">Stack</th>
              <th className="px-4 py-3 font-medium">Update</th>
              <th className="px-4 py-3 text-right font-medium">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {containers.map((c) => (
              <tr
                key={c.id}
                onClick={() => onSelect(c)}
                className="group cursor-pointer transition-colors hover:bg-card-hover"
              >
                <td className="px-4 py-3">
                  <div className="flex items-center gap-2.5">
                    <span
                      className={cx(
                        'h-2 w-2 shrink-0 rounded-full',
                        c.running ? 'bg-success' : 'bg-muted-foreground/50',
                      )}
                      title={c.running ? 'Running' : 'Stopped'}
                    />
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
                  <div className="max-w-[280px]">
                    <div className="truncate font-mono text-xs text-foreground">{c.image_name}</div>
                    <div className="mt-0.5">
                      <Badge variant="muted">{registryLabel(c.registry)}</Badge>
                    </div>
                  </div>
                </td>
                <td className="hidden px-4 py-3 text-muted-foreground lg:table-cell">{c.server_name}</td>
                <td className="hidden px-4 py-3 text-muted-foreground md:table-cell">
                  {c.stack_name || '—'}
                </td>
                <td className="px-4 py-3">
                  {c.update ? (
                    <div className="flex flex-col gap-1">
                      <div className="flex items-center gap-1.5 font-mono text-xs">
                        <span className="text-muted-foreground">{c.update.current_tag}</span>
                        <span className="text-muted-foreground">→</span>
                        <span className="font-medium text-success">{c.update.latest_tag}</span>
                      </div>
                      {c.update.versions_behind > 1 && (
                        <span className="text-[11px] text-muted-foreground">
                          {c.update.versions_behind} versions behind · {timeAgo(c.update.checked_at)}
                        </span>
                      )}
                      {c.update.versions_behind <= 1 && (
                        <span className="text-[11px] text-muted-foreground">
                          {timeAgo(c.update.checked_at)}
                        </span>
                      )}
                    </div>
                  ) : (
                    <span className="text-xs text-muted-foreground">
                      {c.pinned ? 'Pinned' : 'Up to date'}
                    </span>
                  )}
                </td>
                <td className="px-4 py-3">
                  <div className="flex items-center justify-end gap-1.5" onClick={(e) => e.stopPropagation()}>
                    {isAdmin && c.update && (
                      <Button
                        size="sm"
                        variant="primary"
                        loading={busyId === c.id}
                        onClick={() => handleUpdate(c)}
                        title={`Update to ${c.update.latest_tag}`}
                      >
                        <RefreshCw className="h-3.5 w-3.5" />
                        Update
                      </Button>
                    )}
                    {isAdmin && (
                      <Button
                        size="icon"
                        variant="ghost"
                        onClick={() => onPin(c, !c.pinned)}
                        title={c.pinned ? 'Unpin' : 'Pin (ignore updates)'}
                      >
                        {c.pinned ? <PinOff className="h-4 w-4" /> : <Pin className="h-4 w-4" />}
                      </Button>
                    )}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
