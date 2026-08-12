// Labels come from lib/format so the saved-places lists on the start page name
// a folder exactly the way the crumbs do. `homes/<you>` reads as "내 폴더" with
// the username dropped: it is your own home, so repeating it adds nothing.
import { segmentLabel as label } from '../lib/format'
import { Icon } from './Icon'

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
            {/* An SVG chevron rather than the character '›', which is a
                different weight and sits at a different height in every font
                the stack falls through. */}
            <Icon name="chevron" size={14} className="sep" />
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
