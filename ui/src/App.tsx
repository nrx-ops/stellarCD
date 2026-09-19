import { useState } from 'react'
import StellarAppList from './components/StellarAppList'
import RepositoryList from './components/RepositoryList'
import AdminMenu from './components/AdminMenu'

type Tab = 'apps' | 'repositories'

const TABS: { id: Tab; label: string }[] = [
  { id: 'apps', label: 'Apps' },
  { id: 'repositories', label: 'Repositories' },
]

function App() {
  const [tab, setTab] = useState<Tab>('apps')

  return (
    <div className="min-h-screen bg-gray-50">
      <header className="bg-white border-b border-gray-200">
        <div className="max-w-6xl mx-auto px-6 py-4">
          <div className="flex items-center gap-3">
            <div className="w-8 h-8 bg-blue-500 rounded-lg flex items-center justify-center">
              <span className="text-white font-bold">⚡</span>
            </div>
            <h1 className="text-2xl font-bold text-gray-900">stellarCD</h1>
            <span className="text-gray-500 text-sm ml-2">Dashboard</span>
          </div>
        </div>

        <nav className="max-w-6xl mx-auto px-6 flex gap-6">
          {TABS.map(({ id, label }) => (
            <button
              key={id}
              onClick={() => setTab(id)}
              className={`py-2 -mb-px border-b-2 text-sm font-medium transition-colors ${
                tab === id
                  ? 'border-blue-500 text-blue-600'
                  : 'border-transparent text-gray-500 hover:text-gray-800'
              }`}
            >
              {label}
            </button>
          ))}
        </nav>
      </header>

      <main>
        {tab === 'apps' ? <StellarAppList /> : <RepositoryList />}
        <div className="max-w-6xl mx-auto px-6 py-4">
          <AdminMenu />
        </div>
      </main>
    </div>
  )
}

export default App
