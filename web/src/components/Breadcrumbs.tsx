import { TRASH_DIR } from '../lib/format'

/**
 * Labels the path in the words people use.
 *
 * `homes/<you>` reads as "내 폴더" with the name dropped: it is your own home,
 * so repeating your username adds nothing. Someone else's home keeps the name,
 * because there it is the only informative part.
 */
function label(part: string, index: number, parts: string[], user: string): string | null {
  if (index === 0 && part === 'homes') return '내 폴더'
  if (index === 0 && part === 'teams') return '팀 폴더'
  if (index === 1 && parts[0] === 'homes' && part === user) return null
  if (part === TRASH_DIR) return '휴지통'
  return part
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
        const target = parts.slice(0, i + 1).join('/')
        const last = i === parts.length - 1
        return (
          <span key={target} className="crumb">
            <span className="sep" aria-hidden="true">
              ›
            </span>
            {last ? (
              <span className="current" aria-current="page">
                {text}
              </span>
            ) : (
              <button type="button" onClick={() => onNavigate(target)}>
                {text}
              </button>
            )}
          </span>
        )
      })}
    </nav>
  )
}
