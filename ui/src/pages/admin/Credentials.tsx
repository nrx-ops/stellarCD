import { useState } from 'react'
import { useParams } from 'react-router-dom'
import { useCredentials, useDeleteCredential, useUniverse, useWrites } from '../../api/hooks'
import type { CredentialInfo } from '../../api/types'
import CredentialDialog from '../../components/CredentialDialog'
import { Badge, Banner, Button, ConfirmDialog, EmptyState, ErrorState, Loading, Mono } from '../../components/ui'
import { ago, fullDate } from '../../lib/format'
import AdminFrame from './AdminFrame'

const KIND_LABELS: Record<string, string> = {
  Token: 'Git token',
  SSHKey: 'Git SSH key',
  BasicAuth: 'Git password',
  GitHubApp: 'GitHub App',
  GCS: 'GCS',
  Vault: 'Vault',
  Generic: 'Generic',
}

// Credentials lists the Secrets of the Universe's namespace, what uses them,
// and never their values.
export default function Credentials() {
  const { universe } = useParams()
  const writes = useWrites()
  const ns = useUniverse(universe).data?.status?.namespace
  const { data, error, isLoading } = useCredentials(ns)
  const remove = useDeleteCredential(ns ?? '')
  const [editing, setEditing] = useState<CredentialInfo | 'new'>()
  const [deleting, setDeleting] = useState<CredentialInfo>()

  const action = writes.enabled && ns
    ? <Button kind="primary" onClick={() => setEditing('new')}>New credential</Button>
    : undefined

  return (
    <AdminFrame tab="credentials" actions={action}>
      {ns && <p className="text-sm text-slate-500">Secrets of the namespace <Mono>{ns}</Mono>. Their values are never shown.</p>}
      {!ns && !isLoading && <Banner tone="red">The Universe has no namespace yet.</Banner>}
      {isLoading && <Loading />}
      {error != null && <ErrorState error={error} />}
      {data && data.length === 0 && (
        <EmptyState title="No credential yet" text="A run needs at least Git credentials to clone a private repository and state credentials for its backend." action={action} />
      )}
      {data && data.length > 0 && (
        <div className="overflow-hidden rounded-lg border border-slate-200 bg-white">
          <table className="min-w-full divide-y divide-slate-200 text-sm">
            <thead className="bg-slate-50 text-left text-xs font-medium uppercase tracking-wide text-slate-500">
              <tr>
                <th className="px-4 py-3">Name</th>
                <th className="px-4 py-3">Type</th>
                <th className="px-4 py-3">Keys</th>
                <th className="px-4 py-3">Used by</th>
                <th className="px-4 py-3">Created</th>
                <th className="px-4 py-3" />
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {data.map((c) => (
                <tr key={c.name} className="align-top hover:bg-slate-50">
                  <td className="px-4 py-3"><Mono className="font-semibold text-slate-900">{c.name}</Mono></td>
                  <td className="px-4 py-3"><Badge tone="gray">{KIND_LABELS[c.kind] ?? c.kind}</Badge></td>
                  <td className="px-4 py-3"><Mono className="text-slate-600">{c.keys.join(', ')}</Mono></td>
                  <td className="px-4 py-3">
                    {c.usedBy.length === 0 && <span className="text-slate-400">Unused</span>}
                    <ul className="space-y-0.5">
                      {c.usedBy.map((u) => (
                        <li key={`${u.kind}/${u.name}/${u.role}`} className="text-slate-700">
                          {u.kind} <Mono>{u.name}</Mono> <span className="text-slate-400">· {u.role}</span>
                        </li>
                      ))}
                    </ul>
                  </td>
                  <td className="px-4 py-3 text-slate-500" title={fullDate(c.createdAt)}>{ago(c.createdAt)}</td>
                  <td className="whitespace-nowrap px-4 py-3 text-right">
                    {writes.enabled && (
                      <div className="flex justify-end gap-2">
                        <Button size="sm" onClick={() => setEditing(c)}>Replace</Button>
                        <Button size="sm" kind="danger-secondary" disabled={c.usedBy.length > 0}
                          title={c.usedBy.length > 0 ? 'Used by a Galaxy or an Astral: remove it from them first' : undefined}
                          onClick={() => setDeleting(c)}>
                          Delete
                        </Button>
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {editing && ns && (
        <CredentialDialog namespace={ns} existing={editing === 'new' ? undefined : editing} onClose={() => setEditing(undefined)} />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete credential"
          confirmLabel="Delete"
          kind="danger"
          expect={deleting.name}
          busy={remove.isLoading}
          error={remove.error}
          onClose={() => setDeleting(undefined)}
          onConfirm={() => remove.mutate(deleting.name, { onSuccess: () => setDeleting(undefined) })}
        >
          <p>The Secret is deleted from the cluster. Nothing uses it today.</p>
        </ConfirmDialog>
      )}
    </AdminFrame>
  )
}
