import { useState, type FormEvent } from 'react'
import { api } from '../api'
import type { Branding, Me } from '../types'

export function Login({
  brand,
  onSignedIn,
}: {
  brand: Branding
  onSignedIn: (me: Me) => void
}) {
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [broken, setBroken] = useState(false)

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
        {/* The one credential, shared with SMB — worth saying, because it is the
            question people ask first. */}
        <p className="muted small">탐색기에서 쓰는 비밀번호와 같습니다.</p>
      </form>
    </section>
  )
}
