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

// --- operator surface (internal/admin) ---

export interface AdminWhoami {
  admin: boolean
  /** The POSIX group that grants access, so the page can name it. */
  group: string
}

export interface SMBAccount {
  enabled: boolean
}

export interface AdminUser {
  name: string
  uid: number
  gid: number
  home: string
  groups: string[]
  full_name?: string
  /**
   * Absent means Samba could not be asked — which is NOT the same as having no
   * account, so the page renders the two differently.
   */
  smb?: SMBAccount
  usage_bytes?: number
}

export interface AdminGroup {
  name: string
  gid: number
  members: string[]
}

export interface Inventory {
  users: AdminUser[]
  groups: AdminGroup[]
  /** Why a field is missing, so an absence never reads as data. */
  warnings?: string[]
}

export interface Capacity {
  path: string
  total_bytes: number
  free_bytes: number
  used_bytes: number
  total_inodes: number
  free_inodes: number
}

export interface UsageReport {
  /** "zfs" counts everything a uid owns; "du" counts what is reachable now. */
  source: string
  measured_at: string
  by_uid: Record<string, number>
  error?: string
}

export interface DiskReport {
  capacity: Capacity
  usage: UsageReport
}

export interface Drift {
  kind: string
  name: string
  code: string
  want?: number
  got?: number
}

export interface DriftReport {
  findings: Drift[]
  ok: boolean
  error?: string
}
