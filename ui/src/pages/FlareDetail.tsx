import { useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  useApproveFlare, useAstral, useCancelFlare, useConfirmDestroy, useCreateFlare, useFlare, useFlareLog, useWrites,
} from '../api/hooks'
import type { Astral, Flare, FlareStep, Galaxy } from '../api/types'
import { Breadcrumb } from '../components/Layout'
import { Badge, Banner, Button, Card, ConfirmDialog, ErrorState, Loading, Mono, Spinner } from '../components/ui'
import { ago, fullDate, isRunAll, shortSha, toolDetail, triggerLabel } from '../lib/format'
import { driftCounts, isActivePhase, phaseLabel, phaseTone, type Tone } from '../lib/status'
import { useGalaxy } from './AstralDetail'

// SCR-Détail d'une Flare: one run, its steps, its result and the Job's logs.
export default function FlareDetail() {
  const { universe, galaxy: galaxyName, astral: astralName, flare: name } = useParams()
  const galaxyQ = useGalaxy(universe, galaxyName)
  const ns = galaxyQ.data?.metadata.namespace
  const flareQ = useFlare(ns, name)
  const astralQ = useAstral(ns, astralName)

  if (galaxyQ.isLoading || flareQ.isLoading) return <Loading />
  if (flareQ.error) return <ErrorState error={flareQ.error} />
  if (!flareQ.data || !galaxyQ.data) return null

  const flare = flareQ.data
  const astral = astralQ.data
  const base = `/u/${universe}/g/${galaxyName}/a/${astralName}`
  const phase = flare.status?.phase

  return (
    <div className="space-y-5">
      <Breadcrumb items={[
        { label: universe!, to: `/u/${universe}` },
        { label: galaxyQ.data.spec.displayName || galaxyName!, to: `/u/${universe}/g/${galaxyName}` },
        { label: astralName!, to: `${base}/overview` },
        { label: name! },
      ]} />

      <Header flare={flare} astral={astral} />
      {astral && <Credentials astral={astral} galaxy={galaxyQ.data} />}
      <Steps flare={flare} />
      {phase === 'AwaitingApproval' && <Approval flare={flare} />}
      {phase === 'AwaitingConfirmation' && <DestroyConfirmation flare={flare} />}
      {astral && isRunAll(astral) && (flare.status?.unitResults?.length ?? 0) > 0 && <Units flare={flare} astral={astral} />}
      <Result flare={flare} base={base} />
      <Logs flare={flare} />
    </div>
  )
}

function Header({ flare, astral }: { flare: Flare; astral?: Astral }) {
  const writes = useWrites()
  const [cancelling, setCancelling] = useState(false)
  const cancel = useCancelFlare(flare.metadata.namespace!, flare.metadata.name)
  const st = flare.status
  const phase = st?.phase
  const cancellable = !phase || phase === 'Pending' || phase === 'Running' ||
    phase === 'AwaitingApproval' || phase === 'AwaitingConfirmation'
  const started = st?.startTime ?? flare.metadata.creationTimestamp

  return (
    <div className="flex items-start justify-between gap-6">
      <div className="space-y-2">
        <div className="flex items-center gap-3">
          <h1 className="text-2xl font-semibold text-slate-900">
            {flare.spec.action} <Mono className="text-xl font-normal text-slate-500">{flare.metadata.name}</Mono>
          </h1>
          <PhaseBadge flare={flare} />
          {flare.spec.cancel && isActivePhase(phase) && <Badge tone="gray">Cancelling…</Badge>}
        </div>
        <p className="flex flex-wrap items-center gap-x-2 text-sm text-slate-500">
          {astral && isRunAll(astral) && (
            <><span>{flare.spec.scope === 'Unit' ? <Mono>{flare.spec.unitPath}</Mono> : 'all units'}</span><Dot /></>
          )}
          <span>{triggerLabel(flare.spec.trigger, flare.spec.requestedBy)}</span>
          <Dot /><span>commit <Mono>{shortSha(st?.revision ?? flare.spec.revision)}</Mono></span>
          {astral && <><Dot /><span>{toolDetail(astral).replace(/ · \d+ units?$/, '')}</span></>}
          {st?.jobName && <><Dot /><span>Job <Mono>{st.jobName}</Mono></span></>}
          <Dot /><span title={fullDate(started)}>started {ago(started)}</span>
        </p>
      </div>
      {cancellable && writes.enabled && !flare.spec.cancel && (
        <Button kind="secondary" onClick={() => setCancelling(true)}>Cancel</Button>
      )}
      {cancelling && (
        <ConfirmDialog
          title="Cancel this Flare?"
          confirmLabel="Cancel Flare"
          kind="danger"
          busy={cancel.isLoading}
          error={cancel.error}
          onClose={() => setCancelling(false)}
          onConfirm={() => cancel.mutate(undefined, { onSuccess: () => setCancelling(false) })}
        >
          {flare.spec.action === 'Apply' && phase === 'Running' ? (
            <p>Interrupting an apply can leave infrastructure partially changed and the state locked.</p>
          ) : (
            <p>The run stops and its Job is deleted.</p>
          )}
        </ConfirmDialog>
      )}
    </div>
  )
}

function PhaseBadge({ flare }: { flare: Flare }) {
  const phase = flare.status?.phase
  return <Badge tone={phaseTone(phase)}>{phase === 'Running' && <Spinner small />}{phaseLabel(phase)}</Badge>
}

function Dot() {
  return <span className="text-slate-300">·</span>
}

// Credentials names the Secrets a run of this Astral receives, never their
// content. They come from the Astral's current configuration, else its Galaxy.
function Credentials({ astral, galaxy }: { astral: Astral; galaxy: Galaxy }) {
  const git = astral.spec.vcsRepository.secretRef?.name ?? galaxy.spec.vcsSecretRef?.name
  const state = astral.spec.backendSecretRef?.name ?? galaxy.spec.backendConfig?.secretRef?.name
  const cloud = astral.spec.secrets?.map((s) => s.name) ?? []
  const item = (label: string, value?: string) => (
    <span className="flex items-center gap-1.5">
      <span className="text-slate-500">{label}</span>
      {value ? <Mono className="text-slate-800">{value}</Mono> : <span className="text-slate-400">none</span>}
    </span>
  )
  return (
    <div className="flex flex-wrap items-center gap-x-6 gap-y-1 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-sm"
      title="From the Astral's current configuration">
      <span className="font-medium text-slate-800">Credentials</span>
      {item('Git', git)}
      {item('State', state)}
      {cloud.length > 0 && item('Env', cloud.join(', '))}
    </div>
  )
}

// --- Steps ----------------------------------------------------------------

type StepState = 'done' | 'running' | 'failed' | 'todo'

const STEP_LABELS: Record<FlareStep, string> = {
  GitFetch: 'Git fetch',
  Init: 'Init',
  Plan: 'Plan',
  Apply: 'Apply',
  Report: 'Status update',
}

function Steps({ flare }: { flare: Flare }) {
  const steps: FlareStep[] = flare.spec.action === 'Apply'
    ? ['GitFetch', 'Init', 'Plan', 'Apply', 'Report']
    : ['GitFetch', 'Init', 'Plan', 'Report']
  const phase = flare.status?.phase
  const current = flare.status?.step ? steps.indexOf(flare.status.step) : -1

  const stateOf = (i: number): StepState => {
    if (phase === 'Succeeded') return 'done'
    if (current < 0) return 'todo'
    if (i < current) return 'done'
    if (i > current) return 'todo'
    if (phase === 'Failed') return 'failed'
    if (phase === 'Running') return 'running'
    return phase === 'Cancelled' ? 'todo' : 'done'
  }

  return (
    <ol className="flex items-center rounded-lg border border-slate-200 bg-white px-6 py-4">
      {steps.map((s, i) => (
        <li key={s} className="flex flex-1 items-center last:flex-none">
          <div className="flex items-center gap-2">
            <StepIcon state={stateOf(i)} />
            <span className={`text-sm ${stateOf(i) === 'todo' ? 'text-slate-400' : 'font-medium text-slate-800'}`}>{STEP_LABELS[s]}</span>
          </div>
          {i < steps.length - 1 && <div className={`mx-4 h-px flex-1 ${stateOf(i) === 'done' ? 'bg-emerald-300' : 'bg-slate-200'}`} />}
        </li>
      ))}
    </ol>
  )
}

function StepIcon({ state }: { state: StepState }) {
  const base = 'flex h-6 w-6 items-center justify-center rounded-full text-xs font-bold'
  switch (state) {
    case 'done':
      return <span className={`${base} bg-emerald-500 text-white`}>✓</span>
    case 'running':
      return <span className={`${base} text-sky-600`}><Spinner /></span>
    case 'failed':
      return <span className={`${base} bg-red-500 text-white`}>✕</span>
    default:
      return <span className={`${base} border-2 border-slate-300`} />
  }
}

// --- Waiting states -------------------------------------------------------

// The Galaxy's approval policy parks some runs until someone approves them.
// The screens of the specification do not show this state yet.
function Approval({ flare }: { flare: Flare }) {
  const writes = useWrites()
  const approve = useApproveFlare(flare.metadata.namespace!, flare.metadata.name)
  return (
    <Banner
      tone="yellow"
      action={writes.enabled ? (
        <div className="flex gap-2">
          <Button size="sm" kind="ghost" busy={approve.isLoading && approve.variables === false} onClick={() => approve.mutate(false)}>Reject</Button>
          <Button size="sm" kind="primary" busy={approve.isLoading && approve.variables === true} onClick={() => approve.mutate(true)}>Approve</Button>
        </div>
      ) : undefined}
    >
      This {flare.spec.action.toLowerCase()} waits for an approval required by the Galaxy.
      {approve.error != null && <span className="block text-red-700">{(approve.error as Error).message}</span>}
    </Banner>
  )
}

// RG-Protection des destructions: an apply that destroys anything waits for a
// user, who confirms by typing the Astral name.
function DestroyConfirmation({ flare }: { flare: Flare }) {
  const writes = useWrites()
  const [open, setOpen] = useState(false)
  const confirm = useConfirmDestroy(flare.metadata.namespace!, flare.metadata.name)
  const pending = flare.status?.pendingDestroy ?? []
  return (
    <div className="rounded-lg border border-red-200 bg-red-50 p-4">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h3 className="text-sm font-semibold text-red-800">This plan destroys {pending.length === 1 ? 'a resource' : `${pending.length} resources`}</h3>
          <p className="mt-1 text-sm text-red-700">Nothing is applied until you confirm. The apply then only runs if nothing else is destroyed.</p>
        </div>
        {writes.enabled && <Button kind="danger" onClick={() => setOpen(true)}>Confirm destroy</Button>}
      </div>
      <ul className="mt-3 space-y-1">
        {pending.map((p) => <li key={p} className="flex gap-2 text-sm"><span className="font-mono font-bold text-red-600">−</span><Mono className="text-red-900">{p}</Mono></li>)}
      </ul>
      {open && (
        <ConfirmDialog
          title="Confirm destroy"
          confirmLabel="Destroy and apply"
          kind="danger"
          expect={flare.spec.astralRef}
          busy={confirm.isLoading}
          error={confirm.error}
          onClose={() => setOpen(false)}
          onConfirm={(typed) => confirm.mutate(typed, { onSuccess: () => setOpen(false) })}
        >
          <p>These resources will be destroyed: {pending.length}.</p>
        </ConfirmDialog>
      )}
    </div>
  )
}

// --- Units and result -----------------------------------------------------

const UNIT_TONES: Record<string, Tone> = { Succeeded: 'green', Failed: 'red', Skipped: 'gray', Excluded: 'gray' }

function Units({ flare, astral }: { flare: Flare; astral: Astral }) {
  const writes = useWrites()
  const create = useCreateFlare(astral.metadata.namespace!, astral.metadata.name)
  return (
    <Card title="Units">
      <ul className="divide-y divide-slate-100">
        {flare.status?.unitResults?.map((u) => {
          const drift = u.result === 'Succeeded' && u.planSummary &&
            (u.planSummary.toCreate + u.planSummary.toUpdate + u.planSummary.toDestroy + u.planSummary.toReplace > 0)
          return (
            <li key={u.path} className="flex items-center gap-3 py-2">
              <Mono className="flex-1 text-slate-800">{u.path}</Mono>
              <Badge tone={drift ? 'yellow' : UNIT_TONES[u.result] ?? 'gray'} title={u.message}>
                {drift ? 'Drift' : u.result}
              </Badge>
              {u.result === 'Failed' && writes.enabled && !astral.status?.activeFlare && (
                <Button size="sm" busy={create.isLoading} onClick={() => create.mutate({ action: 'Plan', unitPath: u.path })}>
                  Plan this unit
                </Button>
              )}
            </li>
          )
        })}
      </ul>
      {create.error != null && <ErrorState error={create.error} />}
    </Card>
  )
}

function Result({ flare, base }: { flare: Flare; base: string }) {
  const st = flare.status
  if (st?.phase === 'Failed') {
    return <Banner tone="red"><span className="whitespace-pre-wrap">{st.message || 'The run failed without a message.'}</span></Banner>
  }
  if (st?.phase === 'Succeeded' && flare.spec.action === 'Plan') {
    const c = driftCounts(st.planSummary)
    return (
      <div className="flex items-center gap-3 rounded-lg border border-slate-200 bg-white px-4 py-3 text-sm">
        <span className="font-medium text-slate-800">Plan result</span>
        <Badge tone="green">+{c.create}</Badge>
        <Badge tone="yellow">~{c.change}</Badge>
        <Badge tone="red">−{c.destroy}</Badge>
        <Link to={`${base}/drift`} className="ml-auto font-medium text-indigo-600 hover:underline">View drift</Link>
      </div>
    )
  }
  if (st?.phase === 'Cancelled' && st.message) return <Banner tone="yellow">{st.message}</Banner>
  return null
}

// --- Logs -----------------------------------------------------------------

function Logs({ flare }: { flare: Flare }) {
  const live = flare.status?.phase === 'Running'
  const { data, error, isLoading } = useFlareLog(flare.metadata.namespace!, flare.metadata.name, live)
  const box = useRef<HTMLDivElement>(null)
  const lines = data?.log ? data.log.replace(/\n$/, '').split('\n') : []

  // Follow the tail while the Flare runs.
  useEffect(() => {
    if (live && box.current) box.current.scrollTop = box.current.scrollHeight
  }, [live, lines.length])

  const download = () => {
    const blob = new Blob([data?.log ?? ''], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${flare.metadata.name}.log`
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <Card
      title="Pod logs"
      actions={
        <div className="flex items-center gap-3">
          <span className="text-xs text-slate-500">Injected secrets are never shown in logs</span>
          <Button size="sm" disabled={!data?.available} onClick={download}>Download</Button>
        </div>
      }
    >
      {isLoading && <Loading label="Loading logs…" />}
      {error != null && <ErrorState error={error} />}
      {data && !data.available && <p className="text-sm text-slate-500">{data.message || 'Logs for this Flare are no longer available.'}</p>}
      {data?.available && (
        <div ref={box} className="max-h-[32rem] overflow-auto rounded-md bg-slate-950 py-3 font-mono text-xs leading-5 text-slate-200">
          {lines.length === 0 && <div className="px-4 text-slate-500">No output yet.</div>}
          {lines.map((line, i) => (
            <div key={i} className="flex hover:bg-slate-900">
              <span className="w-12 shrink-0 select-none pr-3 text-right text-slate-600">{i + 1}</span>
              <span className="whitespace-pre-wrap break-all pr-4">{line}</span>
            </div>
          ))}
        </div>
      )}
    </Card>
  )
}
