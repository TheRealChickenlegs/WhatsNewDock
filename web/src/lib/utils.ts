export function formatBytes(bytes: number): string {
  if (!bytes || bytes <= 0) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  let v = bytes
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`
}

export function formatDate(value: string): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

export function timeAgo(value: string): string {
  if (!value) return 'never'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return '—'
  const seconds = Math.floor((Date.now() - d.getTime()) / 1000)
  if (seconds < 0) return 'just now'
  const ranges: [number, string][] = [
    [31536000, 'y'],
    [2592000, 'mo'],
    [86400, 'd'],
    [3600, 'h'],
    [60, 'm'],
  ]
  for (const [secs, label] of ranges) {
    const n = Math.floor(seconds / secs)
    if (n >= 1) return `${n}${label} ago`
  }
  return `${Math.max(seconds, 1)}s ago`
}

export function shortId(id: string): string {
  return id ? id.slice(0, 12) : ''
}

export function registryLabel(registry: string): string {
  if (registry === 'docker.io') return 'Docker Hub'
  if (registry === 'ghcr.io') return 'GitHub'
  if (registry === 'registry.gitlab.com') return 'GitLab'
  if (registry === 'gcr.io') return 'GCR'
  if (registry === 'public.ecr.aws') return 'ECR'
  if (registry === 'quay.io') return 'Quay'
  return registry
}

export function cx(...parts: (string | false | null | undefined)[]): string {
  return parts.filter(Boolean).join(' ')
}
