import { Link } from 'react-router-dom'
import type { UnitStatus } from '../../api/types'
import type { TabProps } from '../AstralDetail'
import { Card, Mono, StatusBadge } from '../../components/ui'
import { ago, duration, fullDate, isRunAll, parseDuration, shortSha, triggerLabel } from '../../lib/format'
import { flareResult, unitStatus } from '../../lib/status'

// SCR-Onglet État ("Overview"): a summary of the Astral's status fields.
export default function OverviewTab({ astral, flares, base }: TabProps) {
  const st = astral.status
  const last = flares.find((f) => f.metadata.name === st?.lastFlareRef) ?? flares[0]
  const failedLast = last?.status?.phase === 'Failed' ? last : undefined

  // The newest plan saw a commit that no apply has reached yet.
  const newestRevision = flares.find((f) => f.status?.revision)?.status?.revision
  const pending = newestRevision && st?.lastSyncedRevision && newestRevision !== st.lastSyncedRevision
    ? newestRevision : undefined

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      {failedLast && (
        <div className="rounded-lg border border-red-200 bg-red-50 p-4 lg:col-span-2">
          <div className="text-sm font-medium text-red-800">The last Flare failed</div>
          <p className="mt-1 whitespace-pre-wrap text-sm text-red-700">{failedLast.status?.message || 'No message was reported.'}</p>
          <Link to={`${base}/flares/${failedLast.metadata.name}`} className="mt-2 inline-block text-sm font-medium text-red-800 underline">
            View logs
          </Link>
        </div>
      )}

      <Card title="Last sync">
        {st?.lastSyncedRevision ? (
          <dl className="space-y-1 text-sm">
            <div>Commit <Mono className="text-slate-900">{shortSha(st.lastSyncedRevision)}</Mono></div>
            <div className="text-slate-500" title={fullDate(st.lastSyncTime)}>{ago(st.lastSyncTime)}</div>
            {pending && <div className="pt-1 text-amber-700">New commit <Mono>{shortSha(pending)}</Mono> not applied yet</div>}
          </dl>
        ) : (
          <p className="text-sm text-slate-500">Nothing has been applied yet.</p>
        )}
      </Card>

      <Card title="Last Flare">
        {last ? (
          <div className="space-y-2 text-sm">
            <div className="flex items-center gap-2">
              <span className="font-medium text-slate-900">{last.spec.action}</span>
              <span className="text-slate-500">{triggerLabel(last.spec.trigger, last.spec.requestedBy)}</span>
              <StatusBadge status={flareResult(last)} />
            </div>
            <div className="text-slate-500" title={fullDate(last.metadata.creationTimestamp)}>
              {ago(last.status?.completionTime ?? last.metadata.creationTimestamp)}
            </div>
            <Link to={`${base}/flares/${last.metadata.name}`} className="font-medium text-indigo-600 hover:underline">View Flare</Link>
          </div>
        ) : (
          <p className="text-sm text-slate-500">No Flare has run yet.</p>
        )}
      </Card>

      <Card title="Next check">
        <NextCheck interval={astral.spec.driftDetection?.enabled ? (astral.spec.driftDetection.interval || '1h') : undefined}
          lastChecked={st?.drift?.lastChecked} />
      </Card>

      {isRunAll(astral) && (
        <Card title="Units" actions={<Link to={`${base}/drift`} className="text-sm font-medium text-indigo-600 hover:underline">View drift</Link>}>
          <UnitCounts units={st?.units ?? []} />
        </Card>
      )}

      <details className="rounded-lg border border-slate-200 bg-white lg:col-span-2">
        <summary className="cursor-pointer px-4 py-3 text-sm font-semibold text-slate-900">Conditions</summary>
        <table className="w-full border-t border-slate-100 text-sm">
          <tbody className="divide-y divide-slate-100">
            {(st?.conditions ?? []).map((c) => (
              <tr key={c.type}>
                <td className="px-4 py-2 font-medium text-slate-800">{c.type}</td>
                <td className="px-4 py-2">
                  <span className={c.status === 'True' ? 'text-emerald-700' : c.status === 'False' ? 'text-red-700' : 'text-slate-500'}>{c.status}</span>
                </td>
                <td className="px-4 py-2 text-slate-600">{c.reason}</td>
                <td className="px-4 py-2 text-slate-500">{c.message}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </div>
  )
}

function NextCheck({ interval, lastChecked }: { interval?: string; lastChecked?: string }) {
  if (!interval) return <p className="text-sm text-slate-500">Automatic checks are off.</p>
  const every = parseDuration(interval)
  if (!every || !lastChecked) return <p className="text-sm text-slate-700">Next check soon</p>
  const left = new Date(lastChecked).getTime() + every - Date.now()
  return (
    <p className="text-sm text-slate-700">
      {left > 0 ? `Next check in ${duration(left)}` : 'Next check due now'}
      <span className="block text-slate-500">Checked every {interval}</span>
    </p>
  )
}

function UnitCounts({ units }: { units: UnitStatus[] }) {
  const counts = new Map<string, number>()
  for (const u of units) {
    const label = unitStatus(u).label.toLowerCase()
    counts.set(label, (counts.get(label) ?? 0) + 1)
  }
  if (units.length === 0) return <p className="text-sm text-slate-500">No unit has been planned yet.</p>
  return (
    <p className="text-sm text-slate-700">
      {[...counts.entries()].map(([label, n]) => `${n} ${label}`).join(', ')}
    </p>
  )
}
