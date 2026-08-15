import {
  ApiError,
  type ActivityReport,
  type AdminWhoami,
  type Branding,
  type DiskReport,
  type DriftReport,
  type EnrollmentProgress,
  type IdentityJournalEntry,
  type IdentityMapping,
  type IdentityView,
  type Inventory,
  type Listing,
  type Me,
  type ProvisionView,
  type PublicFolder,
  type SSONotice,
  type SearchDone,
  type SearchHit,
  type TeamsView,
  type TeamWhoami,
  type ShareLink,
} from './types'

/**
 * Builds a URL for a path inside the served tree.
 *
 * Each segment is encoded separately so a slash keeps its meaning as a
 * separator. Nothing here normalises the path: what it means is decided by
 * openat2 in the helper, with the requesting user's credentials, and a second
 * opinion formed in the browser could only ever disagree with it.
 */
export function filesUrl(path: string): string {
  return '/api/files/' + path.split('/').map(encodeURIComponent).join('/')
}

function modeUrl(path: string): string {
  return '/api/mode/' + path.split('/').map(encodeURIComponent).join('/')
}

function dirsUrl(path: string): string {
  return '/api/dirs/' + path.split('/').map(encodeURIComponent).join('/')
}

/**
 * Turns the server's answer into something a person can act on.
 *
 * The server passes the kernel's verdict through untranslated, which is right:
 * it is the authority. Naming what each verdict means for the person reading it
 * is this layer's job.
 */
function explain(status: number, fallback: string): string {
  switch (status) {
    case 401:
      return '로그인이 필요합니다.'
    case 403:
      return '권한이 없습니다. 다른 사람의 폴더이거나, 소속되지 않은 팀입니다.'
    case 404:
      return '찾을 수 없습니다. 이미 지워졌거나 이름이 바뀌었을 수 있습니다.'
    case 409:
      return '같은 이름이 이미 있습니다.'
    case 413:
      return '파일이 너무 큽니다.'
    case 507:
      return '저장 공간이 부족합니다.'
    case 503:
      return '지금 로그인을 확인할 수 없습니다. 관리자에게 알려주세요.'
    default:
      return fallback
  }
}

interface RequestOptions {
  method?: string
  body?: unknown
  signal?: AbortSignal
}

async function request<T>(url: string, opts: RequestOptions = {}): Promise<T> {
  const init: RequestInit = { method: opts.method ?? 'GET', signal: opts.signal }
  if (opts.body instanceof Blob) {
    init.body = opts.body
  } else if (opts.body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(opts.body)
  }

  const res = await fetch(url, init)
  if (!res.ok) {
    let detail = `요청이 실패했습니다 (${res.status})`
    try {
      const body = (await res.json()) as { error?: string }
      if (body.error) detail = body.error
    } catch {
      // Not JSON; the status is all there is.
    }
    throw new ApiError(res.status, explain(res.status, detail))
  }
  if (res.status === 204) return undefined as T
  const type = res.headers.get('Content-Type') ?? ''
  return (type.includes('json') ? await res.json() : await res.text()) as T
}

export const api = {
  whoami: () => request<Me>('/api/whoami'),

  // The folders open to anonymous visitors, for the not-signed-in landing page.
  publicFolders: (signal?: AbortSignal) =>
    request<{ folders: PublicFolder[] }>('/api/public', { signal }),

  // No session required: the login page carries the mark as well.
  branding: () => request<Branding>('/api/branding'),

  /**
   * Changes this person's own password.
   *
   * The current one is required by the server even though a session exists; see
   * internal/server/password.go. Answers with how many other sessions were
   * closed, which is worth showing.
   */
  changePassword: (current: string, next: string) =>
    request<{ sessions_closed: number }>('/api/password', {
      method: 'POST',
      body: { current, new: next },
    }),

  login: (user: string, password: string) =>
    request<Me>('/api/login', { method: 'POST', body: { user, password } }),

  logout: () => request<void>('/api/logout', { method: 'POST' }),

  list: (path: string, signal?: AbortSignal) =>
    request<Listing>(filesUrl(path), { signal }),

  upload: (path: string, file: File, signal?: AbortSignal) =>
    request<void>(filesUrl(path), { method: 'PUT', body: file, signal }),

  remove: (path: string) => request<void>(filesUrl(path), { method: 'DELETE' }),

  mkdir: (path: string) => request<void>(dirsUrl(path), { method: 'POST' }),

  /**
   * Walks below `path` and yields the names that match, as they are found.
   *
   * Not request(): this response is NDJSON and arrives over seconds, because
   * the walk is the slow part and streaming is what hides it. Abort the signal
   * to stop the walk -- the server drops it when the connection goes, so a
   * superseded search stops costing anything almost immediately.
   */
  search: async function* (
    path: string,
    query: string,
    signal: AbortSignal,
  ): AsyncGenerator<SearchHit | SearchDone> {
    const url =
      '/api/search/' +
      path.split('/').map(encodeURIComponent).join('/') +
      '?q=' +
      encodeURIComponent(query)
    const res = await fetch(url, { signal })
    if (!res.ok || !res.body) {
      let detail = `요청이 실패했습니다 (${res.status})`
      try {
        const body = (await res.json()) as { error?: string }
        if (body.error) detail = body.error
      } catch {
        // Not JSON; the status is all there is.
      }
      throw new ApiError(res.status, explain(res.status, detail))
    }

    const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
    let buffer = ''
    try {
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += value
        // A chunk boundary lands wherever the network put it, which is regularly
        // in the middle of a line. Everything up to the last newline is whole;
        // the remainder waits for more bytes.
        let nl: number
        while ((nl = buffer.indexOf('\n')) >= 0) {
          const line = buffer.slice(0, nl)
          buffer = buffer.slice(nl + 1)
          if (line) yield JSON.parse(line) as SearchHit | SearchDone
        }
      }
    } finally {
      // On an abort or an early return, stop the body so the server sees the
      // connection go and abandons its walk.
      void reader.cancel().catch(() => {})
    }
  },

  shares: () => request<{ links: ShareLink[] }>('/api/shares'),

  createShare: (path: string, password: string, ttlHours: number) =>
    request<ShareLink>('/api/shares', {
      method: 'POST',
      body: { path, password, ttl_hours: ttlHours },
    }),

  revokeShare: (token: string) =>
    request<void>('/api/shares/' + encodeURIComponent(token), { method: 'DELETE' }),

  // --- operator surface ---
  //
  // Everything but adminWhoami answers 404 to a non-admin, deliberately: the
  // server does not confirm that an operator API exists at this path. So a
  // failure here is not something to explain away in the interface -- it means
  // the caller should not have asked.
  adminWhoami: () => request<AdminWhoami>('/api/admin/whoami'),

  /**
   * Changes a file's permission bits.
   *
   * Octal as a STRING: a JSON number would arrive as 640 decimal, which is 1200
   * octal — a mode nobody asked for, with a setuid bit in it.
   */
  chmod: (path: string, mode: string) =>
    request<void>(modeUrl(path), { method: 'POST', body: { mode } }),

  /**
   * What the permission dialog needs to warn accurately: the current mode, and
   * whether the file carries a POSIX ACL that narrowing the mode could hide.
   * `acl` is not derivable from the listing — it is not in the mode bits.
   */
  modeInfo: (path: string) =>
    request<{ mode: string; dir: boolean; acl: boolean }>(modeUrl(path)),

  adminUsers: (signal?: AbortSignal) => request<Inventory>('/api/admin/users', { signal }),

  adminDisk: (signal?: AbortSignal) => request<DiskReport>('/api/admin/disk', { signal }),

  adminAudit: (signal?: AbortSignal) => request<DriftReport>('/api/admin/audit', { signal }),

  adminActivity: (params: { user?: string; path?: string; action?: string; days?: number }, signal?: AbortSignal) => {
    const q = new URLSearchParams()
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== '') q.set(k, String(v))
    }
    const qs = q.toString()
    return request<ActivityReport>('/api/admin/activity' + (qs ? `?${qs}` : ''), { signal })
  },

  adminSetSmb: (user: string, enabled: boolean) =>
    request<void>(`/api/admin/users/${encodeURIComponent(user)}/smb`, {
      method: 'POST',
      body: { enabled },
    }),

  /**
   * The seed-derived INITIAL password for a user.
   *
   * `still_initial: false` and no value means they have changed it — usersync
   * would still print the derived one, and it would be wrong. There is no way
   * to read a current password: tdbsam holds an NT hash.
   */
  adminInitialPassword: (user: string) =>
    request<{ still_initial: boolean; password?: string }>(
      `/api/admin/users/${encodeURIComponent(user)}/initial-password`,
    ),

  adminSetPassword: (user: string, password: string) =>
    request<void>(`/api/admin/users/${encodeURIComponent(user)}/password`, {
      method: 'POST',
      body: { password },
    }),

  // --- single sign-on ---
  //
  // Starting a sign-in is a NAVIGATION, not a fetch: the browser has to follow
  // the redirect to the provider and come back with cookies of its own, which
  // an XHR cannot do. So there is no method for it here — the login page sets
  // location.href — and what is left is reading the message the callback left
  // behind.
  ssoNotice: (id: string) =>
    request<SSONotice>(`/api/sso/notice?id=${encodeURIComponent(id)}`),

  // Where an onboarding stands, for the login page to poll. The id came in the
  // notice; no session.
  ssoEnrollment: (id: string, signal?: AbortSignal) =>
    request<EnrollmentProgress>(`/api/sso/enrollment?id=${encodeURIComponent(id)}`, { signal }),

  adminIdentities: (signal?: AbortSignal) =>
    request<IdentityView>('/api/admin/identities', { signal }),

  adminProvisioning: (signal?: AbortSignal) =>
    request<ProvisionView>('/api/admin/provisioning', { signal }),

  adminIdentityJournal: (signal?: AbortSignal) =>
    request<{ entries: IdentityJournalEntry[] }>('/api/admin/identities/journal', { signal }),

  approveIdentity: (account: string, issuer: string, subject: string) =>
    request<IdentityMapping>('/api/admin/identities', {
      method: 'POST',
      body: { account, issuer, subject },
    }),

  discardIdentityRequest: (issuer: string, subject: string) => {
    const q = new URLSearchParams({ issuer, subject })
    return request<void>(`/api/admin/identities/pending?${q}`, { method: 'DELETE' })
  },

  forgetIdentity: (account: string) =>
    request<void>(`/api/admin/identities/${encodeURIComponent(account)}`, { method: 'DELETE' }),

  // --- team ownership ---
  //
  // A separate axis from the admin group: an owner may change their own team's
  // membership and nothing else. Every signed-in user may ask what they own;
  // the answer for most is an empty list.
  teamWhoami: () => request<TeamWhoami>('/api/teams/whoami'),

  teams: (signal?: AbortSignal) => request<TeamsView>('/api/teams', { signal }),

  setTeamMember: (team: string, user: string, member: boolean) =>
    request<void>(`/api/teams/${encodeURIComponent(team)}/members`, {
      method: 'POST',
      body: { user, member },
    }),
}
