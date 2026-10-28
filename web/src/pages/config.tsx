import * as React from 'react'
import { RotateCcw, Save } from 'lucide-react'
import { api } from '@/lib/api'
import type { ConfigFile } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Textarea } from '@/components/ui/input'
import { Page } from '@/components/shell'
import { useToast } from '@/components/ui/toast'

/**
 * Config is the config file as plain text. Saving checks everything first; the
 * previous version is kept and can be restored.
 */
export function ConfigPage() {
  const [yaml, setYaml] = React.useState('')
  const [history, setHistory] = React.useState<string[]>([])
  const [busy, setBusy] = React.useState(false)
  const toast = useToast()

  const load = React.useCallback(async () => {
    const r = await api<ConfigFile>('config')
    setYaml(r.yaml)
    setHistory(r.history ?? [])
  }, [])

  React.useEffect(() => {
    load().catch((e) => toast.push(e.message, 'bad'))
  }, [load, toast])

  async function save() {
    setBusy(true)
    try {
      await api('config', { method: 'PUT', body: { yaml } })
      toast.push('Saved and applied')
      await load()
    } catch (e) {
      toast.push((e as Error).message, 'bad')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Page
      title="Config file"
      subtitle="The same settings as plain text, with a comment above every setting saying what it does. Saving checks everything first; the previous version is kept."
      actions={
        <>
          <Button variant="ghost" size="sm" onClick={load}>
            Reload
          </Button>
          <Button variant="primary" size="sm" disabled={busy} onClick={save}>
            <Save />
            {busy ? 'Checking…' : 'Check, save & apply'}
          </Button>
        </>
      }
    >
      <Card className="mb-4">
        <CardContent className="p-4">
          <Textarea value={yaml} onChange={(e) => setYaml(e.target.value)} rows={28} spellCheck={false} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Earlier versions</CardTitle>
        </CardHeader>
        <CardContent>
          {history.length === 0 ? (
            <p className="text-sm text-text-muted">No earlier versions yet.</p>
          ) : (
            <ul className="divide-y divide-border">
              {history.slice(0, 20).map((id) => (
                <li key={id} className="flex items-center justify-between gap-3 py-2">
                  <span className="mono text-xs text-text-secondary">{id}</span>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={async () => {
                      if (!confirm('Restore ' + id + '?')) return
                      try {
                        await api('rollback', { method: 'POST', body: { id } })
                        toast.push('Restored')
                        await load()
                      } catch (e) {
                        toast.push((e as Error).message, 'bad')
                      }
                    }}
                  >
                    <RotateCcw />
                    Restore
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </Page>
  )
}
