import { ReactNode, useState } from 'react'
import { ApiError } from '../api/client'
import { useSaveCredential } from '../api/hooks'
import type { CredentialInfo, CredentialKind } from '../api/types'
import { cls, Field } from './form'
import { Banner, Button, ErrorState, Modal } from './ui'

// KINDS describes each credential type: the keys the controller and the
// engines read, and how to enter them.
const KINDS: { id: CredentialKind; label: string; help: string }[] = [
  { id: 'Token', label: 'Git · access token', help: 'A personal or project access token, sent over HTTPS' },
  { id: 'SSHKey', label: 'Git · SSH key', help: 'A deploy key, for ssh:// and git@ URLs' },
  { id: 'BasicAuth', label: 'Git · username and password', help: 'For Git servers without tokens' },
  { id: 'GitHubApp', label: 'Git · GitHub App', help: 'The controller exchanges it for a one-hour token; the key never reaches the runs' },
  { id: 'GCS', label: 'GCS · service account', help: 'For the GCS state backend and the Google provider (GOOGLE_CREDENTIALS)' },
  { id: 'Vault', label: 'Vault · token', help: 'Read by the Vault provider from VAULT_TOKEN and VAULT_ADDR. Add it to an Astral under Environment from Secrets' },
  { id: 'Generic', label: 'Other · key and value pairs', help: 'Each key becomes an environment variable of the runs that use it' },
]

type Values = Record<string, string>

// CredentialDialog creates a Secret, or replaces every value of an existing
// one. Values go to the admin API once and are never shown again.
export default function CredentialDialog({ namespace, existing, onClose }: {
  namespace: string
  existing?: CredentialInfo
  onClose: () => void
}) {
  const save = useSaveCredential(namespace, !existing)
  const [name, setName] = useState(existing?.name ?? '')
  const [kind, setKind] = useState<CredentialKind>(existing?.kind ?? 'Token')
  const [values, setValues] = useState<Values>({})
  const [pairs, setPairs] = useState<{ key: string; value: string }[]>(
    () => (existing?.kind === 'Generic' ? existing.keys.map((key) => ({ key, value: '' })) : [{ key: '', value: '' }]),
  )
  const [errors, setErrors] = useState<Record<string, string>>({})
  const set = (k: string, v: string) => setValues({ ...values, [k]: v })

  const data = (): Values => {
    if (kind === 'Generic') return Object.fromEntries(pairs.filter((p) => p.key.trim()).map((p) => [p.key.trim(), p.value]))
    return Object.fromEntries(Object.entries(values).filter(([, v]) => v !== ''))
  }

  const check = (): Record<string, string> => {
    const e: Record<string, string> = {}
    if (!existing && !/^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/.test(name)) {
      e.name = 'Use lowercase letters, digits, dashes and dots'
    }
    const need = (k: string, msg: string) => { if (!values[k]?.trim()) e[k] = msg }
    switch (kind) {
      case 'Token': need('token', 'Paste the token'); break
      case 'SSHKey': need('sshPrivateKey', 'Paste or load the private key'); break
      case 'BasicAuth': need('username', 'Enter the username'); need('password', 'Enter the password'); break
      case 'GitHubApp':
        need('githubAppID', 'Enter the App ID')
        need('githubAppPrivateKey', 'Paste or load the App private key')
        break
      case 'GCS': {
        need('GOOGLE_CREDENTIALS', 'Paste or load the service account key')
        try {
          if (values.GOOGLE_CREDENTIALS && JSON.parse(values.GOOGLE_CREDENTIALS).type !== 'service_account') {
            e.GOOGLE_CREDENTIALS = 'This JSON is not a service account key'
          }
        } catch {
          if (values.GOOGLE_CREDENTIALS) e.GOOGLE_CREDENTIALS = 'This is not valid JSON'
        }
        break
      }
      case 'Vault': need('VAULT_TOKEN', 'Paste the token'); break
      case 'Generic':
        if (!pairs.some((p) => p.key.trim())) e.pairs = 'Add at least one key'
        else if (pairs.some((p) => p.key.trim() && !p.value)) e.pairs = 'Every key needs a value'
        break
    }
    return e
  }

  const submit = async () => {
    const found = check()
    setErrors(found)
    if (Object.keys(found).length > 0) return
    try {
      await save.mutateAsync({ name: name.trim(), data: data() })
      onClose()
    } catch (err) {
      if (err instanceof ApiError && err.field === 'name') setErrors({ name: err.message })
      else if (err instanceof ApiError && err.field === 'data') setErrors({ pairs: err.message })
    }
  }

  const secretInput = (k: string, label: string, opts: { multiline?: boolean; file?: string; help?: string; optional?: boolean; plain?: boolean } = {}) => (
    <Field label={label} required={!opts.optional} error={errors[k]} help={opts.help}>
      {opts.multiline ? (
        <div className="space-y-1">
          <textarea value={values[k] ?? ''} onChange={(e) => set(k, e.target.value)} rows={5} spellCheck={false}
            autoComplete="off" className={`${cls(errors[k])} font-mono text-xs`} />
          {opts.file && <FileLoad accept={opts.file} onLoad={(text) => set(k, text)} />}
        </div>
      ) : (
        <input type={opts.plain ? 'text' : 'password'} value={values[k] ?? ''} onChange={(e) => set(k, e.target.value)}
          autoComplete="off" className={`${cls(errors[k])} font-mono`} />
      )}
    </Field>
  )

  let fields: ReactNode
  switch (kind) {
    case 'Token':
      fields = secretInput('token', 'Token')
      break
    case 'SSHKey':
      fields = secretInput('sshPrivateKey', 'Private key', { multiline: true, file: '*', help: 'OpenSSH format, without passphrase' })
      break
    case 'BasicAuth':
      fields = <>{secretInput('username', 'Username', { plain: true })}{secretInput('password', 'Password')}</>
      break
    case 'GitHubApp':
      fields = (
        <>
          {secretInput('githubAppID', 'App ID', { plain: true })}
          {secretInput('githubAppInstallationID', 'Installation ID', { plain: true, optional: true, help: 'Optional: found from the repository when empty' })}
          {secretInput('githubAppPrivateKey', 'Private key', { multiline: true, file: '.pem', help: 'The .pem file generated in the App settings' })}
        </>
      )
      break
    case 'GCS':
      fields = secretInput('GOOGLE_CREDENTIALS', 'Service account key', { multiline: true, file: '.json', help: 'The JSON key file of the service account' })
      break
    case 'Vault':
      // TODO(spec): authenticate runs to Vault with the Kubernetes auth method
      // instead of a static token. The runner pod has no ServiceAccount token
      // today, so that needs a projected, audience-bound token first.
      fields = (
        <>
          {secretInput('VAULT_TOKEN', 'Token')}
          {secretInput('VAULT_ADDR', 'Address', { plain: true, optional: true, help: 'e.g. https://vault.example.com:8200, unless the provider block sets it' })}
          {secretInput('VAULT_NAMESPACE', 'Namespace', { plain: true, optional: true, help: 'Vault Enterprise namespace, if any' })}
        </>
      )
      break
    case 'Generic':
      fields = (
        <div className="space-y-2">
          {pairs.map((p, i) => (
            <div key={i} className="flex gap-2">
              <input value={p.key} placeholder="KEY" onChange={(e) => setPairs(pairs.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))}
                className={`${cls()} w-2/5 font-mono`} />
              <input type="password" value={p.value} placeholder="value" autoComplete="off"
                onChange={(e) => setPairs(pairs.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))}
                className={`${cls()} flex-1 font-mono`} />
              <button type="button" onClick={() => setPairs(pairs.filter((_, j) => j !== i))} className="px-2 text-slate-400 hover:text-red-600" aria-label="Remove">✕</button>
            </div>
          ))}
          <Button size="sm" onClick={() => setPairs([...pairs, { key: '', value: '' }])}>Add a key</Button>
          {errors.pairs && <p className="text-xs text-red-600">{errors.pairs}</p>}
        </div>
      )
      break
  }

  return (
    <Modal
      title={existing ? `Replace ${existing.name}` : 'New credential'}
      onClose={onClose}
      footer={
        <>
          <Button kind="ghost" onClick={onClose}>Cancel</Button>
          <Button kind="primary" busy={save.isLoading} onClick={() => void submit()}>{existing ? 'Replace values' : 'Create credential'}</Button>
        </>
      }
    >
      <div className="space-y-4">
        {!existing && (
          <Field label="Name" required error={errors.name} help="The name of the Kubernetes Secret">
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="github-token" className={`${cls(errors.name)} font-mono`} />
          </Field>
        )}
        <Field label="Type" help={KINDS.find((k) => k.id === kind)?.help}>
          <select value={kind} onChange={(e) => { setKind(e.target.value as CredentialKind); setValues({}); setErrors({}) }} className={cls()}>
            {KINDS.map((k) => <option key={k.id} value={k.id}>{k.label}</option>)}
          </select>
        </Field>
        {fields}
        <Banner tone="blue">
          {existing ? 'Current values are never shown. Saving replaces every key of the Secret. ' : 'Values are sent once and never shown again. '}
          The admin API is not authenticated yet: use this on a trusted cluster only.
        </Banner>
        {save.error != null && !(save.error instanceof ApiError && (save.error.field === 'name' || save.error.field === 'data')) && (
          <ErrorState error={save.error} />
        )}
      </div>
    </Modal>
  )
}

function FileLoad({ accept, onLoad }: { accept: string; onLoad: (text: string) => void }) {
  return (
    <label className="inline-flex cursor-pointer items-center gap-1 text-xs font-medium text-indigo-600 hover:underline">
      Load from a file
      <input type="file" accept={accept} className="hidden" onChange={(e) => {
        const file = e.target.files?.[0]
        if (file) void file.text().then(onLoad)
        e.target.value = ''
      }} />
    </label>
  )
}
