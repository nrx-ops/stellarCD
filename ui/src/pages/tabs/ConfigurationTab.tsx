import { ReactNode, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ApiError } from '../../api/client'
import { useDeleteAstral, useGalaxies, useSecrets, useUpdateAstral, useWrites } from '../../api/hooks'
import type { TabProps } from '../AstralDetail'
import { fieldFromPath } from '../NewAstral'
import AstralForm, { astralFromForm, backendLabel, formFromAstral, validateForm, YamlDialog, type FormErrors, type FormState } from '../../components/AstralForm'
import { Banner, Button, ConfirmDialog, ErrorState, Mono } from '../../components/ui'

// SCR-Onglet Configuration: the Astral's settings, read then edited with the
// fields and labels of SCR-Création d'un Astral.
export default function ConfigurationTab({ astral, galaxy, universe }: TabProps) {
  const navigate = useNavigate()
  const writes = useWrites()
  const update = useUpdateAstral()
  const remove = useDeleteAstral()
  const galaxiesQ = useGalaxies(universe)
  const secretsQ = useSecrets(astral.metadata.namespace)
  const [form, setForm] = useState<FormState | null>(null)
  const [errors, setErrors] = useState<FormErrors>({})
  const [failure, setFailure] = useState<unknown>()
  const [yaml, setYaml] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [saved, setSaved] = useState<string>()

  const save = async () => {
    if (!form) return
    const found = validateForm(form, galaxy, [])
    setErrors(found)
    setFailure(undefined)
    if (Object.keys(found).length > 0) return
    try {
      const res = await update.mutateAsync(astralFromForm(form, galaxy, astral))
      setForm(null)
      setSaved(res.flare?.metadata.name)
    } catch (err) {
      const field = err instanceof ApiError ? fieldFromPath(err.field) : undefined
      if (field) setErrors({ [field]: (err as Error).message })
      else setFailure(err)
    }
  }

  const s = astral.spec
  const repo = s.vcsRepository
  const g = galaxy.spec.displayName || galaxy.metadata.name
  const own = 'Set on this Astral'
  const inherited = `Inherited from ${g}`
  const urlInherited = !!galaxy.spec.defaultVCSURL && repo.url === galaxy.spec.defaultVCSURL

  return (
    <div className="space-y-4">
      {saved && (
        <Banner tone="blue">Saved. A plan has been started to check the change: <Mono>{saved}</Mono>.</Banner>
      )}

      <div className="flex items-center justify-end gap-2">
        {form ? (
          <>
            <Button kind="ghost" onClick={() => { setForm(null); setErrors({}); setFailure(undefined) }}>Cancel</Button>
            <Button kind="primary" busy={update.isLoading} onClick={() => void save()}>Save</Button>
          </>
        ) : (
          <>
            <Button onClick={() => setYaml(true)}>View YAML</Button>
            {writes.enabled && (
              <>
                <Button onClick={() => { setSaved(undefined); setForm(formFromAstral(astral, galaxy)) }}>Edit</Button>
                <Button kind="danger-secondary" onClick={() => setDeleting(true)}>Delete Astral</Button>
              </>
            )}
          </>
        )}
      </div>

      {failure != null && <ErrorState error={failure} />}

      {form ? (
        <div className="rounded-lg border border-slate-200 bg-white px-6">
          <AstralForm value={form} onChange={setForm} errors={errors} galaxies={galaxiesQ.data ?? [galaxy]}
            galaxy={galaxy} secrets={secretsQ.data ?? []} editing />
        </div>
      ) : (
        <div className="divide-y divide-slate-100 rounded-lg border border-slate-200 bg-white">
          <Group title="Identity">
            <Row label="Galaxy" value={g} />
            <Row label="Name" value={<Mono>{astral.metadata.name}</Mono>} />
          </Group>
          <Group title="Source">
            <Row label="Git repository" value={<Mono>{repo.url}</Mono>} from={urlInherited ? inherited : own} />
            <Row label="Revision" value={<Mono>{repo.branch || 'main'}</Mono>} from={own} />
            <Row label="Path" value={<Mono>{repo.path || '.'}</Mono>} from={own} />
          </Group>
          <Group title="Credentials">
            <Row
              label="Git credentials"
              value={repo.secretRef?.name ?? galaxy.spec.vcsSecretRef?.name ?? 'None'}
              from={repo.secretRef ? own : galaxy.spec.vcsSecretRef ? inherited : undefined}
            />
            <Row label="State backend" value={backendLabel(galaxy)} from={galaxy.spec.backendConfig ? inherited : undefined} />
            <Row
              label="State credentials"
              value={s.backendSecretRef?.name ?? galaxy.spec.backendConfig?.secretRef?.name ?? 'None'}
              from={s.backendSecretRef ? own : galaxy.spec.backendConfig?.secretRef ? inherited : undefined}
            />
            <Row
              label="Environment"
              value={s.secrets?.length ? (
                <ul className="space-y-0.5">
                  {s.secrets.map((x, i) => (
                    <li key={i}>
                      {x.key
                        ? <><Mono>{x.envName || (x.envPrefix ?? 'TF_VAR_') + x.key}</Mono> <span className="text-slate-400">←</span> <Mono>{x.name}/{x.key}</Mono></>
                        : <><Mono>{x.name}</Mono> <span className="text-slate-400">(every key{(x.envPrefix ?? 'TF_VAR_') ? `, prefixed ${x.envPrefix ?? 'TF_VAR_'}` : ''})</span></>}
                    </li>
                  ))}
                </ul>
              ) : 'None'}
              from={s.secrets?.length ? own : undefined}
            />
          </Group>
          <Group title="Engine">
            <Row label="Engine" value={(s.engine ?? 'Terraform').toLowerCase()} from={own} />
            <Row label="Engine version" value={s.engineVersion || 'Runner default'} from={s.engineVersion ? own : undefined} />
            <Row label="Terragrunt" value={s.terragrunt?.enabled
              ? `${s.terragrunt.mode === 'RunAll' ? 'All units (run-all)' : 'Single unit'}${s.terragrunt.version ? ` · ${s.terragrunt.version}` : ''}`
              : 'Not used'} from={own} />
            {!!s.terragrunt?.excludedUnits?.length && (
              <Row label="Excluded units" value={<Mono>{s.terragrunt.excludedUnits.join(', ')}</Mono>} from={own} />
            )}
          </Group>
          <Group title="Execution">
            <Row label="Check interval" value={s.driftDetection?.enabled ? s.driftDetection.interval || '1h' : 'Automatic checks off'} from={own} />
          </Group>
          <Group title="Sync">
            <Row label="Policy" value={s.autoApply ? 'Auto' : 'Manual'} from={own} />
          </Group>
        </div>
      )}

      {yaml && <YamlDialog astral={astral} onClose={() => setYaml(false)} />}
      {deleting && (
        <ConfirmDialog
          title="Delete Astral"
          confirmLabel="Delete Astral"
          kind="danger"
          expect={astral.metadata.name}
          busy={remove.isLoading}
          error={remove.error}
          onClose={() => setDeleting(false)}
          onConfirm={() =>
            remove.mutate(
              { namespace: astral.metadata.namespace!, name: astral.metadata.name },
              { onSuccess: () => navigate(`/u/${universe}/g/${galaxy.metadata.name}`) },
            )
          }
        >
          <p>This removes the Astral from stellarCD. Your infrastructure is not destroyed.</p>
        </ConfirmDialog>
      )}
    </div>
  )
}

function Group({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-1 gap-2 px-6 py-5 md:grid-cols-[12rem_1fr]">
      <h3 className="text-sm font-semibold text-slate-900">{title}</h3>
      <dl className="divide-y divide-slate-50">{children}</dl>
    </div>
  )
}

function Row({ label, value, from }: { label: string; value: ReactNode; from?: string }) {
  return (
    <div className="grid grid-cols-[11rem_1fr_auto] items-baseline gap-4 py-1.5 text-sm">
      <dt className="text-slate-500">{label}</dt>
      <dd className="min-w-0 break-words text-slate-900">{value}</dd>
      <dd className="text-xs text-slate-400">{from}</dd>
    </div>
  )
}
