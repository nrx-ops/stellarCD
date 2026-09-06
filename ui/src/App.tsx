import React from 'react'
import StellarAppList from './components/StellarAppList'

function App() {
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
      </header>

      <main>
        <StellarAppList />
      </main>
    </div>
  )
}

export default App
