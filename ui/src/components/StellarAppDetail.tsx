import { useState } from 'react'
import { useQuery } from 'react-query'
import { getStellarAppEvents, StellarApp } from '../services/api'

interface Props {
  app: StellarApp
  onBack: () => void
}

export default function StellarAppDetail({ app, onBack }: Props) {
  const [showEvents, setShowEvents] = useState(false)

  // The controller reports progress and failures as Kubernetes Events, so those
  // are the per-app history. Terraform run output will be surfaced separately
  // once the executor lands; it does not live in the operator's pod logs.
  const { data: events, isLoading: eventsLoading, error: eventsError } = useQuery(
    ['events', app.metadata.namespace, app.metadata.name],
    () => getStellarAppEvents(app.metadata.namespace, app.metadata.name),
    { enabled: showEvents, refetchInterval: showEvents ? 5000 : false }
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
            <p className="font-mono text-gray-900 break-all">{app.spec.gitRepository.url}</p>
          </div>
          <div>
            <h3 className="text-sm font-semibold text-gray-700 mb-2">Tracked Ref</h3>
            <p className="font-mono text-gray-900">{app.spec.gitRepository.ref || 'main'}</p>
          </div>
          <div>
            <h3 className="text-sm font-semibold text-gray-700 mb-2">Terraform Path</h3>
            <p className="font-mono text-gray-900">{app.spec.terraformPath}</p>
          </div>
          <div>
            <h3 className="text-sm font-semibold text-gray-700 mb-2">Executor</h3>
            <p className="font-mono text-gray-900">{app.spec.executor || 'Terraform'}</p>
          </div>
          <div>
            <h3 className="text-sm font-semibold text-gray-700 mb-2">Reconciliation Interval</h3>
            <p className="font-mono text-gray-900">{app.spec.interval || '5m'}</p>
          </div>
          <div>
            <h3 className="text-sm font-semibold text-gray-700 mb-2">Auto Apply</h3>
            <p className="font-mono text-gray-900">{app.spec.autoApply ? 'Enabled' : 'Disabled'}</p>
          </div>
          <div>
            <h3 className="text-sm font-semibold text-gray-700 mb-2">Credentials Secret</h3>
            <p className="font-mono text-gray-900">
              {app.spec.gitRepository.secretRef?.name || 'None'}
            </p>
          </div>
          <div>
            <h3 className="text-sm font-semibold text-gray-700 mb-2">Created</h3>
            <p className="text-gray-900">{new Date(app.metadata.creationTimestamp).toLocaleString()}</p>
          </div>
        </div>

        {!!app.status?.conditions?.length && (
          <div className="mb-6">
            <h3 className="text-sm font-semibold text-gray-700 mb-2">Conditions</h3>
            <div className="space-y-2">
              {app.status.conditions.map((c) => (
                <div key={c.type} className="border border-gray-200 rounded p-3 text-sm">
                  <div className="flex items-center gap-2">
                    <span className="font-semibold text-gray-900">{c.type}</span>
                    <span
                      className={`px-2 py-0.5 rounded text-xs font-mono ${
                        c.status === 'True'
                          ? 'bg-green-100 text-green-800'
                          : c.status === 'False'
                            ? 'bg-red-100 text-red-800'
                            : 'bg-gray-100 text-gray-800'
                      }`}
                    >
                      {c.status}
                    </span>
                    {c.reason && <span className="text-gray-500 font-mono text-xs">{c.reason}</span>}
                  </div>
                  {c.message && <p className="text-gray-600 mt-1">{c.message}</p>}
                </div>
              ))}
            </div>
          </div>
        )}

        {app.status?.lastError && (
          <div className="bg-red-50 border border-red-200 rounded-lg p-4 mb-6">
            <h3 className="text-red-900 font-semibold mb-2">Last Error</h3>
            <p className="text-red-800 font-mono text-sm">{app.status.lastError}</p>
          </div>
        )}

        <div className="border-t pt-6">
          <button
            onClick={() => setShowEvents(!showEvents)}
            className="px-4 py-2 bg-blue-500 text-white rounded-lg hover:bg-blue-600 mb-4"
          >
            {showEvents ? 'Hide' : 'Show'} Events
          </button>

          {showEvents && (
            <div className="bg-gray-50 border border-gray-200 rounded-lg p-4">
              {eventsLoading ? (
                <p className="text-gray-500">Loading events...</p>
              ) : eventsError ? (
                <p className="text-red-700">Failed to retrieve events.</p>
              ) : events && events.length > 0 ? (
                <div className="space-y-2 max-h-96 overflow-y-auto">
                  {events.map((e, i) => (
                    <div key={`${e.reason}-${e.lastTimestamp}-${i}`} className="text-sm">
                      <div className="flex items-center gap-2">
                        <span
                          className={`px-2 py-0.5 rounded text-xs font-mono ${
                            e.type === 'Warning'
                              ? 'bg-red-100 text-red-800'
                              : 'bg-blue-100 text-blue-800'
                          }`}
                        >
                          {e.reason}
                        </span>
                        {e.lastTimestamp && (
                          <span className="text-xs text-gray-500">
                            {new Date(e.lastTimestamp).toLocaleString()}
                          </span>
                        )}
                        {e.count > 1 && <span className="text-xs text-gray-500">×{e.count}</span>}
                      </div>
                      <p className="text-gray-700 mt-0.5">{e.message}</p>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="text-gray-500">No events recorded for this StellarApp.</p>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
