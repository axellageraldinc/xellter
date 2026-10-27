import * as React from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { AlertTriangle, ArrowLeft, Coins, Wallet, Zap } from 'lucide-react'
import { api } from '@/lib/api'
import type { Usage } from '@/lib/types'
import { idr, num } from '@/lib/format'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TBody, TD, TH, THead, TR, Empty } from '@/components/ui/table'
import { Stat, StatRow } from '@/components/stat'
import { Chart } from '@/components/chart'
import { Page } from '@/components/shell'
import { useToast } from '@/components/ui/toast'

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

      <Card className="mb-4">
        <CardHeader>
          <CardTitle>Requests over time</CardTitle>
        </CardHeader>
        <CardContent>
          <Chart data={data?.series ?? []} unit={window === '30d' ? 'day' : 'hour'} />
        </CardContent>
      </Card>

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
                <TH className="text-right">Cached</TH>
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
                  <TR key={i} clickable={!!drill} onClick={drill ?? undefined}>
                    <TD>
                      <span className="flex items-center gap-2">
                        <span className="mono">{r.label || r.key}</span>
                        {r.billing && (
                          <Badge tone={r.billing === 'free' ? 'success' : r.billing === 'plan' ? 'primary' : 'neutral'}>
                            {r.billing}
                          </Badge>
                        )}
                      </span>
                    </TD>
                    <TD className="tabular text-right">{num(r.requests)}</TD>
                    <TD className={r.errors ? 'tabular text-right text-danger-text' : 'tabular text-right text-text-muted'}>
                      {num(r.errors)}
                    </TD>
                    <TD className="tabular text-right whitespace-nowrap">
                      {num(r.in_tok)} / {num(r.out_tok)}
                    </TD>
                    <TD className="tabular text-right text-text-muted">{num(r.cached_tok)}</TD>
                    <TD className="tabular text-right text-text-muted">{num(r.rtk_saved)}</TD>
                    <TD className="tabular text-right whitespace-nowrap">Rp {idr(r.cost_idr)}</TD>
                  </TR>
                )
              })}
            </TBody>
          </Table>
        )}
      </Card>
    </Page>
  )
}
