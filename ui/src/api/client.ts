import axios, { AxiosError } from 'axios'
import type {
  AdminConfig,
  Astral,
  Flare,
  FlareLog,
  Galaxy,
  List,
  Universe,
} from './types'

// Every request goes to the dashboard's own origin under /api, which nginx (in
// the cluster) or the vite dev server proxies to the operator's admin API. The
// browser never reaches the Kubernetes API server, so no cluster token is
// handled here.
const http = axios.create({
  baseURL: '/api/v1',
  timeout: 30000,
})

// ApiError carries the admin API's user-facing message and, for a validation
// error, the JSON path of the offending field.
export class ApiError extends Error {
  status: number
  field?: string

  constructor(status: number, message: string, field?: string) {
    super(message)
    this.status = status
    this.field = field
  }
}

http.interceptors.response.use(undefined, (err: AxiosError<{ error?: string; field?: string }>) => {
  const status = err.response?.status ?? 0
  const message =
    err.response?.data?.error ??
    (status === 0 ? 'The stellarCD admin API cannot be reached.' : `Request failed (${status}).`)
  return Promise.reject(new ApiError(status, message, err.response?.data?.field))
})

// Errors are left to propagate so React Query can surface them. Swallowing them
// into an empty result would hide RBAC and connectivity failures behind an
// "everything is fine, there is just nothing here" screen.

export async function getConfig(): Promise<AdminConfig> {
  return (await http.get('/config')).data
}

export async function listUniverses(): Promise<Universe[]> {
  return ((await http.get<List<Universe>>('/universes')).data.items ?? [])
}

export async function listGalaxies(universe: string): Promise<Galaxy[]> {
  return ((await http.get<List<Galaxy>>('/galaxies', { params: { universe } })).data.items ?? [])
}

export async function listAstrals(universe: string): Promise<Astral[]> {
  return ((await http.get<List<Astral>>('/astrals', { params: { universe } })).data.items ?? [])
}

export async function getAstral(namespace: string, name: string): Promise<Astral> {
  return (await http.get(`/astrals/${namespace}/${name}`)).data
}

export async function listFlares(namespace: string, astral: string): Promise<Flare[]> {
  return ((await http.get<List<Flare>>(`/astrals/${namespace}/${astral}/flares`)).data.items ?? [])
}

export async function getFlare(namespace: string, name: string): Promise<Flare> {
  return (await http.get(`/flares/${namespace}/${name}`)).data
}

export async function getFlareLog(namespace: string, name: string): Promise<FlareLog> {
  return (await http.get(`/flares/${namespace}/${name}/logs`)).data
}

export async function listSecrets(namespace: string): Promise<string[]> {
  return (await http.get(`/namespaces/${namespace}/secrets`)).data ?? []
}

export async function createAstral(astral: Astral): Promise<Astral> {
  return (await http.post('/astrals', astral)).data
}

export async function updateAstral(astral: Astral): Promise<{ astral: Astral; flare: Flare }> {
  const { namespace, name } = astral.metadata
  return (await http.put(`/astrals/${namespace}/${name}`, astral)).data
}

export async function deleteAstral(namespace: string, name: string): Promise<void> {
  await http.delete(`/astrals/${namespace}/${name}`)
}

export async function createFlare(
  namespace: string,
  astral: string,
  action: 'Plan' | 'Apply',
  unitPath?: string,
): Promise<Flare> {
  return (await http.post(`/astrals/${namespace}/${astral}/flares`, { action, ...(unitPath ? { unitPath } : {}) })).data
}

export async function cancelFlare(namespace: string, name: string): Promise<Flare> {
  return (await http.post(`/flares/${namespace}/${name}/cancel`)).data
}

export async function approveFlare(namespace: string, name: string, approved: boolean): Promise<Flare> {
  return (await http.post(`/flares/${namespace}/${name}/approve`, { approved })).data
}

export async function confirmDestroy(namespace: string, name: string, confirm: string): Promise<Flare> {
  return (await http.post(`/flares/${namespace}/${name}/confirm-destroy`, { confirm })).data
}
