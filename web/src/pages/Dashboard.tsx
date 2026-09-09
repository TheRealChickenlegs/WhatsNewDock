import { Server, Layers, Boxes, RefreshCw, ArrowRight } from 'lucide-react'
import { Link } from 'react-router-dom'
import { useFetch } from '@/lib/hooks'
import { useContainerActions } from '@/hooks/useContainerActions'
import type { Container, Overview } from '@/lib/types'
import { Badge, Card, EmptyState, Loading } from '@/components/ui'
import { timeAgo, registryLabel } from '@/lib/utils'

function StatCard({
  label,
  value,
  icon: Icon,
  accent,
}: {
  label: string
  value: number | string
  icon: typeof Server
  accent: string
}) {
  return (
    <Card className="flex items-center gap-4 p-5">
      <div className={`flex h-11 w-11 shrink-0 items-center justify-center rounded-xl ${accent}`}>
        <Icon className="h-5 w-5" />
      </div>
      <div className="min-w-0">
        <div className="text-2xl font-semibold leading-none text-foreground">{value}</div>
        <div className="mt-1.5 text-xs text-muted-foreground">{label}</div>
      </div>
    </Card>
  )
}

export default function Dashboard() {
  const { data: overview, loading } = useFetch<Overview>('/api/v1/overview', 15000)
  const { data: updates, refetch } = useFetch<Container[]>('/api/v1/updates', 30000)
  const { bannerEl, setSelected, detail, confirmDialog } = useContainerActions(refetch)

  if (loading && !overview) return <Loading />

  const recent = (updates || []).slice(0, 8)

  return (
    <div className="space-y-6">
      <div className="flex items-end justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold text-foreground">Overview</h1>
          <p className="mt-0.5 text-sm text-muted-foreground">
            A single view across all your Docker hosts.
          </p>
        </div>
        {bannerEl}
      </div>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          label={`Servers · ${overview?.servers_online ?? 0} online`}
          value={overview?.servers ?? 0}
          icon={Server}
          accent="bg-primary/10 text-primary"
        />
        <StatCard
          label="Stacks"
          value={overview?.stacks ?? 0}
          icon={Layers}
          accent="bg-warning/10 text-warning"
        />
        <StatCard
          label={`Containers · ${overview?.containers_running ?? 0} running`}
          value={overview?.containers ?? 0}
          icon={Boxes}
          accent="bg-success/10 text-success"
        />
        <StatCard
          label="Updates available"
          value={overview?.updates_available ?? 0}
          icon={RefreshCw}
          accent="bg-danger/10 text-danger"
        />
      </div>

      <Card>
        <div className="flex items-center justify-between border-b border-border px-5 py-3.5">
          <h2 className="text-sm font-semibold text-foreground">Recent updates available</h2>
          <Link
            to="/updates"
            className="inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline"
          >
            View all <ArrowRight className="h-3.5 w-3.5" />
          </Link>
        </div>
        {recent.length === 0 ? (
          <div className="p-5">
            <EmptyState
              title="No updates available"
              hint="Everything is up to date, or update checks have not run yet."
            />
          </div>
        ) : (
          <div className="divide-y divide-border">
            {recent.map((c) => (
              <div
                key={c.id}
                onClick={() => setSelected(c)}
                title="View changelog"
                className="flex cursor-pointer items-center gap-3 px-5 py-3 transition-colors hover:bg-card-hover"
              >
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium text-foreground">{c.name}</div>
                  <div className="truncate font-mono text-xs text-muted-foreground">{c.image_name}</div>
                </div>
                <Badge variant="default">{registryLabel(c.registry)}</Badge>
                <span className="font-mono text-xs text-muted-foreground">{c.image_tag}</span>
                <span className="text-muted-foreground">→</span>
                <span className="font-mono text-xs font-medium text-success">{c.update?.latest_tag}</span>
                {c.update && c.update.versions_behind > 1 && (
                  <Badge variant="primary">{c.update.versions_behind} versions</Badge>
                )}
                <span className="hidden text-xs text-muted-foreground sm:inline">
                  {timeAgo(c.update?.checked_at || '')}
                </span>
              </div>
            ))}
          </div>
        )}
      </Card>

      {detail}
      {confirmDialog}
    </div>
  )
}
