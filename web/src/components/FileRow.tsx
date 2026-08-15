import * as Menu from '@radix-ui/react-dropdown-menu'
import { useState, type KeyboardEvent, type MouseEvent, type ReactNode } from 'react'
import type { Entry } from '../types'
import type { Folded, Match } from '../lib/fuzzy'
import { formatDate, formatSize, iconFor, TRASH_DIR } from '../lib/format'
import { Icon } from './Icon'

/**
 * One listing entry, with everything the search needed precomputed.
 *
 * The extra strings are built once per listing (see Browser) rather than once
 * per keystroke, which is what keeps typing in a 50,000-entry directory
 * responsive.
 */
export interface Row extends Folded {
  entry: Entry
  /** Null when nothing is being searched for. */
  match: Match | null
}

export function FileRow({
  row,
  inTrash,
  favourite,
  onOpen,
  onShare,
  onDelete,
  onToggleFavourite,
  onChmod,
}: {
  row: Row
  inTrash: boolean
  favourite: boolean
  onOpen: () => void
  /** Absent for an anonymous visitor: sessionless sharing is not offered. */
  onShare?: () => void
  onDelete: () => void
  onToggleFavourite: () => void
  onChmod: () => void
}) {
  const entry = row.entry
  // Whether this row's dropdown machinery has been built yet, and whether it is
  // showing. See the comment at the trigger below.
  const [armed, setArmed] = useState(false)
  const [open, setOpen] = useState(false)

  function onKeyDown(ev: KeyboardEvent) {
    // Only when the row itself has the keyboard.
    //
    // Without this, Enter on the ⋯ button opens the menu AND bubbles up to
    // here, which opens the folder underneath it -- so the menu you just asked
    // for is unmounted by the navigation in the same keystroke. The click
    // handler has always had the equivalent guard (the button stops
    // propagation); this one did not, so the control was reachable by keyboard
    // and unusable by it.
    if (ev.target !== ev.currentTarget) return
    if (ev.key === 'Enter' || ev.key === ' ') {
      ev.preventDefault()
      onOpen()
    }
  }

  const isTrashFolder = entry.name === TRASH_DIR
  const kind = iconFor(entry)
  // A directory has nothing to download or share, and the trash folder is the
  // one thing that cannot be thrown away or kept as a shortcut -- together that
  // leaves the menu with no items, and a button that opens an empty box is
  // worse than no button.
  const hasMenu = !(entry.dir && isTrashFolder)

  return (
    // data-row is what the virtualiser measures: it needs one real, laid-out
    // row to know how tall the rest would be.
    <div className="row" data-row role="button" tabIndex={0} onClick={onOpen} onKeyDown={onKeyDown}>
      <span className="icon" data-kind={kind}>
        <Icon name={kind} />
      </span>
      <span className="name">
        <Marked text={row.nfc} positions={row.alignable ? row.match?.positions : undefined} />
      </span>
      {/* Wrapped so the two can move together. On a wide screen the wrapper is
          `display: contents` and they are columns of the row; on a narrow one it
          becomes a line of its own under the name. Without something to move as
          a unit the name was being squeezed to two characters to keep them on
          the same line. */}
      <span className="metas">
        <span className="meta size">{entry.dir ? '' : formatSize(entry.size)}</span>
        <span className="meta date">{formatDate(entry.mod_time)}</span>
      </span>

      <span className="actions">
        {/* One menu instead of a row of always-visible buttons. Two icons per
            row was already crowding the name at 34rem; every action added
            after this one would have taken more of it. The menu costs one
            click and gives the actions room to be named. */}
        {hasMenu && !armed && (
          // A plain button until somebody reaches for it.
          //
          // Radix's DropdownMenu.Root is not free: measured at ~1.85ms per row
          // to mount, which is 0.7s for a 400-file directory and, once search
          // arrived, 0.3s of every keystroke that narrows a big listing down to
          // a few hundred rows -- all of it spent building menus for rows
          // nobody will click. Deferring the machinery to the row that is
          // actually being used costs one extra render of one row.
          <button
            type="button"
            className="icon reveal"
            aria-haspopup="menu"
            aria-label={`${entry.name} 작업`}
            onClick={(ev: MouseEvent) => {
              // The row itself is the open-this control, so a click meant for
              // the menu must not also open what it is a menu for.
              ev.stopPropagation()
              // Mounted already open: to the person clicking, this is the same
              // single click that used to open it.
              setArmed(true)
              setOpen(true)
            }}
          >
            <Icon name="more" size={18} />
          </button>
        )}
        {hasMenu && armed && (
          <Menu.Root open={open} onOpenChange={setOpen}>
            <Menu.Trigger asChild>
              <button
                type="button"
                className="icon reveal"
                aria-label={`${entry.name} 작업`}
                onClick={(ev: MouseEvent) => ev.stopPropagation()}
              >
                <Icon name="more" size={18} />
              </button>
            </Menu.Trigger>

            <Menu.Portal>
              <Menu.Content
                className="menu"
                align="end"
                sideOffset={4}
                // Radix restores focus to the trigger on close, which sits inside
                // the row; without this the row's own handler fires on the way.
                onClick={(ev) => ev.stopPropagation()}
              >
                {!entry.dir && (
                  <>
                    <Menu.Item className="menu-item" onSelect={onOpen}>
                      <Icon name="download" size={18} />
                      다운로드
                    </Menu.Item>
                    {onShare && (
                      <Menu.Item className="menu-item" onSelect={onShare}>
                        <Icon name="link" size={18} />
                        공유 링크 만들기
                      </Menu.Item>
                    )}
                  </>
                )}
                {/* Only folders. A favourite is somewhere to GO -- starring a
                    file would put a row on the start page that downloads
                    something when clicked, which is not what a list of places
                    is for. */}
                {entry.dir && (
                  <Menu.Item className="menu-item" onSelect={onToggleFavourite}>
                    <Icon name={favourite ? 'star-on' : 'star'} size={18} />
                    {favourite ? '즐겨찾기에서 빼기' : '즐겨찾기'}
                  </Menu.Item>
                )}
                {/* Not in the trash: changing the mode of something on its way
                    out is busywork, and the trash is the one place where the
                    answer to "who can read this" stopped mattering. */}
                {!inTrash && (
                  <Menu.Item className="menu-item" onSelect={onChmod}>
                    <Icon name="key" size={18} />
                    권한 변경
                  </Menu.Item>
                )}
                <Menu.Separator className="menu-sep" />
                <Menu.Item className="menu-item danger" onSelect={onDelete}>
                  <Icon name="trash" size={18} />
                  {/* Inside the trash there is nowhere further to move
                      something, so the same action really deletes -- and says
                      so. */}
                  {inTrash ? '완전히 지우기' : '휴지통으로 보내기'}
                </Menu.Item>
              </Menu.Content>
            </Menu.Portal>
          </Menu.Root>
        )}
      </span>
    </div>
  )
}

/**
 * Shows which characters the search matched.
 *
 * Exported because the recursive results draw the same highlight from positions
 * the SERVER computed -- which is only safe because internal/fuzzy and
 * lib/fuzzy agree on what a position means (UTF-16 code units into the NFC
 * name), and testdata/vectors.json is what holds them to it.
 *
 * Without this a fuzzy result list is a set of assertions with no evidence:
 * `ㅎㅇㄹ` returns 회의록 and nobody can see why, and a scattered match looks
 * like a bug rather than a match. Marking the characters turns the ranking into
 * something a person can agree or disagree with.
 *
 * Adjacent positions are merged into one <mark>, which matters more than it
 * sounds: a run of eight highlighted characters as eight elements is eight
 * inline boxes for the text shaper to break between.
 */
export function Marked({ text, positions }: { text: string; positions?: number[] }) {
  if (!positions || positions.length === 0) return <>{text}</>

  const out: ReactNode[] = []
  let at = 0
  for (let i = 0; i < positions.length; ) {
    const start = positions[i] as number
    let end = start
    while (i + 1 < positions.length && positions[i + 1] === end + 1) {
      end++
      i++
    }
    i++
    if (start > at) out.push(text.slice(at, start))
    out.push(<mark key={start}>{text.slice(start, end + 1)}</mark>)
    at = end + 1
  }
  if (at < text.length) out.push(text.slice(at))
  return <>{out}</>
}
