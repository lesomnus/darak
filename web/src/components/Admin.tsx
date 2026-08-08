import { useCallback, useEffect, useState } from 'react'
import { api } from '../api'
import { formatDate, formatSize } from '../lib/format'
import type { AdminUser, DiskReport, DriftReport, Inventory, TeamsView } from '../types'

/**
 * The operator page.
 *
 * What it can do is bounded by something outside the interface: roster.yaml is
 * a version-controlled, read-only input, and its git history is the record of
 * which uid was given to whom. So creating or removing an account is an edit to
 * that file and a restart — there is no button here for it, and its absence is
 * stated on the page rather than left to be discovered.
 *
 * What is here is the reversible half: suspending an SMB account and resetting
 * an SMB password, both of which live in tdbsam and leave the ledger untouched.
 */
export function Admin({
  me,
  isAdmin,
  onError,
}: {
  me: string
  /** Whether to fetch the administrator panels at all. A team owner is not an
   *  administrator and would get 404 from every one of them. */
  isAdmin: boolean
  onError: (message: string) => void
}) {
  const [inv, setInv] = useState<Inventory | null>(null)
  const [disk, setDisk] = useState<DiskReport | null>(null)
  const [drift, setDrift] = useState<DriftReport | null>(null)
  const [teams, setTeams] = useState<TeamsView | null>(null)
  const [busy, setBusy] = useState('')
  const [resetting, setResetting] = useState<string | null>(null)

  const load = useCallback(
    (signal?: AbortSignal) => {
      // Independent panels: one backend being down should cost that panel, not
      // the page. An operator opens this when something is wrong.
      //
      // The teams panel is fetched for everyone; the rest only for an
      // administrator, because an owner gets 404 from them by design and an
      // error toast for each would be the page's first impression.
      api.teams(signal).then(setTeams).catch(handle)
      if (isAdmin) {
        api.adminUsers(signal).then(setInv).catch(handle)
        api.adminDisk(signal).then(setDisk).catch(handle)
        api.adminAudit(signal).then(setDrift).catch(handle)
      }

      function handle(e: unknown) {
        if (signal?.aborted) return
        onError(e instanceof Error ? e.message : '읽을 수 없습니다.')
      }
    },
    [onError, isAdmin],
  )

  useEffect(() => {
    const ac = new AbortController()
    load(ac.signal)
    return () => ac.abort()
  }, [load])

  async function setSmb(user: string, enabled: boolean) {
    setBusy(user)
    try {
      await api.adminSetSmb(user, enabled)
      load()
    } catch (e) {
      onError(e instanceof Error ? e.message : '변경하지 못했습니다.')
    } finally {
      setBusy('')
    }
  }

  return (
    <main className="admin">
      <Capacity disk={disk} />
      <Drift drift={drift} />

      <Teams view={teams} onChanged={() => load()} onError={onError} />

      {isAdmin && (
      <section>
        <h2>
          사용자 <span className="muted small">{inv ? `${inv.users.length}명` : ''}</span>
        </h2>
        {inv?.source === 'nss' && (
          <p className="muted small">
            이 배포는 usersync로 계정을 관리하지 않습니다. 목록은 시스템 이름 서비스에서 읽었으며,
            디렉터리 서비스가 제공하는 계정은 빠져 있을 수 있습니다(winbind는 기본적으로 열거에
            응답하지 않습니다).
          </p>
        )}
        {inv?.warnings?.map((w) => (
          <p className="warn" key={w}>
            {w}
          </p>
        ))}
        <table className="admin-table">
          <thead>
            <tr>
              <th>이름</th>
              <th className="num">uid</th>
              <th>팀</th>
              <th className="num">사용량</th>
              <th>SMB</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {inv?.users.map((u) => (
              <UserRow
                key={u.name}
                user={u}
                self={u.name === me}
                busy={busy === u.name}
                onToggle={(enabled) => void setSmb(u.name, enabled)}
                onReset={() => setResetting(u.name)}
              />
            ))}
          </tbody>
        </table>
        <p className="muted small">
          계정 생성·삭제와 uid는 여기서 하지 않습니다. <code>roster.yaml</code>을 고치고 커밋한 뒤
          재시작하면 반영됩니다 — 누구에게 어떤 uid를 줬는지의 기록이 곧 그 파일의 git 이력입니다.
          팀 소속은 위에서 바로 바꿀 수 있고, 그 편집도 같은 파일에 한 줄로 남습니다.
        </p>
      </section>
      )}


      {resetting && (
        <PasswordDialog
          user={resetting}
          onClose={() => setResetting(null)}
          onError={onError}
        />
      )}
    </main>
  )
}

function UserRow({
  user,
  self,
  busy,
  onToggle,
  onReset,
}: {
  user: AdminUser
  self: boolean
  busy: boolean
  onToggle: (enabled: boolean) => void
  onReset: () => void
}) {
  return (
    <tr>
      <td>
        {user.name}
        {user.full_name && <span className="muted small"> {user.full_name}</span>}
      </td>
      <td className="num muted">{user.uid}</td>
      <td>{user.groups.length ? user.groups.join(', ') : <span className="muted">—</span>}</td>
      <td className="num">
        {user.usage_bytes === undefined ? (
          <span className="muted">—</span>
        ) : (
          formatSize(user.usage_bytes) || '0 B'
        )}
      </td>
      <td>
        <SmbState smb={user.smb} />
      </td>
      <td className="actions">
        {user.smb && (
          <button
            type="button"
            className="ghost"
            disabled={busy || (self && user.smb.enabled)}
            title={
              self && user.smb.enabled ? '자기 계정은 잠글 수 없습니다' : undefined
            }
            onClick={() => onToggle(!user.smb!.enabled)}
          >
            {user.smb.enabled ? '잠금' : '해제'}
          </button>
        )}
        <button type="button" className="ghost" disabled={busy} onClick={onReset}>
          비밀번호
        </button>
      </td>
    </tr>
  )
}

/**
 * Three states, not two. "Samba could not be asked" must not render as
 * "disabled" — that would show a whole column of locked-out users and send
 * someone to fix an outage that is really a missing pdbedit.
 */
function SmbState({ smb }: { smb?: { enabled: boolean } }) {
  if (!smb) return <span className="muted" title="Samba에 물어보지 못했습니다">확인 불가</span>
  return smb.enabled ? (
    <span className="ok">사용</span>
  ) : (
    <span className="warn-text">잠김</span>
  )
}

function Capacity({ disk }: { disk: DiskReport | null }) {
  if (!disk) return null
  const { capacity: c, usage } = disk
  const used = c.total_bytes ? c.used_bytes / c.total_bytes : 0
  const inodesUsed = c.total_inodes ? 1 - c.free_inodes / c.total_inodes : 0

  return (
    <section>
      <h2>저장 공간</h2>
      <div className="meter" role="img" aria-label={`${Math.round(used * 100)}% 사용 중`}>
        <div className={barClass(used)} style={{ width: `${Math.min(used, 1) * 100}%` }} />
      </div>
      <p>
        <strong>{formatSize(c.free_bytes)}</strong> 남음
        <span className="muted small">
          {' '}
          / 전체 {formatSize(c.total_bytes)} · {c.path}
        </span>
      </p>
      {/* Running out of inodes fails writes while df still shows free space,
          so it gets its own line rather than being folded into the bar. */}
      {inodesUsed > 0.8 && (
        <p className="warn">
          inode {Math.round(inodesUsed * 100)}% 사용 — 공간이 남아 있어도 쓰기가 실패할 수
          있습니다.
        </p>
      )}
      <p className="muted small">
        사용량 집계: {usage.source === 'zfs' ? 'ZFS(스냅샷 포함)' : usage.source === 'du' ? '디렉터리 순회' : '아직 없음'}
        {usage.measured_at && !usage.measured_at.startsWith('0001') && ` · ${formatDate(usage.measured_at)}`}
        {usage.error && ` · 마지막 측정 실패: ${usage.error}`}
      </p>
    </section>
  )
}

function barClass(ratio: number): string {
  if (ratio >= 0.95) return 'fill danger'
  if (ratio >= 0.85) return 'fill warn-fill'
  return 'fill'
}

/**
 * The roster/system comparison. Once a directory service owns the accounts
 * (`mode: audit`), this is the only thing still checking that a name resolves
 * to the number the roster reserved for it.
 */
function Drift({ drift }: { drift: DriftReport | null }) {
  if (!drift) return null

  // Nothing to compare against is not a problem to report. Showing a failed
  // exec here told the operator something was broken when the deployment
  // simply does not manage accounts this way.
  if (!drift.available) return null

  if (drift.ok) {
    return (
      <section>
        <h2>선언 대조</h2>
        <p className="ok">roster.yaml과 실제 시스템이 일치합니다.</p>
      </section>
    )
  }
  return (
    <section>
      <h2>선언 대조</h2>
      {drift.error && (
        <p className="warn">
          usersync가 대조에 실패했습니다. roster와 시스템이 어긋났는지 알 수 없는 상태입니다.
          <br />
          <code className="small">{drift.error}</code>
        </p>
      )}
      <ul className="findings">
        {drift.findings.map((f, i) => (
          <li key={`${f.kind}-${f.name}-${i}`}>
            <code>{f.name}</code> <span className="muted small">{f.kind}</span> — {explainDrift(f.code)}
            {f.want !== undefined && <span className="muted small"> (선언 {f.want}</span>}
            {f.got !== undefined && <span className="muted small">, 실제 {f.got}</span>}
            {f.want !== undefined && <span className="muted small">)</span>}
          </li>
        ))}
      </ul>
    </section>
  )
}

function explainDrift(code: string): string {
  switch (code) {
    case 'missing':
      return '선언돼 있는데 시스템에 없습니다'
    case 'id_mismatch':
      return '이름이 선언과 다른 번호로 해석됩니다'
    case 'tombstone_live':
      return '예약(reserved)인데 실제 계정이 살아 있습니다'
    case 'undeclared':
      return '관리 대역 안인데 roster에 없습니다'
    case 'collision':
      return '두 이름이 같은 번호를 씁니다'
    default:
      return code
  }
}

/**
 * Team membership, editable by whoever may.
 *
 * Driven by /api/teams, which returns only the teams the caller may change --
 * all of them for an admin, theirs for an owner, none for anyone else. That is
 * also why it does not read the inventory: an owner is not an administrator and
 * cannot fetch it.
 *
 * The controls being drawn is a rendering decision. The server re-checks on
 * every request, so a browser that lies to itself gains nothing.
 */
function Teams({
  view,
  onChanged,
  onError,
}: {
  view: TeamsView | null
  onChanged: () => void
  onError: (message: string) => void
}) {
  const [busy, setBusy] = useState('')

  if (!view?.teams.length) return null

  async function setMember(team: string, user: string, member: boolean) {
    setBusy(`${team}:${user}`)
    try {
      await api.setTeamMember(team, user, member)
      onChanged()
    } catch (e) {
      onError(e instanceof Error ? e.message : '변경하지 못했습니다.')
    } finally {
      setBusy('')
    }
  }

  return (
    <section>
      <h2>팀</h2>
      {view.teams.map((g) => {
        const outside = view.users.filter((u) => !g.members.includes(u))
        return (
          <div className="team" key={g.name}>
            <h3>
              {g.name}
              <span className="muted small"> gid {g.gid}</span>
              {g.description && <span className="muted small"> · {g.description}</span>}
            </h3>
            {g.owners.length > 0 && (
              <p className="muted small">관리자: {g.owners.join(', ')}</p>
            )}
            <ul className="members">
              {g.members.length === 0 && <li className="muted">비어 있음</li>}
              {g.members.map((m) => (
                <li key={m}>
                  {m}
                  <button
                    type="button"
                    className="ghost tiny"
                    disabled={busy === `${g.name}:${m}`}
                    title={`${m}을(를) ${g.name}에서 제외`}
                    onClick={() => void setMember(g.name, m, false)}
                  >
                    제외
                  </button>
                </li>
              ))}
            </ul>
            {outside.length > 0 && (
              <AddMember
                team={g.name}
                candidates={outside}
                busy={busy.startsWith(`${g.name}:`)}
                onAdd={(user) => void setMember(g.name, user, true)}
              />
            )}
          </div>
        )
      })}
      <p className="muted small">
        팀 소속은 <code>roster.yaml</code>에 기록되고 바로 반영됩니다. 계정 생성·삭제와 uid는
        여전히 손으로 고칩니다.
      </p>
    </section>
  )
}

function AddMember({
  team,
  candidates,
  busy,
  onAdd,
}: {
  team: string
  candidates: string[]
  busy: boolean
  onAdd: (user: string) => void
}) {
  const [pick, setPick] = useState('')
  return (
    <form
      className="add-member"
      onSubmit={(e) => {
        e.preventDefault()
        if (pick) onAdd(pick)
        setPick('')
      }}
    >
      <select value={pick} onChange={(e) => setPick(e.target.value)} aria-label={`${team}에 추가`}>
        <option value="">추가할 사람…</option>
        {candidates.map((c) => (
          <option key={c} value={c}>
            {c}
          </option>
        ))}
      </select>
      <button type="submit" className="ghost tiny" disabled={busy || !pick}>
        추가
      </button>
    </form>
  )
}

/**
 * Setting a password, not showing one.
 *
 * The value is typed here and sent once; nothing stores it and nothing reads it
 * back. On the server it travels on stdin so it never reaches an argv, which
 * anyone who can list processes could read.
 */
function PasswordDialog({
  user,
  onClose,
  onError,
}: {
  user: string
  onClose: () => void
  onError: (message: string) => void
}) {
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      await api.adminSetPassword(user, value)
      setDone(true)
    } catch (err) {
      onError(err instanceof Error ? err.message : '변경하지 못했습니다.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="backdrop" onClick={onClose}>
      <form className="dialog" onClick={(e) => e.stopPropagation()} onSubmit={(e) => void submit(e)}>
        <h3>{user} SMB 비밀번호 재설정</h3>
        {done ? (
          <>
            <p>변경했습니다. 이 값을 본인에게 직접 전달하세요 — 다시 볼 수 없습니다.</p>
            <div className="dialog-actions">
              <button type="button" onClick={onClose}>
                닫기
              </button>
            </div>
          </>
        ) : (
          <>
            <p className="muted small">
              유닉스 로그인 비밀번호가 아니라 SMB 비밀번호입니다. 사용자는 이후 직접 바꿀 수
              있습니다.
            </p>
            <input
              type="password"
              autoFocus
              autoComplete="new-password"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              placeholder="새 비밀번호"
            />
            <div className="dialog-actions">
              <button type="button" className="ghost" onClick={onClose}>
                취소
              </button>
              <button type="submit" disabled={busy || value.length === 0}>
                변경
              </button>
            </div>
          </>
        )}
      </form>
    </div>
  )
}
