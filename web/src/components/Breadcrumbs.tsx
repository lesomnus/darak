import { TRASH_DIR } from '../lib/format'

/**
 * Labels the path in the words people use.
 *
 * `homes/<you>` reads as "내 폴더" with the name dropped: it is your own home,
 * so repeating your username adds nothing.
 */
function label(part: string, index: number, parts: string[], user: string): string | null {
  if (index === 0 && part === 'homes') return '내 폴더'
  if (index === 0 && part === 'teams') return '팀 폴더'
  if (index === 1 && parts[0] === 'homes' && part === user) return null
  if (part === TRASH_DIR) return '휴지통'
  return part
}

/**
 * Where a crumb goes, which is not always the path it stands for.
 *
 * The `homes` crumb is labelled "내 폴더" and used to navigate to `homes/` --
 * which is not your folder, it is the parent holding everyone's. Clicking your
 * own folder's name and landing on a list of your colleagues is a small
 * betrayal of the label, and it was the only way to reach that listing at all.
 * It now goes where it says.
 *
 * (`homes/` is also 0711 on disk now, so the listing is refused rather than
 * merely unlinked -- the interface is not what makes it private.)
 */
function target(parts: string[], index: number, user: string): string {
  const upto = parts.slice(0, index + 1).join('/')
  if (index === 0 && parts[0] === 'homes') return `homes/${user}`
  return upto
}

export function Breadcrumbs({
  path,
  user,
  onNavigate,
}: {
  path: string
  user: string
  onNavigate: (path: string) => void
}) {
  if (path === '') {
    return (
      <nav className="crumbs" aria-label="위치">
        <span className="current">처음</span>
      </nav>
    )
  }

  const parts = path.split('/').filter(Boolean)
  return (
    <nav className="crumbs" aria-label="위치">
      <button type="button" onClick={() => onNavigate('')}>
        처음
      </button>
      {parts.map((part, i) => {
        const text = label(part, i, parts, user)
        if (text === null) return null
        const to = target(parts, i, user)
        // The `homes` crumb resolves to your own home, so when you are already
        // there it is the current location rather than somewhere to go.
        const last = i === parts.length - 1 || to === path
        return (
          <span key={to} className="crumb">
            <span className="sep" aria-hidden="true">
              ›
            </span>
            {last ? (
              <span className="current" aria-current="page">
                {text}
              </span>
            ) : (
              <button type="button" onClick={() => onNavigate(to)}>
                {text}
              </button>
            )}
          </span>
        )
      })}
    </nav>
  )
}
