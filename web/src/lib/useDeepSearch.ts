import { useEffect, useState } from 'react'
import { api } from '../api'
import { compareNames } from './format'
import type { SearchHit } from '../types'

/**
 * Searches below the current directory, on the server.
 *
 * The filter in the header is instant because the listing is already in memory.
 * This is the other thing: a walk of the tree, which costs seconds and round
 * trips, so it is debounced, bounded, cancelled the moment the query changes,
 * and streamed.
 */
export interface DeepSearch {
  hits: SearchHit[]
  /** The walk is still going. */
  running: boolean
  /** A budget stopped it. Saying so is not optional -- see below. */
  truncated: boolean
  visited: number
  error: string
}

const IDLE: DeepSearch = { hits: [], running: false, truncated: false, visited: 0, error: '' }

/** How long to wait before walking. One per pause in typing, not one per key. */
const DEBOUNCE_MS = 300

/**
 * How long the first batch is held before it is drawn.
 *
 * Results arrive in WALK order, not score order. Holding the first ones briefly
 * and sorting them means the screen people actually look at is properly ranked;
 * everything after that is appended below it and nothing already visible ever
 * moves. The alternative -- re-sorting on every arrival -- makes the row under
 * the pointer change while it is being clicked.
 */
const FIRST_BATCH_MS = 250
/** And afterwards, just enough to coalesce a render. */
const APPEND_MS = 100

function byScore(a: SearchHit, b: SearchHit): number {
  return b.score - a.score || compareNames(a.name, b.name)
}

export function useDeepSearch(path: string, query: string, enabled: boolean): DeepSearch {
  const [state, setState] = useState<DeepSearch>(IDLE)

  useEffect(() => {
    if (!enabled) {
      setState(IDLE)
      return
    }

    const ac = new AbortController()
    let live = true
    const buffer: SearchHit[] = []
    let opened = false
    let flushTimer: ReturnType<typeof setTimeout> | undefined

    function flush() {
      flushTimer = undefined
      if (!live || buffer.length === 0) return
      // Sorted WITHIN the batch and appended. Never across batches: that is what
      // keeps a row from moving out from under the cursor.
      const batch = buffer.splice(0).sort(byScore)
      opened = true
      setState((s) => ({ ...s, hits: s.hits.concat(batch) }))
    }

    function schedule() {
      if (flushTimer !== undefined) return
      flushTimer = setTimeout(flush, opened ? APPEND_MS : FIRST_BATCH_MS)
    }

    const start = setTimeout(() => {
      if (!live) return
      setState({ ...IDLE, running: true })
      void (async () => {
        try {
          for await (const msg of api.search(path, query, ac.signal)) {
            if (!live) return
            if ('done' in msg) {
              if (flushTimer !== undefined) clearTimeout(flushTimer)
              flush()
              setState((s) => ({
                ...s,
                running: false,
                truncated: msg.truncated,
                visited: msg.visited,
                error: msg.error ?? '',
              }))
              return
            }
            // The current directory is listed above already, filtered by the same
            // matcher. A second copy of the same row under "하위 폴더" is noise.
            if (!msg.path.includes('/')) continue
            buffer.push(msg)
            schedule()
          }
        } catch (e) {
          // An abort is this effect being torn down, which is not a failure to
          // report -- the query changed, and a new search is already starting.
          if (!live || ac.signal.aborted) return
          setState((s) => ({
            ...s,
            running: false,
            error: e instanceof Error ? e.message : '하위 폴더를 찾지 못했습니다.',
          }))
        }
      })()
    }, DEBOUNCE_MS)

    return () => {
      live = false
      clearTimeout(start)
      if (flushTimer !== undefined) clearTimeout(flushTimer)
      // Drops the connection, which is how the server learns to stop walking.
      ac.abort()
    }
  }, [path, query, enabled])

  return state
}
