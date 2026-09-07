import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { useAuth } from '@/context/AuthContext'
import { Loading } from '@/components/ui'
import Layout from '@/components/Layout'
import Login from '@/pages/Login'
import Dashboard from '@/pages/Dashboard'
import Containers from '@/pages/Containers'
import Servers from '@/pages/Servers'
import ServerDetail from '@/pages/ServerDetail'
import Stacks from '@/pages/Stacks'
import Updates from '@/pages/Updates'
import Settings from '@/pages/Settings'
import Events from '@/pages/Events'

function Protected({ children }: { children: JSX.Element }) {
  const { me, loading } = useAuth()
  const location = useLocation()
  if (loading) {
    return (
      <div className="flex h-screen items-center justify-center">
        <Loading label="Connecting…" />
      </div>
    )
  }
  if (!me?.authenticated) {
    return <Navigate to="/login" state={{ from: location }} replace />
  }
  return children
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route
        path="/"
        element={
          <Protected>
            <Layout />
          </Protected>
        }
      >
        <Route index element={<Dashboard />} />
        <Route path="containers" element={<Containers />} />
        <Route path="servers" element={<Servers />} />
        <Route path="servers/:id" element={<ServerDetail />} />
        <Route path="stacks" element={<Stacks />} />
        <Route path="updates" element={<Updates />} />
        <Route path="settings" element={<Settings />} />
        <Route path="events" element={<Events />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
