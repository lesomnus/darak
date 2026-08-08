import * as Menu from '@radix-ui/react-dropdown-menu'
import type { KeyboardEvent, MouseEvent } from 'react'
import type { Entry } from '../types'
import { formatDate, formatSize, iconFor, TRASH_DIR } from '../lib/format'
import { Icon } from './Icon'

export function FileRow({
  entry,
  inTrash,
  onOpen,
  onShare,
  onDelete,
}: {
  entry: Entry
  inTrash: boolean
  onOpen: () => void
  onShare: () => void
  onDelete: () => void
}) {
  function onKeyDown(ev: KeyboardEvent) {
    if (ev.key === 'Enter' || ev.key === ' ') {
      ev.preventDefault()
      onOpen()
    }
  }

  const isTrashFolder = entry.name === TRASH_DIR
  const kind = iconFor(entry)
  // A directory has nothing to download or share, and the trash folder is the
  // one thing that cannot be thrown away -- together that leaves the menu with
  // no items, and a button that opens an empty box is worse than no button.
  const hasMenu = !entry.dir || !isTrashFolder

  return (
    <div className="row" role="button" tabIndex={0} onClick={onOpen} onKeyDown={onKeyDown}>
      <span className="icon" data-kind={kind}>
        <Icon name={kind} />
      </span>
      <span className="name">{entry.name}</span>
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
        {hasMenu && (
        <Menu.Root>
          <Menu.Trigger asChild>
            <button
              type="button"
              className="icon reveal"
              aria-label={`${entry.name} 작업`}
              // The row itself is the open-this control, so a click meant for
              // the menu must not also open what it is a menu for.
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
                  <Menu.Item className="menu-item" onSelect={onShare}>
                    <Icon name="link" size={18} />
                    공유 링크 만들기
                  </Menu.Item>
                </>
              )}
              {!entry.dir && !isTrashFolder && <Menu.Separator className="menu-sep" />}
              {!isTrashFolder && (
                <Menu.Item className="menu-item danger" onSelect={onDelete}>
                  <Icon name="trash" size={18} />
                  {/* Inside the trash there is nowhere further to move
                      something, so the same action really deletes -- and says
                      so. */}
                  {inTrash ? '완전히 지우기' : '휴지통으로 보내기'}
                </Menu.Item>
              )}
            </Menu.Content>
          </Menu.Portal>
        </Menu.Root>
        )}
      </span>
    </div>
  )
}
