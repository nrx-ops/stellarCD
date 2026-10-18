import { Link, useNavigate, useParams } from 'react-router-dom'
import { useAstral, useCreateFlare, useFlares, useGalaxies, useWrites } from '../api/hooks'
import type { Astral, Flare, Galaxy } from '../api/types'
import { Breadcrumb } from '../components/Layout'
import { Badge, Banner, Button, ErrorState, Loading, Mono, StatusBadge } from '../components/ui'
import { isRunAll, plural, shortSha, toolDetail } from '../lib/format'
import { astralStatus, failedUnits } from '../lib/status'
import ConfigurationTab from './tabs/ConfigurationTab'
import DriftTab from './tabs/DriftTab'
import HistoryTab from './tabs/HistoryTab'
import OverviewTab from './tabs/OverviewTab'

export interface TabProps {
  astral: Astral
  galaxy: Galaxy
  flares: Flare[]
  universe: string
  base: string
}

const TABS = [
  { id: 'overview', label: 'Overview' },
  { id: 'drift', label: 'Drift' },
  { id: 'history', label: 'History' },
  { id: 'configuration', label: 'Configuration' },
] as const

// useGalaxy resolves the Galaxy named in the URL, which also gives the
// namespace the Astral and its Flares live in.
export function useGalaxy(universe?: string, name?: string) {
  const q = useGalaxies(universe)
  return { ...q, data: q.data?.find((g) => g.metadata.name === name) }
}

// SCR-Détail d'un Astral: the shared header, its actions and four tabs.
export default function AstralDetail() {
  const { universe, galaxy: galaxyName, astral: name, tab = 'overview' } = useParams()
  const galaxyQ = useGalaxy(universe, galaxyName)
  const namespace = galaxyQ.data?.metadata.namespace
  const astralQ = useAstral(namespace, name)
  const flaresQ = useFlares(namespace, name)

  if (galaxyQ.isLoading || astralQ.isLoading) return <Loading />
  if (galaxyQ.error) return <ErrorState error={galaxyQ.error} />
  if (!galaxyQ.data) return <ErrorState error={new Error(`Galaxy ${galaxyName} does not exist in Universe ${universe}.`)} />
  if (astralQ.error) return <ErrorState error={astralQ.error} />
  if (!astralQ.data) return null

  const astral = astralQ.data
  const galaxy = galaxyQ.data
  const base = `/u/${universe}/g/${galaxyName}/a/${name}`
  const props: TabProps = { astral, galaxy, flares: flaresQ.data ?? [], universe: universe!, base }

  return (
    <div className="space-y-5">
      <Breadcrumb items={[
        { label: universe!, to: `/u/${universe}` },
        { label: galaxy.spec.displayName || galaxy.metadata.name, to: `/u/${universe}/g/${galaxyName}` },
        { label: astral.metadata.name },
      ]} />
      <Header astral={astral} base={base} />
      <FailedUnitBanner astral={astral} base={base} />
      <Tabs astral={astral} base={base} current={tab} />
      {tab === 'overview' && <OverviewTab {...props} />}
      {tab === 'drift' && <DriftTab {...props} />}
      {tab === 'history' && <HistoryTab {...props} />}
      {tab === 'configuration' && <ConfigurationTab {...props} />}
    </div>
  )
}

function Header({ astral, base }: { astral: Astral; base: string }) {
  const navigate = useNavigate()
  const writes = useWrites()
  const status = astralStatus(astral)
  const ns = astral.metadata.namespace!
  const create = useCreateFlare(ns, astral.metadata.name)
  const runAll = isRunAll(astral)
  const failed = failedUnits(astral)
  const repo = astral.spec.vcsRepository
  const dd = astral.spec.driftDetection
  const active = astral.status?.activeFlare

  return (
    <div className="flex items-start justify-between gap-6">
      <div className="min-w-0 space-y-2">
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="text-2xl font-semibold text-slate-900">{astral.spec.displayName || astral.metadata.name}</h1>
          <StatusBadge status={status} />
          <Badge tone="gray">{astral.spec.autoApply ? 'Auto sync' : 'Manual sync'}</Badge>
        </div>
        <p className="flex flex-wrap items-center gap-x-2 text-sm text-slate-500">
          <span>{toolDetail(astral)}</span>
          <Sep /><Mono>{repo.path || '.'}</Mono>
          <Sep /><span><Mono>{repo.branch || 'main'}</Mono> @ <Mono>{shortSha(astral.status?.lastSyncedRevision)}</Mono></span>
          {dd?.enabled && <><Sep /><span>checked every {dd.interval || '1h'}</span></>}
        </p>
        {astral.spec.description && <p className="text-sm text-slate-600">{astral.spec.description}</p>}
      </div>

      <div className="flex shrink-0 items-center gap-2">
        {active ? (
          <Button kind="secondary" onClick={() => navigate(`${base}/flares/${active}`)}>View running Flare</Button>
        ) : (
          <>
            <Button
              disabled={!writes.enabled}
              title={writes.reason}
              busy={create.isLoading && create.variables?.action === 'Plan'}
              onClick={() => create.mutate({ action: 'Plan' })}
            >
              Run plan
            </Button>
            {status.state === 'drift' || (status.state === 'failed' && astral.status?.syncStatus === 'OutOfSync') ? (
              <Button
                kind="primary"
                disabled={!writes.enabled || failed.length > 0}
                title={failed.length > 0
                  ? `Disabled: ${plural(failed.length, 'unit')} failed. Fix ${failed.length === 1 ? 'it' : 'them'} first.`
                  : writes.reason}
                busy={create.isLoading && create.variables?.action === 'Apply'}
                onClick={() => create.mutate({ action: 'Apply' })}
              >
                {runAll ? 'Reconcile all' : 'Reconcile'}
              </Button>
            ) : null}
          </>
        )}
      </div>
      {create.error != null && (
        <div className="fixed bottom-6 right-6 z-50 max-w-md"><ErrorState error={create.error} /></div>
      )}
    </div>
  )
}

function Sep() {
  return <span className="text-slate-300">·</span>
}

// RG-Run-all bloqué par un unit en échec: a red banner names the failed unit
// and leads to it on the Drift tab.
function FailedUnitBanner({ astral, base }: { astral: Astral; base: string }) {
  const failed = failedUnits(astral)
  if (failed.length === 0) return null
  const first = failed[0]!
  return (
    <Banner
      tone="red"
      action={
        <Link
          to={`${base}/drift?unit=${encodeURIComponent(first.path)}`}
          className="whitespace-nowrap font-medium underline"
        >
          Go to unit
        </Link>
      }
    >
      Unit <Mono>{first.path}</Mono> failed. Fix it before reconciling all units.
      {failed.length > 1 && ` ${failed.length - 1} other ${failed.length === 2 ? 'unit' : 'units'} failed too.`}
    </Banner>
  )
}

function Tabs({ astral, base, current }: { astral: Astral; base: string; current: string }) {
  const drift = astral.status?.syncStatus === 'OutOfSync' ? astral.status.drift : undefined
  const driftCount = drift ? drift.toCreate + drift.toUpdate + drift.toDestroy + drift.toReplace : 0
  return (
    <nav className="flex gap-6 border-b border-slate-200">
      {TABS.map((t) => (
        <Link
          key={t.id}
          to={`${base}/${t.id}`}
          className={`-mb-px flex items-center gap-2 border-b-2 py-2.5 text-sm font-medium ${
            current === t.id ? 'border-indigo-600 text-indigo-700' : 'border-transparent text-slate-500 hover:text-slate-800'
          }`}
        >
          {t.label}
          {t.id === 'drift' && driftCount > 0 && (
            <span className="rounded-full bg-amber-100 px-1.5 text-xs font-semibold tabular-nums text-amber-800">{driftCount}</span>
          )}
        </Link>
      ))}
    </nav>
  )
}
