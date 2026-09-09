import { useState } from 'react'
import { useQuery } from 'react-query'
import { listCRDs } from '../services/api'

export default function AdminMenu() {
  const [showCRDs, setShowCRDs] = useState(false)

  // Read through the operator's admin API, which reports the CRDs actually
  // installed in the cluster rather than a hard-coded list.
  const { data: crds, isLoading, error } = useQuery('crds', listCRDs, {
    enabled: showCRDs,
    refetchInterval: showCRDs ? 30000 : false,
  })

  return (
    <div className="border-t border-gray-200 p-4 mt-4">
      <button
        onClick={() => setShowCRDs(!showCRDs)}
        className="px-4 py-2 bg-purple-600 text-white rounded-lg hover:bg-purple-700 font-semibold"
      >
        {showCRDs ? '✕ Close Admin' : '⚙ Admin'}
      </button>

      {showCRDs && (
        <div className="mt-4 bg-gray-50 border border-gray-200 rounded-lg p-4">
          <h2 className="text-xl font-bold text-gray-900 mb-4">Deployed CRDs</h2>

          {isLoading ? (
            <div className="text-center py-4">
              <div className="inline-block animate-spin rounded-full h-6 w-6 border-b-2 border-purple-600"></div>
            </div>
          ) : error ? (
            <p className="text-red-700 text-center py-4">Failed to load CRDs.</p>
          ) : crds && crds.length > 0 ? (
            <div className="space-y-2">
              {crds.map((crd) => (
                <div key={crd.name} className="bg-white border border-gray-200 rounded p-3">
                  <div className="flex justify-between items-start">
                    <div>
                      <p className="font-semibold text-gray-900 font-mono">{crd.name}</p>
                      <p className="text-sm text-gray-600">
                        Kind: {crd.kind} | Scope: {crd.scope}
                      </p>
                    </div>
                    <div className="flex gap-1 flex-wrap justify-end">
                      {crd.versions.map((v) => (
                        <span
                          key={v}
                          className={`px-2 py-1 text-sm rounded font-mono ${
                            v === crd.storedVersion
                              ? 'bg-blue-100 text-blue-800'
                              : 'bg-gray-100 text-gray-600'
                          }`}
                          title={v === crd.storedVersion ? 'Storage version' : 'Served version'}
                        >
                          {v}
                        </span>
                      ))}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <p className="text-gray-500 text-center py-4">No stellarCD CRDs deployed</p>
          )}
        </div>
      )}
    </div>
  )
}
