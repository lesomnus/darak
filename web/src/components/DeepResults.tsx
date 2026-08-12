import { filesUrl } from '../api'
import type { SearchHit } from '../types'
import type { DeepSearch } from '../lib/useDeepSearch'
import { iconFor } from '../lib/format'
import { Icon } from './Icon'
import { Marked } from './FileRow'

/**
 * What the walk below this directory found.
 *
 * A section of its own, under the folder's own rows, because these are a
 * different kind of answer: they are somewhere else, and each one has to say
 * where. Merging them into the list above would produce a listing of a
 * directory containing files that are not in it.
 */
export function DeepResults({
  search,
  path,
  query,
  onNavigate,
}: {
  search: DeepSearch
  /** The directory that was searched; hit paths are relative to it. */
  path: string
  query: string
  onNavigate: (path: string) => void
}) {
  const { hits, running, truncated, error } = search
  if (!running && hits.length === 0 && !error) {
    return (
      <section className="deep">
        <h2>하위 폴더</h2>
        <p className="muted small deep-note">“{query}”에 맞는 이름이 아래에도 없습니다.</p>
      </section>
    )
  }

  return (
    <section className="deep">
      <h2>
        하위 폴더
        {running ? (
          <span className="muted small">찾는 중…</span>
        ) : (
          <span className="muted small">{hits.length.toLocaleString('ko-KR')}개</span>
        )}
      </h2>

      {error && <p className="error small deep-note">{error}</p>}

      {hits.map((hit) => (
        <DeepRow key={hit.path} hit={hit} path={path} onNavigate={onNavigate} />
      ))}

      {/* Not a footnote. A list cut short by a budget looks exactly like a
          complete list of everything there is, and those are opposite answers
          to the question that was asked. */}
      {truncated && (
        <p className="warn deep-note">
          너무 넓어서 도중에 멈췄습니다. 여기 보이는 것이 전부가 아닙니다 — 더 안쪽 폴더에서
          다시 찾거나, 검색어를 길게 쓰세요.
        </p>
      )}
      {running && <p className="muted small deep-note">아직 찾는 중입니다.</p>}
    </section>
  )
}

function DeepRow({
  hit,
  path,
  onNavigate,
}: {
  hit: SearchHit
  path: string
  onNavigate: (path: string) => void
}) {
  const kind = iconFor(hit)
  const full = path + '/' + hit.path
  // Where it is, which is the whole reason this row is not in the list above.
  //
  // Cut from the PATH, not by trimming the name off the end of it. `path` is
  // the bytes on disk and `name` is the same entry in NFC -- for a file written
  // from a Mac those are different lengths, and subtracting one from the other
  // would slice the last characters off the folder name.
  const cut = hit.path.lastIndexOf('/')
  const at = cut < 0 ? '' : hit.path.slice(0, cut)

  return (
    <div
      className="row"
      role="button"
      tabIndex={0}
      onClick={() => (hit.dir ? onNavigate(full) : (window.location.href = filesUrl(full)))}
      onKeyDown={(ev) => {
        if (ev.target !== ev.currentTarget) return
        if (ev.key !== 'Enter' && ev.key !== ' ') return
        ev.preventDefault()
        if (hit.dir) onNavigate(full)
        else window.location.href = filesUrl(full)
      }}
    >
      <span className="icon" data-kind={kind}>
        <Icon name={kind} />
      </span>
      <span className="name">
        {/* Wrapped, because Marked renders a fragment: without a box of its own
            the filename could not be ellipsized separately from the trail. */}
        <span className="deep-name">
          <Marked text={hit.name} positions={hit.pos} />
        </span>
        {at && <span className="trail">{at}</span>}
      </span>
    </div>
  )
}
