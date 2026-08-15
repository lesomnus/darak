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

/** One line of the search stream (server.searchHit). */
export interface SearchHit {
  /** Relative to the directory that was searched. */
  path: string
  /** The entry's own name, in NFC — what `pos` indexes and what is drawn. */
  name: string
  dir: boolean
  score: number
  /** Matched characters, as UTF-16 code unit offsets into `name`. */
  pos: number[]
}

/**
 * The stream's last line.
 *
 * `truncated` is the whole reason it exists: a list cut short by a budget looks
 * exactly like a complete list of everything there is, and those are opposite
 * answers.
 */
export interface SearchDone {
  done: true
  visited: number
  matches: number
  truncated: boolean
  /** Set when the walk failed after the response had already started. */
  error?: string
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
  /**
   * True when the server answered as the anonymous account rather than a
   * signed-in user: the visitor is browsing the public folders without a
   * session. The interface then offers a sign-in instead of a personal home,
   * and hides everything that belongs to an account.
   */
  anonymous?: boolean
}

/** One folder the roster opens to anonymous visitors (GET /api/public). */
export interface PublicFolder {
  name: string
  description?: string
  /** True when anyone may write, not only read (anonymous:write). */
  write?: boolean
}

/**
 * What this installation calls itself (server.Brand).
 *
 * `logo` says whether GET /api/branding/logo has anything to serve. Asking
 * first, rather than pointing an <img> at it and letting it fail, is what keeps
 * a broken-image glyph out of the corner of every page on a default install.
 */
export interface Branding {
  name: string
  logo: boolean
  /**
   * Whether a provider is CONFIGURED, not whether it is reachable. The button
   * is drawn either way: an outage explains itself far better than a missing
   * button does.
   */
  sso: boolean
}

/**
 * A one-off message from the sign-on callback.
 *
 * Fetched by id rather than read out of the URL, so the text is the server's
 * and not something a link can choose.
 */
export interface SSONotice {
  /** '' when there is nothing to say, 'pending' or 'error'. */
  kind: string
  message?: string
  /** The address the provider asserted, so the person can quote it. */
  address?: string
}

/** One approved binding of a directory identity to an account. */
export interface IdentityMapping {
  account: string
  emails: string[]
  issuer?: string
  /** The provider's immutable handle, recorded at first sign-in. */
  subject?: string
  approved_by?: string
  approved_at?: string
  updated_at?: string
}

/** An identity that signed in and has no mapping yet. It grants nothing. */
export interface IdentityRequest {
  issuer: string
  subject: string
  emails: string[]
  name?: string
  first_seen: string
  last_seen: string
  count: number
}

/** A mapping the roster does not agree with. Reported, never enforced. */
export interface IdentityProblem {
  account: string
  /** 'unknown', 'disabled' or 'reserved'. */
  code: string
}

export interface IdentityView {
  mappings: IdentityMapping[]
  pending: IdentityRequest[]
  problems: IdentityProblem[]
  warning?: string
}

/**
 * One auto-provisioning rule as it is currently applied.
 *
 * Read-only everywhere: the rules are a deployed file, because a page that
 * could edit them would be a way to grant yourself an account. `bearer_file` is
 * the PATH a token is read from — the token itself never leaves the server.
 */
export interface ProvisionRule {
  name?: string
  url: string
  match: {
    domains?: string[]
    claims?: Record<string, string>
  }
  auth: {
    bearer_file?: string
  }
}

export interface ProvisionStatus {
  path: string
  loaded_at?: string
  /** Short content hash, so an operator can tell which version is running. */
  digest?: string
  timeout?: string
  wait?: string
  rules: ProvisionRule[]
  /**
   * Set when the file on disk right now cannot be used. The rules above are
   * then the last good ones, and they are still in force.
   */
  error?: string
  error_at?: string
}

export interface ProvisionView {
  enabled: boolean
  status?: ProvisionStatus
}

export interface IdentityJournalEntry {
  at: string
  /** Empty when the server did it itself, which is what pinning is. */
  by?: string
  action: string
  account?: string
  issuer?: string
  subject?: string
  emails?: string[]
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
  /**
   * 'usersync' or 'nss'. The NSS listing answers a weaker question — winbind
   * declines to enumerate — so the page says which one it is looking at.
   */
  source: string
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
  /**
   * False when usersync is not installed. That is a property of the deployment,
   * not a failure — there is no roster here to compare against.
   */
  available: boolean
  /** Set only when usersync EXISTS and could not answer. */
  error?: string
}

/** Which teams the signed-in user may manage the membership of. */
export interface TeamWhoami {
  teams: string[]
}

export interface TeamView {
  name: string
  gid: number
  description?: string
  owners: string[]
  members: string[]
}

/** What the team panel renders from. Available to owners, who cannot read the
 *  full inventory. */
export interface TeamsView {
  teams: TeamView[]
  users: string[]
}

/** One recorded change (internal/activity.Event). */
export interface ActivityEvent {
  at: string
  user: string
  action: 'create' | 'write' | 'delete' | 'rename' | 'mkdir'
  path: string
  to?: string
  /** 'smb' includes a mounted share — a cifs mount is still SMB underneath. */
  source: 'smb' | 'web'
  from?: string
}

export interface ActivityReport {
  events: ActivityEvent[]
  /** False when no activity directory is configured. */
  enabled: boolean
}
