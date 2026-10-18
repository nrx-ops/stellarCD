import { Link, useParams } from 'react-router-dom'
import { useAstrals, useGalaxies, useWrites } from '../../api/hooks'
import { backendLabel } from '../../components/AstralForm'
import { Badge, Button, EmptyState, ErrorState, Loading, Mono } from '../../components/ui'
import AdminFrame from './AdminFrame'

const POLICIES = { Auto: 'Auto', Manual: 'Manual', RequiresReview: 'Requires review' }

// Galaxies lists the Galaxies of the Universe with their defaults.
export default function Galaxies() {
  const { universe } = useParams()
  const writes = useWrites()
  const { data, error, isLoading } = useGalaxies(universe)
  const astrals = useAstrals(universe).data ?? []

  const action = writes.enabled
    ? <Link to={`/u/${universe}/admin/galaxies/new`}><Button kind="primary">New Galaxy</Button></Link>
    : undefined

  return (
    <AdminFrame tab="galaxies" actions={action}>
      {isLoading && <Loading />}
      {error != null && <ErrorState error={error} />}
      {data && data.length === 0 && (
        <EmptyState title="Create the first Galaxy" text="A Galaxy groups the Astrals of a team or an environment and holds their defaults." action={action} />
      )}
      {data && data.length > 0 && (
        <div className="overflow-hidden rounded-lg border border-slate-200 bg-white">
          <table className="min-w-full divide-y divide-slate-200 text-sm">
            <thead className="bg-slate-50 text-left text-xs font-medium uppercase tracking-wide text-slate-500">
              <tr>
                <th className="px-4 py-3">Galaxy</th>
                <th className="px-4 py-3">Astrals</th>
                <th className="px-4 py-3">Git credentials</th>
                <th className="px-4 py-3">State backend</th>
                <th className="px-4 py-3">Approval</th>
                <th className="px-4 py-3">Discovery</th>
                <th className="px-4 py-3" />
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {data.map((g) => {
                const count = astrals.filter((a) => a.spec.galaxyRef === g.metadata.name && a.metadata.namespace === g.metadata.namespace).length
                const link = `/u/${universe}/admin/galaxies/${g.metadata.name}`
                return (
                  <tr key={`${g.metadata.namespace}/${g.metadata.name}`} className="hover:bg-slate-50">
                    <td className="px-4 py-3">
                      <Link to={link} className="font-semibold text-slate-900 hover:text-indigo-600">{g.spec.displayName || g.metadata.name}</Link>
                      <div className="text-xs text-slate-500"><Mono>{g.metadata.name}</Mono></div>
                    </td>
                    <td className="px-4 py-3 tabular-nums text-slate-700">{count}</td>
                    <td className="px-4 py-3">{g.spec.vcsSecretRef ? <Mono>{g.spec.vcsSecretRef.name}</Mono> : <span className="text-slate-400">None</span>}</td>
                    <td className="px-4 py-3 text-slate-700">
                      {g.spec.backendConfig ? backendLabel(g) : <span className="text-slate-400">In the repositories</span>}
                      {g.spec.backendConfig?.secretRef && <div className="text-xs text-slate-500"><Mono>{g.spec.backendConfig.secretRef.name}</Mono></div>}
                    </td>
                    <td className="px-4 py-3 text-slate-700">{POLICIES[g.spec.approvalPolicy ?? 'RequiresReview']}</td>
                    <td className="px-4 py-3">
                      {g.spec.discovery?.enabled
                        ? <Badge tone="blue">On · {g.status?.discoveredAstrals ?? 0} found</Badge>
                        : <span className="text-slate-400">Off</span>}
                    </td>
                    <td className="px-4 py-3 text-right">
                      {g.spec.archived && <span className="mr-3"><Badge tone="gray">Archived</Badge></span>}
                      <Link to={link}><Button size="sm">{writes.enabled ? 'Edit' : 'View'}</Button></Link>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </AdminFrame>
  )
}
