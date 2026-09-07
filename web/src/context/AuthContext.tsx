import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import { api, setUnauthorizedHandler } from '@/lib/api'
import type { AuthConfig, Me } from '@/lib/types'

interface AuthContextValue {
  me: Me | null
  authConfig: AuthConfig | null
  loading: boolean
  refresh: () => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue>({
  me: null,
  authConfig: null,
  loading: true,
  refresh: async () => {},
  logout: async () => {},
})

export function AuthProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<Me | null>(null)
  const [authConfig, setAuthConfig] = useState<AuthConfig | null>(null)
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(async () => {
    const [meData, cfg] = await Promise.all([
      api.get<Me>('/api/auth/me'),
      api.get<AuthConfig>('/api/auth/config'),
    ])
    setMe(meData)
    setAuthConfig(cfg)
  }, [])

  useEffect(() => {
    setUnauthorizedHandler(() => {
      setMe({ authenticated: false })
    })
    refresh()
      .catch(() => setMe({ authenticated: false }))
      .finally(() => setLoading(false))
  }, [refresh])

  const logout = useCallback(async () => {
    try {
      await api.post('/api/auth/logout')
    } finally {
      setMe({ authenticated: false })
    }
  }, [])

  return (
    <AuthContext.Provider value={{ me, authConfig, loading, refresh, logout }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  return useContext(AuthContext)
}
