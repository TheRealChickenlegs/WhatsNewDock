import { useState } from 'react'
import { Download, Loader2, Sparkles, X } from 'lucide-react'
import { api } from '@/lib/api'
import { useFetch } from '@/lib/hooks'
import { useAuth } from '@/context/AuthContext'
import type { SelfUpdate, SelfUpdateRun, SelfUpdateRunResponse } from '@/lib/types'
import { Button, ConfirmDialog } from '@/components/ui'
import { formatVersion } from '@/lib/utils'

/**
 * SelfUpdateCard sits at the base of the sidebar and only appears when a newer
 * release of WhatsNewDock itself has been published.
 *
 * Applying the update hands the work to a helper container, because the app
 * cannot recreate its own container without killing the process driving it. The
 * request returns before the app goes down, so the card switches to a "coming
 * back" state and reloads the page once the new version answers.
 */
/**
 * RunPanel follows an update that is already under way: the agents are moved
 * first and the controller last, so this reports each agent and then hands over
 * to the reconnect poll. The controller going down is the end of what the page
 * can observe directly.
 */
function RunPanel({ run }: { run: SelfUpdateRun }) {
  return (
    <div className="mx-3 mb-2 rounded-lg border border-primary/40 bg-primary/10 p-3">
      <div className="flex items-start gap-2">
        <Loader2 className="mt-0.5 h-4 w-4 shrink-0 animate-spin text-primary" />
        <div className="min-w-0 flex-1">
          <div className="text-xs font-semibold text-foreground">
            {run.phase === 'controller' ? 'Restarting WhatsNewDock\u2026' : 'Updating agents\u2026'}
          </div>
          <div className="mt-1 space-y-0.5">
            {run.agents.length === 0 && (
              <div className="text-[11px] text-muted-foreground">No agents to update.</div>
            )}
            {run.agents.map((a) => (
              <div key={a.server_id} className="flex items-center gap-1.5 text-[11px]">
                <span
                  className={
                    a.status === 'updated'
                      ? 'text-success'
                      : a.status === 'failed'
                        ? 'text-danger'
                        : a.status === 'skipped'
                          ? 'text-muted-foreground'
                          : 'text-primary'
                  }
                >
                  {a.status === 'updated'
                    ? '\u25cf'
                    : a.status === 'failed'
                      ? '\u2715'
                      : a.status === 'skipped'
                        ? '\u2013'
                        : '\u25cb'}
                </span>
                <span className="truncate text-foreground">{a.name}</span>
                <span className="truncate text-muted-foreground">
                  {a.status === 'queued' ? 'updating\u2026' : a.message || a.status}
                </span>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}

export default function SelfUpdateCard() {
  const { me } = useAuth()
  const { data, refetch } = useFetch<SelfUpdate>('/api/v1/selfupdate', 60000)
  const { data: runData, refetch: refetchRun } = useFetch<SelfUpdateRunResponse>(
    '/api/v1/selfupdate/run',
    3000,
  )
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [started, setStarted] = useState(false)
  const [error, setError] = useState('')

  const run: SelfUpdateRun | undefined = runData?.run
  const running = Boolean(run && !run.finished)

  if (running) return <RunPanel run={run!} />

  if (!data || !data.update_available) return null

  // A release has a version pair to show; a rebuilt moving tag does not — the
  // version string never changed, the image behind it did.
  const isRelease = data.kind === 'release'
  const imageTag = (data.image || '').split(':').pop() || data.image || ''

  const apply = async () => {
    setConfirming(false)
    setBusy(true)
    setError('')
    try {
      await api.post('/api/v1/selfupdate/apply')
      setStarted(true)
      refetchRun()
      // The app is stopped mid-redeploy, so poll until the new version answers
      // and then reload into it.
      const deadline = Date.now() + 5 * 60 * 1000
      const tick = async () => {
        try {
          const next = await api.get<SelfUpdate>('/api/v1/selfupdate')
          if (!next.update_available) {
            window.location.reload()
            return
          }
        } catch {
          // Expected while the container is being replaced.
        }
        if (Date.now() < deadline) setTimeout(tick, 3000)
        else setBusy(false)
      }
      setTimeout(tick, 4000)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not start the update')
      setBusy(false)
      refetch()
    }
  }

  return (
    <>
      <div className="mx-3 mb-2 rounded-lg border border-success/40 bg-success/10 p-3">
        <div className="flex items-start gap-2">
          <Sparkles className="mt-0.5 h-4 w-4 shrink-0 text-success" />
          <div className="min-w-0 flex-1">
            <div className="text-xs font-semibold text-foreground">
              {isRelease ? 'New version detected' : 'New build available'}
            </div>
            <div className="mt-0.5 font-mono text-[11px] text-muted-foreground">
              {isRelease ? (
                <>
                  {formatVersion(data.current)} →{' '}
                  <span className="text-success">{formatVersion(data.latest)}</span>
                </>
              ) : (
                <>
                  <span className="text-success">:{imageTag}</span> has been rebuilt
                </>
              )}
            </div>

            {started ? (
              <div className="mt-2 flex items-center gap-2 text-[11px] text-muted-foreground">
                <Loader2 className="h-3 w-3 animate-spin" />
                Updating — the app will restart…
              </div>
            ) : (
              <div className="mt-2 flex flex-wrap items-center gap-1.5">
                {me?.role === 'admin' && data.can_apply && (
                  <Button size="sm" className="h-7 px-2 text-[11px]" onClick={() => setConfirming(true)}>
                    <Download className="h-3 w-3" />
                    Update now
                  </Button>
                )}
                {data.url && (
                  <a
                    href={data.url}
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex min-h-[28px] items-center py-1 text-[11px] font-medium text-primary hover:underline"
                  >
                    Release notes
                  </a>
                )}
              </div>
            )}

            {error && (
              <div className="mt-1.5 flex items-start gap-1 text-[11px] text-danger">
                <X className="mt-0.5 h-3 w-3 shrink-0" />
                <span className="break-words">{error}</span>
              </div>
            )}

            {me?.role === 'admin' && !data.can_apply && (
              <p className="mt-1.5 text-[11px] leading-relaxed text-muted-foreground">
                Update from the host:
                <br />
                <span className="font-mono">docker compose pull &amp;&amp; docker compose up -d</span>
              </p>
            )}
          </div>
        </div>
      </div>

      <ConfirmDialog
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={apply}
        title="Update WhatsNewDock"
        message={
          <>
            {isRelease ? (
              <>
                Redeploy <span className="font-mono text-foreground">{formatVersion(data.current)}</span> as{' '}
                <span className="font-mono text-foreground">{formatVersion(data.latest)}</span>?
              </>
            ) : (
              <>
                Pull the newest build of{' '}
                <span className="font-mono text-foreground">{data.image}</span>?
              </>
            )}
            <br />
            <span className="text-xs">
              The container is replaced in place — the same name, volumes and settings are kept. The
              app restarts and this page will reload when the new version answers.
              {isRelease && data.image && (
                <>
                  {' '}
                  This pins the running container to {formatVersion(data.latest)}; update the tag in
                  your compose file too, or a later <span className="font-mono">docker compose up</span>{' '}
                  will put the old version back.
                </>
              )}
            </span>
          </>
        }
        confirmLabel="Update"
        loading={busy}
      />
    </>
  )
}
