import { useCallback, useEffect, useState } from 'react'
import { api } from './api'
import type { Me } from './types'
import { usePath } from './lib/usePath'
import { Login } from './components/Login'
import { Breadcrumbs } from './components/Breadcrumbs'
import { Browser } from './components/Browser'
import { ShareDialog } from './components/ShareDialog'
import { SharesDialog } from './components/SharesDialog'
import { Admin } from './components/Admin'

type Session = { state: 'unknown' } | { state: 'out' } | { state: 'in'; me: Me }

export function App() {
  const [session, setSession] = useState<Session>({ state: 'unknown' })
  const [path, navigate] = usePath()
  const [error, setError] = useState('')
  const [sharePath, setSharePath] = useState<string | null>(null)
  const [showShares, setShowShares] = useState(false)
  // Whether to OFFER the page. The gate is on the server, on every route, so
  // this only decides whether a link is drawn -- a browser that lies to itself
  // reaches nothing it could not reach anyway.
  const [isAdmin, setIsAdmin] = useState(false)
  // Teams this person may change the membership of. Admins get all of them,
  // owners get theirs. Same rule as isAdmin: it decides what is DRAWN, and the
  // server decides what works.
  const [myTeams, setMyTeams] = useState<string[]>([])

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

  useEffect(() => {
    if (session.state !== 'in') return
    let cancelled = false
    api
      .adminWhoami()
      .then((r) => !cancelled && setIsAdmin(r.admin))
      .catch(() => !cancelled && setIsAdmin(false))
    api
      .teamWhoami()
      .then((r) => !cancelled && setMyTeams(r.teams))
      .catch(() => !cancelled && setMyTeams([]))
    return () => {
      cancelled = true
    }
  }, [session.state])

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
    setIsAdmin(false)
    setMyTeams([])
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
        {(isAdmin || myTeams.length > 0) && (
          <button
            type="button"
            className="ghost"
            onClick={() => navigate(path === ADMIN_PATH ? '' : ADMIN_PATH)}
          >
            {path === ADMIN_PATH ? '파일로' : isAdmin ? '관리' : '팀'}
          </button>
        )}
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

      {path === ADMIN_PATH ? (
        // Rendered only when the server said so. If it did not, the panels
        // inside would each 404 -- which is the same answer a non-admin gets
        // for the API, so nothing is disclosed by trying.
        isAdmin || myTeams.length > 0 ? (
          <Admin me={user} isAdmin={isAdmin} onError={report} />
        ) : (
          <Start user={user} onNavigate={navigate} />
        )
      ) : path === '' ? (
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

/**
 * The operator page's location.
 *
 * `admin` is not a valid first segment of the served tree (only `homes` and
 * `teams` are), so this can never collide with a real path.
 */
const ADMIN_PATH = 'admin'
