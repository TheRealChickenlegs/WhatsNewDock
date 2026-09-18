import { useState } from 'react'
import { AlertCircle, CheckCircle2, PlugZap } from 'lucide-react'
import { api } from '@/lib/api'
import { Button, Input, Label, Toggle } from '@/components/ui'

export interface EndpointValues {
  docker_host: string
  tls_ca: string
  tls_cert: string
  tls_key: string
}

export const emptyEndpoint: EndpointValues = {
  docker_host: '',
  tls_ca: '',
  tls_cert: '',
  tls_key: '',
}

interface TestResult {
  name: string
  docker_version: string
  os: string
  arch: string
  cpus: number
}

/**
 * DirectEndpointForm collects the connection settings for an agentless host.
 *
 * TLS material is referenced by file path; the paths are never sent back to the
 * browser once saved, so an edit re-enters them deliberately.
 */
export default function DirectEndpointForm({
  initial,
  initialTls,
  submitLabel,
  editing,
  onSubmit,
  onCancel,
}: {
  initial?: EndpointValues
  /** Whether the endpoint already has TLS configured (paths are not returned). */
  initialTls?: boolean
  submitLabel: string
  /** True when an existing endpoint is being changed. */
  editing?: boolean
  onSubmit: (values: EndpointValues) => Promise<void>
  onCancel?: () => void
}) {
  const [values, setValues] = useState<EndpointValues>(initial ?? emptyEndpoint)
  const [tls, setTls] = useState(
    initialTls ?? Boolean(initial?.tls_ca || initial?.tls_cert || initial?.tls_key),
  )
  const set = (patch: Partial<EndpointValues>) => setValues((v) => ({ ...v, ...patch }))

  const [testing, setTesting] = useState(false)
  const [saving, setSaving] = useState(false)
  const [result, setResult] = useState<TestResult | null>(null)
  const [error, setError] = useState('')

  // Settings sent to the API: TLS paths only when the toggle is on.
  const payload = (): EndpointValues =>
    tls
      ? values
      : { docker_host: values.docker_host, tls_ca: '', tls_cert: '', tls_key: '' }

  // Catch the obvious mistakes here so the API's TLS rule is not reported as a
  // confusing transport error.
  const localError = (): string => {
    if (!values.docker_host.trim()) return 'A Docker API endpoint is required.'
    if (tls && !values.tls_ca.trim()) {
      return 'A CA certificate path is required for TLS. Re-enter the mounted paths — for security they are never sent back to the browser.'
    }
    return ''
  }

  const test = async () => {
    const invalid = localError()
    if (invalid) {
      setError(invalid)
      setResult(null)
      return
    }
    setTesting(true)
    setError('')
    setResult(null)
    try {
      const res = await api.post<TestResult>('/api/v1/servers/test', payload())
      setResult(res)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Connection failed')
    } finally {
      setTesting(false)
    }
  }

  const submit = async () => {
    const invalid = localError()
    if (invalid) {
      setError(invalid)
      return
    }
    setSaving(true)
    setError('')
    try {
      await onSubmit(payload())
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not save the endpoint')
    } finally {
      setSaving(false)
    }
  }

  const canTest = values.docker_host.trim().length > 0

  return (
    <div className="space-y-4">
      <div>
        <Label>Docker API endpoint</Label>
        <Input
          value={values.docker_host}
          onChange={(e) => set({ docker_host: e.target.value })}
          placeholder="tcp://10.0.0.5:2376"
          autoFocus
        />
        <p className="mt-1 text-[11px] leading-relaxed text-muted-foreground">
          Reachable from this WhatsNewDock container. Plaintext <code>tcp://</code> is only
          accepted for <code>127.0.0.1</code>; remote hosts must use TLS.
        </p>
      </div>

      <div className="flex items-start justify-between gap-4 rounded-lg border border-border p-3">
        <div>
          <div className="text-sm font-medium text-foreground">TLS</div>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            Required for any remote host. Use a client certificate and key for mutual TLS.
          </p>
        </div>
        <Toggle checked={tls} onChange={setTls} />
      </div>

      {tls && (
        <div className="space-y-3 rounded-lg border border-border p-3">
          <div>
            <Label>CA certificate path</Label>
            <Input
              value={values.tls_ca}
              onChange={(e) => set({ tls_ca: e.target.value })}
              placeholder="/certs/ca.pem"
              className="font-mono text-xs"
            />
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div>
              <Label>Client certificate path</Label>
              <Input
                value={values.tls_cert}
                onChange={(e) => set({ tls_cert: e.target.value })}
                placeholder="/certs/cert.pem"
                className="font-mono text-xs"
              />
            </div>
            <div>
              <Label>Client key path</Label>
              <Input
                value={values.tls_key}
                onChange={(e) => set({ tls_key: e.target.value })}
                placeholder="/certs/key.pem"
                className="font-mono text-xs"
              />
            </div>
          </div>
          <p className="text-[11px] text-muted-foreground">
            Paths are inside the WhatsNewDock container — mount the certificates read-only. For
            security the stored paths are never sent back to the browser.
          </p>
        </div>
      )}

      {result && (
        <div className="flex items-start gap-2 rounded-lg border border-success/40 bg-success/10 p-3 text-xs text-foreground">
          <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-success" />
          <div>
            <div className="font-medium">Connected to {result.name || 'the Docker daemon'}</div>
            <div className="mt-0.5 text-muted-foreground">
              Docker {result.docker_version || '?'} · {result.os || '?'}/{result.arch || '?'} ·{' '}
              {result.cpus} CPUs
            </div>
          </div>
        </div>
      )}

      {error && (
        <div className="flex items-start gap-2 rounded-lg border border-danger/40 bg-danger/10 p-3 text-xs text-foreground">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-danger" />
          <span className="break-words">{error}</span>
        </div>
      )}

      {editing && (
        <p className="text-[11px] text-muted-foreground">
          Saving replaces this endpoint's settings. To change only the name, use the rename control
          on the server card — it leaves the endpoint untouched.
        </p>
      )}

      <div className="flex gap-2">
        <Button variant="secondary" onClick={test} loading={testing} disabled={!canTest}>
          <PlugZap className="h-4 w-4" /> Test connection
        </Button>
        <Button className="flex-1" onClick={submit} loading={saving}>
          {submitLabel}
        </Button>
        {onCancel && (
          <Button variant="ghost" onClick={onCancel} disabled={saving}>
            Cancel
          </Button>
        )}
      </div>
    </div>
  )
}
