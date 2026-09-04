import { useCallback, useEffect, useRef, useState } from 'react'
import { api, filesUrl } from '../api'
import { fetchBytes, resolveTheme } from '../preview/bytes'
import { renderersFor, toPreviewFile, type Cleanup, type Renderer } from '../preview/registry'
import type { Entry } from '../types'
import { iconFor } from '../lib/format'
import { Icon } from './Icon'

/**
 * A directory opened as a workspace: a tree on the left, a stack of tabs on the
 * right, each tab an editor.
 *
 * This is the "open the whole folder in the editor" step above the single-file
 * preview modal. It does NOT introduce a new editing engine — every tab mounts
 * the SAME imperative renderers the modal does (Monaco for `edit`), so a save
 * is still PUT /api/files, still atomic, still the kernel deciding through the
 * helper. What is new is only the shell around them: a tree that lists through
 * the same per-user listing API, and tabs that keep several files open at once.
 *
 * Everything opens as the signed-in user. There is no runtime and no shell
 * here: this is the file browser with a second layout, not code-server.
 */
export function Workspace({
  root,
  onError,
  onClose,
}: {
  /** The directory this workspace is rooted at, e.g. "teams/design". */
  root: string
  onError: (message: string) => void
  onClose: () => void
}) {
  const [tabs, setTabs] = useState<Tab[]>([])
  const [active, setActive] = useState<string | null>(null)
  // path -> whether that tab has unsaved changes. Kept here, not in the tab
  // list, so a FilePane can report its own dirtiness up without the parent
  // re-mounting it.
  const [dirty, setDirty] = useState<Record<string, boolean>>({})

  const open = useCallback((path: string, entry: Entry) => {
    setTabs((cur) => (cur.some((t) => t.path === path) ? cur : [...cur, { path, entry }]))
    setActive(path)
  }, [])

  const setPaneDirty = useCallback((path: string, d: boolean) => {
    setDirty((cur) => (cur[path] === d ? cur : { ...cur, [path]: d }))
  }, [])

  const close = useCallback(
    (path: string) => {
      if (dirty[path] && !window.confirm('저장하지 않은 변경이 있습니다. 이 탭을 닫을까요?')) return
      setTabs((cur) => {
        const next = cur.filter((t) => t.path !== path)
        // Closing the active tab moves focus to its neighbour, not to nothing.
        setActive((a) => (a !== path ? a : (next[next.length - 1]?.path ?? null)))
        return next
      })
      setDirty((cur) => {
        const { [path]: _gone, ...rest } = cur
        return rest
      })
    },
    [dirty],
  )

  const anyDirty = Object.values(dirty).some(Boolean)
  function requestClose() {
    if (anyDirty && !window.confirm('저장하지 않은 변경이 있는 탭이 있습니다. 편집기를 닫을까요?'))
      return
    onClose()
  }

  // Escape closes the workspace (with the dirty guard), matching the modal.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') {
        e.preventDefault()
        requestClose()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [anyDirty])

  return (
    <div className="workspace" role="dialog" aria-modal="true" aria-label={`${root} 편집기`}>
      <header className="ws-head">
        <span className="ws-root" title={root}>
          <Icon name="folder" size={16} />
          {root}
        </span>
        <button type="button" className="icon" aria-label="편집기 닫기" onClick={requestClose}>
          <Icon name="close" size={18} />
        </button>
      </header>

      <div className="ws-body">
        <aside className="ws-tree" aria-label="파일 트리">
          <Tree root={root} depth={0} onOpen={open} onError={onError} activePath={active} />
        </aside>

        <section className="ws-main">
          {tabs.length === 0 ? (
            <p className="ws-empty muted">왼쪽에서 파일을 선택하면 여기서 편집합니다.</p>
          ) : (
            <>
              <div className="ws-tabs" role="tablist">
                {tabs.map((t) => (
                  <div
                    key={t.path}
                    className={t.path === active ? 'ws-tab active' : 'ws-tab'}
                    role="tab"
                    aria-selected={t.path === active}
                  >
                    <button type="button" className="ws-tab-name" onClick={() => setActive(t.path)}>
                      {dirty[t.path] && <span className="ws-dot" aria-label="저장 안 됨">●</span>}
                      {baseName(t.path)}
                    </button>
                    <button
                      type="button"
                      className="ws-tab-x"
                      aria-label={`${baseName(t.path)} 닫기`}
                      onClick={() => close(t.path)}
                    >
                      <Icon name="close" size={13} />
                    </button>
                  </div>
                ))}
              </div>

              {/* Every open tab stays mounted so its editor keeps its buffer,
                  cursor and undo history when you switch away; only the active
                  one is shown. */}
              {tabs.map((t) => (
                <div key={t.path} className="ws-pane" hidden={t.path !== active}>
                  <FilePane
                    path={t.path}
                    entry={t.entry}
                    onError={onError}
                    onDirty={(d) => setPaneDirty(t.path, d)}
                  />
                </div>
              ))}
            </>
          )}
        </section>
      </div>
    </div>
  )
}

interface Tab {
  path: string
  entry: Entry
}

function baseName(path: string): string {
  const i = path.lastIndexOf('/')
  return i < 0 ? path : path.slice(i + 1)
}

/**
 * One directory level of the tree, loaded lazily.
 *
 * A level fetches its own children the first time it is expanded, through the
 * same per-user listing API the browser uses — so a folder this user cannot
 * enter fails here exactly as it would there, with the kernel's verdict.
 */
function Tree({
  root,
  depth,
  onOpen,
  onError,
  activePath,
}: {
  root: string
  depth: number
  onOpen: (path: string, entry: Entry) => void
  onError: (message: string) => void
  activePath: string | null
}) {
  const [entries, setEntries] = useState<Entry[] | null>(null)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const loading = useRef(false)

  // The root loads on mount; deeper levels load when first expanded (the parent
  // renders a child Tree only for an expanded folder, so mounting IS expanding).
  useEffect(() => {
    if (loading.current) return
    loading.current = true
    const ac = new AbortController()
    api
      .list(root, ac.signal)
      .then((l) => setEntries(sortEntries(l.entries)))
      .catch((e: unknown) => {
        if (ac.signal.aborted) return
        setEntries([])
        onError(e instanceof Error ? e.message : '폴더를 열 수 없습니다.')
      })
    return () => ac.abort()
  }, [root, onError])

  if (entries === null) {
    return <p className="ws-tree-note muted small" style={indent(depth)}>불러오는 중…</p>
  }
  if (entries.length === 0) {
    return <p className="ws-tree-note muted small" style={indent(depth)}>비어 있음</p>
  }

  return (
    <ul className="ws-tree-list">
      {entries.map((e) => {
        const path = `${root}/${e.name}`
        const isOpen = expanded.has(e.name)
        return (
          <li key={e.name}>
            <button
              type="button"
              className={path === activePath ? 'ws-node active' : 'ws-node'}
              style={indent(depth)}
              onClick={() => {
                if (e.dir) {
                  setExpanded((cur) => {
                    const next = new Set(cur)
                    next.has(e.name) ? next.delete(e.name) : next.add(e.name)
                    return next
                  })
                } else {
                  onOpen(path, e)
                }
              }}
            >
              {e.dir ? (
                <Icon
                  name="chevron"
                  size={13}
                  className={isOpen ? 'ws-caret open' : 'ws-caret'}
                />
              ) : (
                <span className="ws-caret-spacer" />
              )}
              <Icon name={e.dir ? 'folder' : iconFor(e)} size={15} />
              <span className="ws-node-name">{e.name}</span>
            </button>
            {e.dir && isOpen && (
              <Tree
                root={path}
                depth={depth + 1}
                onOpen={onOpen}
                onError={onError}
                activePath={activePath}
              />
            )}
          </li>
        )
      })}
    </ul>
  )
}

function indent(depth: number): React.CSSProperties {
  return { paddingLeft: `${0.4 + depth * 0.85}rem` }
}

// Folders first, then files, each case-insensitively by name — the order the
// main browser sorts in, so the tree does not disagree with the listing.
function sortEntries(entries: Entry[]): Entry[] {
  return [...entries].sort((a, b) =>
    a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1,
  )
}

/**
 * One editing pane: the imperative renderer mounted into a container.
 *
 * This is the same mount/cleanup dance PreviewModal runs, minus the view
 * switcher — a workspace tab opens straight into the editor (or, for something
 * that cannot be edited, whatever the best renderer is: an image, highlighted
 * source). The pane owns its own Save button; Monaco also saves on Ctrl/Cmd-S
 * from inside the editor.
 */
function FilePane({
  path,
  entry,
  onError,
  onDirty,
}: {
  path: string
  entry: Entry
  onError: (message: string) => void
  onDirty: (dirty: boolean) => void
}) {
  const hostRef = useRef<HTMLDivElement>(null)
  const [loading, setLoading] = useState(true)
  const [saver, setSaver] = useState<(() => Promise<void>) | null>(null)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [unopenable, setUnopenable] = useState(false)

  useEffect(() => {
    onDirty(dirty)
  }, [dirty, onDirty])

  useEffect(() => {
    const host = hostRef.current
    if (!host) return
    const file = toPreviewFile(path, entry)
    const renderers = renderersFor(file)
    // A workspace is for editing, so the editor is the default when the file can
    // take it; otherwise fall back to the best available view (read-only).
    const chosen: Renderer | undefined = renderers.find((r) => r.id === 'edit') ?? renderers[0]
    if (!chosen) {
      setUnopenable(true)
      setLoading(false)
      return
    }

    const theme = resolveTheme()
    let cleanup: Cleanup | undefined
    let cancelled = false
    setLoading(true)
    host.replaceChildren()

    chosen
      .load()
      .then((mod) =>
        mod.mount({
          file,
          el: host,
          theme,
          fetchBytes: (max) => fetchBytes(path, max),
          onError,
          setSaver: (fn) => !cancelled && setSaver(() => fn),
          setDirty: (d) => !cancelled && setDirty(d),
        }),
      )
      .then((c) => {
        if (cancelled) {
          c?.()
          return
        }
        cleanup = c
        setLoading(false)
      })
      .catch((e: unknown) => {
        if (cancelled) return
        setLoading(false)
        onError(e instanceof Error ? e.message : '파일을 열 수 없습니다.')
      })

    return () => {
      cancelled = true
      cleanup?.()
      host.replaceChildren()
    }
    // path+entry identify the file; both are stable for the life of a tab.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path])

  async function save() {
    if (!saver) return
    setSaving(true)
    try {
      await saver()
    } catch (e) {
      onError(e instanceof Error ? e.message : '저장하지 못했습니다.')
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      <div className="ws-pane-head">
        <span className="ws-pane-path" title={path}>
          {path}
        </span>
        {saver && (
          <button type="button" disabled={!dirty || saving} onClick={() => void save()}>
            {saving ? '저장 중…' : '저장'}
          </button>
        )}
        <a className="button ghost" href={filesUrl(path)}>
          다운로드
        </a>
      </div>
      <div className="ws-pane-body">
        {loading && <p className="muted ws-pane-loading">불러오는 중…</p>}
        {unopenable && (
          <p className="muted ws-pane-loading">
            이 파일은 편집기로 열 수 없습니다. 다운로드해서 확인하세요.
          </p>
        )}
        <div ref={hostRef} className="ws-pane-host" />
      </div>
    </>
  )
}
