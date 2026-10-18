import { useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { ApiError } from '../../api/client'
import { useAstrals, useDeleteGalaxy, useGalaxies, useSaveGalaxy, useSecrets, useUniverse, useWrites } from '../../api/hooks'
import type { Galaxy } from '../../api/types'
import { YamlDialog } from '../../components/AstralForm'
import GalaxyForm, {
  emptyGalaxyForm, galaxyFormFrom, galaxyFrom, validateGalaxyForm, type GalaxyFormErrors, type GalaxyFormState,
} from '../../components/GalaxyForm'
import { Breadcrumb } from '../../components/Layout'
import { Banner, Button, ConfirmDialog, ErrorState, Loading } from '../../components/ui'

// fieldFromPath maps an admin API validation path to the Galaxy form field.
function fieldFromPath(path?: string): keyof GalaxyFormState | undefined {
  return ({
    'metadata.name': 'name',
    'spec.backendConfig.bucket': 'bucket',
    'spec.backendConfig.container': 'container',
    'spec.backendConfig.storageAccount': 'storageAccount',
    'spec.discovery.repository.url': 'dUrl',
    'spec.discovery.interval': 'dInterval',
    'spec.discovery.template.driftDetection.interval': 'tInterval',
  } as Record<string, keyof GalaxyFormState>)[path ?? '']
}

// GalaxyEdit creates a Galaxy, or shows and edits an existing one.
export default function GalaxyEdit() {
  const { universe, galaxy: name } = useParams()
  const creating = !name
  const galaxiesQ = useGalaxies(universe)
  const universeQ = useUniverse(universe)
  const existing = galaxiesQ.data?.find((g) => g.metadata.name === name)
  if (galaxiesQ.isLoading || universeQ.isLoading) return <Loading />
  if (galaxiesQ.error) return <ErrorState error={galaxiesQ.error} />
  if (!creating && !existing) return <ErrorState error={new Error(`Galaxy ${name} does not exist in Universe ${universe}.`)} />
  return (
    <Editor
      key={existing?.metadata.resourceVersion ?? 'new'}
      universe={universe!}
      namespace={existing?.metadata.namespace ?? universeQ.data?.status?.namespace}
      galaxy={existing}
      names={(galaxiesQ.data ?? []).map((g) => g.metadata.name)}
    />
  )
}

function Editor({ universe, namespace, galaxy, names }: {
  universe: string
  namespace?: string
  galaxy?: Galaxy
  names: string[]
}) {
  const navigate = useNavigate()
  const writes = useWrites()
  const save = useSaveGalaxy(!galaxy)
  const remove = useDeleteGalaxy()
  const secrets = useSecrets(namespace).data ?? []
  const astrals = (useAstrals(universe).data ?? []).filter((a) => galaxy && a.spec.galaxyRef === galaxy.metadata.name)

  const [form, setForm] = useState<GalaxyFormState>(() => (galaxy ? galaxyFormFrom(galaxy) : emptyGalaxyForm()))
  const [errors, setErrors] = useState<GalaxyFormErrors>({})
  const [failure, setFailure] = useState<unknown>()
  const [yaml, setYaml] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [saved, setSaved] = useState(false)
  const back = `/u/${universe}/admin/galaxies`

  const submit = async (override?: Partial<GalaxyFormState>, archived?: boolean) => {
    const value = { ...form, ...override }
    const found = validateGalaxyForm(value, galaxy ? [] : names)
    setErrors(found)
    setFailure(undefined)
    setSaved(false)
    if (Object.keys(found).length > 0) return
    const resource = galaxyFrom(value, universe, namespace, galaxy)
    if (archived !== undefined) resource.spec.archived = archived
    try {
      const out = await save.mutateAsync(resource)
      if (!galaxy) navigate(`${back}/${out.metadata.name}`)
      else setSaved(true)
    } catch (err) {
      const field = err instanceof ApiError ? fieldFromPath(err.field) : undefined
      if (field) setErrors({ [field]: (err as Error).message })
      else setFailure(err)
    }
  }

  const title = galaxy ? galaxy.spec.displayName || galaxy.metadata.name : 'New Galaxy'

  return (
    <div className="max-w-5xl">
      <Breadcrumb items={[
        { label: universe, to: `/u/${universe}` },
        { label: 'Admin', to: back },
        { label: galaxy ? galaxy.metadata.name : 'New Galaxy' },
      ]} />
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold text-slate-900">{title}</h1>
        {galaxy && (
          <div className="flex gap-2">
            <Button onClick={() => setYaml(true)}>View YAML</Button>
            {writes.enabled && (
              <>
                <Button onClick={() => void submit(undefined, !galaxy.spec.archived)}>
                  {galaxy.spec.archived ? 'Unarchive' : 'Archive'}
                </Button>
                <Button kind="danger-secondary" disabled={astrals.length > 0}
                  title={astrals.length > 0 ? `This Galaxy still has ${astrals.length} Astrals. Move or delete them first.` : undefined}
                  onClick={() => setDeleting(true)}>
                  Delete Galaxy
                </Button>
              </>
            )}
          </div>
        )}
      </div>
      <p className="mt-1 text-sm text-slate-500">Astrals inherit these defaults when they leave a field empty.</p>

      <div className="mt-4 space-y-3">
        {galaxy?.spec.archived && <Banner tone="yellow">This Galaxy is archived: its Astrals keep their state but start no new Flare.</Banner>}
        {saved && <Banner tone="blue">Saved.</Banner>}
        {!namespace && <Banner tone="red">The Universe has no namespace yet, so no Galaxy can be created in it.</Banner>}
        {failure != null && <ErrorState error={failure} />}
      </div>

      <form className="mt-4" onSubmit={(e) => { e.preventDefault(); void submit() }}>
        <fieldset disabled={!writes.enabled}>
          <GalaxyForm value={form} onChange={setForm} errors={errors} secrets={secrets} editing={!!galaxy} />
        </fieldset>
        {writes.enabled && (
          <div className="flex items-center justify-end gap-2 border-t border-slate-200 pt-5">
            <Button kind="ghost" onClick={() => navigate(back)}>Cancel</Button>
            {!galaxy && <Button onClick={() => setYaml(true)}>View YAML</Button>}
            <Button kind="primary" type="submit" busy={save.isLoading} disabled={!namespace}>
              {galaxy ? 'Save' : 'Create Galaxy'}
            </Button>
          </div>
        )}
      </form>

      {yaml && <YamlDialog kind="Galaxy" astral={galaxyFrom(form, universe, namespace, galaxy)} onClose={() => setYaml(false)} />}
      {deleting && galaxy && (
        <ConfirmDialog
          title="Delete Galaxy"
          confirmLabel="Delete Galaxy"
          kind="danger"
          expect={galaxy.metadata.name}
          busy={remove.isLoading}
          error={remove.error}
          onClose={() => setDeleting(false)}
          onConfirm={() => remove.mutate(
            { namespace: galaxy.metadata.namespace!, name: galaxy.metadata.name },
            { onSuccess: () => navigate(back) },
          )}
        >
          <p>This removes the Galaxy from stellarCD. No infrastructure is destroyed.</p>
        </ConfirmDialog>
      )}
    </div>
  )
}
