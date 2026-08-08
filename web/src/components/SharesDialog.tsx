import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api'
import type { ShareLink } from '../types'
import { copyToClipboard } from './ShareDialog'

export function SharesDialog({ onClose }: { onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  const [links, setLinks] = useState<ShareLink[] | null>(null)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    try {
      setLinks((await api.shares()).links)
    } catch (e) {
      setError(e instanceof Error ? e.message : '불러오지 못했습니다.')
    }
  }, [])

  useEffect(() => {
    ref.current?.showModal()
    void load()
  }, [load])

  async function revoke(link: ShareLink) {
    if (!confirm(`이 링크를 폐기합니다. 받은 사람은 더 이상 열 수 없습니다.\n\n${link.name}`)) return
    try {
      await api.revokeShare(link.token)
      await load()
    } catch (e) {
      setError(e instanceof Error ? e.message : '폐기하지 못했습니다.')
    }
  }

  return (
    <dialog ref={ref} onClose={onClose} onCancel={onClose}>
      <h2>내가 만든 공유 링크</h2>
      {error && <p className="error">{error}</p>}

      {links === null && <p className="muted">불러오는 중…</p>}
      {links !== null && links.length === 0 && <p className="muted">만든 링크가 없습니다.</p>}

      {links?.map((link) => (
        <div key={link.token} className="share-item">
          <div>
            {link.name}
            {link.protected && <span title="비밀번호가 필요합니다"> 🔒</span>}
          </div>
          <div className="muted small">
            {link.path} · {new Date(link.expires).toLocaleDateString('ko-KR')}까지
          </div>
          <div className="share-url">
            <input readOnly value={link.url} onFocus={(e) => e.currentTarget.select()} />
            <button type="button" onClick={() => void copyToClipboard(link.url)}>
              복사
            </button>
            <button type="button" className="ghost" onClick={() => void revoke(link)}>
              폐기
            </button>
          </div>
        </div>
      ))}

      <menu>
        <button type="button" onClick={onClose}>
          닫기
        </button>
      </menu>
    </dialog>
  )
}
