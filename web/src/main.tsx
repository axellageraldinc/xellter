import * as React from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import './styles/index.css'
import { api } from '@/lib/api'
import { Shell } from '@/components/shell'
import { ToastProvider } from '@/components/ui/toast'
import { OverviewPage } from '@/pages/overview'
import { ClientsPage } from '@/pages/clients'
import { ClientDetailPage } from '@/pages/client'
import { UsagePage } from '@/pages/usage'
import { ConfigPage } from '@/pages/config'
import { LoginPage } from '@/pages/login'

/** useTheme is the light and dark switch, remembered in the browser. */
function useTheme() {
  const [theme, setTheme] = React.useState<'light' | 'dark'>(() => {
    const saved = localStorage.getItem('xl-theme')
    if (saved === 'light' || saved === 'dark') return saved
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  })

  React.useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    localStorage.setItem('xl-theme', theme)
  }, [theme])

  return [theme, () => setTheme((t) => (t === 'dark' ? 'light' : 'dark'))] as const
}

/** App is the whole panel: the login, or the shell with the four pages. */
function App() {
  const [signedIn, setSignedIn] = React.useState<boolean | null>(null)
  const [theme, toggleTheme] = useTheme()

  React.useEffect(() => {
    api('me')
      .then(() => setSignedIn(true))
      .catch(() => setSignedIn(false))
  }, [])

  if (signedIn === null) {
    return <div className="min-h-screen bg-canvas" />
  }

  if (!signedIn) {
    return <LoginPage onSignedIn={() => setSignedIn(true)} />
  }

  return (
    <Shell
      theme={theme}
      onTheme={toggleTheme}
      onLogout={async () => {
        try {
          await api('logout', { method: 'POST' })
        } finally {
          setSignedIn(false)
        }
      }}
      onLogoutAll={async () => {
        if (!confirm('End every signed-in session?')) return
        try {
          await api('logout-all', { method: 'POST' })
        } finally {
          setSignedIn(false)
        }
      }}
    >
      <Routes>
        <Route path="/" element={<OverviewPage />} />
        <Route path="/clients" element={<ClientsPage />} />
        <Route path="/clients/:name" element={<ClientDetailPage />} />
        <Route path="/usage" element={<UsagePage />} />
        <Route path="/config" element={<ConfigPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Shell>
  )
}

const root = document.getElementById('root')
if (!root) throw new Error('the root element is missing')
createRoot(root).render(
  <React.StrictMode>
    <ToastProvider>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </ToastProvider>
  </React.StrictMode>,
)
