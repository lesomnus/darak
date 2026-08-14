import { useState, type FormEvent } from 'react'
import { api } from '../api'

/** Kept in step with auth.MinPasswordLength, so the form refuses what the
 *  server would refuse rather than asking and being told no. */
const MIN_LENGTH = 8

/**
 * Changing your own password.
 *
 * There is one password per person and it is Samba's, so this changes what the
 * explorer asks for too — the page says so, because "I changed it on the web
 * and now the drive won't mount" is otherwise the next thing that happens.
 *
 * The current password is asked for even though the person is signed in. A
 * session lasts twelve hours and is a bearer token; requiring the old password
 * is what makes this the account holder's act rather than the act of whoever
 * has the cookie.
 */
export function ChangePassword({
  onClose,
  onError,
}: {
  onClose: () => void
  onError: (message: string) => void
}) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [again, setAgain] = useState('')
  const [busy, setBusy] = useState(false)
  const [closed, setClosed] = useState<number | null>(null)

  const mismatch = again.length > 0 && next !== again
  const tooShort = next.length > 0 && next.length < MIN_LENGTH
  const ready = current.length > 0 && next.length >= MIN_LENGTH && next === again && !busy

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      const res = await api.changePassword(current, next)
      setClosed(res.sessions_closed)
    } catch (err) {
      onError(err instanceof Error ? err.message : '바꾸지 못했습니다.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="backdrop" onClick={onClose}>
      <form className="dialog" onClick={(e) => e.stopPropagation()} onSubmit={(e) => void submit(e)}>
        <h3>비밀번호 변경</h3>

        {closed !== null ? (
          <>
            <p>바꿨습니다. 다음 로그인부터 새 비밀번호를 쓰세요.</p>
            <p className="muted small">
              탐색기(SMB)에 저장해 둔 비밀번호도 새 것으로 고쳐야 합니다. 저장된 자격 증명이 옛
              비밀번호로 계속 시도되면 계정이 잠길 수 있는 환경도 있습니다.
              {closed > 0 && ` 다른 기기에서 열려 있던 로그인 ${closed}개는 닫았습니다.`}
            </p>
            <div className="dialog-actions">
              <button type="button" onClick={onClose}>
                닫기
              </button>
            </div>
          </>
        ) : (
          <>
            <p className="muted small">
              웹과 탐색기(SMB)가 <strong>같은 비밀번호</strong>를 씁니다. 여기서 바꾸면 양쪽 다
              바뀝니다.
            </p>

            <label>
              지금 비밀번호
              <input
                type="password"
                autoFocus
                autoComplete="current-password"
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
              />
            </label>
            <label>
              새 비밀번호
              <input
                type="password"
                autoComplete="new-password"
                value={next}
                onChange={(e) => setNext(e.target.value)}
              />
            </label>
            <label>
              새 비밀번호 확인
              <input
                type="password"
                autoComplete="new-password"
                value={again}
                onChange={(e) => setAgain(e.target.value)}
              />
            </label>

            {/* Said up front rather than as a rejection: the only rule is a
                length floor, so there is nothing to guess at. */}
            <p className={tooShort ? 'error small' : 'muted small'}>
              {MIN_LENGTH}자 이상. 길이 말고 다른 제약은 없습니다.
            </p>
            {mismatch && (
              <p className="error small" role="alert">
                새 비밀번호가 서로 다릅니다.
              </p>
            )}

            <div className="dialog-actions">
              <button type="button" className="ghost" onClick={onClose}>
                취소
              </button>
              <button type="submit" disabled={!ready}>
                {busy ? '바꾸는 중…' : '바꾸기'}
              </button>
            </div>
          </>
        )}
      </form>
    </div>
  )
}
