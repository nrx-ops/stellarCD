import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { ApiError, createFlare } from '../api/client'
import { useAstrals, useCreateAstral, useGalaxies, useSecrets } from '../api/hooks'
import AstralForm, { astralFromForm, emptyForm, validateForm, YamlDialog, type FormErrors, type FormState } from '../components/AstralForm'
import { Breadcrumb } from '../components/Layout'
import { Button, ConfirmDialog, ErrorState, Loading } from '../components/ui'

// fieldFromPath maps an admin API validation path to the form field it targets.
export function fieldFromPath(path?: string): keyof FormState | undefined {
  return ({
    'metadata.name': 'name',
    'spec.galaxyRef': 'galaxy',
    'spec.vcsRepository.url': 'url',
    'spec.driftDetection.interval': 'interval',
  } as Record<string, keyof FormState>)[path ?? '']
}

// SCR-Création d'un Astral ("New Astral").
export default function NewAstral() {
  const { universe } = useParams()
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const galaxiesQ = useGalaxies(universe)
  const astralsQ = useAstrals(universe)
  const create = useCreateAstral()

  const [form, setForm] = useState<FormState>(() => emptyForm(params.get('galaxy') ?? ''))
  const [errors, setErrors] = useState<FormErrors>({})
  const [yaml, setYaml] = useState(false)
  const [leaving, setLeaving] = useState(false)
  const [failure, setFailure] = useState<unknown>()

  const galaxy = galaxiesQ.data?.find((g) => g.metadata.name === form.galaxy)
  const secretsQ = useSecrets(galaxy?.metadata.namespace)
  const names = useMemo(
    () => (astralsQ.data ?? []).filter((a) => a.spec.galaxyRef === form.galaxy).map((a) => a.metadata.name),
    [astralsQ.data, form.galaxy],
  )
  const dirty = JSON.stringify(form) !== JSON.stringify(emptyForm(params.get('galaxy') ?? ''))

  useEffect(() => {
    if (!dirty) return
    const warn = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])

  if (galaxiesQ.isLoading) return <Loading />
  if (galaxiesQ.error) return <ErrorState error={galaxiesQ.error} />

  const submit = async () => {
    const found = validateForm(form, galaxy, names)
    setErrors(found)
    setFailure(undefined)
    if (Object.keys(found).length > 0) return
    try {
      const astral = astralFromForm(form, galaxy)
      const created = await create.mutateAsync(astral)
      // With automatic checks on, the controller raises the first plan itself.
      if (!astral.spec.driftDetection?.enabled) {
        await createFlare(created.metadata.namespace!, created.metadata.name, 'Plan')
      }
      navigate(`/u/${universe}/g/${form.galaxy}/a/${created.metadata.name}/overview`)
    } catch (err) {
      const field = err instanceof ApiError ? fieldFromPath(err.field) : undefined
      if (field) setErrors({ [field]: (err as Error).message })
      else setFailure(err)
    }
  }

  const back = () => navigate(`/u/${universe}${params.get('galaxy') ? `/g/${params.get('galaxy')}` : ''}`)

  return (
    <div className="max-w-5xl">
      <Breadcrumb items={[{ label: universe!, to: `/u/${universe}` }, { label: 'New Astral' }]} />
      <h1 className="text-xl font-semibold text-slate-900">New Astral</h1>
      <p className="mt-1 text-sm text-slate-500">Empty fields inherit values from the Galaxy</p>

      <form className="mt-6" onSubmit={(e) => { e.preventDefault(); void submit() }}>
        <AstralForm value={form} onChange={setForm} errors={errors} galaxies={galaxiesQ.data ?? []}
          galaxy={galaxy} secrets={secretsQ.data ?? []} />
        {failure != null && <div className="mb-4"><ErrorState error={failure} /></div>}
        <div className="flex items-center justify-end gap-2 border-t border-slate-200 pt-5">
          <Button kind="ghost" onClick={() => (dirty ? setLeaving(true) : back())}>Cancel</Button>
          <Button onClick={() => setYaml(true)}>View YAML</Button>
          <Button kind="primary" type="submit" busy={create.isLoading}>Create Astral</Button>
        </div>
      </form>

      {yaml && <YamlDialog astral={astralFromForm(form, galaxy)} onClose={() => setYaml(false)} />}
      {leaving && (
        <ConfirmDialog title="Discard changes?" confirmLabel="Discard" kind="danger" onConfirm={back} onClose={() => setLeaving(false)}>
          The Astral has not been created. What you typed will be lost.
        </ConfirmDialog>
      )}
    </div>
  )
}
