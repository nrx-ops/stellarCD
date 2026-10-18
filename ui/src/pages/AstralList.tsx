import { useMemo, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { useAstrals, useWrites } from '../api/hooks'
import type { Astral } from '../api/types'
import { Button, EmptyState, ErrorState, Loading, StatusBadge } from '../components/ui'
import { ago, isRunAll, plural, toolLabel } from '../lib/format'
import { astralStatus, failedUnits, driftedUnits, STATE_ORDER, type AstralState } from '../lib/status'

// SCR-Liste des Astrals: the home screen, every Astral of the Universe at a
// glance, filtered by the Galaxy picked in the left column.
export default function AstralList() {
  const { universe, galaxy } = useParams()
  const [params] = useSearchParams()
  const { data, error, isLoading } = useAstrals(universe)
  const writes = useWrites()
  const [query, setQuery] = useState(params.get('q') ?? '')
  const [state, setState] = useState<AstralState | ''>('')

  const inGalaxy = useMemo(
    () => (data ?? []).filter((a) => !galaxy || a.spec.galaxyRef === galaxy),
    [data, galaxy],
  )
  const counts = useMemo(() => {
    const c: Record<AstralState, number> = { synced: 0, drift: 0, running: 0, failed: 0, unknown: 0 }
    for (const a of inGalaxy) c[astralStatus(a).state]++
    return c
  }, [inGalaxy])

  // Astrals that need an action come first: failed, drift, running, synced.
  // Alphabetical inside a group. [À CONFIRMER]
  const rows = useMemo(() => {
    const q = query.trim().toLowerCase()
    return inGalaxy
      .filter((a) => !q || a.metadata.name.toLowerCase().includes(q) || a.spec.displayName?.toLowerCase().includes(q))
      .filter((a) => !state || astralStatus(a).state === state)
      .sort((a, b) =>
        STATE_ORDER.indexOf(astralStatus(a).state) - STATE_ORDER.indexOf(astralStatus(b).state) ||
        a.metadata.name.localeCompare(b.metadata.name))
  }, [inGalaxy, query, state])

  const newLink = `/u/${universe}/new${galaxy ? `?galaxy=${galaxy}` : ''}`
  const importLink = `/u/${universe}/import${galaxy ? `?galaxy=${galaxy}` : ''}`

  if (isLoading) return <Loading />
  if (error) return <ErrorState error={error} />

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold text-slate-900">Astrals</h1>
        {inGalaxy.length > 0 && writes.enabled && (
          <div className="flex gap-2">
            <Link to={importLink}><Button>Import from a repository</Button></Link>
            <Link to={newLink}><Button kind="primary">New Astral</Button></Link>
          </div>
        )}
      </div>

      {inGalaxy.length === 0 ? (
        <EmptyState
          title="Declare your first Astral"
          text="An Astral watches an IaC folder in your Git repository."
          action={writes.enabled ? (
            <div className="flex gap-2">
              <Link to={importLink}><Button>Import from a repository</Button></Link>
              <Link to={newLink}><Button kind="primary">New Astral</Button></Link>
            </div>
          ) : undefined}
        />
      ) : (
        <>
          <div className="grid grid-cols-4 gap-4">
            <Counter label="Synced" value={counts.synced} active={state === 'synced'} onClick={() => setState(state === 'synced' ? '' : 'synced')} />
            <Counter label="Drift" value={counts.drift} tone="text-amber-600" active={state === 'drift'} onClick={() => setState(state === 'drift' ? '' : 'drift')} />
            <Counter label="Running" value={counts.running} active={state === 'running'} onClick={() => setState(state === 'running' ? '' : 'running')} />
            <Counter label="Failed" value={counts.failed} tone="text-red-600" active={state === 'failed'} onClick={() => setState(state === 'failed' ? '' : 'failed')} />
          </div>

          <div className="flex items-center gap-3">
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search Astrals"
              className="w-72 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm placeholder:text-slate-400 focus:border-indigo-500 focus:outline-none"
            />
            <select
              value={state}
              onChange={(e) => setState(e.target.value as AstralState | '')}
              className="rounded-md border border-slate-300 bg-white py-2 pl-3 pr-8 text-sm focus:border-indigo-500 focus:outline-none"
            >
              <option value="">All statuses</option>
              <option value="synced">Synced</option>
              <option value="drift">Drift</option>
              <option value="running">Running</option>
              <option value="failed">Failed</option>
              <option value="unknown">Unknown</option>
            </select>
          </div>

          <div className="overflow-hidden rounded-lg border border-slate-200 bg-white">
            <table className="min-w-full divide-y divide-slate-200 text-sm">
              <thead className="bg-slate-50 text-left text-xs font-medium uppercase tracking-wide text-slate-500">
                <tr>
                  <th className="px-4 py-3">Astral</th>
                  <th className="px-4 py-3">Tool</th>
                  <th className="px-4 py-3">Status</th>
                  <th className="px-4 py-3">Last Flare</th>
                  <th className="px-4 py-3" />
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {rows.map((a) => <Row key={`${a.metadata.namespace}/${a.metadata.name}`} astral={a} universe={universe!} />)}
                {rows.length === 0 && (
                  <tr><td colSpan={5} className="px-4 py-10 text-center text-slate-500">No Astral matches these filters.</td></tr>
                )}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  )
}

function Counter({ label, value, tone = 'text-slate-900', active, onClick }: {
  label: string
  value: number
  tone?: string
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      onClick={onClick}
      className={`rounded-lg border bg-white px-4 py-3 text-left transition-colors ${
        active ? 'border-indigo-400 ring-1 ring-indigo-400' : 'border-slate-200 hover:border-slate-300'
      }`}
    >
      <div className="text-xs font-medium text-slate-500">{label}</div>
      <div className={`mt-1 text-2xl font-semibold tabular-nums ${tone}`}>{value}</div>
    </button>
  )
}

function Row({ astral, universe }: { astral: Astral; universe: string }) {
  const status = astralStatus(astral)
  const units = astral.status?.units ?? []
  const runAll = isRunAll(astral)
  const affected = failedUnits(astral).length + driftedUnits(astral).length
  const tab = status.state === 'drift' || status.state === 'failed' ? 'drift' : 'overview'
  const link = `/u/${universe}/g/${astral.spec.galaxyRef}/a/${astral.metadata.name}/${tab}`

  return (
    <tr className="hover:bg-slate-50">
      <td className="px-4 py-3">
        <Link to={link} className="font-semibold text-slate-900 hover:text-indigo-600">
          {astral.spec.displayName || astral.metadata.name}
        </Link>
        <div className="text-xs text-slate-500">{astral.spec.galaxyRef}</div>
      </td>
      <td className="px-4 py-3 text-slate-700">
        {toolLabel(astral)}
        {runAll && <div className="text-xs text-slate-500">{plural(units.length, 'unit')}</div>}
      </td>
      <td className="px-4 py-3">
        <StatusBadge status={status} />
        {runAll && affected > 0 && <div className="mt-1 text-xs text-slate-500">{affected} of {units.length} units</div>}
      </td>
      <td className="px-4 py-3 text-slate-600"><LastFlare astral={astral} /></td>
      <td className="px-4 py-3 text-right">
        <Link to={link}><Button size="sm">View</Button></Link>
      </td>
    </tr>
  )
}

// LastFlare reads the Astral's own Flare history, newest first, so the list
// needs no extra request per row.
function LastFlare({ astral }: { astral: Astral }) {
  const last = astral.status?.flareHistory?.[0]
  if (!last) return <span className="text-slate-400">—</span>
  if (astral.status?.activeFlare === last.name) return <span>{last.action} · running</span>
  return <span>{last.action} · {ago(last.completionTime)}</span>
}
