import { useEffect, useState } from 'react'
import { RefreshCw, Plus, Trash2, KeyRound, ShieldCheck } from 'lucide-react'
import { useFetch } from '@/lib/hooks'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import type {
  Settings as SettingsData,
  SelfUpdate as SelfUpdateStatus,
  User,
  AuthSettings,
  AuthSettingsUpdate,
  CheckStatus,
} from '@/lib/types'
import { Badge, Button, Card, CardHeader, Input, Label, Select, Toggle, Loading } from '@/components/ui'
import { formatDate, timeAgo } from '@/lib/utils'

export default function Settings() {
  const { me } = useAuth()
  const isAdmin = me?.role === 'admin'

  if (!isAdmin) {
    return (
      <div className="rounded-xl border border-border bg-card p-8 text-center text-sm text-muted-foreground">
        Admin access required to view settings.
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-foreground">Settings</h1>
        <p className="mt-0.5 text-sm text-muted-foreground">
          Configure update checking, changelog sources and user accounts.
        </p>
      </div>
      <UpdateSettings />
      <SelfUpdateSettings />
      <UserManagement />
      <AuthSettingsForm />
    </div>
  )
}

/**
 * Self-update checking: whether WhatsNewDock watches for its own newer releases
 * and how often. It is a separate card because it is about this app, not about
 * the containers it monitors.
 */
function SelfUpdateSettings() {
  const { data, loading, refetch } = useFetch<SettingsData>('/api/v1/settings')
  const { data: status, refetch: refetchStatus } = useFetch<SelfUpdateStatus>('/api/v1/selfupdate')
  const [enabled, setEnabled] = useState(true)
  const [interval, setInterval] = useState('24h')
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [checking, setChecking] = useState(false)

  useEffect(() => {
    if (data) {
      setEnabled(data.self_update)
      setInterval(data.self_update_interval)
    }
  }, [data])

  const save = async () => {
    setSaving(true)
    try {
      await api.put('/api/v1/settings', {
        self_update: enabled,
        self_update_interval: interval,
      })
      setSaved(true)
      setTimeout(() => setSaved(false), 2500)
      refetch()
      refetchStatus()
    } finally {
      setSaving(false)
    }
  }

  const checkNow = async () => {
    setChecking(true)
    try {
      await api.post('/api/v1/selfupdate/check')
      refetchStatus()
    } finally {
      setChecking(false)
    }
  }

  if (loading && !data) return <Loading />

  return (
    <Card>
      <CardHeader
        title="WhatsNewDock self-update"
        subtitle="Whether this app checks for its own newer releases."
        action={
          <Button variant="secondary" onClick={checkNow} loading={checking}>
            <RefreshCw className="h-4 w-4" /> Check now
          </Button>
        }
      />
      <div className="grid grid-cols-1 gap-5 p-5 md:grid-cols-2">
        <div>
          <Label>Check for new versions</Label>
          <label className="flex w-full items-center justify-between rounded-lg border border-border bg-background px-3 py-2.5">
            <span className="text-sm text-foreground">{enabled ? 'Enabled' : 'Disabled'}</span>
            <Toggle checked={enabled} onChange={setEnabled} />
          </label>
          <p className="mt-1.5 text-[11px] leading-relaxed text-muted-foreground">
            Checks the GitHub releases for{' '}
            <span className="font-mono">{status?.repo || 'this project'}</span>. Nothing is
            downloaded unless you press Update.
          </p>
        </div>
        <div>
          <Label>Check frequency</Label>
          <Input
            value={interval}
            onChange={(e) => setInterval(e.target.value)}
            placeholder="24h"
            className="font-mono"
            disabled={!enabled}
          />
          <p className="mt-1.5 text-[11px] text-muted-foreground">
            Go duration, minimum 1h. Default 24h.
          </p>
        </div>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border px-5 py-4">
        <div className="text-xs text-muted-foreground">
          {status?.update_available ? (
            <span className="text-success">
              {status.current} → <span className="font-medium">{status.latest}</span> available
            </span>
          ) : status?.error ? (
            <span className="text-warning">Last check failed: {status.error}</span>
          ) : status?.checked_at ? (
            <span>Up to date — last checked {timeAgo(status.checked_at)}</span>
          ) : (
            <span>Not checked yet.</span>
          )}
        </div>
        <Button onClick={save} loading={saving}>
          {saved ? 'Saved' : 'Save changes'}
        </Button>
      </div>
    </Card>
  )
}

function UpdateSettings() {
  const { data, loading, refetch } = useFetch<SettingsData>('/api/v1/settings')
  const [changelogCount, setChangelogCount] = useState(5)
  const [includePre, setIncludePre] = useState(false)
  const [interval, setInterval] = useState('6h')
  const [selfUpdate, setSelfUpdate] = useState(true)
  const [selfUpdateInterval, setSelfUpdateInterval] = useState('24h')
  const [ignoreImages, setIgnoreImages] = useState('')
  const [githubToken, setGithubToken] = useState('')
  const [gitlabToken, setGitlabToken] = useState('')
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [checking, setChecking] = useState(false)
  const [checkMsg, setCheckMsg] = useState('')
  const [checkFailed, setCheckFailed] = useState(false)

  useEffect(() => {
    if (data) {
      setChangelogCount(data.changelog_count)
      setIncludePre(data.include_prereleases)
      setInterval(data.update_interval)
      setSelfUpdate(data.self_update)
      setSelfUpdateInterval(data.self_update_interval)
      setIgnoreImages((data.ignore_images || []).join(', '))
    }
  }, [data])

  const save = async () => {
    setSaving(true)
    try {
      await api.put('/api/v1/settings', {
        changelog_count: changelogCount,
        include_prereleases: includePre,
        update_interval: interval,
        self_update: selfUpdate,
        self_update_interval: selfUpdateInterval,
        ignore_images: ignoreImages
          .split(',')
          .map((s) => s.trim())
          .filter(Boolean),
        github_token: githubToken,
        gitlab_token: gitlabToken,
      })
      setGithubToken('')
      setGitlabToken('')
      setSaved(true)
      setTimeout(() => setSaved(false), 2500)
      refetch()
    } finally {
      setSaving(false)
    }
  }

  const checkNow = async () => {
    setChecking(true)
    setCheckMsg('')
    try {
      await api.post('/api/v1/check')
      // The check runs in the background, so wait for it to finish. Returning
      // straight away left the updates list looking empty even when the check
      // went on to find updates.
      const deadline = Date.now() + 5 * 60 * 1000
      for (;;) {
        await new Promise((resolve) => setTimeout(resolve, 1500))
        const state = await api.get<CheckStatus>('/api/v1/check')
        if (state.running) {
          if (Date.now() > deadline) {
            setCheckFailed(true)
            setCheckMsg('Still checking — results will appear on the Updates page when it finishes.')
            break
          }
          continue
        }
        setCheckFailed(Boolean(state.last_error))
        if (state.last_error) {
          setCheckMsg(`Check failed: ${state.last_error}`)
        } else if (state.updates_available) {
          setCheckMsg(
            `Check complete — ${state.updates_available} ` +
              (state.updates_available === 1 ? 'update available.' : 'updates available.'),
          )
        } else {
          setCheckMsg('Check complete — everything is up to date.')
        }
        break
      }
    } catch (e) {
      setCheckFailed(true)
      setCheckMsg(e instanceof Error ? e.message : 'Check failed')
    } finally {
      setChecking(false)
    }
  }

  if (loading && !data) return <Loading />

  return (
    <Card>
      <CardHeader
        title="Update checking"
        subtitle="How WhatsNewDock detects and reports available image updates."
        action={
          <Button variant="secondary" onClick={checkNow} loading={checking}>
            <RefreshCw className="h-4 w-4" /> Check now
          </Button>
        }
      />
      {checkMsg && (
        <p
          role="status"
          className={
            'border-b border-border px-5 py-2 text-xs ' +
            (checkFailed ? 'text-danger' : 'text-muted-foreground')
          }
        >
          {checkMsg}
        </p>
      )}
      <div className="grid grid-cols-1 gap-5 p-5 md:grid-cols-2">
        <div>
          <Label>Changelog versions to keep</Label>
          <Input
            type="number"
            min={1}
            max={100}
            value={changelogCount}
            onChange={(e) => setChangelogCount(Number(e.target.value))}
          />
        </div>
        <div>
          <Label>Update check interval</Label>
          <Select value={interval} onChange={(e) => setInterval(e.target.value)}>
            <option value="1h">Every hour</option>
            <option value="6h">Every 6 hours</option>
            <option value="12h">Every 12 hours</option>
            <option value="24h">Every day</option>
          </Select>
        </div>
        <div>
          <Label>Ignore images (comma-separated globs)</Label>
          <Input
            value={ignoreImages}
            onChange={(e) => setIgnoreImages(e.target.value)}
            placeholder="e.g. registry.example.com/*, */*:beta"
          />
        </div>
        <div className="flex items-end">
          <label className="flex w-full items-center justify-between rounded-lg border border-border bg-background px-3 py-2.5">
            <span className="text-sm text-foreground">Include pre-releases</span>
            <Toggle checked={includePre} onChange={setIncludePre} />
          </label>
        </div>
      </div>

      <div className="border-t border-border px-5 py-4">
        <div className="mb-3 flex items-center gap-2 text-sm font-medium text-foreground">
          <KeyRound className="h-4 w-4" /> Changelog source tokens
        </div>
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <div>
            <Label>GitHub token {data?.github_token_set && <span className="text-success">· set</span>}</Label>
            <Input
              type="password"
              value={githubToken}
              onChange={(e) => setGithubToken(e.target.value)}
              placeholder="ghp_… (leave blank to keep current)"
            />
          </div>
          <div>
            <Label>GitLab token {data?.gitlab_token_set && <span className="text-success">· set</span>}</Label>
            <Input
              type="password"
              value={gitlabToken}
              onChange={(e) => setGitlabToken(e.target.value)}
              placeholder="glpat-… (leave blank to keep current)"
            />
          </div>
        </div>
      </div>

      <div className="flex items-center justify-end gap-3 border-t border-border px-5 py-4">
        {saved && <span className="text-xs text-success">Saved</span>}
        <Button onClick={save} loading={saving}>
          Save settings
        </Button>
      </div>
    </Card>
  )
}

function UserManagement() {
  const { data: users, refetch } = useFetch<User[]>('/api/v1/users')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState('viewer')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [pwUser, setPwUser] = useState<User | null>(null)

  const addUser = async () => {
    setError('')
    setBusy(true)
    try {
      await api.post('/api/v1/users', { username, password, role })
      setUsername('')
      setPassword('')
      refetch()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed')
    } finally {
      setBusy(false)
    }
  }

  const changeRole = async (u: User, r: string) => {
    await api.patch(`/api/v1/users/${u.id}`, { role: r })
    refetch()
  }

  const changePassword = async (u: User, pw: string) => {
    await api.patch(`/api/v1/users/${u.id}`, { password: pw })
    setPwUser(null)
  }

  const deleteUser = async (u: User) => {
    await api.del(`/api/v1/users/${u.id}`)
    refetch()
  }

  return (
    <Card>
      <CardHeader title="Users" subtitle="Local accounts for username/password authentication." />
      <div className="divide-y divide-border">
        {(users || []).map((u) => (
          /* The row wraps on phones so the username is never squeezed to a few
             pixels by the role selector and the action buttons. */
          <div key={u.id} className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3 sm:px-5">
            <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-muted text-sm font-semibold text-foreground">
              {u.username.slice(0, 1).toUpperCase()}
            </div>
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-medium text-foreground">{u.username}</div>
              <div className="truncate text-xs text-muted-foreground">Created {formatDate(u.created_at)}</div>
            </div>
            <div className="flex w-full items-center gap-2 sm:w-auto">
              <Select value={u.role} onChange={(e) => changeRole(u, e.target.value)} className="w-28">
                <option value="admin">admin</option>
                <option value="viewer">viewer</option>
              </Select>
              <Button variant="ghost" size="sm" onClick={() => setPwUser(u)}>
                Set password
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="ml-auto sm:ml-0"
                aria-label={`Delete ${u.username}`}
                onClick={() => deleteUser(u)}
              >
                <Trash2 className="h-4 w-4 text-danger" />
              </Button>
            </div>
          </div>
        ))}
      </div>

      <div className="border-t border-border p-5">
        <div className="mb-3 text-sm font-medium text-foreground">Add user</div>
        <div className="grid grid-cols-1 gap-3 md:grid-cols-[1fr_1fr_140px_auto]">
          <Input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="Username" />
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="Password (min 8 chars)"
          />
          <Select value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="viewer">viewer</option>
            <option value="admin">admin</option>
          </Select>
          <Button onClick={addUser} loading={busy}>
            <Plus className="h-4 w-4" /> Add
          </Button>
        </div>
        {error && <p className="mt-2 text-xs text-danger">{error}</p>}
      </div>

      {pwUser && (
        <PasswordDialog user={pwUser} onClose={() => setPwUser(null)} onSave={changePassword} />
      )}
    </Card>
  )
}

function PasswordDialog({
  user,
  onClose,
  onSave,
}: {
  user: User
  onClose: () => void
  onSave: (u: User, pw: string) => void
}) {
  const [pw, setPw] = useState('')
  return (
    <div className="fixed inset-0 z-[60] flex items-center justify-center p-4">
      <div className="absolute inset-0 bg-black/60" onClick={onClose} />
      <div className="relative z-10 w-full max-w-sm rounded-xl border border-border bg-card p-5">
        <h3 className="text-sm font-semibold text-foreground">Set password for {user.username}</h3>
        <Input
          type="password"
          className="mt-3"
          value={pw}
          onChange={(e) => setPw(e.target.value)}
          placeholder="New password (min 8 chars)"
        />
        <div className="mt-4 flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={() => pw.length >= 8 && onSave(user, pw)}>Save</Button>
        </div>
      </div>
    </div>
  )
}

function AuthSettingsForm() {
  const { data, loading, refetch } = useFetch<AuthSettings>('/api/v1/auth/settings')
  const [passwordOn, setPasswordOn] = useState(true)
  const [oidcOn, setOidcOn] = useState(false)
  const [sessionTtl, setSessionTtl] = useState('24h')
  const [issuerUrl, setIssuerUrl] = useState('')
  const [clientId, setClientId] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [redirectUrl, setRedirectUrl] = useState('')
  const [scopes, setScopes] = useState('openid,profile,email')
  const [usernameClaim, setUsernameClaim] = useState('preferred_username')
  const [defaultRole, setDefaultRole] = useState('viewer')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    if (!data) return
    const mode = data.mode
    setPasswordOn(mode !== 'oidc')
    setOidcOn(mode === 'oidc' || mode === 'both')
    setSessionTtl(data.session_ttl)
    setIssuerUrl(data.oidc.issuer_url || '')
    setClientId(data.oidc.client_id || '')
    setRedirectUrl(data.oidc.redirect_url || '')
    setScopes((data.oidc.scopes || []).join(',') || 'openid,profile,email')
    setUsernameClaim(data.oidc.username_claim || 'preferred_username')
    setDefaultRole(data.oidc.default_role || 'viewer')
  }, [data])

  const canTogglePassword = oidcOn // password can only be disabled while OIDC is on

  const save = async () => {
    setError('')
    setSaved(false)
    if (!passwordOn && !oidcOn) {
      setError('At least one authentication method must remain enabled.')
      return
    }
    const mode = passwordOn && oidcOn ? 'both' : passwordOn ? 'local' : 'oidc'
    const payload: AuthSettingsUpdate = {
      mode,
      session_ttl: sessionTtl,
      oidc: {
        issuer_url: issuerUrl,
        client_id: clientId,
        client_secret: clientSecret,
        redirect_url: redirectUrl,
        scopes: scopes.split(',').map((s) => s.trim()).filter(Boolean),
        username_claim: usernameClaim,
        default_role: defaultRole,
      },
    }
    setSaving(true)
    try {
      await api.put('/api/v1/auth/settings', payload)
      setClientSecret('')
      setSaved(true)
      setTimeout(() => setSaved(false), 2500)
      refetch()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to save')
    } finally {
      setSaving(false)
    }
  }

  if (loading && !data) return <Loading />

  return (
    <Card>
      <CardHeader
        title={
          <span className="inline-flex items-center gap-2">
            <ShieldCheck className="h-4 w-4" /> Authentication
          </span>
        }
        subtitle="Sign-in methods and OpenID Connect. Environment variables seed the initial values; edits here are persisted."
      />

      <div className="space-y-5 p-5">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="flex items-center justify-between rounded-lg border border-border bg-background px-3 py-2.5">
            <div>
              <div className="text-sm text-foreground">Password sign-in</div>
              <div className="text-xs text-muted-foreground">Local username/password accounts</div>
            </div>
            <Toggle
              checked={passwordOn}
              disabled={!canTogglePassword}
              onChange={(v) => setPasswordOn(v)}
            />
          </label>
          <label className="flex items-center justify-between rounded-lg border border-border bg-background px-3 py-2.5">
            <div>
              <div className="text-sm text-foreground">OIDC (SSO)</div>
              <div className="text-xs text-muted-foreground">e.g. PocketID, Authelia, Authentik</div>
            </div>
            <Toggle checked={oidcOn} onChange={setOidcOn} />
          </label>
        </div>

        {!passwordOn && oidcOn && (
          <p className="rounded-lg bg-warning/10 px-3 py-2 text-xs text-warning">
            Password sign-in is disabled. Make sure OIDC works before saving, or you may lock yourself
            out.
          </p>
        )}

        {oidcOn && (
          <div className="space-y-4 rounded-lg border border-border bg-background p-4">
            <div className="text-sm font-medium text-foreground">OpenID Connect</div>
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
              <div>
                <Label>Issuer URL</Label>
                <Input value={issuerUrl} onChange={(e) => setIssuerUrl(e.target.value)} placeholder="https://pocketid.example.com" />
              </div>
              <div>
                <Label>Client ID</Label>
                <Input value={clientId} onChange={(e) => setClientId(e.target.value)} placeholder="whatsnewdock" />
              </div>
              <div>
                <Label>Client secret {data?.oidc.client_secret_set && <span className="text-success">· set</span>}</Label>
                <Input
                  type="password"
                  value={clientSecret}
                  onChange={(e) => setClientSecret(e.target.value)}
                  placeholder={data?.oidc.client_secret_set ? 'leave blank to keep current' : 'client secret'}
                />
              </div>
              <div>
                <Label>Redirect URL (optional)</Label>
                <Input value={redirectUrl} onChange={(e) => setRedirectUrl(e.target.value)} placeholder="auto from base URL" />
              </div>
              <div>
                <Label>Scopes (comma-separated)</Label>
                <Input value={scopes} onChange={(e) => setScopes(e.target.value)} />
              </div>
              <div>
                <Label>Username claim</Label>
                <Input value={usernameClaim} onChange={(e) => setUsernameClaim(e.target.value)} />
              </div>
              <div>
                <Label>Default role for SSO users</Label>
                <Select value={defaultRole} onChange={(e) => setDefaultRole(e.target.value)}>
                  <option value="viewer">viewer</option>
                  <option value="admin">admin</option>
                </Select>
              </div>
            </div>
          </div>
        )}

        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <div>
            <Label>Session timeout</Label>
            <Select value={sessionTtl} onChange={(e) => setSessionTtl(e.target.value)}>
              <option value="1h">1 hour</option>
              <option value="12h">12 hours</option>
              <option value="24h">24 hours</option>
              <option value="168h">7 days</option>
              <option value="720h">30 days</option>
            </Select>
          </div>
        </div>

        {error && <p className="rounded-lg bg-danger/10 px-3 py-2 text-xs text-danger">{error}</p>}

        <div className="flex items-center justify-end gap-3 border-t border-border pt-4">
          {saved && <span className="text-xs text-success">Saved</span>}
          <Button onClick={save} loading={saving}>
            Save authentication
          </Button>
        </div>
      </div>
    </Card>
  )
}
