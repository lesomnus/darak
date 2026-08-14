import { useEffect, useRef, useState, type FormEvent } from 'react'
import { api } from '../api'
import type { Entry } from '../types'

/**
 * Changing a file's permission bits.
 *
 * There is no application-level sharing model here: what somebody may do with a
 * file is the mode, the group and the ACL, and the kernel is what enforces it.
 * So this dialog edits the real thing rather than a metaphor for it — and it
 * shows the octal, because that is the value a person will be told to check by
 * anyone helping them.
 *
 * Presets first, raw octal underneath. The presets are the three answers people
 * actually want in a team folder; the field is there because a mode is a real
 * value and hiding it would make this dialog a worse version of `chmod`.
 */
export function ModeDialog({
  path,
  entry,
  onClose,
  onDone,
  onError,
}: {
  path: string
  entry: Entry
  onClose: () => void
  onDone: () => void
  onError: (message: string) => void
}) {
  const inTeam = path.startsWith('teams/')
  const [mode, setMode] = useState(entry.mode ?? (entry.dir ? '0700' : '0600'))
  const [busy, setBusy] = useState(false)
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => ref.current?.showModal(), [])
  // Whether this file carries a POSIX ACL. Fetched, because it is not in the
  // mode bits the listing already has — and it is the one thing about changing a
  // mode that can go wrong invisibly.
  const [hasACL, setHasACL] = useState(false)

  useEffect(() => {
    let live = true
    api
      .modeInfo(path)
      .then((info) => {
        if (!live) return
        setHasACL(info.acl)
        // Trust the server's mode over the listing's: the two agree, but the
        // dialog is about to write this value back, so it should show what is
        // actually there right now.
        setMode(info.mode)
      })
      .catch(() => {
        // The dialog still works from the listing's mode; only the precise ACL
        // warning is lost.
      })
    return () => {
      live = false
    }
  }, [path])

  const presets: [string, string][] = entry.dir
    ? inTeam
      ? [
          ['2770', '팀원이 읽고 쓰기'],
          ['2750', '팀원은 읽기만'],
        ]
      : [['0700', '나만']]
    : inTeam
      ? [
          ['0660', '팀원이 읽고 쓰기'],
          ['0640', '팀원은 읽기만'],
          ['0600', '나만'],
        ]
      : [['0600', '나만']]

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      await api.chmod(path, mode)
      onDone()
      onClose()
    } catch (err) {
      onError(err instanceof Error ? err.message : '바꾸지 못했습니다.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <dialog ref={ref} onClose={onClose} onCancel={onClose}>
      <form onSubmit={(e) => void submit(e)}>
        <h3>권한 변경</h3>
        <p className="muted small">
          {entry.name} · 현재 <code>{entry.mode ?? '—'}</code>
        </p>

        <div className="mode-presets">
          {presets.map(([value, label]) => (
            <button
              key={value}
              type="button"
              className={mode === value ? '' : 'ghost'}
              onClick={() => setMode(value)}
            >
              {label} <code>{value}</code>
            </button>
          ))}
        </div>

        <label>
          8진수로 직접
          <input
            value={mode}
            onChange={(e) => setMode(e.target.value)}
            autoCapitalize="none"
            autoCorrect="off"
            spellCheck={false}
            inputMode="numeric"
          />
        </label>

        {/* Two things that are not obvious and that people hit. */}
        {entry.dir && inTeam && (
          <p className="muted small">
            팀 폴더는 앞자리 <code>2</code>(setgid)를 유지해야 합니다. 빼면 이후 만들어지는 파일이
            팀 그룹을 갖지 못해 팀원이 열 수 없게 됩니다 — 서버가 거부합니다.
          </p>
        )}
        {/* Only when there really is an ACL. Measured on the deployment's
            filesystem: narrowing the group bits recomputes the ACL mask and can
            make a reader's grant #effective:--- — the entry stays but stops
            working. A generic "might have an ACL" note on every file would train
            people to ignore it; this one appears only when it is true. */}
        {hasACL && (
          <p className="warn small" role="alert">
            이 {entry.dir ? '폴더' : '파일'}에는 <strong>추가 권한(ACL)</strong>이 걸려 있습니다 —
            읽기 전용으로 열어준 팀 등이 있을 수 있습니다. 여기서 그룹 권한을 좁히면 그 권한이 함께
            무력화될 수 있습니다. 읽기 전용 공유를 바꾸려면 권한 대신 roster의 <code>readers</code>를
            수정하세요.
          </p>
        )}

        <div className="dialog-actions">
          <button type="button" className="ghost" onClick={onClose}>
            취소
          </button>
          <button type="submit" disabled={busy || !mode}>
            {busy ? '바꾸는 중…' : '바꾸기'}
          </button>
        </div>
      </form>
    </dialog>
  )
}
