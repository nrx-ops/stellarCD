import { useEffect, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useCreateFlare, useWrites } from '../../api/hooks'
import type { Astral, Flare, PlanSummary, ResourceChange, UnitStatus } from '../../api/types'
import type { TabProps } from '../AstralDetail'
import { ActionSymbol, Badge, Banner, Button, ErrorState, Mono, StatusBadge } from '../../components/ui'
import { ago, destroys, fullDate, isRunAll, plural } from '../../lib/format'
import { driftCounts, unitStatus } from '../../lib/status'

// Within a group: destructions first, then changes, then creations. [À CONFIRMER]
const ACTION_ORDER: Record<string, number> = { Delete: 0, Replace: 1, Update: 2, Create: 3 }

function sortChanges(changes: ResourceChange[]): ResourceChange[] {
  return [...changes].sort((a, b) =>
    (ACTION_ORDER[a.action] ?? 9) - (ACTION_ORDER[b.action] ?? 9) || a.address.localeCompare(b.address))
}

// SCR-Onglet Drift: what an apply would create, change or destroy, resource by
// resource, grouped by unit in Terragrunt run-all.
export default function DriftTab(props: TabProps) {
  return isRunAll(props.astral) ? <RunAllDrift {...props} /> : <SingleDrift {...props} />
}

// lastWholePlan is the newest finished whole-Astral Plan: the one the drift
// counters come from.
function lastWholePlan(flares: Flare[]): Flare | undefined {
  return flares.find((f) =>
    f.spec.action === 'Plan' &&
    (f.spec.scope ?? 'All') === 'All' &&
    (f.status?.phase === 'Succeeded' || f.status?.phase === 'Failed'))
}

function SingleDrift({ astral, flares, base }: TabProps) {
  const plan = lastWholePlan(flares)
  if (!plan) return <Empty text="No plan has run yet." />
  if (plan.status?.phase === 'Failed') {
    return (
      <Banner tone="red" action={<FlareLink base={base} flare={plan} label="View Flare" />}>
        Drift could not be computed.
      </Banner>
    )
  }
  const summary = plan.status?.planSummary ?? astral.status?.drift
  const changes = sortChanges(plan.status?.resourceChanges ?? [])
  if (!summary || changes.length === 0) {
    return <Empty text={`No drift from Git`} detail={`Last plan ${ago(plan.status?.completionTime)}`} />
  }
  return (
    <div className="space-y-4">
      <Summary summary={summary} flare={plan} base={base} />
      {destroys(summary) > 0 && (
        <Banner tone="red">This plan destroys a resource. You&apos;ll be asked to confirm before apply.</Banner>
      )}
      <div className="rounded-lg border border-slate-200 bg-white">
        <Resources changes={changes} truncated={plan.status?.resourceChangesTruncated} />
      </div>
    </div>
  )
}

function RunAllDrift({ astral, flares, base }: TabProps) {
  const [params] = useSearchParams()
  const focus = params.get('unit') ?? undefined
  const [showSynced, setShowSynced] = useState(false)
  const units = astral.status?.units ?? []
  const byName = new Map(flares.map((f) => [f.metadata.name, f]))
  const plan = lastWholePlan(flares)

  if (units.length === 0) {
    if (plan?.status?.phase === 'Failed') {
      return (
        <Banner tone="red" action={<FlareLink base={base} flare={plan} label="View Flare" />}>
          Drift could not be computed.
        </Banner>
      )
    }
    return <Empty text="No plan has run yet." />
  }

  // Failed units first, then drifted ones, expanded. Synced units fold at the bottom.
  const rank = (u: UnitStatus) => ({ failed: 0, drift: 1 }[unitStatus(u).state as string] ?? 2)
  const sorted = [...units].sort((a, b) => rank(a) - rank(b) || a.path.localeCompare(b.path))
  const open = sorted.filter((u) => rank(u) < 2)
  const quiet = sorted.filter((u) => rank(u) === 2)
  const total = astral.status?.drift

  return (
    <div className="space-y-4">
      {total && (total.toCreate + total.toUpdate + total.toDestroy + total.toReplace > 0) && (
        <Summary summary={total} flare={plan} base={base} />
      )}
      {open.length === 0 && <Empty text="No drift from Git" detail={plan ? `Last plan ${ago(plan.status?.completionTime)}` : undefined} />}
      {open.map((u) => (
        <UnitGroup key={u.path} unit={u} astral={astral} flare={u.lastFlare ? byName.get(u.lastFlare) : undefined}
          units={units} base={base} focused={focus === u.path} />
      ))}
      {quiet.length > 0 && (
        <div className="rounded-lg border border-slate-200 bg-white">
          <button
            onClick={() => setShowSynced(!showSynced)}
            className="flex w-full items-center justify-between px-4 py-3 text-left text-sm font-medium text-slate-700 hover:bg-slate-50"
          >
            {plural(quiet.length, 'unit')} in sync
            <span className="text-slate-400">{showSynced ? '▾' : '▸'}</span>
          </button>
          {(showSynced || quiet.some((u) => u.path === focus)) && (
            <div className="space-y-3 border-t border-slate-100 p-3">
              {quiet.map((u) => (
                <UnitGroup key={u.path} unit={u} astral={astral} flare={u.lastFlare ? byName.get(u.lastFlare) : undefined}
                  units={units} base={base} focused={focus === u.path} />
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function UnitGroup({ unit, astral, flare, units, base, focused }: {
  unit: UnitStatus
  astral: Astral
  flare?: Flare
  units: UnitStatus[]
  base: string
  focused: boolean
}) {
  const ref = useRef<HTMLDivElement>(null)
  const writes = useWrites()
  const create = useCreateFlare(astral.metadata.namespace!, astral.metadata.name)
  const status = unitStatus(unit)
  const changes = sortChanges((flare?.status?.resourceChanges ?? []).filter((c) => c.unit === unit.path))
  const active = !!astral.status?.activeFlare
  // Units this one's failure kept from running, as reported by the same Flare.
  const skipped = status.state === 'failed' && flare
    ? units.filter((u) => u.result === 'Skipped' && u.lastFlare === flare.metadata.name).map((u) => u.path)
    : []

  useEffect(() => {
    if (focused) ref.current?.scrollIntoView({ behavior: 'smooth', block: 'center' })
  }, [focused])

  return (
    <div
      ref={ref}
      className={`rounded-lg border bg-white transition-shadow ${focused ? 'border-indigo-400 ring-2 ring-indigo-200' : 'border-slate-200'}`}
    >
      <header className="flex flex-wrap items-center gap-3 border-b border-slate-100 px-4 py-3">
        <Mono className="font-semibold text-slate-900">{unit.path}</Mono>
        <StatusBadge status={{ ...status, drift: undefined }} />
        {unit.drift && <Counters summary={unit.drift} />}
        {flare?.status?.completionTime && (
          <span className="text-xs text-slate-500" title={fullDate(flare.status.completionTime)}>
            planned {ago(flare.status.completionTime)}
          </span>
        )}
        {!active && writes.enabled && unit.result !== 'Excluded' && (
          <div className="ml-auto flex gap-2">
            <Button size="sm" busy={create.isLoading && create.variables?.action === 'Plan'}
              onClick={() => create.mutate({ action: 'Plan', unitPath: unit.path })}>
              Plan this unit
            </Button>
            {status.state === 'drift' && (
              <Button size="sm" kind="primary" busy={create.isLoading && create.variables?.action === 'Apply'}
                onClick={() => create.mutate({ action: 'Apply', unitPath: unit.path })}>
                Reconcile this unit
              </Button>
            )}
          </div>
        )}
      </header>
      {create.error != null && <div className="px-4 pt-3"><ErrorState error={create.error} /></div>}

      {unit.result === 'Excluded' && (
        <p className="px-4 py-3 text-sm text-slate-500">Excluded from automatic checks</p>
      )}
      {status.state === 'failed' && (
        <div className="space-y-2 px-4 py-3">
          <pre className="max-h-72 overflow-auto rounded-md bg-slate-900 p-3 font-mono text-xs leading-relaxed text-red-200">
            {unit.message || 'The tool reported no error output.'}
          </pre>
          {skipped.length > 0 && (
            <p className="text-sm text-slate-600">
              {plural(skipped.length, 'dependent unit')} skipped: {skipped.map((p, i) => (
                <span key={p}>{i > 0 && ', '}<Mono>{p}</Mono></span>
              ))}
            </p>
          )}
          {flare && <FlareLink base={base} flare={flare} label="View Flare" />}
        </div>
      )}
      {status.state === 'drift' && (
        <>
          {unit.drift && destroys(unit.drift) > 0 && (
            <div className="px-4 pt-3">
              <Banner tone="red">This unit destroys a resource. You&apos;ll be asked to confirm.</Banner>
            </div>
          )}
          <Resources changes={changes} truncated={flare?.status?.resourceChangesTruncated} />
        </>
      )}
    </div>
  )
}

function Summary({ summary, flare, base }: { summary: PlanSummary; flare?: Flare; base: string }) {
  const c = driftCounts(summary)
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Badge tone="green">+{c.create} to create</Badge>
      <Badge tone="yellow">~{c.change} to change</Badge>
      <Badge tone="red">−{c.destroy} to destroy</Badge>
      {flare && (
        <span className="ml-auto text-sm text-slate-500">
          Detected {ago(flare.status?.completionTime)} by Flare <FlareLink base={base} flare={flare} label={flare.metadata.name} mono />
        </span>
      )}
    </div>
  )
}

function Counters({ summary }: { summary: PlanSummary }) {
  const c = driftCounts(summary)
  return (
    <span className="font-mono text-xs tabular-nums">
      <span className="text-emerald-700">+{c.create}</span>{' '}
      <span className="text-amber-700">~{c.change}</span>{' '}
      <span className="text-red-700">−{c.destroy}</span>
    </span>
  )
}

// Resources lists one line per resource. The plan JSON kept in the Flare holds
// the address and the action only, so there is no per-attribute diff to show.
function Resources({ changes, truncated }: { changes: ResourceChange[]; truncated?: boolean }) {
  if (changes.length === 0) return <p className="px-4 py-3 text-sm text-slate-500">No resource detail was recorded for this plan.</p>
  return (
    <ul className="divide-y divide-slate-100">
      {changes.map((c) => (
        <li key={`${c.unit ?? ''}/${c.address}`} className="flex items-center gap-3 px-4 py-2">
          <ActionSymbol action={c.action} />
          <Mono className="text-slate-800">{c.address}</Mono>
          {c.action === 'Replace' && <span className="text-xs text-red-600">replaced</span>}
        </li>
      ))}
      {truncated && <li className="px-4 py-2 text-xs text-slate-500">The list is capped at 200 resources.</li>}
    </ul>
  )
}

function FlareLink({ base, flare, label, mono }: { base: string; flare: Flare; label: string; mono?: boolean }) {
  return (
    <Link to={`${base}/flares/${flare.metadata.name}`} className={`font-medium text-indigo-600 hover:underline ${mono ? 'font-mono text-[0.8125rem]' : ''}`}>
      {label}
    </Link>
  )
}

function Empty({ text, detail }: { text: string; detail?: string }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white px-6 py-10 text-center">
      <p className="text-sm font-medium text-slate-900">{text}</p>
      {detail && <p className="mt-1 text-sm text-slate-500">{detail}</p>}
    </div>
  )
}
