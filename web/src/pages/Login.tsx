import { useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { Container, Lock, User, LogIn } from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Button, Input, Label } from '@/components/ui'

export default function Login() {
  const { me, authConfig, refresh } = useAuth()
  const navigate = useNavigate()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  if (me?.authenticated) {
    navigate('/', { replace: true })
  }

  const localEnabled = authConfig?.auth_mode !== 'oidc'

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      await api.post('/api/auth/login', { username, password })
      await refresh()
      navigate('/', { replace: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Login failed')
    } finally {
      setBusy(false)
    }
  }

  const startOIDC = async () => {
    setError('')
    setBusy(true)
    try {
      const { url } = await api.get<{ url: string }>('/api/auth/oidc/url?redirect=/')
      window.location.href = url
    } catch (err) {
      setError(err instanceof Error ? err.message : 'SSO unavailable')
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3">
          <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-gradient-to-br from-primary to-primary/60 text-primary-foreground shadow-lg shadow-primary/20">
            <Container className="h-7 w-7" />
          </div>
          <div className="text-center">
            <h1 className="text-xl font-semibold text-foreground">WhatsNewDock</h1>
            <p className="mt-1 text-sm text-muted-foreground">Sign in to your workspace</p>
          </div>
        </div>

        <div className="rounded-xl border border-border bg-card p-6 shadow-card">
          {authConfig?.oidc_enabled && (
            <>
              <Button className="w-full" variant="secondary" onClick={startOIDC} loading={busy}>
                <LogIn className="h-4 w-4" />
                Continue with SSO
              </Button>
              {localEnabled && (
                <div className="my-5 flex items-center gap-3 text-xs text-muted-foreground">
                  <div className="h-px flex-1 bg-border" />
                  or
                  <div className="h-px flex-1 bg-border" />
                </div>
              )}
            </>
          )}

          {localEnabled && (
            <form onSubmit={submit} className="space-y-4">
              <div>
                <Label>Username</Label>
                <div className="relative">
                  <User className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                  <Input
                    className="pl-9"
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    placeholder="admin"
                    autoComplete="username"
                    autoFocus
                  />
                </div>
              </div>
              <div>
                <Label>Password</Label>
                <div className="relative">
                  <Lock className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                  <Input
                    className="pl-9"
                    type="password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    placeholder="••••••••"
                    autoComplete="current-password"
                  />
                </div>
              </div>

              {error && (
                <p className="rounded-lg bg-danger/10 px-3 py-2 text-xs text-danger">{error}</p>
              )}

              <Button type="submit" className="w-full" loading={busy}>
                Sign in
              </Button>
            </form>
          )}
        </div>
      </div>
    </div>
  )
}
