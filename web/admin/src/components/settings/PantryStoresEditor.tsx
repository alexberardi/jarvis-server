import { useRef, useState, type FormEvent } from 'react'
import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react'
import { parseStores } from '@/api/store'
import { storeURLProblem } from '@/lib/store'
import { Badge, Button, Input } from '@/components/ui'

/** A row being edited. id is the server's (absent on a new store, which cc gives one). */
interface Row {
  key: string
  id?: string
  name: string
  url: string
}

interface RowErrors {
  name?: string
  url?: string
}

const NAME_MAX = 64 // cc's pantryStoreNameMax

let nextKey = 0
const rowKey = () => `store-${++nextKey}`

interface PantryStoresEditorProps {
  value: unknown
  onSave: (value: unknown) => void
  onCancel: () => void
  isSaving: boolean
}

/**
 * PantryStoresEditor edits pantry.stores as a list rather than JSON (decision
 * 2026-11-11-several-pantries): a name and URL per store, in preference order, the first being
 * the default. A store keeps its id through edits, so installs from it follow a new URL; a new
 * store is saved without one and cc makes it.
 */
export default function PantryStoresEditor({ value, onSave, onCancel, isSaving }: PantryStoresEditorProps) {
  const [rows, setRows] = useState<Row[]>(() => parseStores(value).map((s) => ({ key: rowKey(), ...s })))
  const [errors, setErrors] = useState<Record<string, RowErrors>>({})
  const [added, setAdded] = useState<string | null>(null)
  const addButton = useRef<HTMLButtonElement>(null)

  const change = (key: string, field: 'name' | 'url', v: string) => {
    setRows((rs) => rs.map((r) => (r.key === key ? { ...r, [field]: v } : r)))
    setErrors((e) => ({ ...e, [key]: { ...e[key], [field]: undefined } }))
  }
  const move = (from: number, to: number) =>
    setRows((rs) => {
      const next = [...rs]
      const [r] = next.splice(from, 1)
      next.splice(to, 0, r)
      return next
    })
  const add = () => {
    const key = rowKey()
    setRows((rs) => [...rs, { key, name: '', url: '' }])
    setAdded(key)
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const found: Record<string, RowErrors> = {}
    for (const r of rows) {
      const name = r.name.trim()
      const err: RowErrors = {
        name: !name ? 'Enter a name' : [...name].length > NAME_MAX ? `At most ${NAME_MAX} characters` : undefined,
        url: storeURLProblem(r.url),
      }
      if (err.name || err.url) found[r.key] = err
    }
    setErrors(found)
    if (Object.keys(found).length) return
    onSave(rows.map((r) => ({ ...(r.id ? { id: r.id } : {}), name: r.name.trim(), url: r.url.trim() })))
  }

  return (
    <form onSubmit={submit} noValidate className="@container w-full space-y-3">
      {rows.length === 0 ? (
        <p className="text-small text-text-muted">
          No stores. While the Pantry is on, browsing and installing fail until you add one.
        </p>
      ) : (
        <ol className="divide-y divide-border rounded-control border border-border" aria-label="Pantry stores">
          {rows.map((r, i) => {
            const n = i + 1
            const label = r.name.trim() || `store ${n}`
            return (
              <li key={r.key} className="grid gap-3 p-3 @xl:grid-cols-[minmax(0,1fr)_minmax(0,1.5fr)_auto] @xl:items-start">
                <Input
                  label={
                    <span className="flex items-center gap-2">
                      Name {i === 0 && <Badge tone="primary">Default</Badge>}
                    </span>
                  }
                  value={r.name}
                  maxLength={NAME_MAX}
                  autoFocus={r.key === added}
                  onChange={(e) => change(r.key, 'name', e.target.value)}
                  error={errors[r.key]?.name}
                />
                <Input
                  label="URL"
                  type="url"
                  inputMode="url"
                  spellCheck={false}
                  autoComplete="off"
                  placeholder="http://10.0.0.5:7721"
                  value={r.url}
                  onChange={(e) => change(r.key, 'url', e.target.value)}
                  error={errors[r.key]?.url}
                />
                <div className="-ml-2 flex items-center @xl:mt-6 @xl:ml-0">
                  <Button type="button" size="icon" variant="ghost" aria-label={`Move ${label} up`} disabled={i === 0} onClick={() => move(i, i - 1)}>
                    <ArrowUp size={16} aria-hidden />
                  </Button>
                  <Button
                    type="button"
                    size="icon"
                    variant="ghost"
                    aria-label={`Move ${label} down`}
                    disabled={i === rows.length - 1}
                    onClick={() => move(i, i + 1)}
                  >
                    <ArrowDown size={16} aria-hidden />
                  </Button>
                  <Button
                    type="button"
                    size="icon"
                    variant="ghost"
                    className="hover:bg-danger-soft hover:text-danger"
                    aria-label={`Remove ${label}`}
                    onClick={() => {
                      setRows((rs) => rs.filter((x) => x.key !== r.key))
                      addButton.current?.focus() // the removed row took the focused button with it
                    }}
                  >
                    <Trash2 size={16} aria-hidden />
                  </Button>
                </div>
              </li>
            )
          })}
        </ol>
      )}
      <p className="text-small text-text-muted">
        The first store is the default. Nodes download from these URLs, so every node must reach each one. Removing a
        store leaves its packages installed, without updates.
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <Button ref={addButton} type="button" icon={<Plus size={16} aria-hidden />} onClick={add}>
          Add store
        </Button>
        <div className="ml-auto flex items-center gap-2">
          <Button type="submit" variant="primary" loading={isSaving}>
            Save
          </Button>
          <Button type="button" variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        </div>
      </div>
    </form>
  )
}
