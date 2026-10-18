import { ReactNode } from 'react'
import { stringify } from 'yaml'
import type { Astral, Engine, Galaxy, TerragruntMode } from '../api/types'
import { parseDuration } from '../lib/format'
import { Mono, Modal } from './ui'

// FormState holds the fields of SCR-Création d'un Astral, as the user types
// them. Empty means "inherit from the Galaxy" where the Galaxy has a value.
export interface FormState {
  galaxy: string
  name: string
  url: string
  branch: string
  path: string
  gitSecret: string
  engine: Engine
  engineVersion: string
  terragrunt: boolean
  terragruntVersion: string
  terragruntMode: TerragruntMode
  interval: string
  backendSecret: string
  cloudSecrets: string[]
  autoApply: boolean
}

export type FormErrors = Partial<Record<keyof FormState, string>>

export function emptyForm(galaxy = ''): FormState {
  return {
    galaxy, name: '', url: '', branch: '', path: '', gitSecret: '',
    engine: 'Terraform', engineVersion: '',
    terragrunt: false, terragruntVersion: '', terragruntMode: 'Unit',
    interval: '', backendSecret: '', cloudSecrets: [], autoApply: false,
  }
}

export function formFromAstral(a: Astral, galaxy?: Galaxy): FormState {
  const repo = a.spec.vcsRepository
  const inheritedURL = !!galaxy?.spec.defaultVCSURL && repo.url === galaxy.spec.defaultVCSURL
  return {
    galaxy: a.spec.galaxyRef,
    name: a.metadata.name,
    url: inheritedURL ? '' : repo.url,
    branch: repo.branch ?? '',
    path: repo.path === '.' ? '' : repo.path ?? '',
    gitSecret: repo.secretRef?.name ?? '',
    engine: a.spec.engine ?? 'Terraform',
    engineVersion: a.spec.engineVersion ?? '',
    terragrunt: !!a.spec.terragrunt?.enabled,
    terragruntVersion: a.spec.terragrunt?.version ?? '',
    terragruntMode: a.spec.terragrunt?.mode ?? 'Unit',
    interval: a.spec.driftDetection?.enabled ? a.spec.driftDetection.interval ?? '' : '',
    backendSecret: a.spec.backendSecretRef?.name ?? '',
    cloudSecrets: (a.spec.secrets ?? []).map((s) => s.name),
    autoApply: !!a.spec.autoApply,
  }
}

// astralFromForm builds the resource. When editing, `base` keeps every field
// the form does not show (variables, hooks, dependsOn…) untouched.
export function astralFromForm(f: FormState, galaxy: Galaxy | undefined, base?: Astral): Astral {
  const spec = { ...(base?.spec ?? {}) } as Astral['spec']
  spec.universeRef = galaxy?.spec.universeRef ?? base?.spec.universeRef ?? ''
  spec.galaxyRef = f.galaxy
  spec.vcsRepository = {
    ...(base?.spec.vcsRepository ?? {}),
    url: f.url.trim() || galaxy?.spec.defaultVCSURL || '',
    branch: f.branch.trim() || 'main',
    path: f.path.trim() || '.',
    secretRef: f.gitSecret ? { name: f.gitSecret } : undefined,
  }
  spec.engine = f.engine
  spec.engineVersion = f.engineVersion.trim() || undefined
  spec.terragrunt = f.terragrunt
    ? {
        ...(base?.spec.terragrunt ?? {}),
        enabled: true,
        version: f.terragruntVersion.trim() || undefined,
        mode: f.terragruntMode,
        // excludedUnits only applies to run-all.
        excludedUnits: f.terragruntMode === 'RunAll' ? base?.spec.terragrunt?.excludedUnits : undefined,
      }
    : undefined
  spec.driftDetection = f.interval.trim() ? { enabled: true, interval: f.interval.trim() } : undefined
  // Cloud credentials keep the Secret's own key names (envPrefix ""), which is
  // what provider SDKs read: AWS_ACCESS_KEY_ID, ARM_CLIENT_ID…
  const previous = new Map((base?.spec.secrets ?? []).map((s) => [s.name, s]))
  spec.secrets = f.cloudSecrets.length
    ? f.cloudSecrets.map((name) => previous.get(name) ?? { name, envPrefix: '' })
    : undefined
  spec.backendSecretRef = f.backendSecret ? { name: f.backendSecret } : undefined
  spec.autoApply = f.autoApply

  return {
    apiVersion: 'core.stellarcd.io/v1alpha1',
    kind: 'Astral',
    metadata: {
      name: f.name.trim(),
      namespace: galaxy?.metadata.namespace ?? base?.metadata.namespace,
      ...(base?.metadata.resourceVersion ? { resourceVersion: base.metadata.resourceVersion } : {}),
    },
    spec,
  }
}

// needsCredentials mirrors the controller: an SSH URL is private by default.
function needsCredentials(url: string): boolean {
  return url.startsWith('git@') || url.startsWith('ssh://')
}

// backendLabel describes the Galaxy's state backend, e.g. "GCS · bucket tf-state · prefix team-a".
export function backendLabel(galaxy?: Galaxy): string {
  const b = galaxy?.spec.backendConfig
  if (!b) return 'Set by the backend block in the repository'
  const where = b.type === 'AzureBlob'
    ? [b.storageAccount && `account ${b.storageAccount}`, b.container && `container ${b.container}`]
    : [b.bucket && `bucket ${b.bucket}`]
  return [b.type, ...where, b.prefix && `prefix ${b.prefix}`].filter(Boolean).join(' · ')
}

const DNS_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

export function validateForm(f: FormState, galaxy: Galaxy | undefined, names: string[]): FormErrors {
  const e: FormErrors = {}
  if (!f.galaxy) e.galaxy = 'Choose a Galaxy'
  if (!f.name.trim()) e.name = 'Enter a name'
  else if (f.name.length > 63 || !DNS_LABEL.test(f.name)) {
    e.name = 'Use lowercase letters, digits and dashes, starting and ending with a letter or digit'
  } else if (names.includes(f.name)) e.name = 'This name is already used in this Galaxy'
  if (!f.url.trim() && !galaxy?.spec.defaultVCSURL) e.url = 'Enter a Git repository'
  const url = f.url.trim() || galaxy?.spec.defaultVCSURL || ''
  if (needsCredentials(url) && !f.gitSecret && !galaxy?.spec.vcsSecretRef) {
    e.gitSecret = 'This repository is reached over SSH: choose Git credentials'
  }
  if (f.interval.trim() && parseDuration(f.interval.trim()) === undefined) e.interval = 'Expected duration format: 5m, 1h'
  return e
}

// --- Fields ---------------------------------------------------------------

const input =
  'block w-full rounded-md border px-3 py-2 text-sm placeholder:text-slate-400 focus:outline-none focus:ring-1 disabled:bg-slate-50 disabled:text-slate-500'

function Field({ label, help, error, inherited, children, required }: {
  label: string
  help?: ReactNode
  error?: string
  inherited?: string
  children: ReactNode
  required?: boolean
}) {
  return (
    <label className="block">
      <span className="text-sm font-medium text-slate-800">{label}{required && <span className="text-red-500"> *</span>}</span>
      <div className="mt-1">{children}</div>
      {inherited && !error && <span className="mt-1 block text-xs text-slate-400">Inherited: {inherited}</span>}
      {help && !error && <span className="mt-1 block text-xs text-slate-500">{help}</span>}
      {error && <span className="mt-1 block text-xs text-red-600">{error}</span>}
    </label>
  )
}

function cls(error?: string) {
  return `${input} ${error ? 'border-red-400 focus:border-red-500 focus:ring-red-500' : 'border-slate-300 focus:border-indigo-500 focus:ring-indigo-500'}`
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <fieldset className="grid grid-cols-1 gap-5 border-t border-slate-200 py-6 md:grid-cols-[12rem_1fr]">
      <legend className="sr-only">{title}</legend>
      <h2 className="text-sm font-semibold text-slate-900">{title}</h2>
      <div className="grid max-w-2xl grid-cols-1 gap-5 sm:grid-cols-2">{children}</div>
    </fieldset>
  )
}

export default function AstralForm({ value, onChange, errors, galaxies, galaxy, secrets, editing }: {
  value: FormState
  onChange: (v: FormState) => void
  errors: FormErrors
  galaxies: Galaxy[]
  galaxy?: Galaxy
  secrets: string[]
  editing?: boolean
}) {
  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => onChange({ ...value, [k]: v })
  const pathHelp = !value.terragrunt
    ? 'Root module folder'
    : value.terragruntMode === 'RunAll' ? 'Parent folder of the units' : 'Unit folder'

  return (
    <div>
      <Section title="Identity">
        <Field label="Galaxy" required error={errors.galaxy}>
          <select disabled={editing} value={value.galaxy} onChange={(e) => set('galaxy', e.target.value)} className={cls(errors.galaxy)}>
            <option value="">Choose a Galaxy</option>
            {galaxies.map((g) => (
              <option key={`${g.metadata.namespace}/${g.metadata.name}`} value={g.metadata.name}>
                {g.spec.displayName || g.metadata.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Name" required error={errors.name}>
          <input disabled={editing} value={value.name} onChange={(e) => set('name', e.target.value)} placeholder="network" className={`${cls(errors.name)} font-mono`} />
        </Field>
      </Section>

      <Section title="Source">
        <div className="sm:col-span-2">
          <Field label="Git repository" required={!galaxy?.spec.defaultVCSURL} error={errors.url}
            inherited={!value.url ? galaxy?.spec.defaultVCSURL : undefined}>
            <input value={value.url} onChange={(e) => set('url', e.target.value)} placeholder="https://github.com/acme/infra.git" className={`${cls(errors.url)} font-mono`} />
          </Field>
        </div>
        <Field label="Revision" help="Branch to follow">
          <input value={value.branch} onChange={(e) => set('branch', e.target.value)} placeholder="main" className={`${cls()} font-mono`} />
        </Field>
        <Field label="Path" help={pathHelp}>
          <input value={value.path} onChange={(e) => set('path', e.target.value)} placeholder="." className={`${cls()} font-mono`} />
        </Field>
      </Section>

      <Section title="Credentials">
        <div className="sm:col-span-2">
          <Field label="Git credentials" error={errors.gitSecret} help="Used to clone the repository">
            <SecretSelect value={value.gitSecret} onChange={(v) => set('gitSecret', v)} secrets={secrets}
              inherits={galaxy?.spec.vcsSecretRef?.name} error={errors.gitSecret} />
          </Field>
        </div>
        <div className="sm:col-span-2">
          <span className="block text-sm font-medium text-slate-800">State backend</span>
          <span className="mt-1 block text-sm text-slate-600">{backendLabel(galaxy)}</span>
          {galaxy?.spec.backendConfig && <span className="mt-1 block text-xs text-slate-400">Set on the Galaxy</span>}
        </div>
        <div className="sm:col-span-2">
          <Field label="State credentials"
            help={galaxy?.spec.backendConfig?.type === 'GCS' || !galaxy?.spec.backendConfig
              ? 'Reads and writes the state. For GCS, a Secret with a GOOGLE_CREDENTIALS key'
              : 'Reads and writes the state. Its keys become environment variables as they are'}>
            <SecretSelect value={value.backendSecret} onChange={(v) => set('backendSecret', v)} secrets={secrets}
              inherits={galaxy?.spec.backendConfig?.secretRef?.name} />
          </Field>
        </div>
        <div className="sm:col-span-2">
          <Field label="Cloud credentials" help="Optional. Extra credentials for the providers: each key of the Secret becomes an environment variable">
            <select
              multiple
              value={value.cloudSecrets}
              onChange={(e) => set('cloudSecrets', [...e.target.selectedOptions].map((o) => o.value))}
              className={`${cls()} h-24`}
            >
              {secrets.map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
          </Field>
        </div>
      </Section>

      <Section title="Engine">
        <div className="sm:col-span-2">
          <span className="block text-sm font-medium text-slate-800">Engine</span>
          <div className="mt-1 inline-flex rounded-md shadow-sm">
            {(['Terraform', 'OpenTofu'] as Engine[]).map((e, i) => (
              <button
                key={e}
                type="button"
                onClick={() => set('engine', e)}
                className={`px-4 py-2 text-sm font-medium ring-1 ring-inset ring-slate-300 ${i === 0 ? 'rounded-l-md' : '-ml-px rounded-r-md'} ${
                  value.engine === e ? 'z-10 bg-indigo-600 text-white ring-indigo-600' : 'bg-white text-slate-700 hover:bg-slate-50'
                }`}
              >
                {e.toLowerCase()}
              </button>
            ))}
          </div>
          <span className="mt-1 block text-xs text-slate-500">Runs init, plan and apply, and owns the state</span>
        </div>
        <Field label="Engine version">
          <input value={value.engineVersion} onChange={(e) => set('engineVersion', e.target.value)} placeholder="1.9.0" className={`${cls()} font-mono`} />
        </Field>
        <div />
        <label className="flex items-center gap-2 sm:col-span-2">
          <input type="checkbox" checked={value.terragrunt} onChange={(e) => set('terragrunt', e.target.checked)} className="h-4 w-4 rounded border-slate-300 text-indigo-600" />
          <span className="text-sm font-medium text-slate-800">Use Terragrunt</span>
        </label>
        {value.terragrunt && (
          <>
            <Field label="Terragrunt version">
              <input value={value.terragruntVersion} onChange={(e) => set('terragruntVersion', e.target.value)} placeholder="1.1.6" className={`${cls()} font-mono`} />
            </Field>
            <Field label="Mode" help={value.terragruntMode === 'RunAll' ? 'Automatic checks run run-all. You can target a single unit manually.' : undefined}>
              <select value={value.terragruntMode} onChange={(e) => set('terragruntMode', e.target.value as TerragruntMode)} className={cls()}>
                <option value="Unit">Single unit</option>
                <option value="RunAll">All units (run-all)</option>
              </select>
            </Field>
          </>
        )}
      </Section>

      <Section title="Execution">
        <Field label="Check interval" error={errors.interval} help="Leave empty to turn automatic checks off">
          <input value={value.interval} onChange={(e) => set('interval', e.target.value)} placeholder="5m" className={`${cls(errors.interval)} font-mono`} />
        </Field>
      </Section>

      <Section title="Sync">
        <Field label="Policy" help={value.autoApply
          ? 'Auto: a plan that finds changes is applied at once, unless it destroys something'
          : 'Manual: drift is reported, apply waits for you'}>
          <select value={value.autoApply ? 'auto' : 'manual'} onChange={(e) => set('autoApply', e.target.value === 'auto')} className={cls()}>
            <option value="manual">Manual</option>
            <option value="auto">Auto</option>
          </select>
        </Field>
        <div className="self-end pb-2 text-xs text-slate-500">A plan that destroys a resource always asks for confirmation.</div>
      </Section>
    </div>
  )
}

function SecretSelect({ value, onChange, secrets, inherits, error }: {
  value: string
  onChange: (v: string) => void
  secrets: string[]
  inherits?: string
  error?: string
}) {
  return (
    <select value={value} onChange={(e) => onChange(e.target.value)} className={cls(error)}>
      <option value="">{inherits ? `Inherit from the Galaxy (${inherits})` : 'None'}</option>
      {secrets.map((s) => <option key={s} value={s}>{s}</option>)}
      {value && !secrets.includes(value) && <option value={value}>{value} (missing)</option>}
    </select>
  )
}

// YamlDialog shows the resource as YAML, ready to copy into Git.
export function YamlDialog({ astral, onClose }: { astral: Astral; onClose: () => void }) {
  const clean = {
    apiVersion: 'core.stellarcd.io/v1alpha1',
    kind: 'Astral',
    metadata: { name: astral.metadata.name, namespace: astral.metadata.namespace },
    spec: astral.spec,
  }
  const text = stringify(JSON.parse(JSON.stringify(clean)))
  return (
    <Modal title="Astral YAML" onClose={onClose} footer={
      <button onClick={() => void navigator.clipboard?.writeText(text)} className="rounded-md bg-white px-3 py-1.5 text-sm font-medium text-slate-700 ring-1 ring-inset ring-slate-300 hover:bg-slate-50">
        Copy
      </button>
    }>
      <pre className="overflow-auto rounded-md bg-slate-900 p-4 text-xs leading-relaxed text-slate-100"><Mono>{text}</Mono></pre>
    </Modal>
  )
}
