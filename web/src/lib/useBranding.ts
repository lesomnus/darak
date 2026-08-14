import { useEffect, useState } from 'react'
import { api } from '../api'
import type { Branding } from '../types'

/**
 * What to draw in the corner.
 *
 * The mark belongs to whoever runs this, not to the software: an internal file
 * server sits beside a dozen other internal tools and the one in the corner is
 * how people tell them apart at a glance. The server reads the image once at
 * startup from -brand-logo; all this does is ask which one to use.
 *
 * The fallback is not a loading state -- it is what an unconfigured deployment
 * gets, so nothing flashes and nothing is empty while the request is out.
 */
const FALLBACK: Branding = { name: '파일 서버', logo: false, sso: false }

export function useBranding(): Branding {
  const [brand, setBrand] = useState<Branding>(FALLBACK)

  useEffect(() => {
    let cancelled = false
    api
      .branding()
      .then((b) => {
        // A blank name would leave the corner empty, which reads as a bug
        // rather than as a choice.
        if (!cancelled && b.name) setBrand(b)
      })
      .catch(() => {
        // The built-in mark is a perfectly good answer, and every other request
        // this page makes will report its own failure loudly enough.
      })
    return () => {
      cancelled = true
    }
  }, [])

  // The tab title too. A browser with eight tabs open is the other place the
  // name has to do its job.
  useEffect(() => {
    document.title = brand.name
  }, [brand.name])

  return brand
}
