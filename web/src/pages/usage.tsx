import * as React from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { AlertTriangle, ArrowLeft, Coins, PiggyBank, Wallet, X, Zap } from 'lucide-react'
import { api } from '@/lib/api'
import type { Saving, Usage, UsageCall } from '@/lib/types'
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
  const compare = params.get('compare') ?? ''
  // the model whose saving the table column shows
  const [col, setCol] = React.useState('')

  const load = React.useCallback(async () => {
    const qs = new URLSearchParams({ window })
    if (client) qs.set('client', client)
    if (preset) qs.set('preset', preset)
    if (compare) qs.set('compare', compare)
    setData(await api<Usage>('usage/drill?' + qs))
  }, [client, preset, window, compare])

  React.useEffect(() => {
    load().catch((e) => toast.push(e.message, 'bad'))
  }, [load, toast])

  const at = (patch: { client?: string; preset?: string }) => {
    const next = new URLSearchParams({ window })
    if (compare) next.set('compare', compare)
    const c = patch.client === undefined ? client : patch.client
    const p = patch.preset === undefined ? preset : patch.preset
    if (c) next.set('client', c)
    if (p) next.set('preset', p)
    return `/usage?${next}`
  }

  const level = data?.level ?? 'client'
  const ids = (data?.compare ?? []).map((m) => m.id)
  const colModel = ids.includes(col) ? col : ids[0] ?? ''
  const setCompare = (next: string[]) => {
    const n = new URLSearchParams(params)
    n.set('compare', next.join(','))
    setParams(n)
  }
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
                  if (compare) n.set('compare', compare)
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

      <SavingsCard data={data} ids={ids} setCompare={setCompare} col={colModel} setCol={setCol} />

      <Card className="mt-4">
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
                {colModel && <TH className="text-right whitespace-nowrap">Saved vs {colModel}</TH>}
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
                    {colModel && <SavedCell s={r.savings?.find((x) => x.model === colModel)} />}
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

const KIND_NAMES: Record<string, string> = {
  tokens: 'text',
  image: 'images',
  second: 'seconds',
  '1k_chars': 'speech',
  song: 'music',
}

/** kindsLabel names what a model is compared on: text, images, speech and so on. */
function kindsLabel(kinds?: string[]): string {
  return (kinds ?? []).map((k) => KIND_NAMES[k] ?? k).join(' · ')
}

/** SavedCell is a saving as rupiah and a percentage. */
function SavedCell({ s }: { s?: Saving }) {
  if (!s || s.baseline_idr <= 0) return <TD className="tabular text-right text-text-muted">—</TD>
  return (
    <TD className={`tabular whitespace-nowrap text-right ${s.saved_idr < 0 ? 'text-danger-text' : 'text-success-text'}`}>
      Rp {idr(s.saved_idr)} · {s.saved_pct.toFixed(0)}%
    </TD>
  )
}

/**
 * SavingsCard answers "what would this have cost on a popular model, without
 * RTK?" for the level being shown. The first models come from usage.compare_models
 * in the config file; any other model, text or not, can be added here and is kept in the link.
 */
function SavingsCard({
  data,
  ids,
  setCompare,
  col,
  setCol,
}: {
  data: Usage | null
  ids: string[]
  setCompare: (next: string[]) => void
  col: string
  setCol: (id: string) => void
}) {
  const [options, setOptions] = React.useState<{ id: string; endpoint: string }[]>([])
  React.useEffect(() => {
    api<{ models: { id: string; endpoint: string }[] }>('catalog/models')
      .then((r) => setOptions(r.models ?? []))
      .catch(() => setOptions([]))
  }, [])
  const sv = data?.totals.savings ?? []
  const missing = options.filter((o) => !ids.includes(o.id))
  return (
    <Card className="mt-4">
      <CardHeader>
        <CardTitle>
          <span className="flex items-center gap-2">
            <PiggyBank className="size-4" /> Estimated saved cost
          </span>
        </CardTitle>
        <span className="text-xs text-text-muted">vs the same traffic on a popular model, without RTK</span>
      </CardHeader>
      {ids.length === 0 || !data ? (
        <Empty>No model to compare with.</Empty>
      ) : (
        <Table>
          <THead>
            <TR>
              <TH>Compared with</TH>
              <TH className="text-right">Would have cost</TH>
              <TH className="text-right">Saved</TH>
              <TH className="text-right">Saved %</TH>
              <TH className="text-right">of which RTK</TH>
              <TH />
            </TR>
          </THead>
          <TBody>
            {ids.map((id) => {
              const s = sv.find((x) => x.model === id)
              const known = data.compare?.find((m) => m.id === id)?.found
              return (
                <TR key={id} clickable onClick={() => setCol(id)}>
                  <TD>
                    <span className="flex items-center gap-2">
                      <span className="mono">{id}</span>
                      {id === col && <Badge tone="neutral">in table</Badge>}
                      {!known && <Badge tone="warning">not in the catalog</Badge>}
                      {known && <span className="text-xs text-text-muted">{kindsLabel(data.compare?.find((m) => m.id === id)?.kinds)}</span>}
                    </span>
                  </TD>
                  <TD className="tabular text-right">{known && s ? 'Rp ' + idr(s.baseline_idr) : '—'}</TD>
                  <TD className={`tabular text-right ${s && s.saved_idr < 0 ? 'text-danger-text' : 'text-success-text'}`}>
                    {known && s ? 'Rp ' + idr(s.saved_idr) : '—'}
                  </TD>
                  <TD className="tabular text-right">{known && s && s.baseline_idr > 0 ? s.saved_pct.toFixed(1) + '%' : '—'}</TD>
                  <TD className="tabular text-right text-text-muted">{known && s ? 'Rp ' + idr(s.rtk_idr) : '—'}</TD>
                  <TD className="text-right">
                    <button
                      aria-label={`Stop comparing with ${id}`}
                      className="text-text-muted hover:text-text"
                      onClick={(e) => {
                        e.stopPropagation()
                        setCompare(ids.filter((x) => x !== id))
                      }}
                    >
                      <X className="size-4" />
                    </button>
                  </TD>
                </TR>
              )
            })}
          </TBody>
        </Table>
      )}
      <div className="flex flex-wrap items-center gap-2 border-t border-border-subtle px-4 py-2 text-xs text-text-muted">
        <span>Compare with another model:</span>
        <select
          className="h-8 max-w-64 rounded-md border border-border bg-surface px-2 text-xs text-text"
          value=""
          onChange={(e) => e.target.value && setCompare([...ids, e.target.value])}
        >
          <option value="">choose a model…</option>
          {missing.map((o) => (
            <option key={o.id} value={o.id}>
              {o.id} ({o.endpoint})
            </option>
          ))}
        </select>
        <span>Defaults are set in the Config file under usage.compare_models. RTK tokens are counted at 4 characters each. Images, speech, video and music are priced on the units made since this was added; a model only answers for the kinds it can do.</span>
      </div>
    </Card>
  )
}
