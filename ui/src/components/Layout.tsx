import { ReactNode } from 'react'
import { Link, NavLink, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useAstrals, useConfig, useGalaxies, useUniverses } from '../api/hooks'
import { Banner } from './ui'

// Layout is the frame described on the "Navigation" specification page: a top
// bar with the Universe selector, a left column listing the Galaxies, and the
// current screen.
export default function Layout({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-screen flex-col bg-slate-50">
      <TopBar />
      <div className="mx-auto flex w-full max-w-[1400px] flex-1">
        <GalaxyColumn />
        <main className="min-w-0 flex-1 px-8 py-6">
          <WritesNotice />
          {children}
        </main>
      </div>
    </div>
  )
}

function TopBar() {
  const { universe } = useParams()
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const { data: universes } = useUniverses()

  return (
    <header className="sticky top-0 z-40 border-b border-slate-200 bg-white">
      <div className="mx-auto flex h-14 max-w-[1400px] items-center gap-6 px-6">
        <Link to={universe ? `/u/${universe}` : '/'} className="flex items-center gap-2">
          <Logo />
          <span className="text-base font-semibold tracking-tight text-slate-900">stellarCD</span>
        </Link>

        <label className="flex items-center gap-2 text-sm text-slate-500">
          Universe:
          <select
            value={universe ?? ''}
            onChange={(e) => navigate(`/u/${e.target.value}`)}
            className="rounded-md border border-slate-300 bg-white py-1 pl-2 pr-7 text-sm font-medium text-slate-900 focus:border-indigo-500 focus:outline-none"
          >
            {!universe && <option value="">Select…</option>}
            {universes?.map((u) => (
              <option key={u.metadata.name} value={u.metadata.name}>
                {u.spec.displayName || u.metadata.name}
              </option>
            ))}
          </select>
        </label>

        <form
          className="ml-auto"
          onSubmit={(e) => {
            e.preventDefault()
            const q = new FormData(e.currentTarget).get('q') as string
            if (universe) navigate(`/u/${universe}${q ? `?q=${encodeURIComponent(q)}` : ''}`)
          }}
        >
          <input
            name="q"
            key={params.get('q') ?? ''}
            defaultValue={params.get('q') ?? ''}
            placeholder="Search"
            className="w-64 rounded-md border border-slate-300 bg-slate-50 px-3 py-1.5 text-sm placeholder:text-slate-400 focus:border-indigo-500 focus:bg-white focus:outline-none"
          />
        </form>
        {universe && (
          <NavLink
            to={`/u/${universe}/admin`}
            className={({ isActive }) => `text-sm font-medium ${isActive ? 'text-indigo-700' : 'text-slate-600 hover:text-slate-900'}`}
          >
            Admin
          </NavLink>
        )}
        {/* Notifications: [À CONFIRMER] in the specification, not built. */}
        <span
          className="flex h-8 w-8 items-center justify-center rounded-full bg-slate-200 text-xs font-semibold text-slate-600"
          title="The dashboard has no sign-in yet"
        >
          ?
        </span>
      </div>
    </header>
  )
}

function GalaxyColumn() {
  const { universe, galaxy } = useParams()
  const { data: galaxies } = useGalaxies(universe)
  const { data: astrals } = useAstrals(universe)
  if (!universe) return null

  const count = (g?: string) => astrals?.filter((a) => !g || a.spec.galaxyRef === g).length ?? 0
  const item = (active: boolean) =>
    `flex items-center justify-between rounded-md px-3 py-1.5 text-sm ${
      active ? 'bg-indigo-50 font-medium text-indigo-700' : 'text-slate-600 hover:bg-slate-100'
    }`

  return (
    <aside className="w-60 shrink-0 border-r border-slate-200 px-3 py-6">
      <h2 className="px-3 pb-2 text-xs font-semibold uppercase tracking-wide text-slate-400">Galaxies</h2>
      <nav className="space-y-0.5">
        <NavLink end to={`/u/${universe}`} className={() => item(!galaxy)}>
          All <span className="tabular-nums text-slate-400">{count()}</span>
        </NavLink>
        {galaxies?.map((g) => (
          <NavLink
            key={`${g.metadata.namespace}/${g.metadata.name}`}
            to={`/u/${universe}/g/${g.metadata.name}`}
            className={() => item(galaxy === g.metadata.name)}
          >
            <span className="truncate">{g.spec.displayName || g.metadata.name}</span>
            <span className="tabular-nums text-slate-400">{count(g.metadata.name)}</span>
          </NavLink>
        ))}
      </nav>
    </aside>
  )
}

function WritesNotice() {
  const { data } = useConfig()
  if (!data || data.writesEnabled) return null
  return (
    <div className="mb-4">
      <Banner tone="blue">
        Actions are disabled: the operator runs with a read-only admin API. Start it with --admin-api-writes to enable them.
      </Banner>
    </div>
  )
}

function Logo() {
  return (
    <svg viewBox="0 0 24 24" className="h-6 w-6" aria-hidden="true">
      <circle cx="12" cy="12" r="11" className="fill-indigo-600" />
      <path d="M12 5l1.6 4.9H19l-4.2 3 1.6 5L12 15l-4.4 2.9 1.6-5L5 9.9h5.4z" className="fill-white" />
    </svg>
  )
}

// Breadcrumb renders "Universe / Galaxy / Astral[/ Flare]" with each segment
// leading back to its screen.
export function Breadcrumb({ items }: { items: { label: string; to?: string }[] }) {
  return (
    <nav className="mb-3 flex items-center gap-1.5 text-sm text-slate-500">
      {items.map((it, i) => (
        <span key={i} className="flex items-center gap-1.5">
          {i > 0 && <span className="text-slate-300">/</span>}
          {it.to ? (
            <Link to={it.to} className="hover:text-slate-800 hover:underline">{it.label}</Link>
          ) : (
            <span className="text-slate-800">{it.label}</span>
          )}
        </span>
      ))}
    </nav>
  )
}
