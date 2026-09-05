import { useCallback, useEffect, useState } from 'react'
import type { SortDir, SortKey } from './format'

/**
 * How the listing is displayed, remembered across visits.
 *
 * These are per-viewer conveniences, not shared state, so localStorage is the
 * right home: they belong to this browser and matter to nobody else. Every read
 * and write is guarded, because a private window or a browser set to block site
 * data throws on access, and the listing must render regardless.
 */
export type View = 'list' | 'grid'
export type Density = 'comfortable' | 'compact'

/** The optional meta columns the list can show. Name is always shown. */
export interface Columns {
  size: boolean
  date: boolean
}

export interface ViewPrefs {
  view: View
  density: Density
  sortKey: SortKey
  sortDir: SortDir
  columns: Columns
  setView: (v: View) => void
  setDensity: (d: Density) => void
  /** Choosing the key already showing flips the direction, like a column header. */
  setSort: (key: SortKey) => void
  toggleColumn: (c: keyof Columns) => void
}

interface Stored {
  view: View
  density: Density
  sortKey: SortKey
  sortDir: SortDir
  columns: Columns
}

const KEY = 'darak.view'
const DEFAULTS: Stored = {
  view: 'list',
  density: 'comfortable',
  sortKey: 'name',
  sortDir: 'asc',
  columns: { size: true, date: true },
}

function load(): Stored {
  try {
    const raw = localStorage.getItem(KEY)
    if (!raw) return DEFAULTS
    const p = JSON.parse(raw) as Partial<Stored>
    // Merge over the defaults so a value written by an older build that did not
    // know a field yet still starts from something valid.
    return {
      ...DEFAULTS,
      ...p,
      columns: { ...DEFAULTS.columns, ...(p.columns ?? {}) },
    }
  } catch {
    return DEFAULTS
  }
}

export function useViewPrefs(): ViewPrefs {
  const [prefs, setPrefs] = useState<Stored>(load)

  useEffect(() => {
    try {
      localStorage.setItem(KEY, JSON.stringify(prefs))
    } catch {
      // A browser that refuses storage still gets a working session; the choice
      // just does not outlive it.
    }
  }, [prefs])

  const setView = useCallback((view: View) => setPrefs((p) => ({ ...p, view })), [])
  const setDensity = useCallback((density: Density) => setPrefs((p) => ({ ...p, density })), [])
  const setSort = useCallback(
    (key: SortKey) =>
      setPrefs((p) =>
        p.sortKey === key
          ? { ...p, sortDir: p.sortDir === 'asc' ? 'desc' : 'asc' }
          : // A fresh key starts ascending, except size and date, where "most
            // recent" and "biggest first" are what people actually want to see.
            { ...p, sortKey: key, sortDir: key === 'name' ? 'asc' : 'desc' },
      ),
    [],
  )
  const toggleColumn = useCallback(
    (c: keyof Columns) => setPrefs((p) => ({ ...p, columns: { ...p.columns, [c]: !p.columns[c] } })),
    [],
  )

  return { ...prefs, setView, setDensity, setSort, toggleColumn }
}
