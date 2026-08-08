import {
  ApiError,
  type AdminWhoami,
  type DiskReport,
  type DriftReport,
  type Inventory,
  type Listing,
  type Me,
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

  login: (user: string, password: string) =>
    request<Me>('/api/login', { method: 'POST', body: { user, password } }),

  logout: () => request<void>('/api/logout', { method: 'POST' }),

  list: (path: string, signal?: AbortSignal) =>
    request<Listing>(filesUrl(path), { signal }),

  upload: (path: string, file: File, signal?: AbortSignal) =>
    request<void>(filesUrl(path), { method: 'PUT', body: file, signal }),

  remove: (path: string) => request<void>(filesUrl(path), { method: 'DELETE' }),

  mkdir: (path: string) => request<void>(dirsUrl(path), { method: 'POST' }),

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

  adminUsers: (signal?: AbortSignal) => request<Inventory>('/api/admin/users', { signal }),

  adminDisk: (signal?: AbortSignal) => request<DiskReport>('/api/admin/disk', { signal }),

  adminAudit: (signal?: AbortSignal) => request<DriftReport>('/api/admin/audit', { signal }),

  adminSetSmb: (user: string, enabled: boolean) =>
    request<void>(`/api/admin/users/${encodeURIComponent(user)}/smb`, {
      method: 'POST',
      body: { enabled },
    }),

  adminSetPassword: (user: string, password: string) =>
    request<void>(`/api/admin/users/${encodeURIComponent(user)}/password`, {
      method: 'POST',
      body: { password },
    }),

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
