import { useCallback, useState } from 'react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import type { Container } from '@/lib/types'
import ContainerDetail from '@/components/ContainerDetail'
import { ConfirmDialog } from '@/components/ui'

// useContainerActions encapsulates the update/pin flows shared by the container
// views: the container detail modal, the update confirmation dialog and the
// transient feedback banner.
export function useContainerActions(refetch: () => void) {
  const { me } = useAuth()
  const isAdmin = me?.role === 'admin'

  const [selected, setSelected] = useState<Container | null>(null)
  const [confirm, setConfirm] = useState<Container | null>(null)
  const [updating, setUpdating] = useState(false)
  const [banner, setBanner] = useState<{ type: 'success' | 'error'; text: string } | null>(null)

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

  const requestUpdate = useCallback((c: Container) => setConfirm(c), [])

  const confirmUpdate = useCallback(async () => {
    if (!confirm) return
    setUpdating(true)
    try {
      await api.post(`/api/v1/containers/${confirm.id}/update`)
      notify('success', `Update queued for ${confirm.name}`)
      setConfirm(null)
      setTimeout(refetch, 1500)
    } catch (e) {
      notify('error', e instanceof Error ? e.message : 'Update failed')
      setConfirm(null)
    } finally {
      setUpdating(false)
    }
  }, [confirm, refetch])

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
            The image will be pulled and the container recreated with its current configuration.
          </span>
        </>
      }
      confirmLabel="Update"
      danger
      loading={updating}
    />
  )

  return { isAdmin, bannerEl, selected, setSelected, requestUpdate, pin, detail, confirmDialog }
}
