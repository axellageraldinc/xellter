import * as React from 'react'
import { cn } from '@/lib/utils'

/** Badge is a small, quiet label: the only thing that colours a row. */
const tones = {
  neutral: 'bg-surface-muted text-text-secondary',
  primary: 'bg-primary-soft text-primary-text',
  success: 'bg-success-soft text-success-text',
  warning: 'bg-warning-soft text-warning-text',
  danger: 'bg-danger-soft text-danger-text',
  accent: 'bg-accent-soft text-accent-text',
  outline: 'border border-border text-text-secondary',
} as const

export type BadgeTone = keyof typeof tones

export function Badge({
  className,
  tone = 'neutral',
  ...props
}: React.HTMLAttributes<HTMLSpanElement> & { tone?: BadgeTone }) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-sm px-2 py-0.5 text-xs font-medium whitespace-nowrap',
        tones[tone],
        className,
      )}
      {...props}
    />
  )
}

/** Dot is a status dot: the quiet way to show a state in a table. */
export function Dot({ tone = 'success', className }: { tone?: BadgeTone; className?: string }) {
  const colors: Record<BadgeTone, string> = {
    neutral: 'bg-text-muted',
    primary: 'bg-primary',
    success: 'bg-success',
    warning: 'bg-warning',
    danger: 'bg-danger',
    accent: 'bg-accent',
    outline: 'bg-text-muted',
  }
  return <span className={cn('size-2 shrink-0 rounded-full', colors[tone], className)} aria-hidden />
}
