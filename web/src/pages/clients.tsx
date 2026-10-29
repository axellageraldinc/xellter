import * as React from 'react'
import { useNavigate } from 'react-router-dom'
import { KeyRound, Plus, Users } from 'lucide-react'
import { api } from '@/lib/api'
import type { ClientRow } from '@/lib/types'
import { num, pl } from '@/lib/format'
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
import { Table, TBody, TD, TH, THead, TR, Empty } from '@/components/ui/table'
import { Page } from '@/components/shell'
import { useToast } from '@/components/ui/toast'
import { KeyDialog } from '@/pages/client'

type ClientsPayload = { clients: ClientRow[]; purposes: { value: string; label: string }[] }

/** Clients lists the apps that call xellter. */
export function ClientsPage() {
  const [data, setData] = React.useState<ClientsPayload | null>(null)
  const [adding, setAdding] = React.useState(false)
  const [newKey, setNewKey] = React.useState<{ client: string; key: string } | null>(null)
  const nav = useNavigate()
  const toast = useToast()

  const load = React.useCallback(async () => {
    setData(await api<ClientsPayload>('clients'))
  }, [])

  React.useEffect(() => {
    load().catch((e) => toast.push(e.message, 'bad'))
  }, [load, toast])

  return (
    <Page
      title="Clients"
      subtitle="The apps that call xellter. Each one has its own keys and its own presets."
      actions={
        <Button variant="primary" size="sm" onClick={() => setAdding(true)}>
          <Plus />
          New client
        </Button>
      }
    >
      <Card>
        <CardHeader>
          <CardTitle>All clients</CardTitle>
        </CardHeader>
        {!data ? (
          <CardContent className="py-10 text-center text-sm text-text-muted">Loading…</CardContent>
        ) : data.clients.length === 0 ? (
          <Empty>
            No clients yet. A client is one app, for example “my-app” or “editor”.
          </Empty>
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Client</TH>
                <TH>Purpose</TH>
                <TH>Keys</TH>
                <TH>Presets</TH>
                <TH>Serving</TH>
                <TH />
              </TR>
            </THead>
            <TBody>
              {data.clients.map((c) => (
                <TR key={c.name} clickable onClick={() => nav(`/clients/${encodeURIComponent(c.name)}`)}>
                  <TD>
                    <div className="flex items-center gap-2">
                      <span className="font-medium">{c.name}</span>
                      {!c.enabled && <Badge tone="danger">disabled</Badge>}
                    </div>
                  </TD>
                  <TD className="text-text-secondary">{c.purpose_label || c.purpose}</TD>
                  <TD className="text-text-secondary">
                    <span className="flex items-center gap-1.5">
                      <KeyRound className="size-3.5 text-text-muted" />
                      {num(c.keys.length)}
                    </span>
                  </TD>
                  <TD className="text-text-secondary">{pl(c.presets.length, 'preset')}</TD>
                  <TD className="text-text-secondary">
                    {c.presets.length ? (
                      <div className="space-y-0.5">
                        {c.presets.map((p) => (
                          <div key={p.name} className="flex items-center gap-2 text-xs">
                            <span className="font-medium text-text">{p.name}</span>
                            <span className="tabular text-text-muted">
                              {p.serving} serving
                            </span>
                          </div>
                        ))}
                      </div>
                    ) : (
                      <span className="text-text-muted">none</span>
                    )}
                  </TD>
                  <TD className="text-right">
                    <Users className="ml-auto size-4 text-text-muted" />
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      {adding && data && (
        <AddClientDialog
          purposes={data.purposes}
          onClose={() => setAdding(false)}
          onDone={async (name, key) => {
            setAdding(false)
            toast.push(`Client ${name} created`)
            // The key is shown once, so it comes before the navigation: the
            // dialog belongs to this page, and the page goes away when the
            // client page opens.
            setNewKey({ client: name, key })
          }}
        />
      )}

      {newKey && (
        <KeyDialog
          client={newKey.client}
          apiKey={newKey.key}
          onClose={() => {
            const c = newKey.client
            setNewKey(null)
            nav(`/clients/${encodeURIComponent(c)}`)
          }}
        />
      )}
    </Page>
  )
}

/** AddClientDialog makes a client and shows the key it gets, once. */
function AddClientDialog({
  purposes,
  onClose,
  onDone,
}: {
  purposes: { value: string; label: string }[]
  onClose: () => void
  onDone: (name: string, key: string) => void
}) {
  const [name, setName] = React.useState('')
  const [purpose, setPurpose] = React.useState('general')
  const [kenariKey, setKenariKey] = React.useState('')
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')

  async function create() {
    setBusy(true)
    setError('')
    try {
      const r = await api<{ key: string }>('clients', {
        method: 'POST',
        body: { action: 'create', name: name.trim(), purpose, key: kenariKey },
      })
      onDone(name.trim().replace(/\s+/g, '-'), r.key)
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
          <DialogTitle>New client</DialogTitle>
          <DialogDescription>
            A client is one app. You get an xellter key for it next; the key is shown once.
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <Field label="Name" htmlFor="client-name" hint="Letters, digits, dot, dash and underscore.">
            <Input
              id="client-name"
              value={name}
              placeholder="e.g. my-app"
              onChange={(e) => setName(e.target.value)}
              autoFocus
            />
          </Field>
          <Field
            label="Purpose"
            hint="How this client's text preset is scored. It reorders the pool; it never changes which models are in it."
          >
            <Select value={purpose} onValueChange={setPurpose}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {purposes.map((p) => (
                  <SelectItem key={p.value} value={p.value}>
                    {p.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label="API key" hint="Optional. Stored on this server only, never in the config file.">
            <Input
              type="password"
              value={kenariKey}
              placeholder="kn-…"
              autoComplete="off"
              onChange={(e) => setKenariKey(e.target.value)}
            />
          </Field>
          {error && <p className="text-sm text-danger-text">{error}</p>}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={busy || !name.trim()} onClick={create}>
            {busy ? 'Creating…' : 'Create client'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

