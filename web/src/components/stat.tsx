import * as React from 'react'
import { cn } from '@/lib/utils'
import { Card } from '@/components/ui/card'

/**
 * Stat is one number with a label. The row keeps every card the same height,
 * so a long label never makes one card taller than the others.
 */
export function Stat({
  icon: Icon,
  tone = 'primary',
  value,
  label,
}: {
  icon: React.ComponentType<{ className?: string }>
  tone?: 'primary' | 'success' | 'warning' | 'danger' | 'accent'
  value: React.ReactNode
  label: string
}) {
  const tones = {
    primary: 'bg-primary-soft text-primary-text',
    success: 'bg-success-soft text-success-text',
    warning: 'bg-warning-soft text-warning-text',
    danger: 'bg-danger-soft text-danger-text',
    accent: 'bg-accent-soft text-accent-text',
  }
  return (
    <Card className="h-full p-4">
      <div className="flex items-start gap-3">
        <span className={cn('flex size-8 shrink-0 items-center justify-center rounded-md', tones[tone])}>
          <Icon className="size-4" />
        </span>
        <div className="min-w-0">
          <div className="tabular text-xl font-semibold leading-tight tracking-tight text-text">{value}</div>
          <div className="mt-0.5 text-xs leading-snug text-text-muted">{label}</div>
        </div>
      </div>
    </Card>
  )
}

/** StatRow lays the stat cards out: two on a phone, four on a desktop. */
export function StatRow({ children }: { children: React.ReactNode }) {
  return <div className="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-4">{children}</div>
}
