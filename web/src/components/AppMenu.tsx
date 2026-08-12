import * as Popover from '@radix-ui/react-popover'
import { useEffect, useState } from 'react'
import { api } from '../api'
import type { Theme } from '../lib/useTheme'
import { Icon, type IconName } from './Icon'
import { ThemeSwitch } from './ThemeSwitch'

/**
 * Everything that is not a file, behind one button.
 *
 * A Popover rather than a DropdownMenu. Radix's menu imposes menu semantics --
 * roving focus in one dimension, typeahead, Enter-selects -- and this is two
 * columns with a segmented control in one of them and a scrolling list in the
 * other. Under menu semantics the arrow keys would have to choose which column
 * they belong to, and the theme switch would be unreachable by the keys the
 * menu had taken over. A popover keeps ESC, click-outside, the focus trap and
 * the portal, and leaves Tab meaning what it means everywhere else.
 */
export function AppMenu({
  user,
  path,
  theme,
  onTheme,
  canAdmin,
  open,
  onOpenChange,
  onNavigate,
  onShares,
  onSignOut,
}: {
  user: string
  /** Where the browser is, so the folder list can mark the one you are in. */
  path: string
  theme: Theme
  onTheme: (next: Theme) => void
  /** Whether the operator page has anything to show this person. */
  canAdmin: boolean
  /**
   * Controlled by App rather than by this component.
   *
   * The start page is empty on a machine that has never been used, and the way
   * out of that is this card -- so the hint there has to be able to open it,
   * not just say where it is.
   */
  open: boolean
  onOpenChange: (open: boolean) => void
  onNavigate: (path: string) => void
  onShares: () => void
  onSignOut: () => void
}) {
  const setOpen = onOpenChange
  // Held HERE, not in FolderList. Radix unmounts the popover's content on close,
  // so state that lives inside it is thrown away every time the card shuts --
  // which turns "loaded when the card first opens" into a request per open.
  const [teams, setTeams] = useState<string[] | null>(null)
  const [teamsFailed, setTeamsFailed] = useState('')

  useEffect(() => {
    // Not with the page: one request most page loads would never need, for an
    // answer that changes about as often as the org chart.
    if (!open || teams !== null) return
    let cancelled = false
    setTeamsFailed('')
    api
      .list('teams')
      .then((r) => {
        if (cancelled) return
        setTeams(
          r.entries
            .filter((e) => e.dir)
            // readdir order is hash order on most filesystems, which is to say
            // no order at all.
            .map((e) => e.name)
            .sort((a, b) => a.localeCompare(b, 'ko')),
        )
      })
      .catch((e) => {
        // `teams` stays null, so the next open tries again.
        if (!cancelled) {
          setTeamsFailed(e instanceof Error ? e.message : '팀 폴더를 불러오지 못했습니다.')
        }
      })
    return () => {
      cancelled = true
    }
  }, [open, teams])

  function go(to: string) {
    setOpen(false)
    onNavigate(to)
  }

  return (
    <Popover.Root open={open} onOpenChange={onOpenChange}>
      <Popover.Trigger asChild>
        <button type="button" className="icon menu-button" aria-label="메뉴">
          <Icon name="menu" size={20} />
        </button>
      </Popover.Trigger>

      <Popover.Portal>
        <Popover.Content
          className="card"
          align="end"
          sideOffset={6}
          // Without this the card can sit flush against the edge of a phone
          // screen, where the rounded corner and the shadow both disappear.
          collisionPadding={8}
        >
          <div className="card-cols">
            <div className="card-col">
              <p className="card-who" title={user}>
                {user}
              </p>

              <div className="card-theme">
                <span className="card-label">테마</span>
                <ThemeSwitch theme={theme} onChange={onTheme} />
              </div>

              <div className="card-sep" />

              <CardItem
                icon="link"
                label="공유 링크"
                onClick={() => {
                  setOpen(false)
                  onShares()
                }}
              />
              <CardItem
                icon="settings"
                label="설정"
                disabled
                why="아직 준비 중입니다."
                onClick={() => {}}
              />
              {/* Shown to everyone, on purpose. Hiding it would make the page
                  look different to different people for no reason they can
                  see; disabled says "this exists and is not yours", which is
                  the true statement. It gates nothing either way -- every
                  route behind it is checked on the server. */}
              <CardItem
                icon="shield"
                label="관리"
                current={path === 'admin'}
                disabled={!canAdmin}
                why="관리자나 팀 소유자만 사용할 수 있습니다."
                onClick={() => go('admin')}
              />

              {/* Pushed to the bottom of the column: leaving is the last thing
                  on the list and the one action here that ends the session. */}
              <div className="card-sep push" />
              <CardItem icon="logout" label="로그아웃" onClick={onSignOut} />
            </div>

            <div className="card-col folders">
              <p className="card-label">폴더</p>
              <FolderList user={user} path={path} teams={teams} failed={teamsFailed} onGo={go} />
            </div>
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  )
}

function CardItem({
  icon,
  label,
  onClick,
  disabled = false,
  current = false,
  why,
}: {
  icon: IconName
  label: string
  onClick: () => void
  disabled?: boolean
  current?: boolean
  /** Why it is disabled. A dead control with no explanation is a puzzle. */
  why?: string
}) {
  return (
    <button
      type="button"
      className="card-item"
      disabled={disabled}
      // The current location is stated rather than merely tinted, so it reaches
      // a screen reader too.
      aria-current={current ? 'page' : undefined}
      title={disabled ? why : undefined}
      onClick={onClick}
    >
      <Icon name={icon} size={18} />
      <span className="label">{label}</span>
    </button>
  )
}

/**
 * The places worth going, as a list.
 *
 * What it lists is the CONTENTS of teams/, which is every team folder on the
 * server -- not the teams this person belongs to, which nothing here can know:
 * there is no route that answers "which groups am I in". Opening one you are
 * not in gives the same 403 that walking to it by hand gives, so this promises
 * no more than the 팀 폴더 listing already does.
 */
function FolderList({
  user,
  path,
  teams,
  failed,
  onGo,
}: {
  user: string
  path: string
  /** null while it is still being fetched. */
  teams: string[] | null
  failed: string
  onGo: (to: string) => void
}) {
  const home = `homes/${user}`
  const here = (to: string) => path === to || path.startsWith(to + '/')

  return (
    <div className="folder-list">
      <FolderButton
        icon="home"
        label="내 폴더"
        current={here(home)}
        onClick={() => onGo(home)}
      />
      {failed && <p className="muted small folder-note">{failed}</p>}
      {teams === null && !failed && <p className="muted small folder-note">불러오는 중…</p>}
      {teams !== null && teams.length === 0 && (
        <p className="muted small folder-note">팀 폴더가 없습니다.</p>
      )}
      {teams?.map((name) => (
        <FolderButton
          key={name}
          icon="team"
          label={name}
          current={here(`teams/${name}`)}
          onClick={() => onGo(`teams/${name}`)}
        />
      ))}
    </div>
  )
}

function FolderButton({
  icon,
  label,
  current,
  onClick,
}: {
  icon: IconName
  label: string
  current: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      className="card-item folder"
      aria-current={current ? 'page' : undefined}
      // Team names are as long as somebody decided to make them, and the column
      // is fixed; the full one has to be reachable somehow.
      title={label}
      onClick={onClick}
    >
      <Icon name={icon} size={18} />
      <span className="label">{label}</span>
    </button>
  )
}
