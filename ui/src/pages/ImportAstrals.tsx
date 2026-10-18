import { useMemo, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { useQueryClient } from 'react-query'
import { ApiError, createAstral, createFlare, scanRepository } from '../api/client'
import { useGalaxies, useSecrets, useWrites } from '../api/hooks'
import type { Engine, ScannedModule, TerragruntMode } from '../api/types'
import { astralFromForm, emptyForm } from '../components/AstralForm'
import { cls, Field, Section, SecretSelect } from '../components/form'
import { Breadcrumb } from '../components/Layout'
import { Banner, Button, ErrorState, Loading, Mono, Spinner } from '../components/ui'
import { parseDuration } from '../lib/format'

type Outcome = { state: 'pending' | 'done' | 'failed'; message?: string }

const DNS_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/
const lines = (v: string) => v.split('\n').map((l) => l.trim()).filter(Boolean)

// ImportAstrals scans a repository and declares one Astral per root module or
// Terragrunt unit the user ticks. It is a one-off import: unlike Galaxy
// discovery, nothing watches the repository afterwards.
export default function ImportAstrals() {
  const { universe } = useParams()
  const [params] = useSearchParams()
  const qc = useQueryClient()
  const writes = useWrites()
  const galaxiesQ = useGalaxies(universe)

  // Repository to scan.
  const [galaxyName, setGalaxyName] = useState(params.get('galaxy') ?? '')
  const [url, setUrl] = useState('')
  const [branch, setBranch] = useState('')
  const [path, setPath] = useState('')
  const [gitSecret, setGitSecret] = useState('')
  const [terragrunt, setTerragrunt] = useState(false)
  const [mode, setMode] = useState<TerragruntMode>('Unit')
  const [include, setInclude] = useState('')
  const [exclude, setExclude] = useState('')
  const [stripPrefix, setStripPrefix] = useState('')

  // Scan result and selection.
  const [scanning, setScanning] = useState(false)
  const [scanError, setScanError] = useState<unknown>()
  const [modules, setModules] = useState<ScannedModule[]>()
  const [truncated, setTruncated] = useState(false)
  const [picked, setPicked] = useState<Record<string, boolean>>({})
  const [names, setNames] = useState<Record<string, string>>({})
  const [filter, setFilter] = useState('')

  // Settings shared by the imported Astrals.
  const [engine, setEngine] = useState<Engine>('Terraform')
  const [engineVersion, setEngineVersion] = useState('')
  const [terragruntVersion, setTerragruntVersion] = useState('')
  const [interval, setInterval] = useState('')
  const [autoApply, setAutoApply] = useState(false)
  const [backendSecret, setBackendSecret] = useState('')

  const [outcomes, setOutcomes] = useState<Record<string, Outcome>>({})
  const [running, setRunning] = useState(false)

  const galaxy = galaxiesQ.data?.find((g) => g.metadata.name === galaxyName)
  const namespace = galaxy?.metadata.namespace
  const secrets = useSecrets(namespace).data ?? []
  const effectiveURL = url.trim() || galaxy?.spec.defaultVCSURL || ''

  const chosen = useMemo(() => (modules ?? []).filter((m) => picked[m.path]), [modules, picked])
  const visible = useMemo(
    () => (modules ?? []).filter((m) => !filter.trim() || m.path.toLowerCase().includes(filter.trim().toLowerCase())),
    [modules, filter],
  )
  const badNames = chosen.filter((m) => !DNS_LABEL.test(names[m.path] ?? '') || (names[m.path] ?? '').length > 63)
  const dupNames = chosen.filter((m, i) => chosen.findIndex((o) => names[o.path] === names[m.path]) !== i)
  const intervalError = interval.trim() && parseDuration(interval.trim()) === undefined ? 'Expected duration format: 5m, 1h' : undefined

  if (galaxiesQ.isLoading) return <Loading />
  if (galaxiesQ.error) return <ErrorState error={galaxiesQ.error} />

  const scan = async () => {
    if (!namespace || !effectiveURL) return
    setScanning(true)
    setScanError(undefined)
    setOutcomes({})
    try {
      const res = await scanRepository(namespace, {
        url: effectiveURL,
        branch: branch.trim() || undefined,
        path: path.trim() || undefined,
        secret: gitSecret || undefined,
        galaxy: galaxyName,
        terragrunt,
        include: lines(include),
        exclude: lines(exclude),
        stripPrefix: stripPrefix.trim() || undefined,
      })
      setModules(res.modules)
      setTruncated(!!res.truncated)
      // Nothing is ticked up front: a large repository holds hundreds of units.
      setPicked({})
      setFilter('')
      setNames(Object.fromEntries(res.modules.map((m) => [m.path, m.name])))
    } catch (err) {
      setModules(undefined)
      setScanError(err)
    } finally {
      setScanning(false)
    }
  }

  // run creates the Astrals one by one, so a failure on one does not hide the
  // others, then starts a first plan where no automatic check will.
  const run = async () => {
    setRunning(true)
    const base = {
      ...emptyForm(galaxyName),
      url: url.trim(),
      branch: branch.trim(),
      gitSecret,
      engine,
      engineVersion,
      terragrunt,
      terragruntVersion,
      terragruntMode: mode,
      interval,
      autoApply,
      backendSecret,
    }
    for (const m of chosen) {
      setOutcomes((o) => ({ ...o, [m.path]: { state: 'pending' } }))
      try {
        const astral = astralFromForm({ ...base, name: names[m.path]!, path: m.path }, galaxy)
        const created = await createAstral(astral)
        if (!astral.spec.driftDetection?.enabled) {
          await createFlare(created.metadata.namespace!, created.metadata.name, 'Plan')
        }
        setOutcomes((o) => ({ ...o, [m.path]: { state: 'done' } }))
        setPicked((p) => ({ ...p, [m.path]: false }))
      } catch (err) {
        const message = err instanceof ApiError ? err.message : 'The Astral could not be created.'
        setOutcomes((o) => ({ ...o, [m.path]: { state: 'failed', message } }))
      }
    }
    setRunning(false)
    void qc.invalidateQueries('astrals')
    void qc.invalidateQueries('galaxies')
  }

  const doneCount = Object.values(outcomes).filter((o) => o.state === 'done').length
  const finished = !running && Object.keys(outcomes).length > 0
  const canCreate = writes.enabled && chosen.length > 0 && badNames.length === 0 && dupNames.length === 0 && !intervalError && !running

  return (
    <div className="max-w-5xl">
      <Breadcrumb items={[{ label: universe!, to: `/u/${universe}` }, { label: 'Import from a repository' }]} />
      <h1 className="text-xl font-semibold text-slate-900">Import from a repository</h1>
      <p className="mt-1 text-sm text-slate-500">
        Scan a repository, then declare one Astral per root module or Terragrunt unit you pick.
        For a Galaxy that should follow the repository over time, turn on discovery in Admin instead.
      </p>

      <form className="mt-6" onSubmit={(e) => { e.preventDefault(); void scan() }}>
        <Section title="Repository">
          <Field label="Galaxy" required>
            <select value={galaxyName} onChange={(e) => { setGalaxyName(e.target.value); setModules(undefined) }} className={cls()}>
              <option value="">Choose a Galaxy</option>
              {galaxiesQ.data?.map((g) => (
                <option key={`${g.metadata.namespace}/${g.metadata.name}`} value={g.metadata.name}>{g.spec.displayName || g.metadata.name}</option>
              ))}
            </select>
          </Field>
          <div />
          <div className="sm:col-span-2">
            <Field label="Git repository" required={!galaxy?.spec.defaultVCSURL}
              inherited={!url ? galaxy?.spec.defaultVCSURL : undefined} help="GitHub repositories only, for now">
              <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://github.com/acme/infra.git" className={`${cls()} font-mono`} />
            </Field>
          </div>
          <Field label="Revision" help="Branch to scan">
            <input value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" className={`${cls()} font-mono`} />
          </Field>
          <Field label="Path" help="Folder to scan">
            <input value={path} onChange={(e) => setPath(e.target.value)} placeholder="." className={`${cls()} font-mono`} />
          </Field>
          <div className="sm:col-span-2">
            <Field label="Git credentials">
              <SecretSelect value={gitSecret} onChange={setGitSecret} secrets={secrets} inherits={galaxy?.spec.vcsSecretRef?.name} />
            </Field>
          </div>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={terragrunt} onChange={(e) => { setTerragrunt(e.target.checked); setModules(undefined) }}
              className="h-4 w-4 rounded border-slate-300 text-indigo-600" />
            <span className="text-sm font-medium text-slate-800">Terragrunt units (terragrunt.hcl)</span>
          </label>
          <div />
          <Field label="Include" help="One glob per line, e.g. envs/*/apps/*">
            <textarea value={include} onChange={(e) => setInclude(e.target.value)} rows={2} className={`${cls()} font-mono`} />
          </Field>
          <Field label="Exclude" help="One glob per line">
            <textarea value={exclude} onChange={(e) => setExclude(e.target.value)} rows={2} className={`${cls()} font-mono`} />
          </Field>
          <Field label="Strip prefix" help="Removed from the path to build the Astral name">
            <input value={stripPrefix} onChange={(e) => setStripPrefix(e.target.value)} placeholder="terragrunt/environments/" className={`${cls()} font-mono`} />
          </Field>
          <div className="self-end pb-1">
            <Button type="submit" kind="secondary" busy={scanning} disabled={!namespace || !effectiveURL}>Scan repository</Button>
          </div>
        </Section>
      </form>

      {scanError != null && <ErrorState error={scanError} />}

      {modules && (
        <div className="space-y-4 border-t border-slate-200 pt-6">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-semibold text-slate-900">
              {modules.length === 0 ? 'Nothing found' : `${modules.length} ${terragrunt ? 'units' : 'root modules'} found`}
            </h2>
            {modules.length > 0 && (
              <div className="flex items-center gap-3 text-sm">
                <input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter folders"
                  className={`${cls()} w-64 py-1`} />
                <button className="whitespace-nowrap font-medium text-indigo-600 hover:underline"
                  onClick={() => setPicked({ ...picked, ...Object.fromEntries(visible.filter((m) => !m.existing && outcomes[m.path]?.state !== 'done').map((m) => [m.path, true])) })}>
                  Select {filter.trim() ? `the ${visible.length} shown` : 'all'}
                </button>
                <button className="whitespace-nowrap font-medium text-indigo-600 hover:underline" onClick={() => setPicked({})}>Select none</button>
              </div>
            )}
          </div>
          {truncated && <Banner tone="yellow">The host listed only part of the repository: narrow the path to see everything.</Banner>}
          {modules.length === 0 && (
            <p className="text-sm text-slate-500">
              No {terragrunt ? 'terragrunt.hcl' : '.tf file'} matches. Check the path and the filters{terragrunt ? '' : ', or tick Terragrunt units'}.
            </p>
          )}
          {modules.length > 0 && (
            <div className="overflow-hidden rounded-lg border border-slate-200 bg-white">
              <table className="min-w-full divide-y divide-slate-200 text-sm">
                <thead className="bg-slate-50 text-left text-xs font-medium uppercase tracking-wide text-slate-500">
                  <tr><th className="w-10 px-4 py-3" /><th className="px-4 py-3">Folder</th><th className="px-4 py-3">Astral name</th><th className="px-4 py-3" /></tr>
                </thead>
                <tbody className="divide-y divide-slate-100">
                  {visible.map((m) => {
                    const outcome = outcomes[m.path]
                    const name = names[m.path] ?? ''
                    const invalid = picked[m.path] && (!DNS_LABEL.test(name) || name.length > 63 || dupNames.some((d) => d.path === m.path))
                    return (
                      <tr key={m.path} className={m.existing ? 'bg-slate-50 text-slate-400' : ''}>
                        <td className="px-4 py-2">
                          <input type="checkbox" disabled={m.existing || running || outcome?.state === 'done'} checked={!!picked[m.path]}
                            onChange={(e) => setPicked({ ...picked, [m.path]: e.target.checked })}
                            className="h-4 w-4 rounded border-slate-300 text-indigo-600" />
                        </td>
                        <td className="px-4 py-2"><Mono>{m.path}</Mono></td>
                        <td className="px-4 py-2">
                          <input value={name} disabled={m.existing || running || outcome?.state === 'done'}
                            onChange={(e) => setNames({ ...names, [m.path]: e.target.value })}
                            className={`${cls(invalid ? 'bad' : undefined)} py-1 font-mono`} />
                        </td>
                        <td className="whitespace-nowrap px-4 py-2 text-right text-xs">
                          {m.existing && 'Already an Astral'}
                          {outcome?.state === 'pending' && <span className="text-sky-600"><Spinner small /></span>}
                          {outcome?.state === 'done' && <span className="font-medium text-emerald-700">Created</span>}
                          {outcome?.state === 'failed' && <span className="text-red-600">{outcome.message}</span>}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
          {modules.length > 0 && <p className="text-xs text-slate-500">{chosen.length} selected · {visible.length} shown of {modules.length}</p>}
          {(badNames.length > 0 || dupNames.length > 0) && (
            <p className="text-xs text-red-600">
              {dupNames.length > 0 ? 'Two picked folders have the same name. ' : ''}
              {badNames.length > 0 ? 'Names use lowercase letters, digits and dashes, starting and ending with a letter or digit.' : ''}
            </p>
          )}

          {modules.length > 0 && (
            <>
              <Section title="Imported Astrals">
                <Field label="Engine">
                  <select value={engine} onChange={(e) => setEngine(e.target.value as Engine)} className={cls()}>
                    <option value="Terraform">terraform</option>
                    <option value="OpenTofu">opentofu</option>
                  </select>
                </Field>
                <Field label="Engine version">
                  <input value={engineVersion} onChange={(e) => setEngineVersion(e.target.value)} placeholder="1.9.0" className={`${cls()} font-mono`} />
                </Field>
                {terragrunt && (
                  <>
                    <Field label="Terragrunt version">
                      <input value={terragruntVersion} onChange={(e) => setTerragruntVersion(e.target.value)} placeholder="1.1.6" className={`${cls()} font-mono`} />
                    </Field>
                    <Field label="Mode" help={mode === 'RunAll' ? 'Each picked folder is the parent of several units' : 'Each picked folder is one unit'}>
                      <select value={mode} onChange={(e) => setMode(e.target.value as TerragruntMode)} className={cls()}>
                        <option value="Unit">Single unit</option>
                        <option value="RunAll">All units (run-all)</option>
                      </select>
                    </Field>
                  </>
                )}
                <Field label="Check interval" error={intervalError} help="Leave empty to turn automatic checks off: a first plan then runs once">
                  <input value={interval} onChange={(e) => setInterval(e.target.value)} placeholder="1h" className={`${cls(intervalError)} font-mono`} />
                </Field>
                <Field label="Policy" help={autoApply ? 'Auto: changes are applied at once, unless they destroy something' : 'Manual: drift is reported, apply waits for you'}>
                  <select value={autoApply ? 'auto' : 'manual'} onChange={(e) => setAutoApply(e.target.value === 'auto')} className={cls()}>
                    <option value="manual">Manual</option>
                    <option value="auto">Auto</option>
                  </select>
                </Field>
                <div className="sm:col-span-2">
                  <Field label="State credentials" help="Each Astral can still override it afterwards">
                    <SecretSelect value={backendSecret} onChange={setBackendSecret} secrets={secrets} inherits={galaxy?.spec.backendConfig?.secretRef?.name} />
                  </Field>
                </div>
              </Section>

              <div className="flex items-center justify-end gap-3 border-t border-slate-200 pt-5">
                {finished && (
                  <span className="text-sm text-slate-600">
                    {doneCount} of {Object.keys(outcomes).length} created.{' '}
                    <Link to={`/u/${universe}/g/${galaxyName}`} className="font-medium text-indigo-600 hover:underline">View the Astrals</Link>
                  </span>
                )}
                <Button kind="primary" disabled={!canCreate} busy={running} title={writes.reason} onClick={() => void run()}>
                  Create {chosen.length} {chosen.length === 1 ? 'Astral' : 'Astrals'}
                </Button>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  )
}
