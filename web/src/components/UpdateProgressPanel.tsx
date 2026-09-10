import { CheckCircle2, XCircle, X } from 'lucide-react'
import type { UpdateProgress } from '@/lib/types'

export default function UpdateProgressPanel({
  job,
  onDismiss,
}: {
  job: UpdateProgress
  onDismiss: () => void
}) {
  const done = job.status === 'done'
  const failed = job.status === 'failed'
  const indeterminate = job.progress < 0 && !done && !failed
  const pct = done || failed ? 100 : Math.max(0, Math.min(100, job.progress))
  const barColor = failed ? 'bg-danger' : done ? 'bg-success' : 'bg-primary'
  const textColor = failed ? 'text-danger' : done ? 'text-success' : 'text-muted-foreground'

  return (
    <div className="flex w-full min-w-[260px] max-w-sm flex-col gap-2 rounded-lg border border-border bg-card p-3 shadow-card">
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2">
          {done && <CheckCircle2 className="h-4 w-4 shrink-0 text-success" />}
          {failed && <XCircle className="h-4 w-4 shrink-0 text-danger" />}
          <span className="truncate text-sm font-medium text-foreground">{job.name}</span>
        </div>
        <button
          onClick={onDismiss}
          title="Dismiss"
          className="rounded p-0.5 text-muted-foreground hover:bg-card-hover hover:text-foreground"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
        <div
          className={`h-full rounded-full ${barColor} ${
            indeterminate ? 'w-2/5 animate-pulse' : 'transition-[width] duration-300'
          }`}
          style={indeterminate ? undefined : { width: `${pct}%` }}
        />
      </div>

      <div className={`break-words text-xs ${textColor}`}>{job.message}</div>
    </div>
  )
}
