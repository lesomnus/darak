import * as Menu from '@radix-ui/react-dropdown-menu'
import type { MouseEvent } from 'react'
import type { SortKey } from '../lib/format'
import type { ViewPrefs } from '../lib/useViewPrefs'
import { Icon } from './Icon'

const SORTS: { key: SortKey; label: string }[] = [
  { key: 'name', label: '이름' },
  { key: 'mtime', label: '수정한 날짜' },
  { key: 'size', label: '크기' },
]

/**
 * The list/grid toggle and the "view options" menu (density, sort, columns).
 *
 * On the toolbar's right, away from the file actions. Everything it changes is a
 * per-viewer preference remembered in localStorage (see useViewPrefs), so it
 * carries no server state and never fails.
 */
export function ViewControls({ prefs }: { prefs: ViewPrefs }) {
  return (
    <div className="view-controls">
      <div className="seg" role="group" aria-label="보기 방식">
        <button
          type="button"
          className={prefs.view === 'list' ? 'active' : ''}
          aria-pressed={prefs.view === 'list'}
          aria-label="목록으로 보기"
          onClick={() => prefs.setView('list')}
        >
          <Icon name="list" size={17} />
        </button>
        <button
          type="button"
          className={prefs.view === 'grid' ? 'active' : ''}
          aria-pressed={prefs.view === 'grid'}
          aria-label="격자로 보기"
          onClick={() => prefs.setView('grid')}
        >
          <Icon name="grid" size={17} />
        </button>
      </div>

      <Menu.Root>
        <Menu.Trigger asChild>
          <button type="button" className="ghost" aria-label="보기 옵션">
            <Icon name="settings" size={17} />
          </button>
        </Menu.Trigger>
        <Menu.Portal>
          <Menu.Content className="menu" align="end" sideOffset={4}>
            <Menu.Label className="menu-label">정렬</Menu.Label>
            {SORTS.map((s) => (
              <Menu.Item
                key={s.key}
                className="menu-item"
                // Keep the menu open so the direction can be flipped with a
                // second click on the same key without reopening it.
                onSelect={(e) => {
                  e.preventDefault()
                  prefs.setSort(s.key)
                }}
              >
                <span className="menu-check">
                  {prefs.sortKey === s.key && <Icon name="check" size={16} />}
                </span>
                {s.label}
                {prefs.sortKey === s.key && (
                  <span className="menu-hint">{prefs.sortDir === 'asc' ? '오름차순' : '내림차순'}</span>
                )}
              </Menu.Item>
            ))}

            <Menu.Separator className="menu-sep" />
            <Menu.Label className="menu-label">밀도</Menu.Label>
            <Menu.Item
              className="menu-item"
              onSelect={(e: Event) => {
                e.preventDefault()
                prefs.setDensity(prefs.density === 'comfortable' ? 'compact' : 'comfortable')
              }}
            >
              <span className="menu-check">
                {prefs.density === 'compact' && <Icon name="check" size={16} />}
              </span>
              조밀하게
            </Menu.Item>

            {/* Columns apply to the list; in grid there is nothing to show them
                on, so the section is only offered there. */}
            {prefs.view === 'list' && (
              <>
                <Menu.Separator className="menu-sep" />
                <Menu.Label className="menu-label">열</Menu.Label>
                <ColumnItem label="크기" on={prefs.columns.size} onToggle={() => prefs.toggleColumn('size')} />
                <ColumnItem label="수정한 날짜" on={prefs.columns.date} onToggle={() => prefs.toggleColumn('date')} />
              </>
            )}
          </Menu.Content>
        </Menu.Portal>
      </Menu.Root>
    </div>
  )
}

function ColumnItem({ label, on, onToggle }: { label: string; on: boolean; onToggle: () => void }) {
  return (
    <Menu.Item
      className="menu-item"
      onSelect={(e: Event | MouseEvent) => {
        e.preventDefault()
        onToggle()
      }}
    >
      <span className="menu-check">{on && <Icon name="check" size={16} />}</span>
      {label}
    </Menu.Item>
  )
}
