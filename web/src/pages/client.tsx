import * as React from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Activity, Copy, KeyRound, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { api } from '@/lib/api'
import type { ClientDetail, Category } from '@/lib/types'
import { ago, left, num, pl } from '@/lib/format'
import { Badge, Dot } from '@/components/ui/badge'
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
import { Table, TBody, TD, TH, THead, TR, Empty } from '@/components/ui/table'
import { Page } from '@/components/shell'
import { useToast } from '@/components/ui/toast'
import { PresetCard } from '@/pages/preset'

/** ClientDetail is one client: its purpose, its keys, and its presets. */
export function ClientDetailPage() {
  const { name = '' } = useParams()
  const [d, setD] = React.useState<ClientDetail | null>(null)
  const [adding, setAdding] = React.useState<Category | null>(null)
  const [addKeyOpen, setAddKeyOpen] = React.useState(false)
  const [renameKeyName, setRenameKeyName] = React.useState<string | null>(null)
  const [newKey, setNewKey] = React.useState<{ client: string; key: string } | null>(null)
  const nav = useNavigate()
  const toast = useToast()

  const load = React.useCallback(async () => {
    setD(await api<ClientDetail>(`client?name=${encodeURIComponent(name)}`))
  }, [name])

  React.useEffect(() => {
    load().catch((e) => toast.push(e.message, 'bad'))
  }, [load, toast])

  if (!d) return <Page title={name}>Loading…</Page>

  const have = new Set(d.presets.map((p) => p.category))
  const missing = d.categories.filter((c) => !have.has(c.category))

  return (
    <Page
      title={
        <span className="flex items-center gap-2">
          {d.client.name}
          {!d.client.enabled && <Badge tone="danger">disabled</Badge>}
        </span>
      }
      subtitle={`${pl(d.keys.length, 'kenari key')} · ${pl(d.presets.length, 'preset')} · xellter key ${d.client.key_hint || 'none'}`}
      actions={
        <>
          <Button variant="outline" size="sm" onClick={() => nav(`/usage?client=${encodeURIComponent(d.client.name)}`)}>
            <Activity />
            Usage
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={async () => {
              if (!confirm(`Create a new key for ${d.client.name}? The old key stops working at once.`)) return
              try {
                const r = await api<{ key: string }>('clients', {
                  method: 'POST',
                  body: { action: 'rotate', name: d.client.name },
                })
                setNewKey({ client: d.client.name, key: r.key })
              } catch (e) {
                toast.push((e as Error).message, 'bad')
              }
            }}
          >
            <KeyRound />
            New xellter key
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={async () => {
              try {
                await api(`recompute?client=${encodeURIComponent(d.client.name)}`, { method: 'POST' })
                toast.push('Rankings recalculated')
                await load()
              } catch (e) {
                toast.push((e as Error).message, 'bad')
              }
            }}
          >
            <RefreshCw />
            Recalculate
          </Button>
        </>
      }
    >
      <div className="grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle>kenari API keys</CardTitle>
            <Button variant="outline" size="sm" onClick={() => setAddKeyOpen(true)}>
              <Plus />
              Add key
            </Button>
          </CardHeader>
          {d.keys.length === 0 ? (
            <Empty>No kenari key yet. Add one; xellter asks kenari which models it may call.</Empty>
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Key</TH>
                  <TH>Hint</TH>
                  <TH>State</TH>
                  <TH>Can use</TH>
                  <TH />
                </TR>
              </THead>
              <TBody>
                {d.keys.map((k) => {
                  const state =
                    k.state === 'ready'
                      ? ['success', 'Ready']
                      : k.state === 'paused'
                        ? ['warning', 'Paused ' + (left(k.cooldown_until) || left(k.breaker_until) || 'a moment')]
                        : k.state === 'unchecked'
                          ? ['primary', 'Not checked yet']
                          : ['danger', 'No key value']
                  return (
                    <TR key={k.name}>
                      <TD>
                        <div className="font-medium">{k.name}</div>
                        {k.last_error && <div className="mt-0.5 text-xs text-danger-text">{k.last_error}</div>}
                      </TD>
                      <TD className="mono text-text-muted">{k.hint || '—'}</TD>
                      <TD>
                        <span className="flex items-center gap-2 text-sm">
                          <Dot tone={state[0] as 'success'} />
                          {state[1]}
                        </span>
                      </TD>
                      <TD className="text-text-secondary">
                        {k.unrestricted ? 'All models' : k.allowed ? `${num(k.allowed)} models` : k.probed ? 'none' : '—'}
                        {k.blocked > 0 && <span className="ml-2 text-xs text-warning-text">{k.blocked} blocked now</span>}
                      </TD>
                      <TD className="text-right">
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            title="Rename key"
                            onClick={() => setRenameKeyName(k.name)}
                          >
                            <Pencil />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            title="Remove key"
                            onClick={async () => {
                              if (!confirm(`Remove key “${k.name}” from ${d.client.name}? It stays valid at kenari.`)) return
                              try {
                                await api('keys', {
                                  method: 'POST',
                                  body: { client: d.client.name, action: 'remove', name: k.name },
                                })
                                toast.push('Key removed')
                                await load()
                              } catch (e) {
                                toast.push((e as Error).message, 'bad')
                              }
                            }}
                          >
                            <Trash2 />
                          </Button>
                        </div>
                      </TD>
                    </TR>
                  )
                })}
              </TBody>
            </Table>
          )}
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Settings</CardTitle>
          </CardHeader>
          <CardContent className="space-y-5">
            <Field label="Purpose" hint="How this client's text preset is scored.">
              <Select
                value={d.client.purpose}
                onValueChange={async (purpose) => {
                  try {
                    await api('clients', {
                      method: 'POST',
                      body: { action: 'update', name: d.client.name, purpose, enabled: d.client.enabled, note: d.client.note },
                    })
                    toast.push('Purpose saved')
                    await load()
                  } catch (e) {
                    toast.push((e as Error).message, 'bad')
                  }
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {d.purposes.map((p) => (
                    <SelectItem key={p.value} value={p.value}>
                      {p.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            <div className="flex items-center justify-between">
              <div>
                <div className="text-xs font-medium text-text-secondary">Serving</div>
                <div className="text-xs text-text-muted">
                  {d.client.enabled ? 'This client is served' : 'This client is refused'}
                </div>
              </div>
              <Switch
                checked={d.client.enabled}
                onCheckedChange={async (enabled) => {
                  try {
                    await api('clients', {
                      method: 'POST',
                      body: { action: 'update', name: d.client.name, purpose: d.client.purpose, enabled, note: d.client.note },
                    })
                    toast.push(enabled ? 'Client enabled' : 'Client disabled')
                    await load()
                  } catch (e) {
                    toast.push((e as Error).message, 'bad')
                  }
                }}
              />
            </div>

            <div className="border-t border-border pt-4">
              <div className="text-xs font-medium text-text-secondary">Rankings</div>
              <div className="mt-1 text-xs text-text-muted">
                {d.computed_at && !d.computed_at.startsWith('0001')
                  ? 'computed ' + ago(d.computed_at)
                  : 'not computed yet'}
              </div>
              <div className="mt-1 text-xs text-text-muted">
                Refreshed every {d.routing.rank_refresh_minutes || '—'} min, after every catalog sync, and by Recalculate.
              </div>
            </div>
          </CardContent>
        </Card>
      </div>

      <div className="mt-8 mb-3 flex items-center justify-between">
        <h2 className="text-sm font-semibold tracking-tight text-text">Presets</h2>
        {missing.length > 0 && (
          <Select onValueChange={(c) => setAdding(c as Category)}>
            <SelectTrigger className="w-auto" aria-label="Add a preset">
              <span className="flex items-center gap-1.5">
                <Plus className="size-4" />
                Add preset
              </span>
            </SelectTrigger>
            <SelectContent>
              {missing.map((c) => (
                <SelectItem key={c.category} value={c.category}>
                  {c.label} ({c.preset})
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </div>
      <p className="mb-4 text-sm text-text-muted">
        A preset is the model name an app asks for. Behind it is the pool you pick, ordered by the optimization
        you choose. Every free and discounted model of the category is in the pool too, and leaves it again when
        its promo ends.
      </p>

      {d.presets.length === 0 ? (
        <Card>
          <Empty>No presets yet. Add one for each category this client uses.</Empty>
        </Card>
      ) : (
        <div className="grid gap-4 xl:grid-cols-2">
          {d.presets.map((p) => (
            <PresetCard key={p.name} client={d.client.name} preset={p} onChanged={load} />
          ))}
        </div>
      )}

      {adding && (
        <AddPresetDialog
          client={d.client.name}
          category={adding}
          onClose={() => setAdding(null)}
          onDone={async () => {
            setAdding(null)
            toast.push('Preset added')
            await load()
          }}
        />
      )}
      {addKeyOpen && (
        <AddKeyDialog
          client={d.client.name}
          onClose={() => setAddKeyOpen(false)}
          onDone={async () => {
            setAddKeyOpen(false)
            toast.push('Key added; asking kenari which models it can use…')
            await load()
          }}
        />
      )}
      {renameKeyName !== null && (
        <RenameKeyDialog
          client={d.client.name}
          name={renameKeyName}
          onClose={() => setRenameKeyName(null)}
          onDone={async () => {
            setRenameKeyName(null)
            toast.push('Key renamed')
            await load()
          }}
        />
      )}
      {newKey && (
        <KeyDialog
          client={newKey.client}
          apiKey={newKey.key}
          onClose={() => {
            setNewKey(null)
            load().catch(() => {})
          }}
        />
      )}
    </Page>
  )
}

// The three small dialogs below are mounted from the page above; the state
// lives in the page so the body stays readable.

/** AddPresetDialog creates a preset with the owner's own name. */
function AddPresetDialog({
  client,
  category,
  onClose,
  onDone,
}: {
  client: string
  category: Category
  onClose: () => void
  onDone: () => void
}) {
  const [name, setName] = React.useState('')
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')

  async function create() {
    setBusy(true)
    setError('')
    try {
      await api('presets', {
        method: 'POST',
        body: { client, action: 'save', preset: name.trim(), category, optimization: 'balanced', models: [] },
      })
      onDone()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New preset</DialogTitle>
          <DialogDescription>
            The name is the model your app asks for. A client holds one preset per category.
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <Field label="Preset name" htmlFor="preset-name" hint="Letters, digits, dot, dash and underscore.">
            <Input
              id="preset-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. my-fast-text"
              autoFocus
            />
          </Field>
          <Field label="Category">
            <Input value={category} disabled />
          </Field>
          {error && <p className="text-sm text-danger-text">{error}</p>}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={busy || !name.trim()} onClick={create}>
            {busy ? 'Adding…' : 'Add preset'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** AddKeyDialog adds a kenari key to a client. */
function AddKeyDialog({
  client,
  onClose,
  onDone,
}: {
  client: string
  onClose: () => void
  onDone: () => void
}) {
  const [name, setName] = React.useState('main')
  const [key, setKey] = React.useState('')
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')

  async function create() {
    setBusy(true)
    setError('')
    try {
      await api('keys', { method: 'POST', body: { client, action: 'add', name, key } })
      onDone()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add a kenari key</DialogTitle>
          <DialogDescription>
            Paste a key from the kenari dashboard. It is stored on this server only, never in the config file.
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <Field label="Name" htmlFor="key-name">
            <Input id="key-name" value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field label="API key" htmlFor="key-value">
            <Input
              id="key-value"
              type="password"
              value={key}
              placeholder="kn-…"
              autoComplete="off"
              onChange={(e) => setKey(e.target.value)}
            />
          </Field>
          {error && <p className="text-sm text-danger-text">{error}</p>}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={busy || !key.trim()} onClick={create}>
            {busy ? 'Adding…' : 'Add key'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** RenameKeyDialog renames the one thing a key has besides its value. */
function RenameKeyDialog({
  client,
  name,
  onClose,
  onDone,
}: {
  client: string
  name: string
  onClose: () => void
  onDone: () => void
}) {
  const [value, setValue] = React.useState(name)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Rename key “{name}”</DialogTitle>
          <DialogDescription>
            The name is how you recognise the key here. xellter detects everything else by itself.
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <Field label="Name" htmlFor="rename-key">
            <Input id="rename-key" value={value} onChange={(e) => setValue(e.target.value)} autoFocus />
          </Field>
          {error && <p className="text-sm text-danger-text">{error}</p>}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={busy || !value.trim()}
            onClick={async () => {
              setBusy(true)
              setError('')
              try {
                await api('keys', { method: 'POST', body: { client, action: 'rename', name, new_name: value } })
                onDone()
              } catch (e) {
                setError((e as Error).message)
                setBusy(false)
              }
            }}
          >
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** KeyDialog shows an xellter key once, with a copy button. */
export function KeyDialog({ client, apiKey, onClose }: { client: string; apiKey: string; onClose: () => void }) {
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>xellter key for {client}</DialogTitle>
          <DialogDescription>
            Copy it now; it is not stored and cannot be shown again. The app uses it as its API key with the base
            URL <span className="mono">{location.origin}/v1</span>.
          </DialogDescription>
        </DialogHeader>
        <DialogBody>
          <div className="select-all break-all rounded-md border border-dashed border-primary bg-primary-soft px-3 py-2.5 font-mono text-sm">
            {apiKey}
          </div>
        </DialogBody>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() =>
              navigator.clipboard
                ?.writeText(apiKey)
                .then(() => {})
                .catch(() => {})
            }
          >
            <Copy />
            Copy
          </Button>
          <Button variant="primary" onClick={onClose}>
            Done
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
