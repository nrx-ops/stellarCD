import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import type { Flare } from '../../api/types'
import type { TabProps } from '../AstralDetail'
import { Mono, StatusBadge } from '../../components/ui'
import { ago, flareDuration, fullDate, isRunAll, shortSha, triggerLabel } from '../../lib/format'
import { flareResult } from '../../lib/status'

const select =
  'rounded-md border border-slate-300 bg-white py-2 pl-3 pr-8 text-sm focus:border-indigo-500 focus:outline-none'

// SCR-Onglet Historique ("History"): the Astral's Flares, newest first.
export default function HistoryTab({ astral, flares, base }: TabProps) {
  const navigate = useNavigate()
  const runAll = isRunAll(astral)
  const [type, setType] = useState('')
  const [trigger, setTrigger] = useState('')
  const [result, setResult] = useState('')
  const [unit, setUnit] = useState('')

  const scopeOf = (f: Flare) => (f.spec.scope === 'Unit' ? f.spec.unitPath ?? '' : 'All units')
  const unitPaths = [...new Set(flares.filter((f) => f.spec.scope === 'Unit').map((f) => f.spec.unitPath ?? ''))].sort()
  const rows = flares
    .filter((f) => !type || f.spec.action === type)
    .filter((f) => !trigger || (f.spec.trigger ?? 'Manual') === trigger)
    .filter((f) => !result || flareResult(f).label === result)
    .filter((f) => !unit || scopeOf(f) === unit)

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap gap-3">
        <select value={type} onChange={(e) => setType(e.target.value)} className={select}>
          <option value="">All types</option>
          <option value="Plan">Plan</option>
          <option value="Apply">Apply</option>
        </select>
        <select value={trigger} onChange={(e) => setTrigger(e.target.value)} className={select}>
          <option value="">All triggers</option>
          <option value="Schedule">Scheduled</option>
          <option value="GitChange">New commit</option>
          <option value="Drift">Drift</option>
          <option value="Manual">Manual</option>
        </select>
        <select value={result} onChange={(e) => setResult(e.target.value)} className={select}>
          <option value="">All results</option>
          {['No drift', 'Drift', 'Succeeded', 'Failed', 'Running', 'Cancelled'].map((r) => <option key={r}>{r}</option>)}
        </select>
        {runAll && (
          <select value={unit} onChange={(e) => setUnit(e.target.value)} className={select}>
            <option value="">All units</option>
            <option value="All units">Whole Astral</option>
            {unitPaths.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        )}
      </div>

      <div className="overflow-hidden rounded-lg border border-slate-200 bg-white">
        <table className="min-w-full divide-y divide-slate-200 text-sm">
          <thead className="bg-slate-50 text-left text-xs font-medium uppercase tracking-wide text-slate-500">
            <tr>
              <th className="px-4 py-3">Type</th>
              {runAll && <th className="px-4 py-3">Scope</th>}
              <th className="px-4 py-3">Trigger</th>
              <th className="px-4 py-3">Commit</th>
              <th className="px-4 py-3">Started</th>
              <th className="px-4 py-3">Duration</th>
              <th className="px-4 py-3">Result</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            {rows.map((f) => {
              const r = flareResult(f)
              const started = f.status?.startTime ?? f.metadata.creationTimestamp
              return (
                <tr
                  key={f.metadata.name}
                  onClick={() => navigate(`${base}/flares/${f.metadata.name}`)}
                  className="cursor-pointer hover:bg-slate-50"
                >
                  <td className="px-4 py-3 font-medium text-slate-900">{f.spec.action}</td>
                  {runAll && <td className="px-4 py-3 text-slate-600">{f.spec.scope === 'Unit' ? <Mono>{f.spec.unitPath}</Mono> : 'All units'}</td>}
                  <td className="px-4 py-3 text-slate-600">{triggerLabel(f.spec.trigger, f.spec.requestedBy)}</td>
                  <td className="px-4 py-3"><Mono className="text-slate-700">{shortSha(f.status?.revision ?? f.spec.revision)}</Mono></td>
                  <td className="px-4 py-3 text-slate-600" title={fullDate(started)}>{ago(started)}</td>
                  <td className="px-4 py-3 tabular-nums text-slate-600">{flareDuration(f)}</td>
                  <td className="px-4 py-3"><StatusBadge status={r} suffix={r.units} /></td>
                </tr>
              )
            })}
            {rows.length === 0 && (
              <tr><td colSpan={runAll ? 7 : 6} className="px-4 py-10 text-center text-slate-500">
                {flares.length === 0 ? 'No Flare has run yet.' : 'No Flare matches these filters.'}
              </td></tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
