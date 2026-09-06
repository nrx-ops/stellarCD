import React, { useState } from 'react'
import { useQuery } from 'react-query'
import { getStellarAppLogs, StellarApp } from '../services/api'

interface Props {
  app: StellarApp
  onBack: () => void
}

export default function StellarAppDetail({ app, onBack }: Props) {
  const [showLogs, setShowLogs] = useState(false)

  const { data: logs, isLoading: logsLoading } = useQuery(
    ['logs', app.metadata.namespace, app.metadata.name],
    () => getStellarAppLogs(app.metadata.namespace, app.metadata.name),
    { refetchInterval: showLogs ? 2000 : false }
  )

  return (
    <div className="p-6 max-w-4xl mx-auto">
      <button
        onClick={onBack}
        className="mb-4 px-4 py-2 text-gray-600 hover:text-gray-900 flex items-center gap-2"
      >
        ← Back
      </button>

      <div className="bg-white border border-gray-200 rounded-lg p-6">
        <div className="flex justify-between items-start mb-6">
          <div>
            <h1 className="text-3xl font-bold text-gray-900">{app.metadata.name}</h1>
            <p className="text-gray-600">
              Namespace: <span className="font-mono">{app.metadata.namespace}</span>
            </p>
          </div>
          <span className="px-4 py-2 rounded-full font-medium bg-green-100 text-green-800">
            {app.status?.phase || 'Unknown'}
          </span>
        </div>

        <div className="grid grid-cols-2 gap-6 mb-6">
          <div>
            <h3 className="text-sm font-semibold text-gray-700 mb-2">Git Repository</h3>
            <p className="font-mono text-gray-900 break-all">{app.spec.gitRepository}</p>
          </div>
          <div>
            <h3 className="text-sm font-semibold text-gray-700 mb-2">Terraform Path</h3>
            <p className="font-mono text-gray-900">{app.spec.terraformPath}</p>
          </div>
          <div>
            <h3 className="text-sm font-semibold text-gray-700 mb-2">Reconciliation Interval</h3>
            <p className="font-mono text-gray-900">{app.spec.interval}</p>
          </div>
          <div>
            <h3 className="text-sm font-semibold text-gray-700 mb-2">Created</h3>
            <p className="text-gray-900">{new Date(app.metadata.creationTimestamp).toLocaleString()}</p>
          </div>
        </div>

        {app.status?.lastError && (
          <div className="bg-red-50 border border-red-200 rounded-lg p-4 mb-6">
            <h3 className="text-red-900 font-semibold mb-2">Last Error</h3>
            <p className="text-red-800 font-mono text-sm">{app.status.lastError}</p>
          </div>
        )}

        <div className="border-t pt-6">
          <button
            onClick={() => setShowLogs(!showLogs)}
            className="px-4 py-2 bg-blue-500 text-white rounded-lg hover:bg-blue-600 mb-4"
          >
            {showLogs ? 'Hide' : 'Show'} Logs
          </button>

          {showLogs && (
            <div className="bg-gray-900 rounded-lg p-4">
              {logsLoading ? (
                <p className="text-gray-400">Loading logs...</p>
              ) : (
                <pre className="text-gray-200 text-sm overflow-x-auto max-h-96">
                  {logs}
                </pre>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
