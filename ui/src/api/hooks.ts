import { useMutation, useQuery, useQueryClient } from 'react-query'
import * as api from './client'
import type { Astral } from './types'

// Statuses update live without a page reload (SCR-Liste des Astrals,
// "Actualisation"). Polling the admin API is cheap: it reads straight from the
// API server and returns small objects.
const LIVE = 3000

export function useConfig() {
  return useQuery('config', api.getConfig, { staleTime: 60_000 })
}

export function useUniverses() {
  return useQuery('universes', api.listUniverses, { refetchInterval: 30_000 })
}

export function useGalaxies(universe?: string) {
  return useQuery(['galaxies', universe], () => api.listGalaxies(universe!), {
    enabled: !!universe,
    refetchInterval: LIVE,
  })
}

export function useAstrals(universe?: string) {
  return useQuery(['astrals', universe], () => api.listAstrals(universe!), {
    enabled: !!universe,
    refetchInterval: LIVE,
  })
}

export function useAstral(namespace?: string, name?: string) {
  return useQuery(['astral', namespace, name], () => api.getAstral(namespace!, name!), {
    enabled: !!namespace && !!name,
    refetchInterval: LIVE,
  })
}

export function useFlares(namespace?: string, astral?: string) {
  return useQuery(['flares', namespace, astral], () => api.listFlares(namespace!, astral!), {
    enabled: !!namespace && !!astral,
    refetchInterval: LIVE,
  })
}

export function useFlare(namespace?: string, name?: string) {
  return useQuery(['flare', namespace, name], () => api.getFlare(namespace!, name!), {
    enabled: !!namespace && !!name,
    refetchInterval: LIVE,
  })
}

export function useFlareLog(namespace: string, name: string, live: boolean) {
  return useQuery(['flare-log', namespace, name], () => api.getFlareLog(namespace, name), {
    refetchInterval: live ? 2000 : false,
  })
}

export function useSecrets(namespace?: string) {
  return useQuery(['secrets', namespace], () => api.listSecrets(namespace!), {
    enabled: !!namespace,
    staleTime: 30_000,
  })
}

// useRefresh invalidates everything an action can change, so the screens show
// the new Flare at once instead of on the next poll.
function useRefresh() {
  const qc = useQueryClient()
  return () => {
    for (const key of ['astrals', 'astral', 'flares', 'flare', 'galaxies']) {
      void qc.invalidateQueries(key)
    }
  }
}

export function useCreateFlare(namespace: string, astral: string) {
  const refresh = useRefresh()
  return useMutation(
    (v: { action: 'Plan' | 'Apply'; unitPath?: string }) =>
      api.createFlare(namespace, astral, v.action, v.unitPath),
    { onSuccess: refresh },
  )
}

export function useCancelFlare(namespace: string, name: string) {
  const refresh = useRefresh()
  return useMutation(() => api.cancelFlare(namespace, name), { onSuccess: refresh })
}

export function useApproveFlare(namespace: string, name: string) {
  const refresh = useRefresh()
  return useMutation((approved: boolean) => api.approveFlare(namespace, name, approved), { onSuccess: refresh })
}

export function useConfirmDestroy(namespace: string, name: string) {
  const refresh = useRefresh()
  return useMutation((confirm: string) => api.confirmDestroy(namespace, name, confirm), { onSuccess: refresh })
}

export function useCreateAstral() {
  const refresh = useRefresh()
  return useMutation((astral: Astral) => api.createAstral(astral), { onSuccess: refresh })
}

export function useUpdateAstral() {
  const refresh = useRefresh()
  return useMutation((astral: Astral) => api.updateAstral(astral), { onSuccess: refresh })
}

export function useDeleteAstral() {
  const refresh = useRefresh()
  return useMutation((a: { namespace: string; name: string }) => api.deleteAstral(a.namespace, a.name), {
    onSuccess: refresh,
  })
}

// useWrites reports whether action buttons may be used, with the tooltip to
// show when they may not.
export function useWrites(): { enabled: boolean; reason?: string } {
  const { data } = useConfig()
  if (data && !data.writesEnabled) {
    return { enabled: false, reason: 'Actions are disabled on this admin API.' }
  }
  return { enabled: true }
}
