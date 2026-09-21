import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import type { Container, UpdateProgress } from '@/lib/types'
import ContainerDetail from '@/components/ContainerDetail'
import UpdateProgressPanel from '@/components/UpdateProgressPanel'
import { ConfirmDialog } from '@/components/ui'

// useContainerActions encapsulates the update/pin flows shared by the container
// views: the container detail modal, the update confirmation dialog, and the
// feedback (banner + live update progress) surfaces.
export function useContainerActions(refetch: () => void) {
  const { me } = useAuth()
  const isAdmin = me?.role === 'admin'

  const [selected, setSelected] = useState<Container | null>(null)
  const [confirm, setConfirm] = useState<Container | null>(null)
  const [updating, setUpdating] = useState(false)
  const [banner, setBanner] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const [progress, setProgress] = useState<UpdateProgress | null>(null)
  const pollRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(
    () => () => {
      if (pollRef.current) clearTimeout(pollRef.current)
    },
    [],
  )

  const notify = (type: 'success' | 'error', text: string) => {
    setBanner({ type, text })
    setTimeout(() => setBanner(null), 5000)
  }

  const pin = useCallback(
    async (c: Container, pinned: boolean) => {
      try {
        await api.post(`/api/v1/containers/${c.id}/pin`, { pinned })
        notify('success', `${c.name} ${pinned ? 'pinned' : 'unpinned'}`)
        refetch()
      } catch (e) {
        notify('error', e instanceof Error ? e.message : 'Failed')
      }
    },
    [refetch],
  )

  // requestUpdate opens the confirmation dialog. A systemd-managed container
  // cannot be updated while it is stopped: the unit's teardown removed it, and
  // nothing in the Docker API can start a unit again.
  const requestUpdate = useCallback(
    (c: Container) => {
      // A swarm task is left to the server: whether the host is a manager and
      // whether swarm updates are opted in is its call, and its message says
      // exactly what to change. A Quadlet container, by contrast, is refused
      // here because we know it cannot work while it is stopped.
      if (c.managed && !c.swarm_task_id && !c.running) {
        notify(
          'error',
          `${c.name} is managed by ${c.systemd_unit || 'systemd'} and is not running — start it first (systemctl start ${c.systemd_unit || '<unit>'})`,
        )
        return
      }
      setConfirm(c)
    },
    [],
  )

  const pollUpdateStatus = useCallback(
    (id: string) => {
      const tick = async () => {
        let terminal = false
        try {
          const job = await api.get<UpdateProgress>(`/api/v1/containers/${id}/update/status`)
          setProgress(job)
          if (job.status === 'done' || job.status === 'failed') {
            terminal = true
            if (job.status === 'done') {
              setTimeout(() => setProgress(null), 5000)
            }
            refetch()
          }
        } catch {
          setProgress(null)
          terminal = true
        }
        if (!terminal) {
          pollRef.current = setTimeout(tick, 1000)
        }
      }
      tick()
    },
    [refetch],
  )

  const confirmUpdate = useCallback(async () => {
    if (!confirm) return
    setUpdating(true)
    try {
      await api.post(`/api/v1/containers/${confirm.id}/update`)
      setConfirm(null)
      setProgress({
        container_id: confirm.id,
        name: confirm.name,
        status: 'queued',
        progress: -1,
        message: 'Queued',
      })
      pollUpdateStatus(confirm.id)
    } catch (e) {
      notify('error', e instanceof Error ? e.message : 'Update failed')
      setConfirm(null)
    } finally {
      setUpdating(false)
    }
  }, [confirm, refetch, pollUpdateStatus])

  const bannerEl = banner ? (
    <div
      className={
        banner.type === 'success'
          ? 'rounded-lg bg-success/10 px-3 py-2 text-xs text-success'
          : 'rounded-lg bg-danger/10 px-3 py-2 text-xs text-danger'
      }
    >
      {banner.text}
    </div>
  ) : null

  const progressEl = progress ? (
    <UpdateProgressPanel job={progress} onDismiss={() => setProgress(null)} />
  ) : null

  const feedbackEl = bannerEl || progressEl ? (
    <div className="flex w-full flex-col gap-2 sm:w-auto sm:items-end">
      {bannerEl}
      {progressEl}
    </div>
  ) : null

  const detail = (
    <ContainerDetail
      container={selected}
      isAdmin={isAdmin}
      onClose={() => setSelected(null)}
      onUpdate={requestUpdate}
      onPin={pin}
    />
  )

  const confirmDialog = (
    <ConfirmDialog
      open={!!confirm}
      onClose={() => setConfirm(null)}
      onConfirm={confirmUpdate}
      title="Update container"
      message={
        <>
          Update <span className="font-mono text-foreground">{confirm?.name}</span> from{' '}
          <span className="font-mono text-foreground">{confirm?.image_tag}</span> to{' '}
          <span className="font-mono text-foreground">{confirm?.update?.latest_tag}</span>?
          <br />
          <span className="text-xs">
            {confirm?.swarm_service_id
              ? `The image will be pulled and swarm will roll out every replica of ${
                  confirm.swarm_service_name || 'the service'
                }.`
              : confirm?.managed
                ? `The image will be pulled and the systemd unit (${
                    confirm.systemd_unit || 'systemd'
                  }) will recreate the container on it.`
                : 'The image will be pulled and the container recreated with its current configuration.'}
          </span>
        </>
      }
      confirmLabel="Update"
      danger
      loading={updating}
    />
  )

  return { isAdmin, feedbackEl, selected, setSelected, requestUpdate, pin, detail, confirmDialog }
}
