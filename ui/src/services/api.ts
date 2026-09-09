import axios from 'axios'

// Every request goes to the dashboard's own origin under /api, which nginx (in
// the cluster) or the vite dev server proxies to the operator's admin API. The
// browser never reaches the Kubernetes API server, so no cluster token is
// handled here.
const client = axios.create({
  baseURL: '/api/v1',
  timeout: 30000,
})

export type Phase = 'Unknown' | 'Syncing' | 'Synced' | 'Applying' | 'Degraded'

export type ExecutorType = 'Terraform' | 'Terragrunt'

export interface SecretRef {
  name: string
}

// Mirrors GitRepositorySpec in api/v1alpha1: an object, not a bare URL string.
export interface GitRepository {
  url: string
  ref?: string
  secretRef?: SecretRef
}

export interface Condition {
  type: string
  status: 'True' | 'False' | 'Unknown'
  reason?: string
  message?: string
  lastTransitionTime?: string
  observedGeneration?: number
}

export interface StellarApp {
  metadata: {
    name: string
    namespace: string
    creationTimestamp: string
    generation?: number
  }
  spec: {
    gitRepository: GitRepository
    terraformPath: string
    interval?: string
    executor?: ExecutorType
    autoApply?: boolean
  }
  status?: {
    phase?: Phase
    conditions?: Condition[]
    lastSyncTime?: string
    lastError?: string
    observedGeneration?: number
  }
}

export interface CRDInfo {
  // Name is the CRD object name, i.e. "<plural>.<group>".
  name: string
  group: string
  kind: string
  plural: string
  scope: string
  versions: string[]
  storedVersion: string
}

export interface AppEvent {
  type: string
  reason: string
  message: string
  count: number
  firstTimestamp?: string
  lastTimestamp?: string
}

// Errors are left to propagate so React Query can surface them. Swallowing them
// into an empty result would hide RBAC and connectivity failures behind an
// "everything is fine, there is just nothing here" screen.

export async function listStellarApps(namespace?: string): Promise<StellarApp[]> {
  const response = await client.get('/stellarapps', {
    params: namespace ? { namespace } : undefined,
  })
  return response.data?.items ?? []
}

export async function getStellarApp(namespace: string, name: string): Promise<StellarApp> {
  const response = await client.get(`/stellarapps/${namespace}/${name}`)
  return response.data
}

export async function getNamespaces(): Promise<string[]> {
  const response = await client.get('/namespaces')
  return response.data ?? []
}

export async function getStellarAppEvents(namespace: string, name: string): Promise<AppEvent[]> {
  const response = await client.get(`/stellarapps/${namespace}/${name}/events`)
  return response.data ?? []
}

export async function listCRDs(): Promise<CRDInfo[]> {
  const response = await client.get('/crds')
  return response.data ?? []
}
