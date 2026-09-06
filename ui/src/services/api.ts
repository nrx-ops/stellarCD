import axios from 'axios'

const API_BASE = '/api'

const client = axios.create({
  baseURL: API_BASE,
  timeout: 30000,
})

export interface StellarApp {
  metadata: {
    name: string
    namespace: string
    creationTimestamp: string
  }
  spec: {
    gitRepository: string
    terraformPath: string
    interval: string
  }
  status?: {
    phase: 'Syncing' | 'Synced' | 'Degraded' | 'Applying'
    lastSyncTime: string
    lastError?: string
  }
}

export async function listStellarApps(namespace: string = 'default'): Promise<StellarApp[]> {
  try {
    const response = await client.get(
      `/apis/stellar.sh/v1alpha1/namespaces/${namespace}/stellarapps`
    )
    return response.data.items || []
  } catch (error) {
    console.error('Failed to list StellarApps:', error)
    throw error
  }
}

export async function getStellarApp(namespace: string, name: string): Promise<StellarApp> {
  const response = await client.get(
    `/apis/stellar.sh/v1alpha1/namespaces/${namespace}/stellarapps/${name}`
  )
  return response.data
}

export async function getNamespaces(): Promise<string[]> {
  try {
    const response = await client.get('/api/v1/namespaces')
    return (response.data.items || []).map((ns: any) => ns.metadata.name)
  } catch (error) {
    console.error('Failed to list namespaces:', error)
    return ['default']
  }
}

export async function getStellarAppLogs(namespace: string, name: string): Promise<string> {
  try {
    const response = await client.get(
      `/api/v1/namespaces/${namespace}/pods`,
      { params: { labelSelector: `app.kubernetes.io/name=stellarcd,app.kubernetes.io/instance=${name}` } }
    )
    const pods = response.data.items || []
    if (pods.length === 0) return 'No logs available'

    const logResponse = await client.get(
      `/api/v1/namespaces/${namespace}/pods/${pods[0].metadata.name}/log`
    )
    return logResponse.data
  } catch (error) {
    console.error('Failed to get logs:', error)
    return 'Failed to retrieve logs'
  }
}
