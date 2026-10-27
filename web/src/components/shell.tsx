import * as React from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import {
  Activity,
  ChevronLeft,
  FileCode2,
  LayoutDashboard,
  LogOut,
  Moon,
  Sun,
  Users,
} from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'

const NAV = [
  { to: '/', label: 'Overview', icon: LayoutDashboard, end: true },
  { to: '/clients', label: 'Clients', icon: Users, end: false },
  { to: '/usage', label: 'Usage', icon: Activity, end: false },
  { to: '/config', label: 'Config', icon: FileCode2, end: false },
]

export type ShellProps = {
  theme: 'light' | 'dark'
  onTheme: () => void
  onLogout: () => void
  onLogoutAll: () => void
}

/**
 * Shell is the frame around every page: a sidebar with the four places the
 * panel can go, a top bar that says where you are, and a main area that
 * scrolls on its own.
 */
export function Shell({ theme, onTheme, onLogout, onLogoutAll, children }: ShellProps & { children: React.ReactNode }) {
  const [collapsed, setCollapsed] = React.useState(false)
  const [mobileOpen, setMobileOpen] = React.useState(false)
  const loc = useLocation()

  // On a phone the sidebar is a drawer that covers the page.
  React.useEffect(() => {
    setMobileOpen(false)
  }, [loc.pathname])

  return (
    <div className="flex min-h-screen bg-canvas">
      {/* the scrim behind the drawer on a phone */}
      {mobileOpen && (
        <button
          className="fixed inset-0 z-40 bg-[var(--color-overlay)] lg:hidden"
          onClick={() => setMobileOpen(false)}
          aria-label="Close the menu"
        />
      )}

      <aside
        className={cn(
          'fixed inset-y-0 left-0 z-50 flex flex-col border-r border-border bg-surface transition-[width,transform] duration-200',
          'lg:static lg:translate-x-0',
          collapsed ? 'lg:w-[4rem]' : 'lg:w-[var(--sidebar-width)]',
          'w-[var(--sidebar-width)]',
          mobileOpen ? 'translate-x-0' : '-translate-x-full',
        )}
      >
        <div className="flex h-[var(--header-height)] items-center gap-2 border-b border-border px-4">
          <div className="flex size-7 shrink-0 items-center justify-center rounded-md bg-primary text-xs font-bold text-white">
            x
          </div>
          {!collapsed && <span className="text-sm font-semibold tracking-tight">xellter</span>}
          <button
            className="ml-auto hidden rounded-sm p-1 text-text-muted transition-colors hover:bg-surface-hover hover:text-text lg:block"
            onClick={() => setCollapsed((c) => !c)}
            aria-label={collapsed ? 'Expand the sidebar' : 'Collapse the sidebar'}
            title={collapsed ? 'Expand' : 'Collapse'}
          >
            <ChevronLeft className={cn('size-4 transition-transform', collapsed && 'rotate-180')} />
          </button>
        </div>

        <nav className="flex-1 overflow-y-auto p-2">
          <ul className="space-y-0.5">
            {NAV.map((item) => (
              <li key={item.to}>
                <NavLink
                  to={item.to}
                  end={item.end}
                  title={collapsed ? item.label : undefined}
                  className={({ isActive }) =>
                    cn(
                      'flex items-center gap-2.5 rounded-md px-2.5 py-2 text-sm transition-colors',
                      'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30',
                      isActive
                        ? 'bg-primary-soft font-medium text-primary-text'
                        : 'text-text-secondary hover:bg-surface-hover hover:text-text',
                    )
                  }
                >
                  <item.icon className="size-4 shrink-0" />
                  {!collapsed && <span className="truncate">{item.label}</span>}
                </NavLink>
              </li>
            ))}
          </ul>
        </nav>

        <div className="space-y-0.5 border-t border-border p-2">
          <button
            onClick={onTheme}
            title={collapsed ? 'Light or dark' : undefined}
            className={cn(
              'flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-sm text-text-secondary transition-colors',
              'hover:bg-surface-hover hover:text-text',
            )}
          >
            {theme === 'dark' ? <Moon className="size-4 shrink-0" /> : <Sun className="size-4 shrink-0" />}
            {!collapsed && <span>Light / dark</span>}
          </button>
          <button
            onClick={onLogout}
            title={collapsed ? 'Sign out' : undefined}
            className={cn(
              'flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-sm text-text-secondary transition-colors',
              'hover:bg-surface-hover hover:text-text',
            )}
          >
            <LogOut className="size-4 shrink-0" />
            {!collapsed && <span>Sign out</span>}
          </button>
          {!collapsed && (
            <button
              onClick={onLogoutAll}
              className="w-full rounded-md px-2.5 py-2 text-left text-xs text-text-muted transition-colors hover:bg-surface-hover hover:text-text"
            >
              Sign out everywhere
            </button>
          )}
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex h-[var(--header-height)] items-center gap-3 border-b border-border bg-canvas/90 px-4 backdrop-blur lg:hidden">
          <Button variant="ghost" size="icon" onClick={() => setMobileOpen(true)} aria-label="Open the menu">
            <span className="flex flex-col gap-1">
              <span className="block h-0.5 w-4 rounded bg-current" />
              <span className="block h-0.5 w-4 rounded bg-current" />
              <span className="block h-0.5 w-4 rounded bg-current" />
            </span>
          </Button>
          <span className="text-sm font-semibold">xellter</span>
        </header>

        <main className="min-w-0 flex-1">{children}</main>
      </div>
    </div>
  )
}

/** Page is the standard page: a heading, a subtitle, actions, then the body. */
export function Page({
  title,
  subtitle,
  actions,
  children,
}: {
  title: React.ReactNode
  subtitle?: React.ReactNode
  actions?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div className="mx-auto w-full max-w-[88rem] px-4 py-6 lg:px-8 lg:py-8">
      <div className="mb-6 flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-xl font-semibold tracking-tight text-text">{title}</h1>
          {subtitle && <p className="mt-1 text-sm text-text-muted">{subtitle}</p>}
        </div>
        {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
      </div>
      {children}
    </div>
  )
}
