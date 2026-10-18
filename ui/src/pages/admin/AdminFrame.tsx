import { ReactNode } from 'react'
import { Link, useParams } from 'react-router-dom'
import { Breadcrumb } from '../../components/Layout'

// AdminFrame is the shared header of the administration screens: the
// Universe's Galaxies and the credentials of its namespace.
export default function AdminFrame({ tab, actions, children }: {
  tab: 'galaxies' | 'credentials'
  actions?: ReactNode
  children: ReactNode
}) {
  const { universe } = useParams()
  const tabs = [
    { id: 'galaxies', label: 'Galaxies' },
    { id: 'credentials', label: 'Credentials' },
  ]
  return (
    <div className="space-y-5">
      <Breadcrumb items={[{ label: universe!, to: `/u/${universe}` }, { label: 'Admin' }]} />
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold text-slate-900">Admin</h1>
        {actions}
      </div>
      <nav className="flex gap-6 border-b border-slate-200">
        {tabs.map((t) => (
          <Link
            key={t.id}
            to={`/u/${universe}/admin/${t.id}`}
            className={`-mb-px border-b-2 py-2.5 text-sm font-medium ${
              tab === t.id ? 'border-indigo-600 text-indigo-700' : 'border-transparent text-slate-500 hover:text-slate-800'
            }`}
          >
            {t.label}
          </Link>
        ))}
      </nav>
      {children}
    </div>
  )
}
