import type { Astral, Flare, FlareTrigger, PlanSummary } from '../api/types'

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

// ago renders "just now", "4 min ago", "3 h ago", "yesterday 17:42" or a date.
export function ago(iso?: string, now = Date.now()): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  const diff = now - t
  if (diff < MINUTE) return 'just now'
  if (diff < HOUR) return `${Math.floor(diff / MINUTE)} min ago`
  if (diff < DAY) return `${Math.floor(diff / HOUR)} h ago`
  const d = new Date(t)
  const time = d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
  if (diff < 2 * DAY) return `yesterday ${time}`
  return `${d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short' })} ${time}`
}

export function fullDate(iso?: string): string {
  return iso ? new Date(iso).toLocaleString('en-GB') : ''
}

// duration renders a millisecond span as "42s", "3m 05s" or "1h 12m".
export function duration(ms: number): string {
  if (ms < 0) ms = 0
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, '0')}s`
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m`
}

// flareDuration is the run time of a finished Flare, or the time elapsed so far.
export function flareDuration(flare: Flare, now = Date.now()): string {
  const start = flare.status?.startTime
  if (!start) return '—'
  const end = flare.status?.completionTime ? new Date(flare.status.completionTime).getTime() : now
  return duration(end - new Date(start).getTime())
}

// parseDuration reads a Go duration such as "5m", "1h30m" or "90s".
export function parseDuration(value?: string): number | undefined {
  if (!value) return undefined
  const re = /(\d+(?:\.\d+)?)(ms|s|m|h)/g
  let total = 0
  let consumed = 0
  for (const match of value.matchAll(re)) {
    const n = Number(match[1])
    total += n * { ms: 1, s: 1000, m: MINUTE, h: HOUR }[match[2] as 'ms' | 's' | 'm' | 'h']
    consumed += match[0].length
  }
  return consumed === value.length && consumed > 0 ? total : undefined
}

export function shortSha(sha?: string): string {
  return sha ? sha.slice(0, 7) : '—'
}

export function engineLabel(astral: Astral): string {
  return (astral.spec.engine ?? 'Terraform').toLowerCase()
}

export function isRunAll(astral: Astral): boolean {
  const tg = astral.spec.terragrunt
  return !!tg?.enabled && tg.mode === 'RunAll'
}

export function usesTerragrunt(astral: Astral): boolean {
  return !!astral.spec.terragrunt?.enabled
}

// toolLabel is the "Tool" column: the engine alone, or "terragrunt · <engine>".
export function toolLabel(astral: Astral): string {
  return usesTerragrunt(astral) ? `terragrunt · ${engineLabel(astral)}` : engineLabel(astral)
}

// toolDetail is the header detail line, e.g. "terragrunt run-all · opentofu 1.8 · 6 units".
export function toolDetail(astral: Astral): string {
  const engine = [engineLabel(astral), astral.spec.engineVersion].filter(Boolean).join(' ')
  if (!usesTerragrunt(astral)) return engine
  const parts = [isRunAll(astral) ? 'terragrunt run-all' : 'terragrunt', engine]
  if (isRunAll(astral)) parts.push(plural(astral.status?.units?.length ?? 0, 'unit'))
  return parts.join(' · ')
}

export function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

// initials turns a requester such as "nicolas.roux" or "Nicolas Roux" into "NR".
export function initials(who?: string): string {
  if (!who) return ''
  const parts = who.split(/[\s._@-]+/).filter(Boolean).slice(0, 2)
  return parts.map((p) => p[0]!.toUpperCase()).join('')
}

export function triggerLabel(trigger?: FlareTrigger, requestedBy?: string): string {
  switch (trigger) {
    case 'Schedule':
      return 'Scheduled'
    case 'GitChange':
      return 'New commit'
    case 'Drift':
      return 'Drift'
    default: {
      const who = initials(requestedBy)
      return who ? `Manual · ${who}` : 'Manual'
    }
  }
}

// destroys counts deletions and replacements: a replacement destroys first.
export function destroys(s?: PlanSummary): number {
  return s ? (s.toDestroy ?? 0) + (s.toReplace ?? 0) : 0
}

export function hasChanges(s?: PlanSummary): boolean {
  return !!s && (s.toCreate > 0 || s.toUpdate > 0 || destroys(s) > 0)
}
