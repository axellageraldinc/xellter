import * as React from 'react'
import { cn } from '@/lib/utils'

export type Point = { bucket: string; requests: number; errors: number; cost_idr: number; rtk_saved: number }

/**
 * Chart draws the requests over time as a bar chart, in plain SVG: no chart
 * library, no bundle weight, and it scales with the text.
 */
export function Chart({
  data,
  unit = 'hour',
  height = 160,
}: {
  data: Point[]
  unit?: 'hour' | 'day'
  height?: number
}) {
  const [hover, setHover] = React.useState<number | null>(null)

  if (!data.length) {
    return <div className="py-10 text-center text-sm text-text-muted">No traffic in this period.</div>
  }

  const max = Math.max(1, ...data.map((d) => d.requests))
  const errors = data.reduce((a, d) => a + d.errors, 0)
  const requests = data.reduce((a, d) => a + d.requests, 0)

  // The labels are thinned so they never touch, whatever the width.
  const every = Math.max(1, Math.ceil(data.length / 8))

  return (
    <div className="relative">
      <svg
        viewBox={`0 0 ${data.length * 24} ${height}`}
        preserveAspectRatio="none"
        className="w-full"
        style={{ height }}
        role="img"
        aria-label={`Requests over time: ${requests} requests, ${errors} errors`}
        onMouseLeave={() => setHover(null)}
      >
        {data.map((d, i) => {
          const h = (d.requests / max) * (height - 8)
          const eh = (d.errors / max) * (height - 8)
          const x = i * 24
          return (
            <g key={d.bucket} onMouseEnter={() => setHover(i)}>
              <rect x={x} y={0} width={24} height={height} fill="transparent" />
              <rect x={x + 2} y={height - h} width={20} height={h} rx={2} className="fill-primary/85" />
              {d.errors > 0 && (
                <rect x={x + 2} y={height - eh} width={20} height={Math.max(1, eh)} rx={1} className="fill-danger" />
              )}
            </g>
          )
        })}
      </svg>

      <div className="mt-2 flex justify-between text-[10px] text-text-muted">
        {data.map((d, i) => (
          <span key={d.bucket} className={cn(i % every === 0 ? '' : 'invisible')}>
            {label(d.bucket, unit)}
          </span>
        ))}
      </div>

      {hover !== null && data[hover] && (
        <div
          className="pointer-events-none absolute top-0 z-10 min-w-36 -translate-x-1/2 rounded-md border border-border bg-surface px-2.5 py-2 text-xs shadow-lg"
          style={{ left: `${((hover + 0.5) / data.length) * 100}%` }}
        >
          <div className="font-medium text-text">{label(data[hover].bucket, unit)}</div>
          <div className="mt-1 tabular text-text-secondary">
            {data[hover].requests} requests
            {data[hover].errors > 0 && <span className="text-danger"> · {data[hover].errors} failed</span>}
          </div>
        </div>
      )}

      <div className="mt-3 flex flex-wrap items-center gap-4 text-xs text-text-muted">
        <span className="flex items-center gap-1.5">
          <span className="size-2.5 rounded-sm bg-primary/85" /> requests
        </span>
        {errors > 0 && (
          <span className="flex items-center gap-1.5">
            <span className="size-2.5 rounded-sm bg-danger" /> failed
          </span>
        )}
        <span className="tabular ml-auto">
          {requests} requests · {errors} failed
        </span>
      </div>
    </div>
  )
}

/** label turns a bucket key into something readable. */
function label(bucket: string, unit: 'hour' | 'day'): string {
  const d = new Date(bucket)
  if (Number.isNaN(d.getTime())) return bucket
  if (unit === 'day') return d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short' })
  return d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
}
