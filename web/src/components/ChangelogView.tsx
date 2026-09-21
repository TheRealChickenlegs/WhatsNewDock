import { useEffect, useMemo, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { ChevronDown, ExternalLink } from 'lucide-react'
import type { Release } from '@/lib/types'
import { Badge } from '@/components/ui'
import { cx, formatDate } from '@/lib/utils'

interface Props {
  releases: Release[]
  currentTag?: string
  latestTag?: string
  versionsBehind?: number
  sourceUrl?: string
}

export default function ChangelogView({
  releases,
  currentTag,
  latestTag,
  versionsBehind = 0,
  sourceUrl,
}: Props) {
  const [open, setOpen] = useState<Set<string>>(new Set())
  const initialized = useRef(false)

  const sorted = useMemo(
    () =>
      [...releases].sort((a, b) => {
        const ta = new Date(a.published_at).getTime() || 0
        const tb = new Date(b.published_at).getTime() || 0
        return tb - ta
      }),
    [releases],
  )

  const currentIndex = useMemo(() => {
    const i = sorted.findIndex((r) => r.tag === currentTag)
    return i >= 0 ? i : versionsBehind
  }, [sorted, currentTag, versionsBehind])

  useEffect(() => {
    if (!initialized.current && sorted.length > 0) {
      setOpen(new Set([sorted[0].tag]))
      initialized.current = true
    }
  }, [sorted])

  if (sorted.length === 0) {
    return (
      <p className="py-6 text-center text-sm text-muted-foreground">
        No changelog available for this image.
      </p>
    )
  }

  const toggle = (tag: string) => {
    setOpen((prev) => {
      const next = new Set(prev)
      if (next.has(tag)) next.delete(tag)
      else next.add(tag)
      return next
    })
  }

  return (
    <div className="space-y-2">
      {versionsBehind > 0 && (
        <div className="rounded-lg border border-primary/30 bg-primary/10 px-3 py-2 text-xs text-foreground">
          <span className="font-semibold text-primary">{versionsBehind} update{versionsBehind > 1 ? 's' : ''} available</span>
          {' · '}
          <span className="font-mono">{currentTag}</span> →{' '}
          <span className="font-mono font-medium">{latestTag}</span>
        </div>
      )}

      {sourceUrl && (
        <a
          href={sourceUrl}
          target="_blank"
          rel="noreferrer"
          className="inline-flex min-h-[32px] items-center gap-1 py-1 text-xs text-primary hover:underline"
        >
          View source repository <ExternalLink className="h-3 w-3" />
        </a>
      )}

      <div className="space-y-2">
        {sorted.map((r, i) => {
          const isNew = i < currentIndex
          const isCurrent = r.tag === currentTag
          const expanded = open.has(r.tag)

          return (
            <div key={r.id || r.tag} className="overflow-hidden rounded-lg border border-border bg-background">
              <button
                onClick={() => toggle(r.tag)}
                className="flex w-full flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5 text-left hover:bg-card-hover sm:flex-nowrap sm:px-4"
              >
                <ChevronDown
                  className={cx('h-4 w-4 shrink-0 text-muted-foreground transition-transform', expanded && 'rotate-180')}
                />
                <span className="min-w-0 break-all font-mono text-sm font-medium text-foreground">{r.tag}</span>
                {isNew && <Badge variant="primary">new</Badge>}
                {isCurrent && <Badge variant="success">current</Badge>}
                {r.prerelease && <Badge variant="warning">pre-release</Badge>}
                <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                  {r.published_at ? formatDate(r.published_at) : ''}
                </span>
              </button>
              {expanded && (
                <div className="border-t border-border px-3 py-3 sm:px-4">
                  {r.title && r.title !== r.tag && (
                    <h4 className="mb-2 text-sm font-semibold text-foreground">{r.title}</h4>
                  )}
                  {r.body ? (
                    <div className="md-body min-w-0">
                      <ReactMarkdown
                        remarkPlugins={[remarkGfm]}
                        components={{
                          // Release notes love wide tables. Give them their own
                          // scroll container rather than letting them push the
                          // whole panel — and the page — sideways.
                          table: ({ children }) => (
                            <div className="md-table-scroll">
                              <table>{children}</table>
                            </div>
                          ),
                        }}
                      >
                        {r.body}
                      </ReactMarkdown>
                    </div>
                  ) : (
                    <p className="text-sm text-muted-foreground">No release notes provided.</p>
                  )}
                  {r.url && (
                    <a
                      href={r.url}
                      target="_blank"
                      rel="noreferrer"
                      className="mt-1 inline-flex min-h-[32px] items-center gap-1 py-1 text-xs text-primary hover:underline"
                    >
                      Open release <ExternalLink className="h-3 w-3" />
                    </a>
                  )}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
