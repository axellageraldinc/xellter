import * as React from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { AlertTriangle, ArrowLeft, Coins, Wallet, Zap } from 'lucide-react'
import { api } from '@/lib/api'
import type { Usage, UsageCall } from '@/lib/types'
import { idr, num, secs, wib } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TBody, TD, TH, THead, TR, Empty } from '@/components/ui/table'
import { Stat, StatRow } from '@/components/stat'
import { Page } from '@/components/shell'
import { useToast } from '@/components/ui/toast'
import { BillingBadge } from '@/components/billing-badge'

/**
 * Usage is a drill-down: all clients, then one client, then its presets, then
 * the models inside a preset. Every level is a link.
 */
export function UsagePage() {
  const [params, setParams] = useSearchParams()
  const [data, setData] = React.useState<Usage | null>(null)
  const nav = useNavigate()
  const toast = useToast()

  const client = params.get('client') ?? ''
  const preset = params.get('preset') ?? ''
  const window = params.get('window') ?? '24h'

  const load = React.useCallback(async () => {
    const qs = new URLSearchParams({ window })
    if (client) qs.set('client', client)
    if (preset) qs.set('preset', preset)
    setData(await api<Usage>('usage/drill?' + qs))
  }, [client, preset, window])

  React.useEffect(() => {
    load().catch((e) => toast.push(e.message, 'bad'))
  }, [load, toast])

  const at = (patch: { client?: string; preset?: string }) => {
    const next = new URLSearchParams({ window })
    const c = patch.client === undefined ? client : patch.client
    const p = patch.preset === undefined ? preset : patch.preset
    if (c) next.set('client', c)
    if (p) next.set('preset', p)
    return `/usage?${next}`
  }

  const level = data?.level ?? 'client'
  const totals = data?.totals ?? {}

  return (
    <Page
      title="Usage"
      subtitle={
        <>
          {window === '30d' ? 'Last 30 days' : 'Last 24 hours'}
          {client && ` · ${client}`}
          {preset && ` / ${preset}`}
        </>
      }
      actions={
        <>
          <div className="inline-flex rounded-md border border-border p-0.5">
            {(['24h', '30d'] as const).map((w) => (
              <button
                key={w}
                onClick={() => {
                  const n = new URLSearchParams({ window: w })
                  if (client) n.set('client', client)
                  if (preset) n.set('preset', preset)
                  setParams(n)
                }}
                className={
                  window === w
                    ? 'rounded-sm bg-surface px-2.5 py-1 text-xs font-medium text-text shadow-xs'
                    : 'rounded-sm px-2.5 py-1 text-xs text-text-secondary transition-colors hover:text-text'
                }
              >
                {w === '24h' ? '24 h' : '30 d'}
              </button>
            ))}
          </div>
          {level !== 'client' && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => nav(at(level === 'model' ? { preset: '' } : { client: '', preset: '' }))}
            >
              <ArrowLeft />
              Back
            </Button>
          )}
        </>
      }
    >
      <StatRow>
        <Stat
          icon={Zap}
          tone="primary"
          value={num(totals.requests)}
          label={`requests · ${num(totals.in_tok)} in / ${num(totals.out_tok)} out tokens`}
        />
        <Stat
          icon={AlertTriangle}
          tone={totals.errors ? 'warning' : 'success'}
          value={totals.requests ? ((totals.errors ?? 0) / totals.requests * 100).toFixed(1) + '%' : '0%'}
          label={`failed (${num(totals.errors)} of ${num(totals.requests)})`}
        />
        <Stat
          icon={Wallet}
          tone="success"
          value={num(Math.round((totals.rtk_saved ?? 0) / 1000)) + ' k'}
          label="characters saved by RTK"
        />
        <Stat
          icon={Coins}
          tone="accent"
          value={'Rp ' + idr(totals.cost_idr)}
          label="at list price, free and plan included"
        />
      </StatRow>

      <Card>
        <CardHeader>
          <CardTitle>
            {level === 'client' ? 'Clients' : level === 'preset' ? `Presets of ${client}` : `Models in ${preset}`}
          </CardTitle>
        </CardHeader>
        {(!data || data.rows.length === 0) ? (
          <Empty>No traffic in this period.</Empty>
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Name</TH>
                <TH className="text-right">Requests</TH>
                <TH className="text-right">Errors</TH>
                <TH className="text-right">Tokens in / out</TH>
                <TH className="text-right">Cache hit</TH>
                <TH className="text-right">RTK saved</TH>
                <TH className="text-right">Rp at list price</TH>
              </TR>
            </THead>
            <TBody>
              {data.rows.map((r, i) => {
                const drill =
                  level === 'client'
                    ? () => nav(at({ client: r.client || r.key, preset: '' }))
                    : level === 'preset'
                      ? () => nav(at({ preset: r.preset || r.key }))
                      : null
                return (
                  <TR key={i} clickable={!!drill && !r.removed} onClick={drill && !r.removed ? drill : undefined}>
                    <TD>
                      <span className="flex items-center gap-2">
                        <span className={r.removed ? 'mono text-text-muted line-through' : 'mono'}>
                          {r.label || r.key}
                        </span>
                        {r.billing && <BillingBadge billing={r.billing} />}
                        {r.removed && <Badge tone="neutral">no longer a client</Badge>}
                      </span>
                    </TD>
                    <TD className="tabular text-right">{num(r.requests)}</TD>
                    <TD className={r.errors ? 'tabular text-right text-danger-text' : 'tabular text-right text-text-muted'}>
                      {num(r.errors)}
                    </TD>
                    <TD className="tabular text-right whitespace-nowrap">
                      {num(r.in_tok)} / {num(r.out_tok)}
                    </TD>
                    <TD className="tabular text-right text-text-muted">
                      {r.in_tok > 0 ? ((r.cached_tok / r.in_tok) * 100).toFixed(0) + '%' : '—'}
                    </TD>
                    <TD className="tabular text-right text-text-muted">{num(r.rtk_saved)}</TD>
                    <TD className="tabular text-right whitespace-nowrap">Rp {idr(r.cost_idr)}</TD>
                  </TR>
                )
              })}
            </TBody>
          </Table>
        )}
      </Card>

      {data?.search && <SearchCard s={data.search} />}

      <CallsCard calls={data?.calls ?? []} level={level} />
    </Page>
  )
}

const PAGE_SIZE = 10

/** CallsCard is the trace of the newest 100 model calls of this level, ten to a page. */
function CallsCard({ calls, level }: { calls: UsageCall[]; level: 'client' | 'preset' | 'model' }) {
  const [page, setPage] = React.useState(0)
  const pages = Math.max(1, Math.ceil(calls.length / PAGE_SIZE))
  // a new window or level brings a new list: start again from the newest
  React.useEffect(() => setPage(0), [calls])
  const shown = calls.slice(page * PAGE_SIZE, (page + 1) * PAGE_SIZE)
  return (
    <Card className="mt-4">
      <CardHeader>
        <CardTitle>Recent calls</CardTitle>
        <span className="text-xs text-text-muted">GMT+7 · newest {calls.length} of at most 100</span>
      </CardHeader>
      {calls.length === 0 ? (
        <Empty>No calls in this period.</Empty>
      ) : (
        <>
          <Table>
            <THead>
              <TR>
                {level === 'client' && <TH>Client</TH>}
                {level === 'preset' && <TH>Preset</TH>}
                <TH>Model</TH>
                <TH>Time</TH>
                <TH>Result</TH>
                <TH className="text-right">Tokens in / out</TH>
                <TH className="text-right">Cache hit</TH>
                <TH className="text-right">RTK saved</TH>
                <TH className="text-right">Rp at list price</TH>
              </TR>
            </THead>
            <TBody>
              {shown.map((c, i) => (
                <TR key={i}>
                  {level === 'client' && <TD className="mono">{c.client || '—'}</TD>}
                  {level === 'preset' && <TD className="mono">{c.preset || '—'}</TD>}
                  <TD className="mono">{c.model || '—'}</TD>
                  <TD className="tabular whitespace-nowrap text-text-muted">{wib(c.ts)}</TD>
                  <TD>
                    <Badge tone={c.ok ? 'success' : 'danger'}>{c.ok ? 'success' : `error${c.status ? ' ' + c.status : ''}`}</Badge>
                  </TD>
                  <TD className="tabular whitespace-nowrap text-right">
                    {num(c.in_tok)} / {num(c.out_tok)}
                  </TD>
                  <TD className="tabular text-right text-text-muted">
                    {c.in_tok > 0 ? ((c.cached_tok / c.in_tok) * 100).toFixed(0) + '%' : '—'}
                  </TD>
                  <TD className="tabular text-right text-text-muted">{num(c.rtk_saved)}</TD>
                  <TD className="tabular whitespace-nowrap text-right">Rp {idr(c.cost_idr)}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
          {pages > 1 && (
            <div className="flex items-center justify-between border-t border-border-subtle px-4 py-2 text-xs text-text-muted">
              <span>
                Page {page + 1} of {pages}
              </span>
              <span className="flex gap-2">
                <Button variant="outline" size="sm" disabled={page === 0} onClick={() => setPage(page - 1)}>
                  Previous
                </Button>
                <Button variant="outline" size="sm" disabled={page >= pages - 1} onClick={() => setPage(page + 1)}>
                  Next
                </Button>
              </span>
            </div>
          )}
        </>
      )}
    </Card>
  )
}

/**
 * SearchCard is the X search service. It is not a model and has no tokens: it is
 * billed per answer, and kenari's own 503 ("no usable answer") is retried for
 * free, so a failure here is a call that gave up and cost nothing.
 */
function SearchCard({ s }: { s: NonNullable<Usage['search']> }) {
  return (
    <Card className="mt-4">
      <CardHeader>
        <CardTitle>X search</CardTitle>
        <span className="text-xs text-text-muted">a service of kenari, billed per answer</span>
      </CardHeader>
      <div className="grid grid-cols-2 gap-x-6 gap-y-3 px-4 pb-4 text-sm lg:grid-cols-6">
        <Fact label="Calls" value={num(s.calls)} />
        <Fact label="Answered (charged)" value={num(s.ok)} />
        <Fact label="Gave up (free)" value={num(s.failed)} tone={s.failed ? 'danger' : undefined} />
        <Fact label="Free retries" value={num(s.retries)} />
        <Fact label="Average answer" value={s.ok ? secs(s.avg_ms) : '—'} />
        <Fact label="Spent" value={'Rp ' + idr(s.cost_idr)} />
      </div>
      <div className="border-t border-border-subtle px-4 py-2 text-xs text-text-muted">
        Model: {s.model ? <span className="mono text-text">{s.model}</span> : 'not reported by kenari'}
      </div>
    </Card>
  )
}

function Fact({ label, value, tone }: { label: string; value: string; tone?: 'danger' }) {
  return (
    <div>
      <div className="text-xs text-text-muted">{label}</div>
      <div className={tone === 'danger' ? 'tabular text-danger-text' : 'tabular'}>{value}</div>
    </div>
  )
}
