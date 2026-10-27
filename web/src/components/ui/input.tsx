import * as React from 'react'
import { cn } from '@/lib/utils'

export const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
  function Input({ className, ...props }, ref) {
    return (
      <input
        ref={ref}
        className={cn(
          'h-9 w-full rounded-md border border-border bg-surface px-3 text-sm text-text',
          'placeholder:text-text-muted transition-colors',
          'hover:border-border-strong focus:border-primary focus:outline-none focus:ring-3 focus:ring-primary/20',
          'disabled:cursor-not-allowed disabled:opacity-50',
          className,
        )}
        {...props}
      />
    )
  },
)

export const Textarea = React.forwardRef<HTMLTextAreaElement, React.TextareaHTMLAttributes<HTMLTextAreaElement>>(
  function Textarea({ className, ...props }, ref) {
    return (
      <textarea
        ref={ref}
        className={cn(
          'w-full rounded-md border border-border bg-surface px-3 py-2 font-mono text-xs text-text',
          'placeholder:text-text-muted transition-colors',
          'hover:border-border-strong focus:border-primary focus:outline-none focus:ring-3 focus:ring-primary/20',
          className,
        )}
        {...props}
      />
    )
  },
)

export function Label({ className, ...props }: React.LabelHTMLAttributes<HTMLLabelElement>) {
  return <label className={cn('text-xs font-medium text-text-secondary', className)} {...props} />
}

/** Field is a label, a control and a hint: the one way a form is laid out. */
export function Field({
  label,
  hint,
  htmlFor,
  children,
  className,
}: {
  label: string
  hint?: string
  htmlFor?: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn('space-y-1.5', className)}>
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint && <p className="text-xs leading-relaxed text-text-muted">{hint}</p>}
    </div>
  )
}
