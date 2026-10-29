import * as React from 'react'
import { KeyRound, Pencil, Plus, Trash2 } from 'lucide-react'
import { api } from '@/lib/api'
import type { Overview } from '@/lib/types'
import { pl } from '@/lib/format'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle } from '@/components/ui/card'
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
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { useToast } from '@/components/ui/toast'

const BUILT_IN = 'kenari'

type Dlg = { kind: 'add' } | { kind: 'url'; id: string; url: string } | { kind: 'account'; id: string }

/**
 * ProvidersCard lists the upstreams: the built-in one and any added here. A provider is a name and a base URL;
 * its keys are added to a client, and an optional account key (full access, read-only use) shows its plan.
 */
export function ProvidersCard({ data, reload }: { data: Overview; reload: () => Promise<void> }) {
  const toast = useToast()
  const [dlg, setDlg] = React.useState<Dlg | null>(null)

  const call = async (path: string, body: Record<string, unknown>, ok: string) => {
    try {
      await api(path, { method: 'POST', body })
      toast.push(ok)
      setDlg(null)
      await reload()
    } catch (e) {
      toast.push((e as Error).message, 'bad')
    }
  }

  return (
    <Card className="mt-4">
      <CardHeader>
        <CardTitle>Providers</CardTitle>
        <Button variant="outline" size="sm" onClick={() => setDlg({ kind: 'add' })}>
          <Plus />
          Add provider
        </Button>
      </CardHeader>
      <Table>
        <THead>
          <TR>
            <TH>Provider</TH>
            <TH />
          </TR>
        </THead>
        <TBody>
          {data.providers.map((p) => {
            const ak = data.account_keys.find((k) => k.provider === p.id)
            return (
              <TR key={p.id}>
                <TD>
                  <div className="flex items-center gap-2 font-medium">
                    {p.id}
                    {ak?.configured && <Badge tone="success">account key</Badge>}
                  </div>
                  <div className="mono mt-0.5 max-w-[11rem] truncate text-xs text-text-muted sm:max-w-none">{p.base_url}</div>
                  <div className="mt-0.5 text-xs text-text-muted">
                    {pl(p.models, 'model')} · {pl(p.keys, 'key')}
                  </div>
                </TD>
                <TD className="text-right">
                  <div className="flex justify-end gap-1">
                    <Button variant="ghost" size="icon-sm" title="Edit address" onClick={() => setDlg({ kind: 'url', id: p.id, url: p.base_url })}>
                      <Pencil />
                    </Button>
                    <Button variant="ghost" size="icon-sm" title="Account key (reads plan and balance)" onClick={() => setDlg({ kind: 'account', id: p.id })}>
                      <KeyRound />
                    </Button>
                    {p.id !== BUILT_IN && (
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        title="Remove provider"
                        onClick={() => {
                          if (confirm(`Remove provider “${p.id}”? Its keys and ticked models must be removed first.`))
                            void call('providers', { action: 'remove', id: p.id }, 'Provider removed')
                        }}
                      >
                        <Trash2 />
                      </Button>
                    )}
                  </div>
                </TD>
              </TR>
            )
          })}
        </TBody>
      </Table>

      {dlg?.kind === 'add' && (
        <ProviderDialog
          title="Add a provider"
          description="A provider speaks kenari's API and may be any host. Its models are listed as <name>/<model>; add a key for it on a client."
          id=""
          url=""
          onClose={() => setDlg(null)}
          onSave={(id, url) => call('providers', { action: 'add', id, base_url: url }, 'Provider added')}
        />
      )}
      {dlg?.kind === 'url' && (
        <ProviderDialog
          title={`Address of ${dlg.id}`}
          description="The root URL of the provider's API."
          id={dlg.id}
          url={dlg.url}
          onClose={() => setDlg(null)}
          onSave={(id, url) => call('providers', { action: 'update', id, base_url: url }, 'Address saved')}
        />
      )}
      {dlg?.kind === 'account' && (
        <AccountKeyDialog
          provider={dlg.id}
          configured={!!data.account_keys.find((k) => k.provider === dlg.id)?.configured}
          onClose={() => setDlg(null)}
          onSave={(key) => call('account-key', { provider: dlg.id, key }, key ? 'Account key saved' : 'Account key removed')}
        />
      )}
    </Card>
  )
}

function ProviderDialog({
  title,
  description,
  id: id0,
  url: url0,
  onClose,
  onSave,
}: {
  title: string
  description: string
  id: string
  url: string
  onClose: () => void
  onSave: (id: string, url: string) => Promise<void>
}) {
  const [id, setId] = React.useState(id0)
  const [url, setUrl] = React.useState(url0)
  const [busy, setBusy] = React.useState(false)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          {!id0 && (
            <Field label="Name" hint="Lowercase letters, digits and dashes. It prefixes the provider's models." htmlFor="prov-id">
              <Input id="prov-id" value={id} onChange={(e) => setId(e.target.value.toLowerCase())} autoFocus />
            </Field>
          )}
          <Field label="Base URL" htmlFor="prov-url">
            <Input id="prov-url" value={url} placeholder="https://api.example.com" onChange={(e) => setUrl(e.target.value)} />
          </Field>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={busy || !id.trim() || !url.trim()}
            onClick={async () => {
              setBusy(true)
              await onSave(id.trim(), url.trim())
              setBusy(false)
            }}
          >
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function AccountKeyDialog({
  provider,
  configured,
  onClose,
  onSave,
}: {
  provider: string
  configured: boolean
  onClose: () => void
  onSave: (key: string) => Promise<void>
}) {
  const [key, setKey] = React.useState('')
  const [busy, setBusy] = React.useState(false)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Account key of {provider}</DialogTitle>
          <DialogDescription>
            A full-access key of your account. It is only used to read plan quota and balance, never to serve a request. It is
            stored on this server only.
          </DialogDescription>
        </DialogHeader>
        <DialogBody>
          <Field label="Account key" htmlFor="acct-key">
            <Input id="acct-key" type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} />
          </Field>
        </DialogBody>
        <DialogFooter>
          {configured && (
            <Button
              variant="ghost"
              className="mr-auto text-danger-text"
              disabled={busy}
              onClick={async () => {
                setBusy(true)
                await onSave('')
                setBusy(false)
              }}
            >
              Remove key
            </Button>
          )}
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={busy || !key.trim()}
            onClick={async () => {
              setBusy(true)
              await onSave(key.trim())
              setBusy(false)
            }}
          >
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
