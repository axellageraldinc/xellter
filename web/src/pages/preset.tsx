import * as React from 'react'
import { Check, ChevronDown, Pencil, Search, Sparkles, Trash2 } from 'lucide-react'
import { api } from '@/lib/api'
import type { CatalogRow, PresetView } from '@/lib/types'
import { ctx, rp, secs } from '@/lib/format'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { useToast } from '@/components/ui/toast'
import { BillingBadge } from '@/components/billing-badge'
import { OutHistory, OutOfServiceList } from '@/components/model-health'

const OPTIMIZATIONS = [
  { value: 'cheapest', label: 'Cheapest', help: 'Price only: input, output, cache and promos.' },
  { value: 'balanced', label: 'Balanced', help: 'Half price, half quality from the public leaderboards.' },
  { value: 'best_quality', label: 'Best Quality', help: 'The benchmark of this category only.' },
]

const KIND_ICON: Record<string, string> = {
  chat: 'T',
  images: 'I',
  videos: 'V',
  audio_speech: 'S',
  audio_transcription: 'R',
  music: 'M',
  systemone: 'O',
}

/**
 * PresetCard is one preset: its name, its optimization, the models it serves
 * right now, and the pool behind it.
 */
export function PresetCard({
  client,
  preset,
  onChanged,
}: {
  client: string
  preset: PresetView
  onChanged: () => void
}) {
  const [optimization, setOptimization] = React.useState(preset.optimization)
  const [models, setModels] = React.useState<string[]>(preset.models)
  const [excluded, setExcluded] = React.useState<string[]>(preset.excluded ?? [])
  const [sticky, setSticky] = React.useState(preset.sticky)
  const [picking, setPicking] = React.useState(false)
  const [renaming, setRenaming] = React.useState(false)
  const toast = useToast()

  const dirty =
    optimization !== preset.optimization ||
    sticky !== preset.sticky ||
    [...excluded].sort().join('\n') !== [...(preset.excluded ?? [])].sort().join('\n') ||
    models.length !== preset.models.length ||
    [...models].sort().join('\n') !== [...preset.models].sort().join('\n')
  const ranking = preset.ranking
  const outNow = new Set((preset.out_of_service ?? []).map((o) => o.model))
  // What is routed right now: the top list without the models that are out, each one replaced by the next
  // reserve model, as the router does (ADR 0020). The out models are listed under "Out for now".
  const candidates = React.useMemo(() => {
    const top = ranking?.candidates ?? []
    const live = top.filter((c) => !outNow.has(c.model))
    const fill = (ranking?.reserve ?? []).filter((c) => !outNow.has(c.model)).slice(0, top.length - live.length)
    return [...live, ...fill]
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ranking, preset.out_of_service])

  // The card follows the server when it changes underneath it.
  React.useEffect(() => {
    setOptimization(preset.optimization)
    setModels(preset.models)
    setSticky(preset.sticky)
    setExcluded(preset.excluded ?? [])
  }, [preset.optimization, preset.models, preset.sticky, preset.excluded])

  async function save() {
    try {
      await api('presets', {
        method: 'POST',
        body: {
          client,
          action: 'save',
          preset: preset.name,
          category: preset.category,
          optimization,
          models,
          excluded,
          sticky,
        },
      })
      toast.push('Preset saved')
      onChanged()
    } catch (e) {
      toast.push((e as Error).message, 'bad')
    }
  }

  async function remove() {
    if (!confirm(`Delete preset “${preset.name}” of ${client}? Apps asking for it get an error until you add it again.`))
      return
    try {
      await api('presets', { method: 'POST', body: { client, action: 'delete', preset: preset.name } })
      toast.push('Preset deleted')
      onChanged()
    } catch (e) {
      toast.push((e as Error).message, 'bad')
    }
  }

  return (
    <Card className="flex h-full min-w-0 flex-col">
      <CardHeader className="items-start">
        <div className="flex min-w-0 items-start gap-3">
          <span
            className="flex size-8 shrink-0 items-center justify-center rounded-md bg-primary-soft text-xs font-semibold text-primary-text"
            aria-hidden
          >
            {KIND_ICON[preset.category] ?? 'P'}
          </span>
          <div className="min-w-0">
            <CardTitle className="flex items-center gap-2">
              <span className="truncate">{preset.name}</span>
              {dirty && <Badge tone="warning">unsaved</Badge>}
            </CardTitle>
            <div className="mt-0.5 text-xs text-text-muted">
              {preset.label} · {models.length} in the pool
              {preset.auto_models.length > 0 && ` · ${preset.auto_models.length} added automatically`} ·{' '}
              {candidates.length} serving
            </div>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Button variant="ghost" size="icon-sm" title="Rename this preset" onClick={() => setRenaming(true)}>
            <Pencil />
          </Button>
          <Button variant="ghost" size="icon-sm" title="Delete this preset" onClick={remove}>
            <Trash2 />
          </Button>
        </div>
      </CardHeader>

      <CardContent className="flex min-h-0 flex-1 flex-col gap-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Optimization" hint={OPTIMIZATIONS.find((o) => o.value === optimization)?.help}>
            <Select
              value={optimization}
              onValueChange={(v) => setOptimization(v as typeof optimization)}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {OPTIMIZATIONS.map((o) => (
                  <SelectItem key={o.value} value={o.value}>
                    {o.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label="Pool" hint="Every model you tick is in the pool; the optimization only orders it.">
            <div className="flex items-center gap-2">
              <Button variant="outline" size="sm" className="flex-1" onClick={() => setPicking(true)}>
                Choose models
              </Button>
              <Button variant="primary" size="sm" disabled={!dirty} onClick={save}>
                <Check />
                Save
              </Button>
            </div>
          </Field>
        </div>

        {/* One model per session, or a fresh one on every turn. */}
        <div className="flex items-start justify-between gap-4 rounded-md border border-border bg-surface-muted/40 px-3 py-2.5">
          <div className="min-w-0">
            <div className="text-xs font-medium text-text-secondary">Sticky session</div>
            <div className="mt-0.5 text-xs text-text-muted">
              {sticky
                ? 'A session keeps one model for its whole life, so the provider cache stays warm.'
                : 'Every turn is routed again, so the traffic spreads over the whole pool.'}
            </div>
          </div>
          <Switch
            checked={sticky}
            onCheckedChange={setSticky}
            aria-label="Sticky session"
            title={sticky ? 'Sticky session is on' : 'Sticky session is off'}
          />
        </div>

        {/* The models that are free or discounted and that xellter put in the
            pool by itself. They come and go on their own. */}
        {(preset.auto_models.length > 0 || preset.excluded_models?.length > 0) && (
          <div className="rounded-md border border-accent/25 bg-accent-soft/50 px-3 py-2.5">
            <div className="flex items-center gap-2">
              <Sparkles className="size-3.5 shrink-0 text-accent-text" />
              <span className="text-xs font-medium text-accent-text">
                {preset.auto_models.length} free or discounted model
                {preset.auto_models.length === 1 ? '' : 's'} added automatically
              </span>
            </div>
            <div className="mt-1.5 flex flex-wrap gap-1">
              {preset.auto_models.map((m) => (
                <span key={m} className="mono rounded-sm bg-surface px-1.5 py-0.5 text-[11px] text-text-secondary">
                  {m}
                </span>
              ))}
            </div>
            {preset.excluded_models?.length > 0 && (
              <div className="mt-2 border-t border-accent/20 pt-2">
                <div className="text-xs text-text-muted">
                  {preset.excluded_models.length} removed on purpose, so they are not added again:
                </div>
                <div className="mt-1.5 flex flex-wrap gap-1">
                  {preset.excluded_models.map((m) => (
                    <span key={m} className="mono rounded-sm bg-surface px-1.5 py-0.5 text-[11px] text-text-muted line-through">
                      {m}
                    </span>
                  ))}
                </div>
              </div>
            )}
          </div>
        )}

        <div>
          <div className="mb-2 text-xs font-semibold tracking-wide text-text-muted uppercase">
            Serving now
          </div>
          {candidates.length === 0 ? (
            <p className="text-sm text-danger-text">Nothing available right now.</p>
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH className="w-8">#</TH>
                  <TH>Model</TH>
                  <TH>Billing</TH>
                  <TH className="text-right">Score</TH>
                  <TH>Last 24 h</TH>
                  <TH>Why it is here</TH>
                </TR>
              </THead>
              <TBody>
                {candidates.map((c, i) => (
                  <TR key={c.model}>
                    <TD className="tabular text-text-muted">{i + 1}</TD>
                    <TD className="mono">
                      {c.model}
                    </TD>
                    <TD>
                      <BillingBadge billing={c.billing} />
                    </TD>
                    <TD className="tabular text-right">{c.score.toFixed(3)}</TD>
                    <TD className="tabular text-xs whitespace-nowrap text-text-muted" data-testid="model-stat">
                      {(() => {
                        const st = preset.stats?.[c.model]
                        if (!st) return 'no traffic'
                        return (
                          <span title="Median wait for the first token of the streamed answers, share of failed requests and requests, last 24 hours">
                            <span className="font-medium text-text">{st.ttft_p50_ms ? secs(st.ttft_p50_ms) : 'no stream'}</span>
                            <br />
                            {st.fail_pct}% failed · {st.requests} req
                          </span>
                        )
                      })()}
                    </TD>
                    <TD className="max-w-[12rem] truncate text-xs text-text-muted" title={c.reason}>{c.reason}</TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </div>

        <OutOfServiceList items={preset.out_of_service} />
        <OutHistory events={preset.history} />

        {(ranking?.rejected?.length ?? 0) > 0 && (
          <details className="group">
            <summary className="cursor-pointer list-none text-xs font-semibold tracking-wide text-text-muted uppercase">
              Left out ({ranking?.rejected?.length ?? 0})
            </summary>
            <div className="mt-2 space-y-1.5">
              {groupReasons(ranking?.rejected ?? []).map(([why, models]) => (
                <div key={why} className="flex gap-2 text-xs">
                  <span className="tabular shrink-0 rounded-sm bg-surface-muted px-1.5 py-0.5 text-text-secondary">
                    {models.length}
                  </span>
                  <span className="text-text-secondary">{why}</span>
                </div>
              ))}
            </div>
          </details>
        )}
      </CardContent>

      {picking && (
        <ModelPicker
          client={client}
          preset={preset}
          selected={models}
          excluded={excluded}
          onClose={() => setPicking(false)}
          onSave={(next, nextExcluded) => {
            setModels(next)
            setExcluded(nextExcluded)
            setPicking(false)
          }}
        />
      )}

      {renaming && (
        <RenamePresetDialog
          client={client}
          preset={preset.name}
          onClose={() => setRenaming(false)}
          onDone={async (newName) => {
            setRenaming(false)
            toast.push('Preset renamed')
            await onChanged()
            return newName
          }}
        />
      )}
    </Card>
  )
}

/** ModelPicker is the catalog as a checklist: the family, then the models. */
function ModelPicker({
  client,
  preset,
  selected,
  excluded,
  onClose,
  onSave,
}: {
  client: string
  preset: PresetView
  selected: string[]
  excluded: string[]
  onClose: () => void
  onSave: (models: string[], excluded: string[]) => void
}) {
  const [rows, setRows] = React.useState<CatalogRow[] | null>(null)
  const [draft, setDraft] = React.useState<string[]>(selected)
  const [out, setOut] = React.useState<string[]>(excluded)
  // A model xellter adds on its own is ticked until the owner unticks it (ADR 0035).
  const auto = React.useMemo(() => new Set([...preset.auto_models, ...excluded]), [preset.auto_models, excluded])
  const isOn = (id: string) => draft.includes(id) || (auto.has(id) && !out.includes(id))
  const setMany = (ids: string[], on: boolean) => {
    setDraft((d) => {
      const rest = d.filter((x) => !ids.includes(x))
      return on ? [...rest, ...ids.filter((id) => !auto.has(id))] : rest
    })
    setOut((o) => {
      const rest = o.filter((x) => !ids.includes(x))
      return on ? rest : [...rest, ...ids.filter((id) => auto.has(id))]
    })
  }
  const [query, setQuery] = React.useState('')
  const [open, setOpen] = React.useState<Record<string, boolean>>({})
  const toast = useToast()

  React.useEffect(() => {
    api<{ models: CatalogRow[] }>(
      `catalog/models?client=${encodeURIComponent(client)}&category=${encodeURIComponent(preset.category)}`,
    )
      .then((r) => setRows(r.models))
      .catch((e) => toast.push(e.message, 'bad'))
  }, [client, preset.category, toast])

  const families = React.useMemo(() => {
    const map = new Map<string, CatalogRow[]>()
    for (const m of rows ?? []) {
      if (query && !m.id.toLowerCase().includes(query.toLowerCase())) continue
      const list = map.get(m.owner || 'other') ?? []
      list.push(m)
      map.set(m.owner || 'other', list)
    }
    return [...map.entries()].sort((a, b) => a[0].localeCompare(b[0]))
  }, [rows, query])

  const toggle = (id: string, on: boolean) => setMany([id], on)

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent wide>
        <DialogHeader>
          <DialogTitle>Choose the models of “{preset.name}”</DialogTitle>
          <DialogDescription>
            Free and discounted models of this category are ticked for you. Untick one you do not want and it stays
            out, even after the catalog syncs.
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-3">
          <div className="relative">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-text-muted" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search the models"
              className="pl-9"
            />
          </div>

          {!rows ? (
            <p className="py-8 text-center text-sm text-text-muted">Loading the catalog…</p>
          ) : families.length === 0 ? (
            <p className="py-8 text-center text-sm text-text-muted">kenari has no model of this kind.</p>
          ) : (
            <div className="space-y-2">
              {families.map(([owner, list]) => {
                const ids = list.map((m) => m.id)
                const picked = ids.filter(isOn).length
                const all = picked === ids.length
                return (
                  <div key={owner} className="rounded-md border border-border">
                    <button
                      className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm font-medium transition-colors hover:bg-surface-hover"
                      onClick={() => setOpen((o) => ({ ...o, [owner]: !o[owner] }))}
                    >
                      <ChevronDown className={cn('size-4 text-text-muted transition-transform', open[owner] && 'rotate-180')} />
                      <span>{owner}</span>
                      <span className="tabular ml-auto text-xs text-text-muted">
                        {picked}/{ids.length}
                      </span>
                      <span
                        role="checkbox"
                        aria-checked={all}
                        tabIndex={0}
                        onClick={(e) => {
                          e.stopPropagation()
                          setMany(ids, !all)
                        }}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter' || e.key === ' ') {
                            e.stopPropagation()
                            e.preventDefault()
                            setMany(ids, !all)
                          }
                        }}
                        className={cn(
                          'flex size-4 items-center justify-center rounded border transition-colors',
                          all ? 'border-primary bg-primary text-white' : 'border-border-strong',
                        )}
                        aria-label={`Select all ${owner} models`}
                      >
                        {all && <Check className="size-3" />}
                      </span>
                    </button>

                    {open[owner] && (
                      <div className="border-t border-border">
                        {list.map((m) => {
                          const on = isOn(m.id)
                          return (
                            <label
                              key={m.id}
                              className={cn(
                                'flex cursor-pointer items-center gap-3 border-b border-border-subtle px-3 py-2 text-xs last:border-0',
                                on && 'bg-primary-soft/40',
                              )}
                            >
                              <input
                                type="checkbox"
                                className="size-3.5 rounded border-border-strong accent-primary"
                                checked={on}
                                onChange={(e) => toggle(m.id, e.target.checked)}
                              />
                              <span className="mono min-w-0 flex-1 truncate">{m.id}</span>
                              {m.callable && <BillingBadge billing={m.billing} />}
                              {m.promo_off > 0 && <Badge tone="warning">promo −{Math.round(m.promo_off)}%</Badge>}
                              {!m.callable && <Badge tone="danger">no key may call it</Badge>}
                              <span className="tabular w-14 shrink-0 text-right text-text-muted">{ctx(m.context)}</span>
                              <span className="tabular w-28 shrink-0 text-right text-text-muted">
                                {priceLine(m)}
                              </span>
                            </label>
                          )
                        })}
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </DialogBody>
        <DialogFooter>
          <span className="mr-auto text-xs text-text-muted">{draft.length + [...auto].filter((id) => !out.includes(id) && !draft.includes(id)).length} models ticked</span>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => onSave(draft, out)}>
            Use these models
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** RenamePresetDialog renames the model name the app asks for. */
function RenamePresetDialog({
  client,
  preset,
  onClose,
  onDone,
}: {
  client: string
  preset: string
  onClose: () => void
  onDone: (newName: string) => void
}) {
  const [name, setName] = React.useState(preset)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Rename “{preset}”</DialogTitle>
          <DialogDescription>
            The name is the model your app asks for. Renaming it means the app must ask for the new name.
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <Field label="New name" htmlFor="rename-preset">
            <Input id="rename-preset" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>
          {error && <p className="text-sm text-danger-text">{error}</p>}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={busy || !name.trim() || name === preset}
            onClick={async () => {
              setBusy(true)
              setError('')
              try {
                await api('presets', {
                  method: 'POST',
                  body: { client, action: 'rename', preset, new_name: name.trim() },
                })
                onDone(name.trim())
              } catch (e) {
                setError((e as Error).message)
                setBusy(false)
              }
            }}
          >
            Rename
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** priceLine is the exact price line: in, out, cache read, cache write, in rupiah. */
function priceLine(m: CatalogRow): string {
  const unit = (m.lines ?? []).map((l) => `${rp(l.idr)}/${l.unit}`).join(' · ')
  if (!m.free && !m.in_idr && !m.out_idr && unit) return unit
  const p = (v: number) => (v > 0 ? rp(v) : m.free ? 'Rp0' : '—')
  return `in ${p(m.in_idr)} · out ${p(m.out_idr)}`
}

/** groupReasons groups the left-out models by the reason they were left out. */
function groupReasons(rejected: { model: string; why: string }[]): [string, string[]][] {
  const map = new Map<string, string[]>()
  for (const r of rejected) {
    const list = map.get(r.why) ?? []
    list.push(r.model)
    map.set(r.why, list)
  }
  return [...map.entries()].sort((a, b) => b[1].length - a[1].length)
}
