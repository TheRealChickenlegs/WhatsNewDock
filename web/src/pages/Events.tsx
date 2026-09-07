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
            <div key={e.id} className="flex items-center gap-3 px-5 py-3">
              <Badge variant={kindVariant(e.kind)}>{e.kind}</Badge>
              <div className="min-w-0 flex-1">
                <span className="text-sm text-foreground">{e.message}</span>
                <div className="text-xs text-muted-foreground">by {e.actor}</div>
              </div>
              <span className="shrink-0 text-xs text-muted-foreground">{formatDate(e.timestamp)}</span>
            </div>
          ))}
        </Card>
      )}
    </div>
  )
}
