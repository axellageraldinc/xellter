import * as React from 'react'
import { AlertCircle, CheckCircle2, Info } from 'lucide-react'
import { cn } from '@/lib/utils'

type Toast = { id: number; text: string; tone: 'ok' | 'bad' | 'info' }

const ToastContext = React.createContext<{
  push: (text: string, tone?: 'ok' | 'bad' | 'info') => void
} | null>(null)

/** useToast gives a page the one way to tell the owner something happened. */
export function useToast() {
  const ctx = React.useContext(ToastContext)
  if (!ctx) throw new Error('useToast must be used inside the toast provider')
  return ctx
}

/** ToastProvider shows the messages, one at a time, in the corner. */
export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [toasts, setToasts] = React.useState<Toast[]>([])
  const next = React.useRef(1)

  const push = React.useCallback((text: string, tone: 'ok' | 'bad' | 'info' = 'ok') => {
    const id = next.current++
    setToasts((t) => [...t, { id, text, tone }])
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), tone === 'bad' ? 7000 : 3000)
  }, [])

  const value = React.useMemo(() => ({ push }), [push])

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div
        className="pointer-events-none fixed bottom-6 left-1/2 z-[100] flex w-[min(28rem,calc(100vw-2rem))] -translate-x-1/2 flex-col gap-2"
        role="status"
        aria-live="polite"
      >
        {toasts.map((t) => (
          <div
            key={t.id}
            className={cn(
              'pointer-events-auto flex items-start gap-2.5 rounded-lg border px-3.5 py-2.5 text-sm shadow-lg',
              t.tone === 'bad'
                ? 'border-danger/30 bg-danger-soft text-danger-text'
                : t.tone === 'info'
                  ? 'border-border bg-surface text-text'
                  : 'border-success/30 bg-success-soft text-success-text',
            )}
          >
            {t.tone === 'bad' ? (
              <AlertCircle className="mt-0.5 size-4 shrink-0" />
            ) : t.tone === 'info' ? (
              <Info className="mt-0.5 size-4 shrink-0" />
            ) : (
              <CheckCircle2 className="mt-0.5 size-4 shrink-0" />
            )}
            <span className="leading-snug">{t.text}</span>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}
