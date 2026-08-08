import { useCallback, useEffect, useState } from 'react'

/**
 * Three states, not two.
 *
 * `system` is the default and it is not the same as picking whichever one the
 * OS happens to be on right now: someone whose laptop turns dark at sunset
 * wants the page to follow, and a two-state toggle silently takes that away the
 * first time it is touched.
 *
 * The stylesheet does the actual work through `color-scheme` and
 * `light-dark()`, so all this has to do is say which of the three is in force.
 * That also means the native pieces -- scrollbars, date pickers, the file
 * chooser -- follow along, which a class-based theme does not get.
 */
export type Theme = 'system' | 'light' | 'dark'

const KEY = 'darak.theme'

export function useTheme(): [Theme, (next: Theme) => void] {
  const [theme, setTheme] = useState<Theme>(read)

  useEffect(() => {
    const root = document.documentElement
    // Absent rather than `system`: the stylesheet's default branch is bare
    // `:root`, which an attribute selector would have to be written to dodge.
    if (theme === 'system') root.removeAttribute('data-theme')
    else root.setAttribute('data-theme', theme)
  }, [theme])

  const choose = useCallback((next: Theme) => {
    setTheme(next)
    try {
      if (next === 'system') localStorage.removeItem(KEY)
      else localStorage.setItem(KEY, next)
    } catch {
      // Private mode, or storage full. The choice still applies to this tab;
      // only remembering it for the next one is lost, and that is not worth
      // failing over.
    }
  }, [])

  return [theme, choose]
}

function read(): Theme {
  try {
    const saved = localStorage.getItem(KEY)
    if (saved === 'light' || saved === 'dark') return saved
  } catch {
    // As above.
  }
  return 'system'
}
