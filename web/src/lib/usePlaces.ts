import { useCallback, useEffect, useState } from 'react'

/**
 * The folders this browser remembers: the ones you starred, and the ones you
 * were last in.
 *
 * All of it is localStorage. Nothing is sent anywhere -- there is no route that
 * would store it, and a per-user preferences table is a much larger thing to
 * add than this is worth. The cost is that it does not follow you to another
 * machine, which for a list of shortcuts is a fair trade.
 *
 * Keyed by USERNAME, and that part is not cosmetic. A shared laptop would
 * otherwise show the previous person's team folder names -- `teams/hr-2026`
 * says something even to someone who cannot open it -- to whoever signs in
 * next.
 */

/** How many directories to remember. Ten fills a short list without becoming a
 *  second file browser that has to be scrolled. */
const MAX_RECENTS = 10

const key = (user: string, what: string) => `darak.v1.${user}.${what}`

/**
 * localStorage throws rather than returning null in a few real situations --
 * Safari's private mode, a browser configured to refuse site data, a quota that
 * is somehow full. None of them should take the page down over a list of
 * shortcuts, so every access is guarded and a failure just means the feature
 * quietly does nothing.
 */
function read(user: string, what: string): string[] {
  try {
    const raw = localStorage.getItem(key(user, what))
    if (!raw) return []
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    // Written by an older version of this code, or by hand. Anything that is
    // not a plain non-empty string is dropped rather than rendered.
    return parsed.filter((p): p is string => typeof p === 'string' && p !== '')
  } catch {
    return []
  }
}

function write(user: string, what: string, value: string[]): void {
  try {
    localStorage.setItem(key(user, what), JSON.stringify(value))
  } catch {
    // See above. The in-memory state still updated, so the current tab behaves
    // correctly for as long as it is open.
  }
}

export interface Places {
  favourites: string[]
  /** Most recent first. */
  recents: string[]
  isFavourite: (path: string) => boolean
  toggleFavourite: (path: string) => void
  forgetFavourite: (path: string) => void
  clearRecents: () => void
}

/**
 * @param user  whose lists to read; the empty string while signing in.
 * @param path  where the browser is now, which is what gets recorded.
 */
export function usePlaces(user: string, path: string): Places {
  const [favourites, setFavourites] = useState<string[]>([])
  const [recents, setRecents] = useState<string[]>([])

  // Reloaded when the user changes, which is what makes signing out and back in
  // as somebody else show their lists rather than the previous person's.
  useEffect(() => {
    setFavourites(user ? read(user, 'favourites') : [])
    setRecents(user ? read(user, 'recents') : [])
  }, [user])

  useEffect(() => {
    // '' is the start page and 'admin' is not a directory; neither is somewhere
    // you can be sent back to usefully.
    if (!user || path === '' || path === 'admin') return
    setRecents((prev) => {
      // Already at the front: revisiting the same folder is not an event, and
      // rewriting storage on every render of the same page is waste.
      if (prev[0] === path) return prev
      const next = [path, ...prev.filter((p) => p !== path)].slice(0, MAX_RECENTS)
      write(user, 'recents', next)
      return next
    })
  }, [user, path])

  const isFavourite = useCallback((p: string) => favourites.includes(p), [favourites])

  const toggleFavourite = useCallback(
    (p: string) => {
      if (!user || !p) return
      setFavourites((prev) => {
        const next = prev.includes(p) ? prev.filter((x) => x !== p) : [...prev, p]
        write(user, 'favourites', next)
        return next
      })
    },
    [user],
  )

  const forgetFavourite = useCallback(
    (p: string) => {
      if (!user) return
      setFavourites((prev) => {
        const next = prev.filter((x) => x !== p)
        write(user, 'favourites', next)
        return next
      })
    },
    [user],
  )

  const clearRecents = useCallback(() => {
    if (!user) return
    setRecents([])
    write(user, 'recents', [])
  }, [user])

  return { favourites, recents, isFavourite, toggleFavourite, forgetFavourite, clearRecents }
}
