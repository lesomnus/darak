import { useCallback, useEffect, useState } from 'react'
import { api } from './api'
import type { Me, PublicFolder } from './types'
import { usePath } from './lib/usePath'
import { useTheme } from './lib/useTheme'
import { useBranding } from './lib/useBranding'
import { usePlaces, type Places } from './lib/usePlaces'
import { describePath } from './lib/format'
import { Icon, type IconName } from './components/Icon'
import { Login } from './components/Login'
import { TopBar } from './components/TopBar'
import { Browser } from './components/Browser'
import { ShareDialog } from './components/ShareDialog'
import { ChangePassword } from './components/ChangePassword'
import { SharesDialog } from './components/SharesDialog'
import { Admin } from './components/Admin'

type Session = { state: 'unknown' } | { state: 'out' } | { state: 'in'; me: Me }

export function App() {
  const [session, setSession] = useState<Session>({ state: 'unknown' })
  const [path, navigate] = usePath()
  const [theme, setTheme] = useTheme()
  const brand = useBranding()
  const [error, setError] = useState('')
  // Lives here rather than in Browser because the box that fills it is in the
  // header, which Browser is not inside of.
  const [query, setQuery] = useState('')
  // Controlled, so the start page's hint can OPEN the menu rather than only
  // point at it. On an empty first screen, being told where a control is and
  // being taken to it are different amounts of help.
  const [menuOpen, setMenuOpen] = useState(false)
  const [sharePath, setSharePath] = useState<string | null>(null)
  const [showShares, setShowShares] = useState(false)
  const [changingPassword, setChangingPassword] = useState(false)
  // An anonymous visitor asked to sign in. Kept separate from the session so
  // cancelling returns them to the public folders they were browsing rather
  // than to a locked-out screen.
  const [wantsLogin, setWantsLogin] = useState(false)
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
    // The anonymous visitor owns nothing and administers nothing; asking would
    // only 401. Skip it, and leave both at their empty defaults.
    if (session.state !== 'in' || session.me.anonymous) return
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

  // A filter belongs to the directory it was typed in. Carrying it to the next
  // one shows an empty folder that is not empty, with the reason sitting in a
  // box at the top of the screen that nobody re-reads after they have used it
  // once. Keyed on `path` rather than done inside navigate() so that the back
  // button clears it too.
  useEffect(() => {
    setQuery('')
  }, [path])

  // Above the session gate below, because hooks cannot be called conditionally.
  // The empty user before sign-in reads and records nothing.
  const places = usePlaces(session.state === 'in' ? session.me.user : '', path)

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
  const anonymous = session.state === 'in' && session.me.anonymous === true
  // The login screen shows when there is no session at all, or when an anonymous
  // visitor asked for it. In the anonymous case cancelling returns them to the
  // public folders, so the screen is not a dead end.
  if (session.state === 'out' || wantsLogin) {
    // usePlaces is called unconditionally above, so this early return does not
    // skip a hook.
    return (
      <Login
        brand={brand}
        onSignedIn={(me) => {
          setSession({ state: 'in', me })
          setWantsLogin(false)
        }}
        onCancel={anonymous ? () => setWantsLogin(false) : undefined}
      />
    )
  }

  const user = session.me.user

  return (
    <>
      <TopBar
        brand={brand}
        path={path}
        user={user}
        query={query}
        onQuery={setQuery}
        // Only a listing can be filtered. The start page holds two buttons and
        // the operator page has its own filters.
        searchable={path !== '' && path !== ADMIN_PATH}
        theme={theme}
        onTheme={setTheme}
        canAdmin={isAdmin || myTeams.length > 0}
        menuOpen={menuOpen}
        onMenuOpen={setMenuOpen}
        onNavigate={navigate}
        onShares={() => setShowShares(true)}
        onPassword={() => setChangingPassword(true)}
        onSignOut={() => void signOut()}
        anonymous={anonymous}
        onSignIn={() => setWantsLogin(true)}
      />

      {error && (
        <p className="error bar" role="alert">
          {error}
        </p>
      )}

      {anonymous ? (
        // No account: the operator page is unreachable, so the admin path just
        // falls back to the public landing. Browsing works the same — the
        // kernel decides what the anonymous account can open.
        path === '' || path === ADMIN_PATH ? (
          <AnonStart onNavigate={navigate} onError={report} onSignIn={() => setWantsLogin(true)} />
        ) : (
          <Browser
            path={path}
            query={query}
            isFavourite={places.isFavourite}
            onToggleFavourite={places.toggleFavourite}
            onNavigate={navigate}
            onError={report}
            // No sessionless sharing: the share routes need a signed-in user.
            onShare={undefined}
          />
        )
      ) : path === ADMIN_PATH ? (
        // Rendered only when the server said so. If it did not, the panels
        // inside would each 404 -- which is the same answer a non-admin gets
        // for the API, so nothing is disclosed by trying.
        isAdmin || myTeams.length > 0 ? (
          <Admin me={user} isAdmin={isAdmin} sso={brand.sso} onError={report} />
        ) : (
          <Start
            user={user}
            places={places}
            onNavigate={navigate}
            onOpenMenu={() => setMenuOpen(true)}
          />
        )
      ) : path === '' ? (
        <Start
          user={user}
          places={places}
          onNavigate={navigate}
          onOpenMenu={() => setMenuOpen(true)}
        />
      ) : (
        <Browser
          path={path}
          query={query}
          isFavourite={places.isFavourite}
          onToggleFavourite={places.toggleFavourite}
          onNavigate={navigate}
          onError={report}
          onShare={setSharePath}
        />
      )}

      {sharePath !== null && (
        <ShareDialog path={sharePath} onClose={() => setSharePath(null)} onError={report} />
      )}
      {showShares && <SharesDialog onClose={() => setShowShares(false)} />}
      {changingPassword && (
        <ChangePassword onClose={() => setChangingPassword(false)} onError={report} />
      )}
    </>
  )
}

/**
 * The first screen: where things live, and where you have been.
 *
 * Landing on a bare listing of the served root would show `homes` and `teams`,
 * which are directory names rather than an answer to "where are my files".
 *
 * The two lists under them come from localStorage, so on a machine that has
 * never been used they are empty -- and an empty first screen is exactly when a
 * person has no idea what this thing contains. That case gets the hint, and the
 * hint OPENS the menu rather than describing where it is.
 */
function Start({
  user,
  places,
  onNavigate,
  onOpenMenu,
}: {
  user: string
  places: Places
  onNavigate: (path: string) => void
  onOpenMenu: () => void
}) {
  const { favourites, recents } = places
  return (
    <main className="start">
      <button type="button" className="row" onClick={() => onNavigate(`homes/${user}`)}>
        <span className="icon" data-kind="folder">
          <Icon name="home" size={22} />
        </span>
        <span className="name">내 폴더</span>
        <Icon name="chevron" size={16} className="go" />
      </button>
      <button type="button" className="row" onClick={() => onNavigate('teams')}>
        <span className="icon" data-kind="doc">
          <Icon name="team" size={22} />
        </span>
        <span className="name">팀 폴더</span>
        <Icon name="chevron" size={16} className="go" />
      </button>

      {favourites.length > 0 && (
        <section className="places">
          <h2>즐겨찾기</h2>
          {favourites.map((p) => (
            <PlaceRow
              key={p}
              path={p}
              user={user}
              icon="star-on"
              onGo={() => onNavigate(p)}
              // A starred folder that was deleted or that you lost access to
              // cannot be un-starred from inside it -- you cannot get inside it.
              // So the only way out is here.
              onForget={() => places.forgetFavourite(p)}
              forgetLabel="즐겨찾기에서 빼기"
            />
          ))}
        </section>
      )}

      {recents.length > 0 && (
        <section className="places">
          <h2>
            최근
            <button type="button" className="tiny ghost" onClick={places.clearRecents}>
              지우기
            </button>
          </h2>
          {recents.map((p) => (
            <PlaceRow key={p} path={p} user={user} icon="clock" onGo={() => onNavigate(p)} />
          ))}
        </section>
      )}

      {favourites.length === 0 && recents.length === 0 && (
        <p className="start-hint">
          여기에는 즐겨찾기한 폴더와 마지막으로 열어본 폴더가 쌓입니다. 아직 아무것도 없네요.
          <br />
          갈 수 있는 폴더 목록은 오른쪽 위 <Icon name="menu" size={15} className="inline-ico" />{' '}
          메뉴에 있습니다.
          <button type="button" className="ghost" onClick={onOpenMenu}>
            <Icon name="menu" size={17} />
            폴더 목록 열기
          </button>
        </p>
      )}
    </main>
  )
}

/**
 * The anonymous landing: the folders anyone may open without signing in.
 *
 * These come from the roster's `anonymous:` levels, not from a directory scan,
 * so the list is exactly what was declared public — a read-only folder is marked
 * as such, a writable one as read-write. Everything else on the server stays
 * invisible, and the kernel still decides what the anonymous account can open.
 */
function AnonStart({
  onNavigate,
  onError,
  onSignIn,
}: {
  onNavigate: (path: string) => void
  onError: (message: string) => void
  onSignIn: () => void
}) {
  const [folders, setFolders] = useState<PublicFolder[] | null>(null)
  useEffect(() => {
    const ac = new AbortController()
    api
      .publicFolders(ac.signal)
      .then((r) => setFolders(r.folders))
      .catch((e) => {
        if (ac.signal.aborted) return
        setFolders([])
        onError(e instanceof Error ? e.message : '공개 폴더를 불러오지 못했습니다.')
      })
    return () => ac.abort()
  }, [onError])

  return (
    <main className="start">
      <p className="muted anon-lead">로그인 없이 볼 수 있는 공개 폴더입니다.</p>

      {folders === null ? (
        <p className="muted">불러오는 중…</p>
      ) : folders.length === 0 ? (
        <p className="start-hint">아직 공개된 폴더가 없습니다.</p>
      ) : (
        folders.map((f) => (
          <button
            key={f.name}
            type="button"
            className="row"
            onClick={() => onNavigate(`teams/${f.name}`)}
          >
            <span className="icon" data-kind="folder">
              <Icon name="team" size={22} />
            </span>
            <span className="name">
              {f.name}
              {f.description && <span className="muted small"> — {f.description}</span>}
            </span>
            <span className="muted small anon-tag">{f.write ? '읽기·쓰기' : '읽기 전용'}</span>
            <Icon name="chevron" size={16} className="go" />
          </button>
        ))
      )}

      <p className="start-hint">
        계정이 있으신가요?{' '}
        <button type="button" className="ghost" onClick={onSignIn}>
          로그인
        </button>
      </p>
    </main>
  )
}

/**
 * One saved place.
 *
 * Two lines: the folder's own name, and the way to it. The last segment alone
 * is ambiguous -- three teams can each have a `문서` -- and the whole path on
 * one line is unreadable at the width of a row.
 */
function PlaceRow({
  path,
  user,
  icon,
  onGo,
  onForget,
  forgetLabel,
}: {
  path: string
  user: string
  icon: IconName
  onGo: () => void
  onForget?: () => void
  forgetLabel?: string
}) {
  const { name, trail } = describePath(path, user)
  return (
    <div className="place">
      <button type="button" className="row" onClick={onGo}>
        <span className="icon" data-kind={icon === 'star-on' ? 'folder' : undefined}>
          <Icon name={icon} size={18} />
        </span>
        <span className="name">
          {name}
          {trail && <span className="trail">{trail}</span>}
        </span>
      </button>
      {onForget && (
        <button
          type="button"
          className="icon place-forget"
          aria-label={`${name} ${forgetLabel}`}
          title={forgetLabel}
          onClick={onForget}
        >
          <Icon name="close" size={15} />
        </button>
      )}
    </div>
  )
}

/**
 * The operator page's location.
 *
 * `admin` is not a valid first segment of the served tree (only `homes` and
 * `teams` are), so this can never collide with a real path.
 */
const ADMIN_PATH = 'admin'
