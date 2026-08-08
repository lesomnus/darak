import { useCallback, useEffect, useState } from 'react'

/**
 * The current location as a path inside the served tree.
 *
 * A router library would be a dependency earning very little here: there is one
 * route, and it is the path itself. Reserved prefixes map to the start screen so
 * that landing on an API URL by accident does not try to list it as a folder.
 */
function read(): string {
  const p = decodeURIComponent(window.location.pathname).replace(/^\/+/, '')
  if (p === '' || p.startsWith('api/') || p.startsWith('s/')) return ''
  return p
}

function href(path: string): string {
  return '/' + path.split('/').map(encodeURIComponent).join('/')
}

export function usePath(): [string, (next: string) => void] {
  const [path, setPath] = useState(read)

  useEffect(() => {
    const onPop = () => setPath(read())
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  const navigate = useCallback((next: string) => {
    window.history.pushState({}, '', href(next))
    setPath(next)
  }, [])

  return [path, navigate]
}
