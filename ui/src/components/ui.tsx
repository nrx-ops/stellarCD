import { ReactNode, useEffect, useState } from 'react'
import type { PlanSummary } from '../api/types'
import { driftCounts, type DisplayStatus, type Tone } from '../lib/status'

const TONES: Record<Tone, string> = {
  green: 'bg-emerald-50 text-emerald-700 ring-emerald-600/20',
  yellow: 'bg-amber-50 text-amber-800 ring-amber-600/25',
  blue: 'bg-sky-50 text-sky-700 ring-sky-600/20',
  red: 'bg-red-50 text-red-700 ring-red-600/20',
  gray: 'bg-slate-100 text-slate-600 ring-slate-500/20',
}

const DOTS: Record<Tone, string> = {
  green: 'bg-emerald-500',
  yellow: 'bg-amber-500',
  blue: 'bg-sky-500',
  red: 'bg-red-500',
  gray: 'bg-slate-400',
}

export function Badge({ tone, children, title }: { tone: Tone; children: ReactNode; title?: string }) {
  return (
    <span
      title={title}
      className={`inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 text-xs font-medium ring-1 ring-inset whitespace-nowrap ${TONES[tone]}`}
    >
      {children}
    </span>
  )
}

export function StatusBadge({ status, suffix }: { status: DisplayStatus; suffix?: string }) {
  const running = status.tone === 'blue'
  return (
    <Badge tone={status.tone}>
      <span className={`h-1.5 w-1.5 rounded-full ${DOTS[status.tone]} ${running ? 'animate-pulse' : ''}`} />
      {status.label}
      {status.drift && <DriftInline summary={status.drift} />}
      {suffix && <span className="font-normal opacity-80">· {suffix}</span>}
    </Badge>
  )
}

// DriftInline renders "+2 ~1 −1", skipping zero counters.
export function DriftInline({ summary }: { summary: PlanSummary }) {
  const c = driftCounts(summary)
  return (
    <span className="font-mono tabular-nums">
      {c.create > 0 && <span className="text-emerald-700"> +{c.create}</span>}
      {c.change > 0 && <span className="text-amber-700"> ~{c.change}</span>}
      {c.destroy > 0 && <span className="text-red-700"> −{c.destroy}</span>}
    </span>
  )
}

export function ActionSymbol({ action }: { action: string }) {
  const map: Record<string, [string, string]> = {
    Create: ['+', 'text-emerald-600'],
    Update: ['~', 'text-amber-600'],
    Delete: ['−', 'text-red-600'],
    Replace: ['±', 'text-red-600'],
  }
  const [sym, cls] = map[action] ?? ['?', 'text-slate-400']
  return <span className={`inline-block w-4 text-center font-mono font-bold ${cls}`}>{sym}</span>
}

type ButtonKind = 'primary' | 'secondary' | 'danger' | 'danger-secondary' | 'ghost'

const BUTTONS: Record<ButtonKind, string> = {
  primary: 'bg-indigo-600 text-white hover:bg-indigo-500 shadow-sm disabled:bg-indigo-300',
  secondary: 'bg-white text-slate-700 ring-1 ring-inset ring-slate-300 hover:bg-slate-50 disabled:text-slate-400',
  danger: 'bg-red-600 text-white hover:bg-red-500 shadow-sm disabled:bg-red-300',
  'danger-secondary': 'bg-white text-red-600 ring-1 ring-inset ring-red-300 hover:bg-red-50 disabled:text-red-300',
  ghost: 'text-slate-600 hover:bg-slate-100 disabled:text-slate-300',
}

export function Button({
  kind = 'secondary',
  size = 'md',
  children,
  disabled,
  title,
  onClick,
  type = 'button',
  busy,
}: {
  kind?: ButtonKind
  size?: 'sm' | 'md'
  children: ReactNode
  disabled?: boolean
  title?: string
  onClick?: () => void
  type?: 'button' | 'submit'
  busy?: boolean
}) {
  const pad = size === 'sm' ? 'px-2.5 py-1 text-xs' : 'px-3.5 py-2 text-sm'
  return (
    // A disabled button swallows hover events, so the tooltip sits on a wrapper.
    <span title={title} className="inline-flex">
      <button
        type={type}
        disabled={disabled || busy}
        onClick={onClick}
        className={`inline-flex items-center gap-2 rounded-md font-medium transition-colors disabled:cursor-not-allowed ${pad} ${BUTTONS[kind]}`}
      >
        {busy && <Spinner small />}
        {children}
      </button>
    </span>
  )
}

export function Spinner({ small }: { small?: boolean }) {
  const size = small ? 'h-3.5 w-3.5' : 'h-5 w-5'
  return <span className={`inline-block ${size} animate-spin rounded-full border-2 border-current border-r-transparent`} />
}

export function Card({ title, children, actions, className = '' }: {
  title?: ReactNode
  children: ReactNode
  actions?: ReactNode
  className?: string
}) {
  return (
    <section className={`rounded-lg border border-slate-200 bg-white ${className}`}>
      {(title || actions) && (
        <header className="flex items-center justify-between gap-3 border-b border-slate-100 px-4 py-3">
          <h3 className="text-sm font-semibold text-slate-900">{title}</h3>
          {actions}
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  )
}

export function Banner({ tone, children, action }: { tone: 'red' | 'yellow' | 'blue'; children: ReactNode; action?: ReactNode }) {
  const cls = {
    red: 'border-red-200 bg-red-50 text-red-800',
    yellow: 'border-amber-200 bg-amber-50 text-amber-900',
    blue: 'border-sky-200 bg-sky-50 text-sky-900',
  }[tone]
  return (
    <div className={`flex items-center justify-between gap-4 rounded-md border px-4 py-3 text-sm ${cls}`}>
      <div>{children}</div>
      {action}
    </div>
  )
}

export function ErrorState({ error }: { error: unknown }) {
  const message = error instanceof Error ? error.message : 'Something went wrong.'
  return <Banner tone="red">{message}</Banner>
}

export function Loading({ label = 'Loading…' }: { label?: string }) {
  return (
    <div className="flex items-center gap-3 p-8 text-sm text-slate-500">
      <Spinner /> {label}
    </div>
  )
}

export function EmptyState({ title, text, action }: { title: string; text?: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed border-slate-300 bg-white px-6 py-14 text-center">
      <h3 className="text-base font-semibold text-slate-900">{title}</h3>
      {text && <p className="max-w-md text-sm text-slate-500">{text}</p>}
      {action}
    </div>
  )
}

export function Modal({ title, children, onClose, footer }: {
  title: string
  children: ReactNode
  onClose: () => void
  footer?: ReactNode
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/40 p-4" onClick={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        className="flex max-h-[85vh] w-full max-w-2xl flex-col rounded-lg bg-white shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <header className="flex items-center justify-between border-b border-slate-100 px-5 py-4">
          <h2 className="text-base font-semibold text-slate-900">{title}</h2>
          <button onClick={onClose} className="text-slate-400 hover:text-slate-600" aria-label="Close">✕</button>
        </header>
        <div className="overflow-auto px-5 py-4 text-sm text-slate-700">{children}</div>
        {footer && <footer className="flex justify-end gap-2 border-t border-slate-100 px-5 py-3">{footer}</footer>}
      </div>
    </div>
  )
}

// ConfirmDialog asks for a plain yes, or for a typed value when `expect` is set.
export function ConfirmDialog({ title, children, confirmLabel, kind = 'primary', expect, busy, error, onConfirm, onClose }: {
  title: string
  children: ReactNode
  confirmLabel: string
  kind?: ButtonKind
  expect?: string
  busy?: boolean
  error?: unknown
  onConfirm: (typed: string) => void
  onClose: () => void
}) {
  const [typed, setTyped] = useState('')
  const ok = expect === undefined || typed === expect
  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <Button kind="ghost" onClick={onClose}>Keep</Button>
          <Button kind={kind} disabled={!ok} busy={busy} onClick={() => onConfirm(typed)}>{confirmLabel}</Button>
        </>
      }
    >
      <div className="space-y-3">
        {children}
        {expect !== undefined && (
          <label className="block">
            <span className="text-slate-600">Type <span className="font-mono font-semibold">{expect}</span> to confirm</span>
            <input
              autoFocus
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              className="mt-1 block w-full rounded-md border border-slate-300 px-3 py-2 font-mono text-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            />
          </label>
        )}
        {error != null && <ErrorState error={error} />}
      </div>
    </Modal>
  )
}

export function Mono({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <span className={`font-mono text-[0.8125rem] ${className}`}>{children}</span>
}
