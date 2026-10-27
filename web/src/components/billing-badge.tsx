import { Badge, type BadgeTone } from '@/components/ui/badge'

/**
 * BillingBadge is the one place a billing class is shown.
 *
 * The three classes are what the owner pays with, not what the model is:
 *
 * - **free** — the model costs nothing.
 * - **plan** — the model is covered by the kenari plan.
 * - **payg** — the model is paid for out of the balance, model by model.
 *
 * The wording comes from the request log (`free`, `plan`, `payg`); the router
 * calls the last one `balance`, so both are understood here.
 */
const LABELS: Record<string, { text: string; tone: BadgeTone }> = {
  free: { text: 'free', tone: 'success' },
  plan: { text: 'plan', tone: 'primary' },
  payg: { text: 'payg', tone: 'danger' },
  balance: { text: 'payg', tone: 'danger' },
}

export function BillingBadge({
  billing,
  className,
  title,
}: {
  billing: string | undefined
  className?: string
  title?: string
}) {
  if (!billing) return null
  const spec = LABELS[billing.toLowerCase()]
  if (!spec) return null
  return (
    <Badge tone={spec.tone} className={className} title={title}>
      {spec.text}
    </Badge>
  )
}
