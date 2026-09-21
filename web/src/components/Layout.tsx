import { NavLink, Outlet, useLocation } from 'react-router-dom'
import {
  LayoutDashboard,
  Boxes,
  RefreshCw,
  Server,
  Layers,
  History,
  Settings,
  Moon,
  Sun,
  LogOut,
  Container,
  Info,
  Menu,
  X,
} from 'lucide-react'
import { useAuth } from '@/context/AuthContext'
import SelfUpdateCard from '@/components/SelfUpdateCard'
import { cx } from '@/lib/utils'
import { useEffect, useState } from 'react'

const navItems = [
  { to: '/', label: 'Dashboard', icon: LayoutDashboard, end: true },
  { to: '/containers', label: 'Containers', icon: Boxes, end: false },
  { to: '/updates', label: 'Updates', icon: RefreshCw, end: false },
  { to: '/servers', label: 'Servers', icon: Server, end: false },
  { to: '/stacks', label: 'Stacks', icon: Layers, end: false },
  { to: '/events', label: 'Activity', icon: History, end: false },
  { to: '/settings', label: 'Settings', icon: Settings, end: false },
  { to: '/about', label: 'About', icon: Info, end: false },
]

// The sidebar is a permanent column at lg and an off-canvas drawer below it.
// Tracking that boundary in React lets the closed drawer stay out of the tab
// order instead of being a focus trap for keyboard users.
function useIsDesktop() {
  const [isDesktop, setIsDesktop] = useState(
    () => typeof window !== 'undefined' && window.matchMedia('(min-width: 1024px)').matches,
  )
  useEffect(() => {
    const mq = window.matchMedia('(min-width: 1024px)')
    const onChange = () => setIsDesktop(mq.matches)
    onChange()
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [])
  return isDesktop
}

export default function Layout() {
  const { me, logout } = useAuth()
  const [theme, setTheme] = useState<'dark' | 'light'>('dark')
  const [navOpen, setNavOpen] = useState(false)
  const isDesktop = useIsDesktop()
  const { pathname } = useLocation()

  const toggleTheme = () => {
    const next = theme === 'dark' ? 'light' : 'dark'
    setTheme(next)
    document.documentElement.classList.toggle('dark', next === 'dark')
  }

  // Navigating always dismisses the drawer, so a tap on a nav item does not
  // leave it covering the page it just opened.
  useEffect(() => {
    setNavOpen(false)
  }, [pathname])

  // While the drawer is open: Escape closes it, and the page behind it must not
  // scroll. On desktop the sidebar is part of the layout and none of this runs.
  useEffect(() => {
    if (!navOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setNavOpen(false)
    }
    document.addEventListener('keydown', onKey)
    const previous = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.removeEventListener('keydown', onKey)
      document.body.style.overflow = previous
    }
  }, [navOpen])

  const sidebar = (
    <aside
      id="app-nav"
      className={cx(
        'fixed inset-y-0 left-0 z-50 flex w-72 max-w-[85vw] flex-col border-r border-border bg-card',
        'transition-transform duration-200 ease-out will-change-transform',
        'lg:static lg:z-auto lg:w-60 lg:max-w-none lg:translate-x-0 lg:transition-none',
        navOpen ? 'translate-x-0 shadow-2xl lg:shadow-none' : '-translate-x-full',
      )}
      aria-hidden={!isDesktop && !navOpen}
      {...(!isDesktop && !navOpen ? { inert: '' } : {})}
    >
      <div className="flex h-14 shrink-0 items-center gap-2.5 border-b border-border px-4">
        <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-gradient-to-br from-primary to-primary/60 text-primary-foreground shadow-sm">
          <Container className="h-4.5 w-4.5" />
        </div>
        <div className="min-w-0 leading-tight">
          <div className="truncate text-sm font-semibold text-foreground">WhatsNewDock</div>
          <div className="text-[10px] text-muted-foreground">Update monitor</div>
        </div>
        <button
          onClick={() => setNavOpen(false)}
          aria-label="Close navigation"
          className="ml-auto rounded-md p-2 text-muted-foreground hover:bg-card-hover hover:text-foreground lg:hidden"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      <nav className="flex-1 space-y-0.5 overflow-y-auto p-3">
        {navItems.map(({ to, label, icon: Icon, end }) => (
          <NavLink
            key={to}
            to={to}
            end={end}
            className={({ isActive }) =>
              cx(
                'group flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-colors lg:py-2',
                isActive
                  ? 'bg-accent text-foreground'
                  : 'text-muted-foreground hover:bg-card-hover hover:text-foreground',
              )
            }
          >
            <Icon className="h-4 w-4 shrink-0" />
            {label}
          </NavLink>
        ))}
      </nav>

      {/* Sits directly above the account block; renders nothing unless a newer
          release has actually been published. */}
      <SelfUpdateCard />

      <div className="border-t border-border p-3">
        <div className="flex items-center gap-2.5 rounded-lg px-2 py-1.5">
          <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-semibold text-foreground">
            {(me?.username || '?').slice(0, 1).toUpperCase()}
          </div>
          <div className="min-w-0 flex-1 leading-tight">
            <div className="truncate text-sm font-medium text-foreground">{me?.username}</div>
            <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{me?.role}</div>
          </div>
          <button
            onClick={logout}
            title="Sign out"
            aria-label="Sign out"
            className="rounded-md p-1.5 text-muted-foreground hover:bg-card-hover hover:text-foreground"
          >
            <LogOut className="h-4 w-4" />
          </button>
        </div>
      </div>
    </aside>
  )

  return (
    <div className="flex h-dvh overflow-hidden bg-background">
      {/* Backdrop for the off-canvas drawer (below lg only). */}
      <div
        onClick={() => setNavOpen(false)}
        aria-hidden="true"
        className={cx(
          'fixed inset-0 z-40 bg-black/60 backdrop-blur-sm transition-opacity lg:hidden',
          navOpen ? 'opacity-100' : 'pointer-events-none opacity-0',
        )}
      />

      {sidebar}

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center gap-2 border-b border-border bg-card/60 px-3 backdrop-blur sm:px-6">
          <button
            data-nav-toggle
            onClick={() => setNavOpen((v) => !v)}
            aria-label={navOpen ? 'Close navigation' : 'Open navigation'}
            aria-expanded={navOpen}
            aria-controls="app-nav"
            className="rounded-lg border border-border p-2 text-muted-foreground hover:bg-card-hover hover:text-foreground lg:hidden"
          >
            <Menu className="h-4.5 w-4.5" />
          </button>

          <div className="min-w-0 flex-1 truncate text-sm text-muted-foreground">
            <span className="hidden md:inline">Monitor every container. Read every changelog.</span>
            <span className="text-sm font-semibold text-foreground md:hidden">WhatsNewDock</span>
          </div>

          <button
            onClick={toggleTheme}
            aria-label="Toggle theme"
            className="shrink-0 rounded-lg border border-border p-2 text-muted-foreground hover:bg-card-hover hover:text-foreground"
            title="Toggle theme"
          >
            {theme === 'dark' ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
          </button>
        </header>

        <main className="flex-1 overflow-y-auto">
          <div className="mx-auto max-w-[1400px] px-4 py-5 sm:px-6 sm:py-6">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}
