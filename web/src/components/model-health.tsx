import { ChevronDown, Clock, Gauge, History, OctagonX, TriangleAlert, type LucideIcon } from 'lucide-react'
import type { ModelEvent, OutOfService } from '@/lib/types'
import { Badge, type BadgeTone } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { human, left, when } from '@/lib/format'

/**
 * Why a model is out of a preset, in the owner's words. The label is always text, the icon only repeats it, so
 * the state never depends on colour alone.
 */
const KINDS: Record<string, { label: string; icon: LucideIcon; tone: BadgeTone }> = {
  slow: { label: 'Slow', icon: Gauge, tone: 'warning' },
  unreliable: { label: 'Unreliable', icon: TriangleAlert, tone: 'danger' },
  errors: { label: 'Errors', icon: OctagonX, tone: 'danger' },
}

const kindOf = (kind: string) => KINDS[kind] ?? { label: kind || 'Out', icon: TriangleAlert, tone: 'neutral' as BadgeTone }

const TONE_CHIP: Record<BadgeTone, string> = {
  neutral: 'bg-surface-muted text-text-secondary',
  primary: 'bg-primary-soft text-primary-text',
  success: 'bg-success-soft text-success-text',
  warning: 'bg-warning-soft text-warning-text',
  danger: 'bg-danger-soft text-danger-text',
  accent: 'bg-accent-soft text-accent-text',
  outline: 'border border-border text-text-secondary',
}

const TONE_DOT: Record<BadgeTone, string> = {
  neutral: 'bg-text-muted',
  primary: 'bg-primary',
  success: 'bg-success',
  warning: 'bg-warning',
  danger: 'bg-danger',
  accent: 'bg-accent',
  outline: 'bg-text-muted',
}

/** KindBadge is the label of a reason: an icon and a word, whole on one line. */
function KindBadge({ kind }: { kind: string }) {
  const k = kindOf(kind)
  const Icon = k.icon
  return (
    <Badge tone={k.tone}>
      <Icon className="size-3 shrink-0" aria-hidden="true" />
      {k.label}
    </Badge>
  )
}

/** ModelName keeps a long model id whole: it breaks between words, never inside the badge next to it. */
function ModelName({ id }: { id: string }) {
  return <span className="mono min-w-0 text-[13px] font-medium break-words text-text">{id}</span>
}

/**
 * OutOfServiceList is the models that are out of this preset right now: who, why (with the numbers) and when they
 * come back. It is not shown while nobody is out.
 */
export function OutOfServiceList({ items }: { items: OutOfService[] | undefined }) {
  if (!items || items.length === 0) return null
  return (
    <section
      aria-labelledby="out-now-title"
      className="overflow-hidden rounded-md border border-warning/30 bg-warning-soft/30"
      data-testid="out-of-service"
    >
      <header className="flex items-start gap-2.5 px-3 py-2.5">
        <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning-text" aria-hidden="true" />
        <div className="min-w-0">
          <h3 id="out-now-title" className="text-sm font-semibold text-text">
            Out for now <span className="tabular font-normal text-text-muted">({items.length})</span>
          </h3>
          <p className="text-xs text-text-muted">
            Skipped for new conversations; another model takes the place. They come back on their own and are
            judged again.
          </p>
        </div>
      </header>
      <ul className="divide-y divide-border-subtle border-t border-warning/20 bg-surface">
        {items.map((o) => {
          const k = kindOf(o.kind)
          const Icon = k.icon
          const back = left(o.until)
          return (
            <li key={o.model} className="flex flex-col gap-2 px-3 py-3 sm:flex-row sm:items-start sm:gap-4">
              <span className={cn('mt-0.5 hidden size-7 shrink-0 items-center justify-center rounded-md sm:flex', TONE_CHIP[k.tone])}>
                <Icon className="size-4" aria-hidden="true" />
              </span>
              <div className="min-w-0 flex-1 space-y-1">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <ModelName id={o.model} />
                  <KindBadge kind={o.kind} />
                  {o.strikes > 1 && (
                    <Badge tone="outline" title="It came back and was bad again">
                      out {o.strikes} times in a row
                    </Badge>
                  )}
                </div>
                <p className="text-xs leading-relaxed text-text-secondary">{o.reason}</p>
              </div>
              <div className="tabular flex shrink-0 items-center gap-1.5 text-xs text-text-secondary sm:pt-1">
                <Clock className="size-3.5 text-text-muted" aria-hidden="true" />
                <span>{back ? `Back in ${back}` : 'Back any moment'}</span>
              </div>
            </li>
          )
        })}
      </ul>
    </section>
  )
}

/** OutHistory is who was taken out of this preset before, newest first, as a timeline. */
export function OutHistory({ events }: { events: ModelEvent[] | undefined }) {
  if (!events || events.length === 0) return null
  const now = Date.now()
  return (
    <details className="group rounded-md border border-border bg-surface" data-testid="out-history">
      <summary className="flex cursor-pointer list-none items-center gap-2 rounded-md px-3 py-2.5 text-sm font-semibold text-text focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary [&::-webkit-details-marker]:hidden">
        <History className="size-4 shrink-0 text-text-muted" aria-hidden="true" />
        <span>Taken out before</span>
        <span className="tabular font-normal text-text-muted">({events.length})</span>
        <ChevronDown
          className="ml-auto size-4 shrink-0 text-text-muted transition-transform duration-200 group-open:rotate-180 motion-reduce:transition-none"
          aria-hidden="true"
        />
      </summary>
      <ol className="mx-3 mb-3 ml-4 border-l border-border-strong pt-1">
        {events.map((e) => {
          const k = kindOf(e.kind)
          const still = e.until > now
          return (
            <li key={`${e.ts}-${e.model}`} className="relative space-y-1 pb-4 pl-4 last:pb-1">
              <span
                aria-hidden="true"
                className={cn('absolute top-1.5 -left-[5px] size-2.5 rounded-full border-2 border-surface', TONE_DOT[k.tone])}
              />
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <time
                  dateTime={new Date(e.ts).toISOString()}
                  title={new Date(e.ts).toLocaleString()}
                  className="tabular text-xs font-medium text-text-secondary"
                >
                  {when(e.ts)}
                </time>
                <ModelName id={e.model} />
                <KindBadge kind={e.kind} />
                {still ? (
                  <Badge tone="warning">still out</Badge>
                ) : (
                  <span className="tabular text-xs text-text-muted">out for {human(Math.max(0, (e.until - e.ts) / 1000))}</span>
                )}
              </div>
              <p className="text-xs leading-relaxed text-text-secondary">{e.reason}</p>
            </li>
          )
        })}
      </ol>
    </details>
  )
}
