import { useState, type ReactNode } from 'react'
import { Check, Pencil, X } from 'lucide-react'
import { api } from '@/lib/api'

interface Props {
  id: string
  name: string
  onSaved: (name: string) => void
  renderName?: (name: string) => ReactNode
}

export default function ServerNameEdit({ id, name, onSaved, renderName }: Props) {
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(name)
  const [busy, setBusy] = useState(false)

  const cancel = () => {
    setValue(name)
    setEditing(false)
  }

  const save = async () => {
    const trimmed = value.trim()
    if (!trimmed || trimmed === name) {
      cancel()
      return
    }
    setBusy(true)
    try {
      await api.patch(`/api/v1/servers/${id}`, { name: trimmed })
      onSaved(trimmed)
      setEditing(false)
    } catch {
      cancel()
    } finally {
      setBusy(false)
    }
  }

  if (editing) {
    return (
      <span className="inline-flex items-center gap-1">
        <input
          autoFocus
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') save()
            else if (e.key === 'Escape') cancel()
          }}
          disabled={busy}
          className="h-7 w-44 rounded-lg border border-border bg-background px-2 text-sm font-medium text-foreground outline-none focus:ring-2 focus:ring-primary/50"
        />
        <button
          onClick={save}
          disabled={busy}
          title="Save"
          className="rounded-md p-1 text-success hover:bg-card-hover"
        >
          <Check className="h-4 w-4" />
        </button>
        <button
          onClick={cancel}
          disabled={busy}
          title="Cancel"
          className="rounded-md p-1 text-muted-foreground hover:bg-card-hover"
        >
          <X className="h-4 w-4" />
        </button>
      </span>
    )
  }

  return (
    <span className="group/name inline-flex items-center gap-1.5">
      {renderName ? renderName(name) : <span>{name}</span>}
      <button
        onClick={() => {
          setValue(name)
          setEditing(true)
        }}
        title="Rename server"
        className="rounded-md p-1 text-muted-foreground opacity-0 transition-opacity hover:bg-card-hover hover:text-foreground group-hover/name:opacity-100 focus:opacity-100"
      >
        <Pencil className="h-3.5 w-3.5" />
      </button>
    </span>
  )
}
