import { useEffect, useRef, useState } from 'react'
import { api } from '../api'
import type { ShareLink } from '../types'

const TTL_CHOICES = [
  { hours: 24, label: '1일' },
  { hours: 24 * 7, label: '7일' },
  { hours: 24 * 30, label: '30일' },
]

export function ShareDialog({
  path,
  onClose,
  onError,
}: {
  path: string
  onClose: () => void
  onError: (message: string) => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const [password, setPassword] = useState('')
  const [ttl, setTtl] = useState(24 * 7)
  const [created, setCreated] = useState<ShareLink | null>(null)
  const [copied, setCopied] = useState(false)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    ref.current?.showModal()
  }, [])

  async function create() {
    setBusy(true)
    try {
      const link = await api.createShare(path, password, ttl)
      setCreated(link)
      setCopied(await copyToClipboard(link.url))
    } catch (e) {
      onError(e instanceof Error ? e.message : '링크를 만들지 못했습니다.')
      onClose()
    } finally {
      setBusy(false)
    }
  }

  const name = path.split('/').pop() ?? path

  return (
    <dialog ref={ref} onClose={onClose} onCancel={onClose}>
      {created === null ? (
        <>
          <h2>공유 링크 만들기</h2>
          <p className="muted small">{name}</p>

          <label>
            비밀번호 <span className="muted small">(비워두면 링크만으로 열립니다)</span>
            <input
              type="text"
              autoComplete="off"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>
          <label>
            유효 기간
            <select value={ttl} onChange={(e) => setTtl(Number(e.target.value))}>
              {TTL_CHOICES.map((c) => (
                <option key={c.hours} value={c.hours}>
                  {c.label}
                </option>
              ))}
            </select>
          </label>

          <menu>
            <button type="button" className="ghost" onClick={onClose}>
              취소
            </button>
            <button type="button" onClick={create} disabled={busy}>
              {busy ? '만드는 중…' : '만들기'}
            </button>
          </menu>
        </>
      ) : (
        <>
          <h2>링크를 만들었습니다</h2>
          <p className="muted small">
            {new Date(created.expires).toLocaleDateString('ko-KR')}까지 열립니다.
            {created.protected && ' 비밀번호가 필요합니다.'}
          </p>
          <div className="share-url">
            <input readOnly value={created.url} onFocus={(e) => e.currentTarget.select()} />
            <button
              type="button"
              onClick={async () => setCopied(await copyToClipboard(created.url))}
            >
              {copied ? '복사됨' : '복사'}
            </button>
          </div>
          {/* The clipboard needs a secure context; over plain HTTP the field
              above is the fallback, so say so rather than looking broken. */}
          {!copied && <p className="muted small">복사되지 않으면 위 주소를 직접 선택해 복사하세요.</p>}
          <menu>
            <button type="button" onClick={onClose}>
              닫기
            </button>
          </menu>
        </>
      )}
    </dialog>
  )
}

export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}
