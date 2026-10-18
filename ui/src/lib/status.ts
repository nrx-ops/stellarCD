// Status rules from the specification page "UX - Statuts et couleurs": one
// colour code for an Astral, a unit and a Flare, used by every screen.
import type { Astral, Flare, FlarePhase, PlanSummary, UnitStatus } from '../api/types'
import { destroys, hasChanges, isRunAll } from './format'

export type Tone = 'green' | 'yellow' | 'blue' | 'red' | 'gray'

export type AstralState = 'running' | 'failed' | 'drift' | 'synced' | 'unknown'

export interface DisplayStatus {
  state: AstralState
  label: string
  tone: Tone
  drift?: PlanSummary
}

// Display priority when several conditions hold: running, then failed, then
// drift, then synced. [À CONFIRMER in the specification.]
export const STATE_ORDER: AstralState[] = ['failed', 'drift', 'running', 'synced', 'unknown']

export function failedUnits(astral: Astral): UnitStatus[] {
  if (!isRunAll(astral)) return []
  return (astral.status?.units ?? []).filter((u) => u.result === 'Failed')
}

export function driftedUnits(astral: Astral): UnitStatus[] {
  return (astral.status?.units ?? []).filter((u) => u.result !== 'Failed' && u.syncStatus === 'OutOfSync')
}

// activeAction tells "Planning" from "Applying" for the Flare holding the lock.
function activeAction(astral: Astral): string | undefined {
  const active = astral.status?.activeFlare
  if (!active) return undefined
  return astral.status?.flareHistory?.find((f) => f.name === active)?.action
}

export function astralStatus(astral: Astral): DisplayStatus {
  const s = astral.status
  if (s?.activeFlare) {
    const label = activeAction(astral) === 'Apply' ? 'Applying' : 'Planning'
    return { state: 'running', label, tone: 'blue' }
  }
  if (s?.lastFlareStatus === 'Failed' || failedUnits(astral).length > 0) {
    return { state: 'failed', label: 'Failed', tone: 'red' }
  }
  if (s?.syncStatus === 'OutOfSync') {
    return { state: 'drift', label: 'Drift', tone: 'yellow', drift: s.drift }
  }
  if (s?.syncStatus === 'Synced') {
    return { state: 'synced', label: 'Synced', tone: 'green' }
  }
  return { state: 'unknown', label: 'Unknown', tone: 'gray' }
}

export function unitStatus(unit: UnitStatus): DisplayStatus {
  if (unit.result === 'Failed') return { state: 'failed', label: 'Failed', tone: 'red' }
  if (unit.result === 'Excluded') return { state: 'unknown', label: 'Excluded', tone: 'gray' }
  if (unit.result === 'Skipped') return { state: 'unknown', label: 'Skipped', tone: 'gray' }
  if (unit.syncStatus === 'OutOfSync') return { state: 'drift', label: 'Drift', tone: 'yellow', drift: unit.drift }
  if (unit.syncStatus === 'Synced') return { state: 'synced', label: 'Synced', tone: 'green' }
  return { state: 'unknown', label: 'Unknown', tone: 'gray' }
}

export function isActivePhase(phase?: FlarePhase): boolean {
  return !phase || phase === 'Pending' || phase === 'Running' ||
    phase === 'AwaitingApproval' || phase === 'AwaitingConfirmation'
}

export function phaseTone(phase?: FlarePhase): Tone {
  switch (phase) {
    case 'Succeeded':
      return 'green'
    case 'Failed':
      return 'red'
    case 'Running':
    case 'Pending':
      return 'blue'
    case 'AwaitingApproval':
    case 'AwaitingConfirmation':
      return 'yellow'
    default:
      return 'gray'
  }
}

export function phaseLabel(phase?: FlarePhase): string {
  switch (phase) {
    case 'AwaitingApproval':
      return 'Awaiting approval'
    case 'AwaitingConfirmation':
      return 'Awaiting confirmation'
    case undefined:
      return 'Pending'
    default:
      return phase
  }
}

// flareResult is the "Result" badge of the History tab.
export function flareResult(flare: Flare): DisplayStatus & { units?: string } {
  const st = flare.status
  const phase = st?.phase
  const units = st?.unitResults ?? []
  if (phase === 'Failed') {
    const failed = units.filter((u) => u.result === 'Failed').length
    return {
      state: 'failed',
      label: 'Failed',
      tone: 'red',
      units: units.length > 0 && failed > 0 ? `${failed} of ${units.length} units` : undefined,
    }
  }
  if (phase === 'Cancelled') return { state: 'unknown', label: 'Cancelled', tone: 'gray' }
  if (isActivePhase(phase)) {
    if (phase === 'AwaitingConfirmation' || phase === 'AwaitingApproval') {
      return { state: 'running', label: phaseLabel(phase), tone: 'yellow' }
    }
    return { state: 'running', label: 'Running', tone: 'blue' }
  }
  if (flare.spec.action === 'Plan') {
    return hasChanges(st?.planSummary)
      ? { state: 'drift', label: 'Drift', tone: 'yellow', drift: st?.planSummary }
      : { state: 'synced', label: 'No drift', tone: 'green' }
  }
  return { state: 'synced', label: 'Succeeded', tone: 'green' }
}

export function driftCounts(s?: PlanSummary): { create: number; change: number; destroy: number } {
  return { create: s?.toCreate ?? 0, change: s?.toUpdate ?? 0, destroy: destroys(s) }
}
