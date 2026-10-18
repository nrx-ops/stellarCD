import { Navigate, Route, Routes } from 'react-router-dom'
import { useUniverses } from './api/hooks'
import Layout from './components/Layout'
import { EmptyState, ErrorState, Loading } from './components/ui'
import AstralDetail from './pages/AstralDetail'
import AstralList from './pages/AstralList'
import FlareDetail from './pages/FlareDetail'
import NewAstral from './pages/NewAstral'

// Routes follow the URL scheme proposed on the "Navigation" page
// ([À CONFIRMER]): /u/<universe>/g/<galaxy>/a/<astral>/<tab>, so a link to any
// screen can be shared.
export default function App() {
  return (
    <Routes>
      <Route path="/" element={<Home />} />
      <Route path="/u/:universe" element={<Layout><AstralList /></Layout>} />
      <Route path="/u/:universe/new" element={<Layout><NewAstral /></Layout>} />
      <Route path="/u/:universe/g/:galaxy" element={<Layout><AstralList /></Layout>} />
      <Route path="/u/:universe/g/:galaxy/a/:astral/flares/:flare" element={<Layout><FlareDetail /></Layout>} />
      <Route path="/u/:universe/g/:galaxy/a/:astral/:tab?" element={<Layout><AstralDetail /></Layout>} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

function Home() {
  const { data, error, isLoading } = useUniverses()
  if (isLoading) return <Loading />
  if (error) return <div className="p-8"><ErrorState error={error} /></div>
  if (!data?.length) {
    return (
      <div className="p-8">
        <EmptyState title="No Universe yet" text="A platform administrator creates Universes and Galaxies before Astrals can be declared." />
      </div>
    )
  }
  return <Navigate to={`/u/${data[0]!.metadata.name}`} replace />
}
