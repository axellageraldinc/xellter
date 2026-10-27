import * as React from 'react'
import { api } from '@/lib/api'

/** Login is the sign-in page: username, password and the authenticator code. */
export function LoginPage({ onSignedIn }: { onSignedIn: () => void }) {
  const [username, setUsername] = React.useState('')
  const [password, setPassword] = React.useState('')
  const [code, setCode] = React.useState('')
  const [error, setError] = React.useState('')
  const [busy, setBusy] = React.useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api('login', { method: 'POST', body: { username, password, code } })
      onSignedIn()
    } catch (err) {
      setError((err as Error).message)
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-canvas px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 text-center">
          <div className="mx-auto mb-3 flex size-10 items-center justify-center rounded-lg bg-primary text-base font-bold text-white">
            x
          </div>
          <h1 className="text-lg font-semibold tracking-tight">xellter</h1>
          <p className="mt-1 text-sm text-text-muted">Sign in to the panel</p>
        </div>

        <form onSubmit={submit} className="space-y-4 rounded-xl border border-border bg-surface p-6">
          <div className="space-y-1.5">
            <label htmlFor="username" className="text-xs font-medium text-text-secondary">
              Username
            </label>
            <input
              id="username"
              className="h-9 w-full rounded-md border border-border bg-surface px-3 text-sm text-text placeholder:text-text-muted focus:border-primary focus:outline-none focus:ring-3 focus:ring-primary/20"
              value={username}
              autoComplete="username"
              onChange={(e) => setUsername(e.target.value)}
              required
              autoFocus
            />
          </div>

          <div className="space-y-1.5">
            <label htmlFor="password" className="text-xs font-medium text-text-secondary">
              Password
            </label>
            <input
              id="password"
              type="password"
              className="h-9 w-full rounded-md border border-border bg-surface px-3 text-sm text-text placeholder:text-text-muted focus:border-primary focus:outline-none focus:ring-3 focus:ring-primary/20"
              value={password}
              autoComplete="current-password"
              onChange={(e) => setPassword(e.target.value)}
              required
            />
          </div>

          <div className="space-y-1.5">
            <label htmlFor="code" className="text-xs font-medium text-text-secondary">
              Authenticator code
            </label>
            <input
              id="code"
              inputMode="numeric"
              autoComplete="one-time-code"
              className="h-9 w-full rounded-md border border-border bg-surface px-3 text-sm tracking-widest text-text placeholder:tracking-normal placeholder:text-text-muted focus:border-primary focus:outline-none focus:ring-3 focus:ring-primary/20"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              required
            />
            <p className="text-xs text-text-muted">A recovery code works here too.</p>
          </div>

          {error && (
            <p className="rounded-md border border-danger/30 bg-danger-soft px-3 py-2 text-sm text-danger-text" role="alert">
              {error}
            </p>
          )}

          <button
            type="submit"
            disabled={busy}
            className="h-9 w-full rounded-md bg-primary text-sm font-medium text-white transition-colors hover:bg-primary-hover disabled:opacity-50"
          >
            {busy ? 'Signing in…' : 'Sign in'}
          </button>
        </form>
      </div>
    </div>
  )
}
