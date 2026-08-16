/**
 * The icon set.
 *
 * Emoji were here before, and they are the wrong tool for a UI: 📁 is a
 * different size, weight and colour on macOS, Windows and Android, none of
 * which the page controls, and none of which match each other. Half the reason
 * the interface looked unfinished was that its most repeated element rendered
 * differently on every machine that opened it.
 *
 * These are drawn on one 24x24 grid with one stroke width, so they line up with
 * each other and with the text beside them. They inherit `currentColor`, which
 * is what lets a menu item tint its icon by hovering the item rather than the
 * icon. Inline rather than a sprite or a font: the whole set is smaller than
 * one HTTP request, and there is no flash before it arrives.
 */
import type { ReactNode, SVGProps } from 'react'

export type IconName =
  | 'folder'
  | 'file'
  | 'image'
  | 'video'
  | 'audio'
  | 'pdf'
  | 'archive'
  | 'sheet'
  | 'doc'
  | 'home'
  | 'team'
  | 'trash'
  | 'link'
  | 'upload'
  | 'download'
  | 'folder-plus'
  | 'more'
  | 'chevron'
  | 'sun'
  | 'moon'
  | 'monitor'
  | 'close'
  | 'check'
  | 'copy'
  | 'shield'
  | 'logout'
  | 'disk'
  | 'clock'
  | 'search'
  | 'settings'
  | 'menu'
  | 'star'
  | 'star-on'
  | 'key'
  | 'lock'

interface Props extends Omit<SVGProps<SVGSVGElement>, 'name'> {
  name: IconName
  /** Edge length in px. The stroke is scaled with it so a large icon does not
      turn spindly and a small one does not turn into a blob. */
  size?: number
}

export function Icon({ name, size = 20, className, ...rest }: Props) {
  return (
    <svg
      // Merged, not replaced: a caller passing className wants to add a
      // position or a colour, not to lose the layout every icon depends on.
      className={className ? `ico ${className}` : 'ico'}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={(1.7 * 20) / size}
      strokeLinecap="round"
      strokeLinejoin="round"
      // Decorative by default: every place these are used either sits beside a
      // text label or is a control that carries its own aria-label. A caller
      // that needs otherwise passes role/aria-label through.
      aria-hidden="true"
      focusable="false"
      {...rest}
    >
      {PATHS[name]}
    </svg>
  )
}

const PATHS: Record<IconName, ReactNode> = {
  folder: <path d="M3 7a2 2 0 0 1 2-2h4l2 2.5h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />,
  file: (
    <>
      <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
      <path d="M14 3v5h5" />
    </>
  ),
  image: (
    <>
      <rect x="3" y="4.5" width="18" height="15" rx="2" />
      <circle cx="8.5" cy="9.5" r="1.6" />
      <path d="m3.5 16.5 4-4a1.6 1.6 0 0 1 2.3 0l4.7 4.7M14 14.5l1.6-1.6a1.6 1.6 0 0 1 2.3 0l2.6 2.6" />
    </>
  ),
  video: (
    <>
      <rect x="3" y="5" width="18" height="14" rx="2" />
      <path d="m10.5 9 4.5 3-4.5 3z" />
    </>
  ),
  audio: (
    <>
      <path d="M9 17.5V6.8l10-2v9" />
      <circle cx="7" cy="17.5" r="2" />
      <circle cx="17" cy="15.5" r="2" />
    </>
  ),
  pdf: (
    <>
      <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
      <path d="M14 3v5h5M8.5 13h7M8.5 16.5h4.5" />
    </>
  ),
  archive: (
    <>
      <path d="m3.5 8 8-4.1a1 1 0 0 1 .9 0l8.1 4.1v8.3a1 1 0 0 1-.55.9l-7.5 3.7a1 1 0 0 1-.9 0l-7.5-3.7a1 1 0 0 1-.55-.9z" />
      <path d="m3.5 8 8.5 4.2L20.5 8M12 12.2V21" />
    </>
  ),
  sheet: (
    <>
      <rect x="3" y="4.5" width="18" height="15" rx="2" />
      <path d="M3 9.5h18M9.5 9.5v10" />
    </>
  ),
  doc: (
    <>
      <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
      <path d="M14 3v5h5M8.5 13h7M8.5 16.5h7" />
    </>
  ),
  home: (
    <>
      <path d="m3 11 9-7 9 7" />
      <path d="M5.5 9.6V19a1 1 0 0 0 1 1h11a1 1 0 0 0 1-1V9.6" />
      <path d="M9.5 20v-5a1 1 0 0 1 1-1h3a1 1 0 0 1 1 1v5" />
    </>
  ),
  team: (
    <>
      <circle cx="9" cy="8" r="3.4" />
      <path d="M2.5 20v-1.4A4.6 4.6 0 0 1 7.1 14h3.8a4.6 4.6 0 0 1 4.6 4.6V20" />
      <path d="M16.2 4.9a3.4 3.4 0 0 1 0 6.2M17.4 14h.5a4.6 4.6 0 0 1 4.6 4.6V20" />
    </>
  ),
  trash: (
    <>
      <path d="M4 7h16M9.5 7V5.6A1.6 1.6 0 0 1 11.1 4h1.8a1.6 1.6 0 0 1 1.6 1.6V7" />
      <path d="m6.6 7 .8 11.2a2 2 0 0 0 2 1.8h5.2a2 2 0 0 0 2-1.8L17.4 7" />
      <path d="M10.2 11v5.5M13.8 11v5.5" />
    </>
  ),
  link: (
    <>
      <path d="M10.6 13.4a4 4 0 0 0 5.7 0l2.5-2.5a4 4 0 1 0-5.7-5.7l-1.4 1.4" />
      <path d="M13.4 10.6a4 4 0 0 0-5.7 0l-2.5 2.5a4 4 0 1 0 5.7 5.7l1.4-1.4" />
    </>
  ),
  upload: (
    <>
      <path d="M12 15.5V4M7.8 8.2 12 4l4.2 4.2" />
      <path d="M4 15v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3" />
    </>
  ),
  download: (
    <>
      <path d="M12 4v11.5M7.8 11.3l4.2 4.2 4.2-4.2" />
      <path d="M4 15v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3" />
    </>
  ),
  'folder-plus': (
    <>
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2.5h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
      <path d="M12 11v5.5M9.25 13.75h5.5" />
    </>
  ),
  // Filled, because three hairline rings read as noise at this size.
  more: (
    <g fill="currentColor" stroke="none">
      <circle cx="5.5" cy="12" r="1.6" />
      <circle cx="12" cy="12" r="1.6" />
      <circle cx="18.5" cy="12" r="1.6" />
    </g>
  ),
  chevron: <path d="m9.5 5.5 6.5 6.5-6.5 6.5" />,
  sun: (
    <>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.3 5.3l1.4 1.4M17.3 17.3l1.4 1.4M18.7 5.3l-1.4 1.4M6.7 17.3l-1.4 1.4" />
    </>
  ),
  moon: <path d="M20.5 14.7A8.6 8.6 0 0 1 9.3 3.5a8.6 8.6 0 1 0 11.2 11.2z" />,
  monitor: (
    <>
      <rect x="3" y="4.5" width="18" height="12" rx="2" />
      <path d="M8.5 20h7M12 16.5V20" />
    </>
  ),
  close: <path d="m6.5 6.5 11 11M17.5 6.5l-11 11" />,
  check: <path d="m5 12.5 4.5 4.5L19 7.5" />,
  copy: (
    <>
      <rect x="9" y="9" width="11" height="11" rx="2" />
      <path d="M5.5 15H5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1h9a1 1 0 0 1 1 1v.5" />
    </>
  ),
  shield: (
    <>
      <path d="M12 3.2 4.8 6v5.6c0 4.2 2.9 7.6 7.2 9.2 4.3-1.6 7.2-5 7.2-9.2V6z" />
      <path d="m9 12.2 2.2 2.2 4-4.2" />
    </>
  ),
  logout: (
    <>
      <path d="M14 4h4a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-4" />
      <path d="m9 8.5-3.5 3.5L9 15.5M5.5 12H15" />
    </>
  ),
  disk: (
    <>
      <path d="M3.5 12.5 6.2 5.9A2 2 0 0 1 8.1 4.6h7.8a2 2 0 0 1 1.9 1.3l2.7 6.6" />
      <rect x="3.5" y="12.5" width="17" height="6.9" rx="2" />
      <path d="M7 16h.01" />
    </>
  ),
  clock: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 7.2V12l3 1.9" />
    </>
  ),
  search: (
    <>
      <circle cx="10.8" cy="10.8" r="6.3" />
      <path d="m15.5 15.5 4.4 4.4" />
    </>
  ),
  // Not `more` (⋯), which already means "this one row's actions" in the
  // listing. Two different things opening from two different places must not
  // wear the same mark.
  menu: <path d="M4 7h16M4 12h16M4 17h16" />,
  // The two states of one control, so they are the same outline and only the
  // fill changes -- a star that also changed shape when you pressed it would
  // read as a different button.
  star: <path d="m12 3.6 2.6 5.3 5.9.9-4.3 4.1 1 5.8-5.2-2.7-5.2 2.7 1-5.8-4.3-4.1 5.9-.9z" />,
  'star-on': (
    <path
      fill="currentColor"
      d="m12 3.6 2.6 5.3 5.9.9-4.3 4.1 1 5.8-5.2-2.7-5.2 2.7 1-5.8-4.3-4.1 5.9-.9z"
    />
  ),
  // A cog, and it has to be a cog: the first attempt was a circle with eight
  // spokes, which is the `sun` above with a bigger middle -- so "설정" and
  // "밝게" were the same picture, two inches apart, in the same card.
  settings: (
    <>
      <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
      <circle cx="12" cy="12" r="3" />
    </>
  ),
  key: (
    <>
      <circle cx="8" cy="15" r="4" />
      <path d="m10.9 12.1 8.6-8.6M17 6l2.5 2.5M14.5 8.5 17 11" />
    </>
  ),
  lock: (
    <>
      <rect x="4.5" y="10.5" width="15" height="9.5" rx="2" />
      <path d="M8 10.5V7a4 4 0 0 1 8 0v3.5" />
    </>
  ),
}
