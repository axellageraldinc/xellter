import * as React from 'react'
import { useNavigate } from 'react-router-dom'
import { AlertTriangle, CheckCircle2, Coins, RefreshCw, Wallet, Zap } from 'lucide-react'
import { api } from '@/lib/api'
import type { Account, Overview, ProviderOption, Usage } from '@/lib/types'
import { idr, left, num, pl } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TBody, TD, TH, THead, TR, Empty } from '@/components/ui/table'
import { Stat, StatRow } from '@/components/stat'
import { Chart } from '@/components/chart'
import { Page } from '@/components/shell'
import { ProvidersCard } from '@/components/providers-card'
import { useToast } from '@/components/ui/toast'

/** The plan windows, shortest first: the five-hour one, then the week, then the month. */
const WINDOW_ORDER = ['five_hour', '5h', 'week', 'month']

const WINDOW_LABEL: Record<string, string> = {
  five_hour: '5-hour window',
  '5h': '5-hour window',
  week: 'This week',
  month: 'This month',
}

/** Overview is the landing page: is anything wrong, what happened today, and the plan. */
export function OverviewPage() {
  const [data, setData] = React.useState<Overview | null>(null)
  const [usage, setUsage] = React.useState<Usage | null>(null)
  const [busy, setBusy] = React.useState(false)
  const nav = useNavigate()
  const toast = useToast()

  const load = React.useCallback(async () => {
    // The usage drill answers totals, rows and the hourly series: the cards and the
    // chart both read it. /api/usage returns a bare array of buckets and has no totals,
    // which left every number on this page at zero.
    const [o, u] = await Promise.all([api<Overview>('overview'), api<Usage>('usage/drill?window=24h')])
    setData(o)
    setUsage(u)
  }, [])

  React.useEffect(() => {
    load().catch((e) => toast.push(e.message, 'bad'))
    const t = setInterval(() => {
      if (!document.hidden) load().catch(() => {})
    }, 10000)
    return () => clearInterval(t)
  }, [load, toast])

  if (!data) return <Page title="Overview">Loading…</Page>

  const t = usage?.totals ?? {}
  const accounts = data.accounts ?? []

  return (
    <Page
      title="Overview"
      subtitle={`Up ${Math.round((data.uptime_s ?? 0) / 60)} min · ${num(data.catalog?.models)} models`}
      actions={
        <Button
          variant="outline"
          size="sm"
          disabled={busy}
          onClick={async () => {
            setBusy(true)
            try {
              await api('catalog/refresh', { method: 'POST' })
              toast.push('Catalog refreshed')
              await load()
            } catch (e) {
              toast.push((e as Error).message, 'bad')
            } finally {
              setBusy(false)
            }
          }}
        >
          <RefreshCw className={busy ? 'animate-spin' : ''} />
          Refresh catalog
        </Button>
      }
    >
      {(data.alerts ?? []).length > 0 && (
        <div className="mb-6 space-y-2">
          {data.alerts.map((a, i) => (
            <div
              key={i}
              className={
                a.level === 'bad'
                  ? 'flex items-center gap-3 rounded-lg border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger-text'
                  : a.level === 'warn'
                    ? 'flex items-center gap-3 rounded-lg border border-warning/30 bg-warning-soft px-4 py-3 text-sm text-warning-text'
                    : 'flex items-center gap-3 rounded-lg border border-border bg-surface px-4 py-3 text-sm text-text-secondary'
              }
            >
              {a.level === 'info' ? (
                <CheckCircle2 className="size-4 shrink-0" />
              ) : (
                <AlertTriangle className="size-4 shrink-0" />
              )}
              <span className="min-w-0 flex-1">{a.text}</span>
              {a.client && (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => nav(`/clients/${encodeURIComponent(a.client ?? '')}`)}
                >
                  Open
                </Button>
              )}
            </div>
          ))}
        </div>
      )}

      <StatRow>
        <Stat icon={Zap} tone="primary" value={num(t.requests)} label="requests in 24 h" />
        <Stat
          icon={AlertTriangle}
          tone={t.errors ? 'warning' : 'success'}
          value={t.requests ? ((t.errors ?? 0) / t.requests * 100).toFixed(1) + '%' : '0%'}
          label="failed in 24 h"
        />
        <Stat icon={Coins} tone="accent" value={'Rp ' + idr(t.cost_idr)} label="at list price, 24 h" />
        <Stat
          icon={Wallet}
          tone="success"
          value={num(Math.round((t.rtk_saved ?? 0) / 1000)) + ' k'}
          label="characters saved by RTK"
        />
      </StatRow>

      <div className="grid gap-4 lg:grid-cols-5">
        <Card className="lg:col-span-3">
          <CardHeader>
            <CardTitle>Requests in the last 24 hours</CardTitle>
          </CardHeader>
          <CardContent>
            <Chart data={usage?.series ?? []} />
          </CardContent>
        </Card>

        <div className="space-y-4 lg:col-span-2">
          {(data.providers ?? []).map((p) => (
            <PlanCard
              key={p.id}
              provider={p}
              account={accounts.find((a) => a.provider === p.id)}
              multi={(data.providers ?? []).length > 1}
            />
          ))}
        </div>
      </div>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle>Clients</CardTitle>
        </CardHeader>
        {(data.clients ?? []).length === 0 ? (
          <Empty>No clients yet. Add one to start routing.</Empty>
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Client</TH>
                <TH>Keys &amp; presets</TH>
                <TH className="text-right">Requests 24 h</TH>
                <TH className="text-right">Cost 24 h</TH>
                <TH />
              </TR>
            </THead>
            <TBody>
              {data.clients.map((c) => (
                <TR key={c.name} clickable onClick={() => nav(`/clients/${encodeURIComponent(c.name)}`)}>
                  <TD className="font-medium">
                    <span className="flex items-center gap-2">
                      {c.name}
                      {!c.enabled && <Badge tone="danger">disabled</Badge>}
                    </span>
                  </TD>
                  <TD className="text-text-secondary">
                    {pl(c.keys.length, 'key')} · {pl(c.presets.length, 'preset')}
                  </TD>
                  <TD className="tabular text-right">{num(c.requests_24h)}</TD>
                  <TD className="tabular text-right">Rp {idr(c.cost_24h)}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>
      <ProvidersCard data={data} reload={load} />
    </Page>
  )
}

/** PlanCard is one provider's plan windows and balance. A provider with no key yet says so instead. */
function PlanCard({ provider, account, multi }: { provider: ProviderOption; account?: Account; multi: boolean }) {
  const live = account?.plan_source === 'api'
  return (
    <Card>
      <CardHeader>
        <CardTitle>{multi ? `${provider.id} plan & balance` : 'Plan & balance'}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {account ? (
          <>
            <div className="flex items-center gap-2">
              <span className="text-sm font-medium">{account.plan || 'No plan'}</span>
              <Badge tone={live ? 'success' : 'warning'}>{live ? 'live' : 'estimated'}</Badge>
            </div>
            {account.windows && Object.keys(account.windows).length > 0 ? (
              WINDOW_ORDER.filter((n) => account.windows?.[n]).map((name) => {
                const w = account.windows![name]
                const pct = w.budget_rp > 0 ? Math.min(100, (w.used_rp / w.budget_rp) * 100) : 0
                return (
                  <div key={name}>
                    <div className="mb-1 flex justify-between text-xs">
                      <span className="text-text-secondary">{WINDOW_LABEL[name] ?? name}</span>
                      <span className="tabular text-text-muted">
                        Rp {idr(w.used_rp)} of {idr(w.budget_rp)}
                      </span>
                    </div>
                    <div className="h-1.5 overflow-hidden rounded-full bg-surface-muted">
                      <div
                        className={pct > 97 ? 'h-full bg-danger' : pct > 80 ? 'h-full bg-warning' : 'h-full bg-primary'}
                        style={{ width: `${pct}%` }}
                      />
                    </div>
                    {w.resets_at && !w.resets_at.startsWith('0001') && (
                      <div className="mt-1 text-xs text-text-muted">Resets {left(w.resets_at)}</div>
                    )}
                  </div>
                )
              })
            ) : (
              <p className="text-xs text-text-muted">
                {provider.id} has not reported the plan windows yet. Add its account key (Providers, below) to read them.
              </p>
            )}
            {account.balance_idr !== undefined && (
              <div className="flex justify-between border-t border-border pt-3 text-xs">
                <span className="text-text-secondary">Balance</span>
                <span className="tabular font-medium">Rp {idr(account.balance_idr)}</span>
              </div>
            )}
          </>
        ) : (
          <p className="text-xs text-text-muted">No key at {provider.id} yet. Add one to a client to read plan and balance.</p>
        )}
      </CardContent>
    </Card>
  )
}
