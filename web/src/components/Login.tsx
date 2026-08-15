import { useEffect, useState, type FormEvent } from 'react'
import { api } from '../api'
import type { Branding, Me, SSONotice } from '../types'

// The stages an onboarding passes through, in order, for the stepper. DENIED and
// FAILED are not steps — they replace the card's tone rather than advance it.
const ENROLL_STAGES: { key: string; label: string }[] = [
  { key: 'STAGE_REQUESTED', label: '요청됨' },
  { key: 'STAGE_CREATING', label: '생성 중' },
  { key: 'STAGE_AWAITING_APPROVAL', label: '승인 대기' },
  { key: 'STAGE_READY', label: '준비됨' },
]

function isTerminalStage(s: string): boolean {
  return s === 'STAGE_READY' || s === 'STAGE_DENIED'
}

/**
 * A first-time SSO sign-in that has no account yet, followed live.
 *
 * The notice handed the page an id and the stage it started at, so the first
 * paint is the real state. From there it polls until the onboarding reaches a
 * terminal stage — READY, at which point the person just signs in again (the
 * form is right above), or DENIED. A static "waiting for approval" line was the
 * thing that confused people; this says which of "being created", "waiting for
 * an admin" and "ready" is actually true.
 */
function EnrollmentCard({
  id,
  initialStage,
  initialMessage,
  address,
}: {
  id: string
  initialStage: string
  initialMessage: string
  address?: string
}) {
  const [stage, setStage] = useState(initialStage)
  const [message, setMessage] = useState(initialMessage)

  useEffect(() => {
    if (isTerminalStage(initialStage)) return
    // Server-Sent Events: the server emits a stage each time it changes and
    // closes the stream at a terminal one. EventSource reconnects on its own if
    // the connection drops, so there is no polling loop to keep here.
    const es = new EventSource(`/api/sso/enrollment?id=${encodeURIComponent(id)}`)
    es.onmessage = (ev) => {
      try {
        const p = JSON.parse(ev.data) as { stage: string; message: string; account: string }
        setStage(p.stage)
        if (p.message) setMessage(p.message)
        if (isTerminalStage(p.stage)) es.close()
      } catch {
        // A malformed frame is skipped; the next one replaces the state anyway.
      }
    }
    return () => es.close()
  }, [id, initialStage])

  const failed = stage === 'STAGE_DENIED' || stage === 'STAGE_FAILED'
  const active = ENROLL_STAGES.findIndex((s) => s.key === stage)

  return (
    <div className={failed ? 'error' : 'notice'} role="status" aria-live="polite">
      <p>{message}</p>
      {!failed && (
        <ol className="enroll-steps">
          {ENROLL_STAGES.map((s, i) => (
            <li key={s.key} data-state={i < active ? 'done' : i === active ? 'active' : 'todo'}>
              {s.label}
            </li>
          ))}
        </ol>
      )}
      {stage === 'STAGE_READY' && (
        <p className="small">준비됐습니다. 위에서 다시 로그인하세요.</p>
      )}
      {address && (
        <p className="small">
          인식된 주소: <code>{address}</code>
        </p>
      )}
    </div>
  )
}

export function Login({
  brand,
  onSignedIn,
  onCancel,
}: {
  brand: Branding
  onSignedIn: (me: Me) => void
  /** When set, the page offers a way back — for an anonymous visitor who opened
   *  it from the public folders and decided not to sign in after all. */
  onCancel?: () => void
}) {
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [broken, setBroken] = useState(false)
  const [notice, setNotice] = useState<SSONotice | null>(null)

  // A sign-on attempt that did not end in a session comes back here with an id
  // to collect its message by. The id is single-use and the text comes from the
  // server, so a shared or edited link says nothing.
  useEffect(() => {
    const id = new URLSearchParams(window.location.search).get('sso')
    if (!id) return
    // Take it out of the address bar immediately: it is spent, and leaving it
    // there means a refresh looks like a failure that just happened again.
    window.history.replaceState(null, '', window.location.pathname)
    api
      .ssoNotice(id)
      .then((n) => n.kind && setNotice(n))
      .catch(() => {
        // Nothing to say beyond what the page already says.
      })
  }, [])

  async function submit(ev: FormEvent<HTMLFormElement>) {
    ev.preventDefault()
    const form = new FormData(ev.currentTarget)
    setBusy(true)
    setError('')
    try {
      onSignedIn(
        await api.login(String(form.get('user') ?? ''), String(form.get('password') ?? '')),
      )
    } catch (e) {
      setError(e instanceof Error ? e.message : '로그인하지 못했습니다.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="login">
      <form onSubmit={submit}>
        {/* The mark belongs here more than anywhere else: this is the page that
            has to answer "what am I signing in to" before anyone has agreed to
            type a password into it. Which is why /api/branding takes no
            session. */}
        {brand.logo && !broken && (
          <img
            className="login-logo"
            src="/api/branding/logo"
            alt=""
            onError={() => setBroken(true)}
          />
        )}
        <h1>{brand.name}</h1>
        <p className="muted">회사 계정으로 로그인하세요.</p>

        {/* What the sign-on attempt ended in. A pending request is not an
            error — the person did nothing wrong and there is something they can
            do right now, which is the last line of it. */}
        {notice && notice.kind === 'enrollment' && notice.enrollment_id ? (
          <EnrollmentCard
            id={notice.enrollment_id}
            initialStage={notice.stage ?? ''}
            initialMessage={notice.message ?? ''}
            address={notice.address}
          />
        ) : notice ? (
          <div className={notice.kind === 'pending' ? 'notice' : 'error'} role="alert">
            <p>{notice.message}</p>
            {notice.address && (
              <p className="small">
                인식된 주소: <code>{notice.address}</code>
              </p>
            )}
            {notice.kind === 'pending' && (
              <p className="small">계정이 있다면 지금도 아이디와 비밀번호로 로그인할 수 있습니다.</p>
            )}
          </div>
        ) : null}

        {/* A link, not a fetch: the browser has to follow the redirect to the
            provider and come back with its own cookies. */}
        {brand.sso && (
          <>
            <a className="button sso" href="/api/sso/login">
              회사 계정(SSO)으로 로그인
            </a>
            <p className="divider">
              <span>또는</span>
            </p>
          </>
        )}

        <label>
          아이디
          <input
            name="user"
            autoComplete="username"
            autoCapitalize="none"
            autoCorrect="off"
            spellCheck={false}
            required
            autoFocus
          />
        </label>
        <label>
          비밀번호
          <input name="password" type="password" autoComplete="current-password" required />
        </label>

        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        <button type="submit" disabled={busy}>
          {busy ? '확인 중…' : '로그인'}
        </button>
        {onCancel && (
          <button type="button" className="ghost" onClick={onCancel}>
            공개 폴더 계속 둘러보기
          </button>
        )}
        {/* The one credential, shared with SMB — worth saying, because it is the
            question people ask first. */}
        <p className="muted small">탐색기에서 쓰는 비밀번호와 같습니다.</p>
      </form>
    </section>
  )
}
