import type { ApprovalPolicy, BackendConfig, Engine, Galaxy, TerragruntMode } from '../api/types'
import { parseDuration } from '../lib/format'
import { Field, Section, SecretSelect, cls } from './form'

// GalaxyFormState holds the fields of the Galaxy admin form as typed.
export interface GalaxyFormState {
  name: string
  displayName: string
  description: string
  teamEmail: string
  provider: 'GitHub' | 'GitLab' | 'Gitea'
  defaultURL: string
  gitSecret: string
  backendType: '' | BackendConfig['type']
  bucket: string
  prefix: string
  region: string
  container: string
  storageAccount: string
  backendSecret: string
  approvalPolicy: ApprovalPolicy
  discovery: boolean
  dUrl: string
  dBranch: string
  dPath: string
  dSecret: string
  dInterval: string
  dInclude: string
  dExclude: string
  dStripPrefix: string
  dMaxAstrals: string
  dPrune: boolean
  tEngine: Engine
  tEngineVersion: string
  tTerragrunt: boolean
  tTerragruntVersion: string
  tTerragruntMode: TerragruntMode
  tAutoApply: boolean
  tInterval: string
}

export type GalaxyFormErrors = Partial<Record<keyof GalaxyFormState, string>>

export function emptyGalaxyForm(): GalaxyFormState {
  return {
    name: '', displayName: '', description: '', teamEmail: '',
    provider: 'GitHub', defaultURL: '', gitSecret: '',
    backendType: '', bucket: '', prefix: '', region: '', container: '', storageAccount: '', backendSecret: '',
    approvalPolicy: 'RequiresReview',
    discovery: false, dUrl: '', dBranch: '', dPath: '', dSecret: '', dInterval: '', dInclude: '', dExclude: '',
    dStripPrefix: '', dMaxAstrals: '', dPrune: false,
    tEngine: 'Terraform', tEngineVersion: '', tTerragrunt: false, tTerragruntVersion: '', tTerragruntMode: 'Unit',
    tAutoApply: false, tInterval: '',
  }
}

const lines = (v?: string[]) => (v ?? []).join('\n')
const parseLines = (v: string) => v.split('\n').map((l) => l.trim()).filter(Boolean)

export function galaxyFormFrom(g: Galaxy): GalaxyFormState {
  const s = g.spec
  const b = s.backendConfig
  const d = s.discovery
  const t = d?.template
  return {
    name: g.metadata.name,
    displayName: s.displayName ?? '',
    description: s.description ?? '',
    teamEmail: s.teamEmail ?? '',
    provider: s.defaultVCSProvider ?? 'GitHub',
    defaultURL: s.defaultVCSURL ?? '',
    gitSecret: s.vcsSecretRef?.name ?? '',
    backendType: b?.type ?? '',
    bucket: b?.bucket ?? '',
    prefix: b?.prefix ?? '',
    region: b?.region ?? '',
    container: b?.container ?? '',
    storageAccount: b?.storageAccount ?? '',
    backendSecret: b?.secretRef?.name ?? '',
    approvalPolicy: s.approvalPolicy ?? 'RequiresReview',
    discovery: !!d?.enabled,
    dUrl: d?.repository.url ?? '',
    dBranch: d?.repository.branch ?? '',
    dPath: d?.repository.path === '.' ? '' : d?.repository.path ?? '',
    dSecret: d?.repository.secretRef?.name ?? '',
    dInterval: d?.interval ?? '',
    dInclude: lines(d?.include),
    dExclude: lines(d?.exclude),
    dStripPrefix: d?.stripPrefix ?? '',
    dMaxAstrals: d?.maxAstrals ? String(d.maxAstrals) : '',
    dPrune: !!d?.prune,
    tEngine: t?.engine ?? 'Terraform',
    tEngineVersion: t?.engineVersion ?? '',
    tTerragrunt: !!t?.terragrunt?.enabled,
    tTerragruntVersion: t?.terragrunt?.version ?? '',
    tTerragruntMode: t?.terragrunt?.mode ?? 'Unit',
    tAutoApply: !!t?.autoApply,
    tInterval: t?.driftDetection?.enabled ? t.driftDetection.interval ?? '' : '',
  }
}

// galaxyFrom builds the resource. When editing, `base` keeps every field the
// form does not show (labels, PR settings…) untouched.
export function galaxyFrom(f: GalaxyFormState, universe: string, namespace: string | undefined, base?: Galaxy): Galaxy {
  const spec = { ...(base?.spec ?? {}) } as Galaxy['spec']
  const opt = (v: string) => v.trim() || undefined
  spec.universeRef = universe
  spec.displayName = opt(f.displayName)
  spec.description = opt(f.description)
  spec.teamEmail = opt(f.teamEmail)
  spec.defaultVCSProvider = f.provider
  spec.defaultVCSURL = opt(f.defaultURL)
  spec.vcsSecretRef = f.gitSecret ? { name: f.gitSecret } : undefined
  spec.backendConfig = f.backendType
    ? {
        type: f.backendType,
        bucket: f.backendType === 'AzureBlob' ? undefined : opt(f.bucket),
        container: f.backendType === 'AzureBlob' ? opt(f.container) : undefined,
        storageAccount: f.backendType === 'AzureBlob' ? opt(f.storageAccount) : undefined,
        prefix: opt(f.prefix),
        region: f.backendType === 'S3' ? opt(f.region) : undefined,
        secretRef: f.backendSecret ? { name: f.backendSecret } : undefined,
      }
    : undefined
  spec.approvalPolicy = f.approvalPolicy

  if (f.discovery || base?.spec.discovery) {
    const prev = base?.spec.discovery
    spec.discovery = {
      ...(prev ?? {}),
      enabled: f.discovery,
      repository: {
        ...(prev?.repository ?? {}),
        provider: f.provider,
        url: f.dUrl.trim(),
        branch: opt(f.dBranch) ?? 'main',
        path: opt(f.dPath) ?? '.',
        secretRef: f.dSecret ? { name: f.dSecret } : undefined,
      },
      interval: opt(f.dInterval),
      include: parseLines(f.dInclude).length ? parseLines(f.dInclude) : undefined,
      exclude: parseLines(f.dExclude).length ? parseLines(f.dExclude) : undefined,
      stripPrefix: opt(f.dStripPrefix),
      maxAstrals: f.dMaxAstrals.trim() ? Number(f.dMaxAstrals) : undefined,
      prune: f.dPrune,
      template: {
        ...(prev?.template ?? {}),
        engine: f.tEngine,
        engineVersion: opt(f.tEngineVersion),
        terragrunt: f.tTerragrunt
          ? { ...(prev?.template?.terragrunt ?? {}), enabled: true, version: opt(f.tTerragruntVersion), mode: f.tTerragruntMode }
          : undefined,
        autoApply: f.tAutoApply,
        driftDetection: f.tInterval.trim() ? { enabled: true, interval: f.tInterval.trim() } : undefined,
      },
    }
  }

  return {
    apiVersion: 'core.stellarcd.io/v1alpha1',
    kind: 'Galaxy',
    metadata: {
      name: f.name.trim(),
      namespace: base?.metadata.namespace ?? namespace,
      ...(base?.metadata.resourceVersion ? { resourceVersion: base.metadata.resourceVersion } : {}),
    },
    spec,
  }
}

const DNS_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

export function validateGalaxyForm(f: GalaxyFormState, names: string[]): GalaxyFormErrors {
  const e: GalaxyFormErrors = {}
  if (!f.name.trim()) e.name = 'Enter a name'
  else if (f.name.length > 63 || !DNS_LABEL.test(f.name)) {
    e.name = 'Use lowercase letters, digits and dashes, starting and ending with a letter or digit'
  } else if (names.includes(f.name)) e.name = 'This name is already used in this Universe'
  if ((f.backendType === 'GCS' || f.backendType === 'S3') && !f.bucket.trim()) e.bucket = 'Enter the bucket holding the state'
  if (f.backendType === 'AzureBlob') {
    if (!f.storageAccount.trim()) e.storageAccount = 'Enter the storage account'
    if (!f.container.trim()) e.container = 'Enter the container'
  }
  if (f.discovery && !f.dUrl.trim()) e.dUrl = 'Enter the repository to scan'
  for (const k of ['dInterval', 'tInterval'] as const) {
    if (f[k].trim() && parseDuration(f[k].trim()) === undefined) e[k] = 'Expected duration format: 5m, 1h'
  }
  if (f.dMaxAstrals.trim() && !(Number.isInteger(Number(f.dMaxAstrals)) && Number(f.dMaxAstrals) > 0)) {
    e.dMaxAstrals = 'Enter a positive whole number'
  }
  return e
}

export default function GalaxyForm({ value, onChange, errors, secrets, editing }: {
  value: GalaxyFormState
  onChange: (v: GalaxyFormState) => void
  errors: GalaxyFormErrors
  secrets: string[]
  editing?: boolean
}) {
  const set = <K extends keyof GalaxyFormState>(k: K, v: GalaxyFormState[K]) => onChange({ ...value, [k]: v })
  const text = (k: keyof GalaxyFormState, placeholder = '', mono = false) => (
    <input
      value={value[k] as string}
      onChange={(e) => set(k, e.target.value as never)}
      placeholder={placeholder}
      className={`${cls(errors[k])} ${mono ? 'font-mono' : ''}`}
    />
  )
  const check = (k: keyof GalaxyFormState, label: string) => (
    <label className="flex items-center gap-2">
      <input type="checkbox" checked={value[k] as boolean} onChange={(e) => set(k, e.target.checked as never)}
        className="h-4 w-4 rounded border-slate-300 text-indigo-600" />
      <span className="text-sm font-medium text-slate-800">{label}</span>
    </label>
  )

  return (
    <div>
      <Section title="Identity">
        <Field label="Name" required error={errors.name}>
          <input disabled={editing} value={value.name} onChange={(e) => set('name', e.target.value)} placeholder="platform"
            className={`${cls(errors.name)} font-mono`} />
        </Field>
        <Field label="Display name">{text('displayName', 'Platform team')}</Field>
        <div className="sm:col-span-2"><Field label="Description">{text('description')}</Field></div>
        <Field label="Team email" help="Who to contact about this Galaxy">{text('teamEmail', 'team@example.com')}</Field>
      </Section>

      <Section title="Defaults">
        <Field label="Git provider">
          <select value={value.provider} onChange={(e) => set('provider', e.target.value as GalaxyFormState['provider'])} className={cls()}>
            <option>GitHub</option><option>GitLab</option><option>Gitea</option>
          </select>
        </Field>
        <div />
        <div className="sm:col-span-2">
          <Field label="Default Git repository" help="Astrals of this Galaxy use it when they name no repository">
            {text('defaultURL', 'https://github.com/acme/infra.git', true)}
          </Field>
        </div>
        <Field label="Approval policy" help={{
          Auto: 'Runs start without approval',
          Manual: 'Every run waits for an approval',
          RequiresReview: 'Applies wait for an approval, plans run at once',
        }[value.approvalPolicy]}>
          <select value={value.approvalPolicy} onChange={(e) => set('approvalPolicy', e.target.value as ApprovalPolicy)} className={cls()}>
            <option value="Auto">Auto</option>
            <option value="Manual">Manual</option>
            <option value="RequiresReview">Requires review</option>
          </select>
        </Field>
      </Section>

      <Section title="Credentials">
        <div className="sm:col-span-2">
          <Field label="Git credentials" help="Used to clone the repositories of the Astrals that set none">
            <SecretSelect value={value.gitSecret} onChange={(v) => set('gitSecret', v)} secrets={secrets} />
          </Field>
        </div>
        <Field label="State backend" help={value.backendType ? undefined : 'None: each repository declares its own backend block'}>
          <select value={value.backendType} onChange={(e) => set('backendType', e.target.value as GalaxyFormState['backendType'])} className={cls()}>
            <option value="">None</option>
            <option value="GCS">GCS</option>
            <option value="S3">S3</option>
            <option value="AzureBlob">Azure Blob</option>
          </select>
        </Field>
        <div />
        {(value.backendType === 'GCS' || value.backendType === 'S3') && (
          <Field label="Bucket" required error={errors.bucket}>{text('bucket', 'acme-tf-state', true)}</Field>
        )}
        {value.backendType === 'AzureBlob' && (
          <>
            <Field label="Storage account" required error={errors.storageAccount}>{text('storageAccount', '', true)}</Field>
            <Field label="Container" required error={errors.container}>{text('container', 'tfstate', true)}</Field>
          </>
        )}
        {value.backendType && <Field label="Prefix" help="Folder of this Galaxy's states inside the bucket">{text('prefix', 'platform', true)}</Field>}
        {value.backendType === 'S3' && <Field label="Region">{text('region', 'eu-west-3', true)}</Field>}
        <div className="sm:col-span-2">
          <Field label="State credentials" help={value.backendType === 'GCS' || !value.backendType
            ? 'Reads and writes the state. For GCS, a Secret with a GOOGLE_CREDENTIALS key. An Astral can override it'
            : 'Reads and writes the state. Its keys become environment variables as they are. An Astral can override it'}>
            <SecretSelect value={value.backendSecret} onChange={(v) => set('backendSecret', v)} secrets={secrets} />
          </Field>
        </div>
      </Section>

      <Section title="Discovery">
        <div className="sm:col-span-2">
          {check('discovery', 'Discover Astrals from a repository')}
          <span className="mt-1 block text-xs text-slate-500">
            stellarCD scans the repository and declares one Astral per root module or Terragrunt unit it finds.
          </span>
        </div>
        {value.discovery && (
          <>
            <div className="sm:col-span-2">
              <Field label="Repository" required error={errors.dUrl}>{text('dUrl', 'https://github.com/acme/infra.git', true)}</Field>
            </div>
            <Field label="Revision" help="Branch to scan">{text('dBranch', 'main', true)}</Field>
            <Field label="Path" help="Folder to scan">{text('dPath', '.', true)}</Field>
            <div className="sm:col-span-2">
              <Field label="Git credentials">
                <SecretSelect value={value.dSecret} onChange={(v) => set('dSecret', v)} secrets={secrets}
                  inherits={value.gitSecret || undefined} />
              </Field>
            </div>
            <Field label="Scan interval" error={errors.dInterval}>{text('dInterval', '10m', true)}</Field>
            <Field label="Maximum Astrals" error={errors.dMaxAstrals} help="Stops a wrong pattern from flooding the Galaxy">
              {text('dMaxAstrals', '50', true)}
            </Field>
            <Field label="Include" help="One glob per line, e.g. envs/*/apps/*">
              <textarea value={value.dInclude} onChange={(e) => set('dInclude', e.target.value)} rows={3} className={`${cls()} font-mono`} />
            </Field>
            <Field label="Exclude" help="One glob per line">
              <textarea value={value.dExclude} onChange={(e) => set('dExclude', e.target.value)} rows={3} className={`${cls()} font-mono`} />
            </Field>
            <Field label="Strip prefix" help="Removed from the path to build the Astral name">{text('dStripPrefix', 'terragrunt/environments/', true)}</Field>
            <div className="self-end pb-2">
              {check('dPrune', 'Delete Astrals whose folder disappeared')}
              <span className="mt-1 block text-xs text-slate-500">Deleting an Astral never destroys infrastructure.</span>
            </div>

            <h3 className="pt-2 text-sm font-semibold text-slate-900 sm:col-span-2">Discovered Astrals</h3>
            <Field label="Engine">
              <select value={value.tEngine} onChange={(e) => set('tEngine', e.target.value as Engine)} className={cls()}>
                <option value="Terraform">terraform</option>
                <option value="OpenTofu">opentofu</option>
              </select>
            </Field>
            <Field label="Engine version">{text('tEngineVersion', '1.9.0', true)}</Field>
            <div className="sm:col-span-2">{check('tTerragrunt', 'Use Terragrunt')}</div>
            {value.tTerragrunt && (
              <>
                <Field label="Terragrunt version">{text('tTerragruntVersion', '1.1.6', true)}</Field>
                <Field label="Mode">
                  <select value={value.tTerragruntMode} onChange={(e) => set('tTerragruntMode', e.target.value as TerragruntMode)} className={cls()}>
                    <option value="Unit">Single unit</option>
                    <option value="RunAll">All units (run-all)</option>
                  </select>
                </Field>
              </>
            )}
            <Field label="Check interval" error={errors.tInterval} help="Leave empty to turn automatic checks off">
              {text('tInterval', '1h', true)}
            </Field>
            <Field label="Policy" help={value.tAutoApply ? 'Auto: changes are applied at once, unless they destroy something' : 'Manual: drift is reported, apply waits for you'}>
              <select value={value.tAutoApply ? 'auto' : 'manual'} onChange={(e) => set('tAutoApply', e.target.value === 'auto')} className={cls()}>
                <option value="manual">Manual</option>
                <option value="auto">Auto</option>
              </select>
            </Field>
          </>
        )}
      </Section>
    </div>
  )
}
