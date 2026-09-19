import { useState } from 'react'
import { useQuery } from 'react-query'
import {
  listRepositories,
  getNamespaces,
  AuthMethod,
  RepositoryInfo,
  RepositoryState,
} from '../services/api'

const ALL_NAMESPACES = ''

// Connection states are grouped by what an operator should do about them, not
// by HTTP status: green needs nothing, red needs a credential or a URL fixed,
// amber means the check could not run at all.
const STATE_STYLE: Record<RepositoryState, string> = {
  Connected: 'bg-green-100 text-green-800',
  Unauthorized: 'bg-red-100 text-red-800',
  NotFound: 'bg-red-100 text-red-800',
  Misconfigured: 'bg-red-100 text-red-800',
  Unreachable: 'bg-orange-100 text-orange-800',
  Unsupported: 'bg-gray-100 text-gray-700',
  Unknown: 'bg-gray-100 text-gray-700',
}

const STATE_LABEL: Record<RepositoryState, string> = {
  Connected: 'Connected',
  Unauthorized: 'Auth failed',
  NotFound: 'Not found',
  Misconfigured: 'Misconfigured',
  Unreachable: 'Unreachable',
  Unsupported: 'Not checkable',
  Unknown: 'Unknown',
}

const AUTH_LABEL: Record<AuthMethod, string> = {
  None: 'anonymous',
  SSHKey: 'SSH key',
  GitHubApp: 'GitHub App',
  Token: 'token',
  BasicAuth: 'username + password',
  Unusable: 'unusable secret',
}

function StateBadge({ state }: { state: RepositoryState }) {
  return (
    <span className={`px-3 py-1 rounded-full text-sm font-medium ${STATE_STYLE[state] ?? STATE_STYLE.Unknown}`}>
      {STATE_LABEL[state] ?? state}
    </span>
  )
}

export default function RepositoryList() {
  const [namespace, setNamespace] = useState(ALL_NAMESPACES)

  const { data: namespaces } = useQuery('namespaces', getNamespaces)

  const { data: repos, isLoading, error, refetch, isFetching } = useQuery(
    ['repositories', namespace],
    () => listRepositories(namespace || undefined),
    // The operator caches each verdict for a minute, so polling here only costs
    // a Kubernetes list; it does not re-hit the git remotes.
    { refetchInterval: 15000 }
  )

  const connected = (repos || []).filter((r) => r.connection.state === 'Connected').length
  const broken = (repos || []).filter((r) =>
    ['Unauthorized', 'NotFound', 'Misconfigured'].includes(r.connection.state)
  ).length

  return (
    <div className="p-6 max-w-6xl mx-auto">
      <div className="mb-6">
        <div className="flex items-baseline gap-3 mb-4">
          <h1 className="text-3xl font-bold text-gray-900">Repositories</h1>
          {repos && repos.length > 0 && (
            <span className="text-sm text-gray-600">
              {connected} connected
              {broken > 0 && <span className="text-red-700"> · {broken} failing</span>}
            </span>
          )}
        </div>

        <div className="flex gap-4 mb-2">
          <select
            value={namespace}
            onChange={(e) => setNamespace(e.target.value)}
            className="px-4 py-2 border border-gray-300 rounded-lg"
          >
            <option value={ALL_NAMESPACES}>All namespaces</option>
            {(namespaces || []).map((ns) => (
              <option key={ns} value={ns}>
                {ns}
              </option>
            ))}
          </select>

          <button
            onClick={() => refetch()}
            disabled={isFetching}
            className="px-4 py-2 bg-blue-500 text-white rounded-lg hover:bg-blue-600 disabled:opacity-50"
          >
            {isFetching ? 'Checking…' : 'Refresh'}
          </button>
        </div>

        <p className="text-xs text-gray-500">
          Each status is a live check from the operator against the git remote, using the same
          credentials a run would. Verdicts are cached for a minute.
        </p>
      </div>

      {isLoading && (
        <div className="text-center py-8">
          <div className="inline-block animate-spin rounded-full h-8 w-8 border-b-2 border-blue-500"></div>
        </div>
      )}

      {!!error && (
        <div className="bg-red-50 border border-red-200 rounded-lg p-4 mb-4">
          <p className="text-red-800">
            Failed to load repositories. Check that the stellarCD admin API is reachable.
          </p>
        </div>
      )}

      {repos && repos.length === 0 && (
        <div className="text-center py-8 text-gray-500">
          {namespace
            ? `No repositories configured in namespace "${namespace}"`
            : 'No repositories configured. Create an Astral to track one.'}
        </div>
      )}

      <div className="space-y-4">
        {(repos || []).map((repo: RepositoryInfo) => (
          <div
            key={`${repo.kind}/${repo.namespace}/${repo.name}`}
            className="bg-white border border-gray-200 rounded-lg p-4"
          >
            <div className="flex justify-between items-start gap-4">
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <h2 className="text-lg font-semibold text-gray-900 truncate">
                    {repo.displayName || repo.name}
                  </h2>
                  <span className="px-2 py-0.5 rounded bg-gray-100 text-gray-600 text-xs font-medium">
                    {repo.kind}
                  </span>
                </div>
                <p className="text-sm text-gray-600">
                  Namespace: <span className="font-mono">{repo.namespace}</span>
                  {repo.galaxy && (
                    <>
                      {' · Galaxy: '}
                      <span className="font-mono">{repo.galaxy}</span>
                    </>
                  )}
                </p>
              </div>
              <StateBadge state={repo.connection.state} />
            </div>

            <div className="mt-3 grid grid-cols-1 md:grid-cols-2 gap-4 text-sm">
              <div className="min-w-0">
                <p className="text-gray-600">Repository</p>
                <a
                  href={repo.url}
                  target="_blank"
                  rel="noreferrer"
                  className="font-mono text-blue-700 hover:underline break-all"
                >
                  {repo.url}
                </a>
                {repo.branch && <span className="text-gray-500 font-mono"> @ {repo.branch}</span>}
              </div>
              <div className="min-w-0">
                <p className="text-gray-600">Module path</p>
                <p className="font-mono text-gray-900 break-all">{repo.path || '.'}</p>
              </div>
            </div>

            <div className="mt-3 text-sm">
              <p className="text-gray-600">Credentials</p>
              <p className="text-gray-900">
                {AUTH_LABEL[repo.connection.authMethod] ?? repo.connection.authMethod}
                {repo.secretName && (
                  <>
                    {' from Secret '}
                    <span className="font-mono">{repo.secretName}</span>
                    {repo.secretScope && (
                      <span className="text-gray-500"> (set on the {repo.secretScope})</span>
                    )}
                  </>
                )}
                {repo.secretMissing && (
                  <span className="text-red-700 font-medium"> — this Secret does not exist</span>
                )}
              </p>
            </div>

            <p
              className={`text-xs mt-3 ${
                repo.connection.state === 'Connected' ? 'text-gray-500' : 'text-red-700'
              }`}
            >
              {repo.connection.message}
              {repo.connection.checkedAt && (
                <span className="text-gray-400">
                  {' · checked '}
                  {new Date(repo.connection.checkedAt).toLocaleTimeString()}
                </span>
              )}
            </p>
          </div>
        ))}
      </div>
    </div>
  )
}
