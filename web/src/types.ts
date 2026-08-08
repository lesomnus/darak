// Mirrors of what the Go handlers emit. They are written out rather than
// inferred so a change on either side shows up as a type error here instead of
// as an empty column in the browser.

/** One row of a directory listing (server.Entry). */
export interface Entry {
  name: string
  dir: boolean
  size: number
  /** RFC 3339. */
  mod_time: string
  /** Four octal digits, e.g. "0660". */
  mode: string
}

export interface Listing {
  path: string
  entries: Entry[]
}

/** An issued capability link (server.shareView). */
export interface ShareLink {
  token: string
  url: string
  path: string
  name: string
  created: string
  expires: string
  protected: boolean
}

export interface Me {
  user: string
}

/**
 * A failed request.
 *
 * `status` is kept because the server answers with the kernel's verdict, and the
 * difference between "not permitted" and "not found" is the only thing a person
 * can act on.
 */
export class ApiError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}
