import { useEffect, useRef, useState } from 'react'
import type { Branding } from '../types'
import type { Theme } from '../lib/useTheme'
import { AppMenu } from './AppMenu'
import { Breadcrumbs } from './Breadcrumbs'
import { Icon } from './Icon'

/**
 * The bar, and the path under it.
 *
 * Three things on one line -- the mark, the search box, one button -- and the
 * location on a quieter second line. The path used to share the line with five
 * controls, which on a phone left it about two characters wide; it is the thing
 * that says where you are, and it loses that argument to nothing else here.
 *
 * The line is a grid rather than a flex row so the search box is centred in the
 * WINDOW, not in whatever space the mark happens to leave. A logo is an
 * operator's file and can be any width, and a search box that drifts sideways
 * depending on how long the company's name is looks like a mistake.
 */
export function TopBar({
  brand,
  path,
  user,
  query,
  onQuery,
  searchable,
  theme,
  onTheme,
  canAdmin,
  menuOpen,
  onMenuOpen,
  onNavigate,
  onShares,
  onSignOut,
}: {
  brand: Branding
  path: string
  user: string
  query: string
  onQuery: (q: string) => void
  /** False where there is no listing to filter -- the start and admin pages. */
  searchable: boolean
  theme: Theme
  onTheme: (next: Theme) => void
  canAdmin: boolean
  /** Controlled from App so the start page's hint can open the card. */
  menuOpen: boolean
  onMenuOpen: (open: boolean) => void
  onNavigate: (path: string) => void
  onShares: () => void
  onSignOut: () => void
}) {
  return (
    <header>
      {/* Not `bar`: the error banner and the upload progress fill both already
          use that name, and a grid template leaking into either of them is a
          layout bug two components away from where it was written. */}
      <div className="topbar">
        <Brand brand={brand} onClick={() => onNavigate('')} />
        <SearchBox query={query} onQuery={onQuery} enabled={searchable} />
        <AppMenu
          user={user}
          path={path}
          theme={theme}
          onTheme={onTheme}
          canAdmin={canAdmin}
          open={menuOpen}
          onOpenChange={onMenuOpen}
          onNavigate={onNavigate}
          onShares={onShares}
          onSignOut={onSignOut}
        />
      </div>

      {/* Not on the start page: there the whole content is the two places you
          can go, and a breadcrumb reading "처음" under it says nothing. */}
      {path !== '' && (
        <div className="pathbar">
          <Breadcrumbs path={path} user={user} onNavigate={onNavigate} />
        </div>
      )}
    </header>
  )
}

function Brand({ brand, onClick }: { brand: Branding; onClick: () => void }) {
  // The server said there is a logo, so there is one -- unless it restarted
  // with a different flag between that answer and this request. Cheap enough to
  // survive.
  const [broken, setBroken] = useState(false)

  return (
    <button
      type="button"
      className="brand"
      // The visible name is hidden on a narrow screen, so the button cannot
      // rely on it for a name of its own.
      aria-label={`${brand.name} — 처음으로`}
      title="처음으로"
      onClick={onClick}
    >
      {brand.logo && !broken ? (
        <img
          className="brand-logo"
          src="/api/branding/logo"
          alt=""
          onError={() => setBroken(true)}
        />
      ) : (
        <Icon name="folder" size={22} className="brand-mark" />
      )}
      <span className="brand-name">{brand.name}</span>
    </button>
  )
}

/**
 * Filters the directory you are looking at.
 *
 * It says so, in the placeholder, because a search box in the middle of a top
 * bar reads as "search everything" and this one cannot: there is no endpoint
 * that walks the tree, and inventing one client-side would mean listing every
 * directory a user can reach on every keystroke.
 */
function SearchBox({
  query,
  onQuery,
  enabled,
}: {
  query: string
  onQuery: (q: string) => void
  enabled: boolean
}) {
  const ref = useRef<HTMLInputElement>(null)

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        ref.current?.focus()
        ref.current?.select()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <div className={enabled ? 'search' : 'search off'}>
      <Icon name="search" size={17} className="search-mark" />
      <input
        ref={ref}
        type="search"
        value={query}
        disabled={!enabled}
        placeholder={enabled ? '이 폴더에서 찾기' : '찾기'}
        aria-label="이 폴더에서 파일 이름으로 찾기"
        // Filenames are not prose; the browser's own guesses about them are
        // always wrong and the first capital letter is actively harmful.
        autoComplete="off"
        autoCorrect="off"
        autoCapitalize="off"
        spellCheck={false}
        onChange={(e) => onQuery(e.target.value)}
        onKeyDown={(e) => {
          if (e.key !== 'Escape') return
          // First press clears, second gets out of the box. Clearing and
          // blurring at once loses whichever one you meant.
          if (query) {
            e.preventDefault()
            onQuery('')
          } else {
            ref.current?.blur()
          }
        }}
      />
      {query && (
        <button
          type="button"
          className="icon search-clear"
          aria-label="검색어 지우기"
          onClick={() => {
            onQuery('')
            ref.current?.focus()
          }}
        >
          <Icon name="close" size={15} />
        </button>
      )}
    </div>
  )
}
