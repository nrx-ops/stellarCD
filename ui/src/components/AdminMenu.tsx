import { useState } from 'react'
import { useQuery } from 'react-query'
import axios from 'axios'

interface CRDInfo {
  name: string
  version: string
  group: string
  kind: string
}

export default function AdminMenu() {
  const [showCRDs, setShowCRDs] = useState(false)

  const { data: crds, isLoading } = useQuery<CRDInfo[]>(
    'crds',
    async () => {
      try {
        const response = await axios.get('/api/v1/admin/crds')
        return response.data
      } catch {
        return []
      }
    },
    { refetchInterval: 30000 }
  )

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
          ) : crds && crds.length > 0 ? (
            <div className="space-y-2">
              {crds.map((crd) => (
                <div
                  key={`${crd.group}/${crd.kind}`}
                  className="bg-white border border-gray-200 rounded p-3"
                >
                  <div className="flex justify-between items-start">
                    <div>
                      <p className="font-semibold text-gray-900">{crd.name}</p>
                      <p className="text-sm text-gray-600">
                        Kind: {crd.kind} | Group: {crd.group}
                      </p>
                    </div>
                    <span className="px-2 py-1 bg-blue-100 text-blue-800 text-sm rounded font-mono">
                      v{crd.version}
                    </span>
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <p className="text-gray-500 text-center py-4">No CRDs deployed</p>
          )}
        </div>
      )}
    </div>
  )
}
