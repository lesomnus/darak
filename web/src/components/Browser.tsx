import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, filesUrl } from '../api'
import type { Entry } from '../types'
import { compareNames, domainRoot, sortEntriesBy, TRASH_DIR } from '../lib/format'
import { foldOf, needleOf, scoreFolded, type Match } from '../lib/fuzzy'
import { useWindowVirtual } from '../lib/useVirtual'
import { useViewPrefs } from '../lib/useViewPrefs'
import { useDeepSearch } from '../lib/useDeepSearch'
import { DeepResults } from './DeepResults'
import { FileRow, type Row } from './FileRow'
import { ViewControls } from './ViewControls'
import { ModeDialog } from './ModeDialog'
import { PreviewModal } from './PreviewModal'
import { previewable, toPreviewFile } from '../preview/registry'
import { useDialogs } from '../lib/dialogs'
import { Icon } from './Icon'

// The narrowest a grid card may get before another column would crowd it. Cols
// are computed in JS (not CSS auto-fill) because the virtualiser has to know how
// many items sit on a row to map an index to a position.
const GRID_CARD_MIN = 150

interface UploadState {
  current: string
  done: number
  total: number
}

export function Browser({
  path,
  query,
  isFavourite,
  onToggleFavourite,
  onNavigate,
  onError,
  onShare,
  onOpenWorkspace,
}: {
  path: string
  /** What was typed in the header's search box. Filters this listing only. */
  query: string
  isFavourite: (path: string) => boolean
  onToggleFavourite: (path: string) => void
  onNavigate: (path: string) => void
  onError: (message: string) => void
  /** Absent for an anonymous visitor: the share routes need a signed-in user. */
  onShare?: (path: string) => void
  /** Opens this directory in the workspace editor (tree + tabs). */
  onOpenWorkspace?: (path: string) => void
}) {
  const dialogs = useDialogs()
  const prefs = useViewPrefs()
  const [entries, setEntries] = useState<Entry[] | null>(null)
  const [loadError, setLoadError] = useState('')
  const [upload, setUpload] = useState<UploadState | null>(null)
  const [dragging, setDragging] = useState(false)
  const [chmodding, setChmodding] = useState<{ path: string; entry: Entry } | null>(null)
  const [previewing, setPreviewing] = useState<{ path: string; entry: Entry } | null>(null)

  // Sorted once per listing (or when the sort choice changes) rather than once
  // per render: at 50,000 entries a Korean-collation sort is not something to
  // redo because a drag started.
  const sorted = useMemo(
    () => (entries === null ? null : sortEntriesBy(entries, prefs.sortKey, prefs.sortDir)),
    [entries, prefs.sortKey, prefs.sortDir],
  )

  // Everything the matcher needs, computed ONCE per listing rather than once
  // per keystroke: normalising and folding 50,000 names is the expensive half,
  // and it does not depend on what has been typed.
  const index = useMemo<Row[] | null>(() => {
    if (sorted === null) return null
    return sorted.map((entry) => ({ entry, ...foldOf(entry.name), match: null }))
  }, [sorted])

  // Prepared once per keystroke rather than once per entry.
  const needle = useMemo(() => needleOf(query), [query])
  const filtering = needle.text !== ''
  const visible = useMemo<Row[] | null>(() => {
    if (index === null || !filtering) return index
    const out: Row[] = []
    for (const row of index) {
      const match = scoreFolded(row, needle)
      if (match) out.push({ ...row, match })
    }
    // Best first. Sorting a filtered list by name would bury the thing that
    // matched best under everything that merely matched, which is the whole
    // difference between a filter and a search.
    out.sort(
      (a, b) =>
        (b.match as Match).score - (a.match as Match).score ||
        (a.entry.dir === b.entry.dir
          ? compareNames(a.entry.name, b.entry.name)
          : a.entry.dir
            ? -1
            : 1),
    )
    return out
  }, [index, needle])

  // Grid columns are measured, not chosen by CSS auto-fill: the virtualiser has
  // to know how many cards sit on a row to map an item's index to a vertical
  // position. Measured off a wrapper that is always present.
  const gridWrapRef = useRef<HTMLDivElement>(null)
  const [gridCols, setGridCols] = useState(1)
  useEffect(() => {
    if (prefs.view !== 'grid') return
    const el = gridWrapRef.current
    if (!el) return
    const measure = () => setGridCols(Math.max(1, Math.floor(el.clientWidth / GRID_CARD_MIN)))
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [prefs.view])

  const cols = prefs.view === 'grid' ? gridCols : 1
  const itemCount = visible?.length ?? 0
  // In grid mode the virtualiser counts ROWS of `cols` cards, and each rendered
  // grid-row carries the [data-row] it measures; in list mode a row IS an item.
  const rowCount = prefs.view === 'grid' ? Math.ceil(itemCount / cols) : itemCount

  // A filtered list is a DIFFERENT list, so the window goes back to its top --
  // otherwise typing while scrolled deep leaves you past the end of the result
  // and looking at nothing. View, column count and density change the row height
  // the virtualiser measured, so they reset it too.
  const virtual = useWindowVirtual({
    count: rowCount,
    // Separated by an escape that cannot occur in a path, so no pair of
    // (directory, query) can collide with another and skip the reset.
    resetKey: `${path}\u0000${needle.text}\u0000${prefs.view}\u0000${cols}\u0000${prefs.density}`,
  })
  useEffect(() => {
    window.scrollTo(0, 0)
  }, [needle.text])

  // And the other half of searching: a walk of the tree below here, on the
  // server. Two characters is the floor -- below that the server only accepts a
  // contiguous hit anyway, and walking twenty thousand entries for one letter
  // is work nobody asked for.
  const deep = useDeepSearch(path, needle.text, needle.text.length >= 2)

  // Windowing unmounts rows as they leave the viewport, and one of them may be
  // the row the keyboard is on. When that happens the browser drops focus to
  // <body> and the next Tab starts over from the top of the document -- so
  // scrolling with the keyboard loses your place entirely. Catching it here
  // puts focus on the list itself, which keeps Tab where the user was.
  const focusWasInList = useRef(false)
  useEffect(() => {
    const el = virtual.ref.current
    if (!el || !focusWasInList.current) return
    if (document.activeElement === document.body) {
      el.focus({ preventScroll: true })
      focusWasInList.current = false
    }
  })

  const inTrash = path.endsWith('/' + TRASH_DIR)
  const starred = isFavourite(path)
  // A file can only be written inside a permission domain; the two top levels
  // are navigation, not places to put things.
  const canWrite = domainRoot(path) !== null

  const reload = useCallback(async () => {
    setLoadError('')
    try {
      setEntries((await api.list(path)).entries)
    } catch (e) {
      setEntries(null)
      setLoadError(e instanceof Error ? e.message : '불러오지 못했습니다.')
    }
  }, [path])

  useEffect(() => {
    setEntries(null)
    // Opening a directory puts you at ITS top. Without this the window keeps
    // whatever offset the previous listing was scrolled to, which with
    // windowing means arriving in the middle of a list you have not seen the
    // start of -- and the virtualiser reads the window's scroll, so it would
    // faithfully render row 4,000 of a directory holding nine.
    window.scrollTo(0, 0)
    void reload()
  }, [reload])

  const uploadFiles = useCallback(
    async (files: File[]) => {
      if (files.length === 0) return
      if (!canWrite) {
        onError('파일은 내 폴더나 팀 폴더 안에만 올릴 수 있습니다.')
        return
      }
      for (const [i, file] of files.entries()) {
        setUpload({ current: file.name, done: i, total: files.length })
        try {
          await api.upload(path + '/' + file.name, file)
        } catch (e) {
          onError(`${file.name}: ${e instanceof Error ? e.message : '올리지 못했습니다.'}`)
        }
      }
      setUpload(null)
      await reload()
    },
    [canWrite, onError, path, reload],
  )

  async function remove(entry: Entry) {
    const ok = await dialogs.confirm(
      inTrash
        ? { title: '완전히 지울까요?', message: `"${entry.name}"을(를) 되돌릴 수 없이 지웁니다.`, danger: true, confirmLabel: '완전히 지우기' }
        : { title: '휴지통으로 보낼까요?', message: `"${entry.name}"을(를) 휴지통으로 보냅니다.`, confirmLabel: '휴지통으로' },
    )
    if (!ok) return
    try {
      await api.remove(path + '/' + entry.name)
      await reload()
    } catch (e) {
      onError(e instanceof Error ? e.message : '지우지 못했습니다.')
    }
  }

  // Move a dragged entry into a folder in this listing. The destination is a
  // directory PATH; the server keeps the entry's own name. A drop onto the
  // folder the entry already sits in is a no-op, not a round trip.
  async function moveInto(from: string, toDir: string) {
    const base = from.slice(from.lastIndexOf('/') + 1)
    if (`${toDir}/${base}` === from) return
    try {
      await api.move(from, toDir)
      await reload()
    } catch (e) {
      onError(e instanceof Error ? e.message : '옮기지 못했습니다.')
    }
  }

  async function mkdir() {
    const name = await dialogs.prompt({ title: '새 폴더', placeholder: '폴더 이름', confirmLabel: '만들기' })
    if (!name) return
    try {
      await api.mkdir(path + '/' + name.trim())
      await reload()
    } catch (e) {
      onError(e instanceof Error ? e.message : '만들지 못했습니다.')
    }
  }

  async function rename(entry: Entry) {
    // Prefilled with the current name so a small edit is a small gesture; the
    // server keeps it in this folder, so this only ever changes the name.
    const answer = await dialogs.prompt({ title: '이름 변경', initial: entry.name, confirmLabel: '변경' })
    if (answer === null) return
    const next = answer.trim()
    if (!next || next === entry.name) return
    try {
      await api.rename(path + '/' + entry.name, next)
      await reload()
    } catch (e) {
      onError(e instanceof Error ? e.message : '이름을 바꾸지 못했습니다.')
    }
  }

  function openTrash() {
    const domain = domainRoot(path)
    if (!domain) {
      onError('휴지통은 내 폴더나 팀 폴더 안에서 볼 수 있습니다.')
      return
    }
    onNavigate(`${domain}/${TRASH_DIR}`)
  }

  // One row, rendered the same way whether it sits in the list or the grid —
  // only the variant (and, for the list, which columns show) differs, so the
  // action wiring is not written twice.
  const renderCell = (row: Row, variant: 'row' | 'card') => {
    const entry = row.entry
    const child = path + '/' + entry.name
    return (
      <FileRow
        key={entry.name}
        row={row}
        path={child}
        variant={variant}
        columns={prefs.columns}
        inTrash={inTrash}
        favourite={isFavourite(child)}
        onOpen={() => {
          if (entry.dir) onNavigate(child)
          // Previewable in place; otherwise a download, where the browser
          // streams it and Range/resume come from the server.
          else if (previewable(toPreviewFile(child, entry))) setPreviewing({ path: child, entry })
          else window.location.href = filesUrl(child)
        }}
        onShare={onShare ? () => onShare(child) : undefined}
        onDelete={() => void remove(entry)}
        onToggleFavourite={() => onToggleFavourite(child)}
        onChmod={() => setChmodding({ path: child, entry })}
        onRename={() => void rename(entry)}
        // Drag a row onto a folder to move it there. Only offered where a write
        // could succeed (inside a permission domain, not the trash); the kernel
        // still has the final say on the drop.
        onMove={canWrite && !inTrash ? moveInto : undefined}
      />
    )
  }

  return (
    <div
      className={dragging ? 'browser dragging' : 'browser'}
      onDragOver={(e) => {
        // Only files dragged in from the OS raise the upload hint. An internal
        // row being dragged onto a folder carries our own type instead, and is
        // handled by the row it lands on -- reacting to it here would flash the
        // whole-folder "drop to upload" overlay over a move.
        if (!canWrite || !e.dataTransfer.types.includes('Files')) return
        e.preventDefault()
        setDragging(true)
      }}
      onDragLeave={(e) => {
        // Only when the pointer has actually left this element, or moving over a
        // child would flicker the hint off and on.
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false)
      }}
      onDrop={(e) => {
        if (!e.dataTransfer.types.includes('Files')) return
        e.preventDefault()
        setDragging(false)
        void uploadFiles([...e.dataTransfer.files])
      }}
    >
      <div className="toolbar">
        <label className="button">
          <Icon name="upload" size={17} />
          파일 올리기
          <input
            type="file"
            multiple
            hidden
            onChange={(e) => {
              void uploadFiles([...(e.target.files ?? [])])
              e.target.value = ''
            }}
          />
        </label>
        <button type="button" className="ghost" onClick={() => void mkdir()} disabled={!canWrite}>
          <Icon name="folder-plus" size={17} />새 폴더
        </button>
        <button type="button" className="ghost" onClick={openTrash}>
          <Icon name="trash" size={17} />
          휴지통
        </button>
        {/* Open the whole folder in the editor. Only where it makes sense: a
            real directory in the tree, never the trash (you edit things on
            their way in, not on their way out). */}
        {onOpenWorkspace && !inTrash && canWrite && (
          <button type="button" className="ghost" onClick={() => onOpenWorkspace(path)}>
            <Icon name="pencil" size={17} />
            편집기로 열기
          </button>
        )}
        {/* On the toolbar rather than only in a row's menu, because the folder
            you want to keep is usually the one you are standing in -- you got
            here, and now you want to get back. */}
        <button
          type="button"
          className="ghost"
          aria-pressed={starred}
          title={starred ? '즐겨찾기에서 빼기' : '첫 화면에 즐겨찾기로 두기'}
          onClick={() => onToggleFavourite(path)}
        >
          <Icon name={starred ? 'star-on' : 'star'} size={17} />
          즐겨찾기
        </button>

        {/* View, density, sort and column controls, pushed to the right so the
            file actions stay on the left where the eye starts. */}
        <ViewControls prefs={prefs} />
      </div>

      {dragging && <div className="drop-hint">여기에 놓으면 올라갑니다</div>}

      {upload && (
        <div className="progress">
          {/* The bar needs a track behind it, or a 10%-done upload reads as a
              stray blue dash rather than as progress through something. */}
          <div
            className="track"
            role="progressbar"
            aria-valuenow={upload.done}
            aria-valuemax={upload.total}
          >
            <div className="bar" style={{ width: `${(upload.done / upload.total) * 100}%` }} />
          </div>
          <span className="muted small">
            {upload.current} ({upload.done + 1}/{upload.total})
          </span>
        </div>
      )}

      <main>
        {/* aria-live sits on the STATUS only. It used to wrap the rows, which
            with windowing would make a screen reader announce the list again
            on every scroll -- the DOM changes constantly by design. */}
        <div aria-live="polite">
          {loadError && <p className="empty error">{loadError}</p>}
          {!loadError && entries === null && <Skeleton />}
          {!loadError && entries !== null && entries.length === 0 && (
            <p className="empty">{inTrash ? '휴지통이 비어 있습니다.' : '비어 있습니다.'}</p>
          )}
          {/* A folder that HAS files and is showing none is a different state
              from an empty one, and saying "비어 있습니다" here would be a lie
              that hides the filter causing it. */}
          {!loadError && filtering && sorted !== null && sorted.length > 0 && visible?.length === 0 && (
            <p className="empty">
              “{query.trim()}”에 맞는 이름이 없습니다.
              <br />
              <span className="small">이 폴더 안에서만 찾습니다.</span>
            </p>
          )}
          {/* A directory that loaded WITH files used to say nothing at all: the
              live region wrapped the rows before, and taking it off them (the
              windowed list rewrites itself on every scroll, which a screen
              reader would read out) removed the only announcement there was.
              The count is the useful sentence anyway -- the rows themselves are
              navigable once you know they arrived. */}
          {!loadError && sorted !== null && visible !== null && visible.length > 0 && (
            <p className="sr-only">
              {filtering
                ? `${sorted.length.toLocaleString('ko-KR')}개 중 ${visible.length.toLocaleString('ko-KR')}개 일치`
                : `${visible.length.toLocaleString('ko-KR')}개 항목`}
            </p>
          )}
        </div>

        {/* Above the list, not below it. Below, it sits after the spacer that
            stands in for every row not rendered -- which on the listings this
            notice exists for is several hundred thousand pixels down. */}
        {(virtual.active || filtering) && sorted && visible && visible.length > 0 && (
          <p className="muted small count">
            {filtering
              ? `${sorted.length.toLocaleString('ko-KR')}개 중 ${visible.length.toLocaleString('ko-KR')}개`
              : `${visible.length.toLocaleString('ko-KR')}개`}
            {virtual.active &&
              ' — 화면에 보이는 만큼만 그립니다. 브라우저의 페이지 내 찾기(Ctrl+F)는 보이는 범위에서만 동작합니다.'}
          </p>
        )}

        {visible !== null && (
          <div ref={gridWrapRef} className={`listing ${prefs.view} density-${prefs.density}`}>
            <div
              ref={virtual.ref}
              // Focusable so there is somewhere to put focus when the row that
              // held it is scrolled out and unmounted. Without this, focus falls
              // to <body> and the next Tab restarts from the top of the page.
              tabIndex={-1}
              onFocusCapture={() => {
                focusWasInList.current = true
              }}
            >
              {/* Spacers stand in for the rows (grid ROWS, in grid mode) that are
                  not rendered, so the scrollbar describes the whole directory
                  rather than the part currently in the document. */}
              {virtual.padTop > 0 && <div style={{ height: virtual.padTop }} aria-hidden="true" />}
              {prefs.view === 'grid'
                ? // Each rendered grid-row holds `cols` cards and carries the
                  // [data-row] the virtualiser measures, so windowing counts rows
                  // of cards rather than cards.
                  Array.from({ length: virtual.end - virtual.start }, (_, i) => {
                    const r = virtual.start + i
                    const slice = visible.slice(r * cols, r * cols + cols)
                    return (
                      <div
                        key={r}
                        data-row
                        className="grid-row"
                        style={{ gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))` }}
                      >
                        {slice.map((row) => renderCell(row, 'card'))}
                      </div>
                    )
                  })
                : visible.slice(virtual.start, virtual.end).map((row) => renderCell(row, 'row'))}
              {virtual.padBottom > 0 && (
                <div style={{ height: virtual.padBottom }} aria-hidden="true" />
              )}
            </div>
          </div>
        )}

        {/* Below the folder's own rows, and outside the windowed container:
            these come from a walk of the tree, they each live somewhere else,
            and the virtualiser measures a row inside that container to work out
            how tall the rest of THIS listing would be. */}
        {needle.text.length >= 2 && (
          <DeepResults search={deep} path={path} query={query.trim()} onNavigate={onNavigate} />
        )}
      </main>

      {previewing && (
        <PreviewModal
          path={previewing.path}
          entry={previewing.entry}
          onClose={() => setPreviewing(null)}
          onError={onError}
        />
      )}
      {chmodding && (
        <ModeDialog
          path={chmodding.path}
          entry={chmodding.entry}
          onClose={() => setChmodding(null)}
          onDone={() => void reload()}
          onError={onError}
        />
      )}
    </div>
  )
}

/**
 * Placeholder rows while the listing loads.
 *
 * The text "불러오는 중…" was one line in the middle of an empty page, so the
 * whole list jumped into place underneath it when the response landed. Rows of
 * the right height mean the only thing that changes is their contents.
 *
 * Six of them: enough to fill the fold on a phone, few enough that a directory
 * with two files does not flash a wall of grey.
 */
function Skeleton() {
  return (
    <div aria-hidden="true">
      {[0, 1, 2, 3, 4, 5].map((i) => (
        <div className="row skeleton-row" key={i}>
          <span className="icon">
            <span className="skeleton" />
          </span>
          <span className="name">
            {/* Varied widths -- six identical bars read as a loading graphic,
                varied ones read as filenames that have not arrived. */}
            <span className="skeleton" style={{ width: `${[58, 34, 71, 45, 62, 39][i]}%` }} />
          </span>
        </div>
      ))}
    </div>
  )
}
