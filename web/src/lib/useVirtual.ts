import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'

/**
 * Renders only the rows that are on screen.
 *
 * Measured before this existed: a directory of 50,000 entries produced 800,070
 * DOM nodes, took 34 seconds to show its first row, and left the page
 * responding to a hover in 856ms. The server had answered in 2.3 seconds --
 * every bit of the rest was the browser being asked to lay out fifty thousand
 * rows nobody was looking at.
 *
 * Against the WINDOW's scroll, not a scroll container of its own. A container
 * would be easier to compute, and it would change how the page behaves: the
 * sticky header, the phone's address bar hiding as you scroll, and the momentum
 * at the bottom of the page all come from the document scrolling. Keeping that
 * costs one getBoundingClientRect per frame.
 */
export interface Virtual {
  /** Put this on the element that wraps the rows. */
  ref: React.RefObject<HTMLDivElement | null>
  /** Slice the items with these. */
  start: number
  end: number
  /** Spacer heights that stand in for the rows not rendered. */
  padTop: number
  padBottom: number
  /** Whether windowing is actually in force, which callers may want to say. */
  active: boolean
}

interface Options {
  count: number
  /**
   * Changes when the list becomes a DIFFERENT list, so the window can go back
   * to the top.
   *
   * `count` is the wrong signal for this: deleting one file changes the count
   * without changing which list you are looking at, and snapping to row 0
   * while someone is scrolled deep is its own bug. Keyed on the path instead,
   * the window resets exactly when the content it describes is replaced.
   */
  resetKey?: string
  /**
   * Below this many rows, everything is rendered and this hook does nothing.
   *
   * The number is chosen by what virtualising COSTS, not by what it saves: the
   * browser's own find-in-page cannot match text that is not in the document,
   * so above this line Ctrl-F stops finding files further down the list. That
   * is worth giving up only where the alternative is unusable.
   *
   * Measured un-virtualised, on this row markup: 1,000 rows took 888ms to
   * first paint and 16,070 DOM nodes, which is slow but fine; 10,000 took 5.0s
   * and 160,070, which is not. 500 puts the ceiling at roughly 450ms while
   * leaving find-in-page working for the directories people actually keep.
   */
  threshold?: number
  /** Rows kept beyond each edge, so a fast scroll does not show blank space. */
  overscan?: number
}

/** How many rows to lay out before a row has been measured. */
const FIRST_PAINT = 60

export function useWindowVirtual({
  count,
  resetKey = '',
  threshold = 500,
  overscan = 10,
}: Options): Virtual {
  const ref = useRef<HTMLDivElement | null>(null)
  const [rowHeight, setRowHeight] = useState(0)
  const [range, setRange] = useState({ start: 0, end: FIRST_PAINT })

  // Adjusted during render, not in an effect: an effect runs AFTER the browser
  // has painted, so the first frame of a new directory would be drawn through
  // the previous one's scroll offset -- an empty viewport under a full-height
  // spacer, corrected a frame later. React re-runs this component before
  // painting instead.
  const [seenKey, setSeenKey] = useState(resetKey)
  if (seenKey !== resetKey) {
    setSeenKey(resetKey)
    // FIRST_PAINT only applies before anything has been measured. Once a row
    // height is known -- which it is by the second keystroke in a search box --
    // one screenful is exactly computable, and laying out sixty rows to show
    // fifteen of them is the most expensive part of a keystroke: each one
    // mounts a dropdown menu.
    setRange({
      start: 0,
      end: rowHeight > 0 ? Math.ceil(window.innerHeight / rowHeight) + overscan : FIRST_PAINT,
    })
  }

  const windowing = count > threshold
  // Until a row has been measured there is no way to know how tall the list is.
  // Rendering everything meanwhile would be the exact cost this avoids, so the
  // first pass lays out one screenful and measures that.
  const measured = rowHeight > 0

  const update = useCallback(() => {
    const el = ref.current
    if (!el || rowHeight <= 0) return
    // Distance from the top of the document to the top of the list.
    const listTop = el.getBoundingClientRect().top + window.scrollY
    const scrolledInto = window.scrollY - listTop
    const first = Math.floor(scrolledInto / rowHeight)
    const fits = Math.ceil(window.innerHeight / rowHeight)

    const start = Math.max(0, first - overscan)
    const end = Math.min(count, Math.max(0, first) + fits + overscan)
    setRange((prev) => (prev.start === start && prev.end === end ? prev : { start, end }))
  }, [count, rowHeight, overscan])

  // Measure after every commit: the row height is not a constant. It changes
  // with the viewport (below 34rem a row wraps its size and date onto a second
  // line) and with the font the browser actually resolved.
  useLayoutEffect(() => {
    if (!windowing) return
    const row = ref.current?.querySelector<HTMLElement>('[data-row]')
    if (!row) return
    const h = row.getBoundingClientRect().height
    // Only on a real change, or this schedules itself forever.
    if (h > 0 && Math.abs(h - rowHeight) > 0.5) setRowHeight(h)
  })

  useEffect(() => {
    if (!windowing || !measured) return

    let queued = false
    const onScroll = () => {
      // Coalesce to one recompute per frame. A scroll fires far more often than
      // the screen is painted, and each one of these reads layout.
      if (queued) return
      queued = true
      requestAnimationFrame(() => {
        queued = false
        update()
      })
    }
    update()
    window.addEventListener('scroll', onScroll, { passive: true })
    window.addEventListener('resize', onScroll)
    return () => {
      window.removeEventListener('scroll', onScroll)
      window.removeEventListener('resize', onScroll)
    }
  }, [windowing, measured, update])

  if (!windowing) {
    return { ref, start: 0, end: count, padTop: 0, padBottom: 0, active: false }
  }
  if (!measured) {
    return { ref, start: 0, end: Math.min(count, FIRST_PAINT), padTop: 0, padBottom: 0, active: true }
  }

  const start = Math.min(range.start, count)
  const end = Math.min(range.end, count)
  return {
    ref,
    start,
    end,
    padTop: start * rowHeight,
    padBottom: Math.max(0, (count - end) * rowHeight),
    active: true,
  }
}
