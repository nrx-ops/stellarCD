import React, { useState } from 'react'
import { useQuery } from 'react-query'
import { listStellarApps, getNamespaces, StellarApp } from '../services/api'
import StellarAppDetail from './StellarAppDetail'

export default function StellarAppList() {
  const [selectedApp, setSelectedApp] = useState<StellarApp | null>(null)
  const [namespace, setNamespace] = useState('default')

  const { data: namespaces, isLoading: namespacesLoading } = useQuery(
    'namespaces',
    getNamespaces
  )

  const { data: apps, isLoading, error, refetch } = useQuery(
    ['stellarApps', namespace],
    () => listStellarApps(namespace),
    { refetchInterval: 5000 }
  )

  const getStatusColor = (status?: string) => {
    switch (status) {
      case 'Synced':
        return 'bg-green-100 text-green-800'
      case 'Degraded':
        return 'bg-red-100 text-red-800'
      case 'Syncing':
        return 'bg-blue-100 text-blue-800'
      case 'Applying':
        return 'bg-yellow-100 text-yellow-800'
      default:
        return 'bg-gray-100 text-gray-800'
    }
  }

  if (selectedApp) {
    return <StellarAppDetail app={selectedApp} onBack={() => setSelectedApp(null)} />
  }

  return (
    <div className="p-6 max-w-6xl mx-auto">
      <div className="mb-6">
        <h1 className="text-3xl font-bold text-gray-900 mb-4">StellarCD Apps</h1>

        <div className="flex gap-4 mb-4">
          <select
            value={namespace}
            onChange={(e) => setNamespace(e.target.value)}
            className="px-4 py-2 border border-gray-300 rounded-lg"
          >
            {(namespaces || []).map((ns) => (
              <option key={ns} value={ns}>
                {ns}
              </option>
            ))}
          </select>

          <button
            onClick={() => refetch()}
            className="px-4 py-2 bg-blue-500 text-white rounded-lg hover:bg-blue-600"
          >
            Refresh
          </button>
        </div>
      </div>

      {isLoading && (
        <div className="text-center py-8">
          <div className="inline-block animate-spin rounded-full h-8 w-8 border-b-2 border-blue-500"></div>
        </div>
      )}

      {error && (
        <div className="bg-red-50 border border-red-200 rounded-lg p-4 mb-4">
          <p className="text-red-800">Failed to load StellarApps. Make sure the API proxy is configured.</p>
        </div>
      )}

      {apps && apps.length === 0 && (
        <div className="text-center py-8 text-gray-500">
          No StellarApps found in namespace "{namespace}"
        </div>
      )}

      <div className="space-y-4">
        {(apps || []).map((app) => (
          <div
            key={`${app.metadata.namespace}/${app.metadata.name}`}
            onClick={() => setSelectedApp(app)}
            className="bg-white border border-gray-200 rounded-lg p-4 hover:shadow-lg cursor-pointer transition-shadow"
          >
            <div className="flex justify-between items-start">
              <div className="flex-1">
                <h2 className="text-lg font-semibold text-gray-900">
                  {app.metadata.name}
                </h2>
                <p className="text-sm text-gray-600">
                  Namespace: <span className="font-mono">{app.metadata.namespace}</span>
                </p>
              </div>
              <span className={`px-3 py-1 rounded-full text-sm font-medium ${getStatusColor(app.status?.phase)}`}>
                {app.status?.phase || 'Unknown'}
              </span>
            </div>

            <div className="mt-3 grid grid-cols-2 gap-4 text-sm">
              <div>
                <p className="text-gray-600">Git Repository</p>
                <p className="font-mono text-gray-900">{app.spec.gitRepository}</p>
              </div>
              <div>
                <p className="text-gray-600">Terraform Path</p>
                <p className="font-mono text-gray-900">{app.spec.terraformPath}</p>
              </div>
            </div>

            {app.status?.lastSyncTime && (
              <p className="text-xs text-gray-500 mt-2">
                Last sync: {new Date(app.status.lastSyncTime).toLocaleString()}
              </p>
            )}

            {app.status?.lastError && (
              <p className="text-xs text-red-600 mt-1 truncate">Error: {app.status.lastError}</p>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
