import { useCallback, useEffect, useState } from 'react'
import { api } from './api'
import type { Me } from './types'
import { usePath } from './lib/usePath'
import { Login } from './components/Login'
import { Breadcrumbs } from './components/Breadcrumbs'
import { Browser } from './components/Browser'
import { ShareDialog } from './components/ShareDialog'
import { SharesDialog } from './components/SharesDialog'

type Session = { state: 'unknown' } | { state: 'out' } | { state: 'in'; me: Me }

export function App() {
  const [session, setSession] = useState<Session>({ state: 'unknown' })
  const [path, navigate] = usePath()
  const [error, setError] = useState('')
  const [sharePath, setSharePath] = useState<string | null>(null)
  const [showShares, setShowShares] = useState(false)

  // A live session goes straight in, so a reload does not ask again.
  useEffect(() => {
    let cancelled = false
    api
      .whoami()
      .then((me) => !cancelled && setSession({ state: 'in', me }))
      .catch(() => !cancelled && setSession({ state: 'out' }))
    return () => {
      cancelled = true
    }
  }, [])

  const report = useCallback((message: string) => {
    setError(message)
    // Long enough to read, short enough not to become furniture.
    window.setTimeout(() => setError((current) => (current === message ? '' : current)), 8000)
  }, [])

  async function signOut() {
    try {
      await api.logout()
    } catch {
      // Leaving either way; a failed logout still ends the session locally.
    }
    setSession({ state: 'out' })
    navigate('')
  }

  if (session.state === 'unknown') return null
  if (session.state === 'out') {
    return <Login onSignedIn={(me) => setSession({ state: 'in', me })} />
  }

  const user = session.me.user

  return (
    <>
      <header>
        <button type="button" className="icon" title="처음으로" onClick={() => navigate('')}>
          📁
        </button>
        <Breadcrumbs path={path} user={user} onNavigate={navigate} />
        <span className="spacer" />
        <button type="button" className="ghost" onClick={() => setShowShares(true)}>
          공유 링크
        </button>
        <span className="muted small who">{user}</span>
        <button type="button" className="ghost" onClick={() => void signOut()}>
          로그아웃
        </button>
      </header>

      {error && (
        <p className="error bar" role="alert">
          {error}
        </p>
      )}

      {path === '' ? (
        <Start user={user} onNavigate={navigate} />
      ) : (
        <Browser path={path} onNavigate={navigate} onError={report} onShare={setSharePath} />
      )}

      {sharePath !== null && (
        <ShareDialog path={sharePath} onClose={() => setSharePath(null)} onError={report} />
      )}
      {showShares && <SharesDialog onClose={() => setShowShares(false)} />}
    </>
  )
}

/**
 * The two places anything lives.
 *
 * Landing on a bare listing of the served root would show `homes` and `teams`,
 * which are directory names rather than an answer to "where are my files".
 */
function Start({ user, onNavigate }: { user: string; onNavigate: (path: string) => void }) {
  return (
    <main className="start">
      <button type="button" className="row" onClick={() => onNavigate(`homes/${user}`)}>
        <span className="icon" aria-hidden="true">
          🏠
        </span>
        <span className="name">내 폴더</span>
      </button>
      <button type="button" className="row" onClick={() => onNavigate('teams')}>
        <span className="icon" aria-hidden="true">
          👥
        </span>
        <span className="name">팀 폴더</span>
      </button>
    </main>
  )
}
