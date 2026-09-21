import { useFetch } from '@/lib/hooks'
import type { AuditEvent } from '@/lib/types'
import { Badge, Card, Loading, EmptyState } from '@/components/ui'
import { formatDate } from '@/lib/utils'

const kindVariant = (kind: string): 'success' | 'danger' | 'primary' | 'default' => {
  if (kind.startsWith('update_done')) return 'success'
  if (kind.startsWith('update_failed')) return 'danger'
  if (kind.startsWith('update_requested')) return 'primary'
  if (kind.startsWith('server')) return 'primary'
  return 'default'
}

export default function Events() {
  const { data: events, loading } = useFetch<AuditEvent[]>('/api/v1/events', 10000)

  if (loading && !events) return <Loading />

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-semibold text-foreground">Activity</h1>
        <p className="mt-0.5 text-sm text-muted-foreground">Recent actions and update events.</p>
      </div>

      {(events || []).length === 0 ? (
        <EmptyState title="No activity yet" hint="Sign-ins and update actions will appear here." />
      ) : (
        <Card className="divide-y divide-border">
          {(events || []).map((e) => (
            /* On phones the badge and timestamp share the first line and the
               message gets its own full-width row; from sm up it is one row. */
            <div key={e.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 sm:flex-nowrap sm:px-5">
              <Badge variant={kindVariant(e.kind)}>{e.kind}</Badge>
              <span className="ml-auto shrink-0 text-xs text-muted-foreground sm:order-last sm:ml-0">
                {formatDate(e.timestamp)}
              </span>
              <div className="w-full min-w-0 sm:w-auto sm:flex-1">
                <span className="break-words text-sm text-foreground">{e.message}</span>
                <div className="truncate text-xs text-muted-foreground">by {e.actor}</div>
              </div>
            </div>
          ))}
        </Card>
      )}
    </div>
  )
}
