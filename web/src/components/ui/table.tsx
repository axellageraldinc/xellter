import * as React from 'react'
import { cn } from '@/lib/utils'

/** Table is a plain table with a quiet header and hairline rows. */
export function Table({ className, ...props }: React.TableHTMLAttributes<HTMLTableElement>) {
  return (
    <div className="scroll-thin w-full overflow-x-auto">
      <table className={cn('w-full border-collapse text-sm', className)} {...props} />
    </div>
  )
}

export function THead({ className, ...props }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <thead className={cn('bg-surface-muted', className)} {...props} />
}

export function TBody({ className, ...props }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody className={cn('', className)} {...props} />
}

export function TR({
  className,
  clickable = false,
  onClick,
  ...props
}: React.HTMLAttributes<HTMLTableRowElement> & { clickable?: boolean }) {
  return (
    <tr
      onClick={onClick}
      className={cn(
        'border-b border-border-subtle last:border-0',
        clickable && 'cursor-pointer transition-colors hover:bg-surface-hover',
        className,
      )}
      {...props}
    />
  )
}

export function TH({ className, ...props }: React.ThHTMLAttributes<HTMLTableCellElement>) {
  return (
    <th
      className={cn(
        'px-4 py-2 text-left text-xs font-medium tracking-wide text-text-muted whitespace-nowrap',
        className,
      )}
      {...props}
    />
  )
}

export function TD({ className, ...props }: React.TdHTMLAttributes<HTMLTableCellElement>) {
  return <td className={cn('px-4 py-2.5 align-middle', className)} {...props} />
}

/** Empty is what a table shows when it has nothing: a sentence, not a blank. */
export function Empty({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <div className={cn('px-5 py-10 text-center text-sm text-text-muted', className)}>{children}</div>
  )
}
