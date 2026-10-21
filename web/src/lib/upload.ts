import { api, uploadFile } from '../api'
import { ApiError } from '../types'
import type { UploadPlan } from './uploadPlan'

// Sending a plan built by ./uploadPlan. Kept apart from it so the planning,
// which is pure, can be tested under Node without a browser or a server.

export interface UploadProgress {
  /** Bytes sent across every file so far. */
  sent: number
  total: number
  filesDone: number
  filesTotal: number
  /** What is being sent right now, for the status line. */
  current: string
}

export interface UploadFailure {
  rel: string
  message: string
}

// Three at a time. The helper multiplexes one user's requests, so a slow large
// file does not hold up the small ones behind it; more than this mostly adds
// contention on the same disk and the same uplink.
const CONCURRENCY = 3

/**
 * Sends a plan into `base`. Directories first, in order; then files, a few at a
 * time. A directory that already exists is fine (that is a merge); any other
 * failure is recorded against what it was for, and the rest carries on — one
 * unreadable file should not cost the other two thousand.
 *
 * Aborting the signal stops everything in flight and starts nothing new.
 */
export async function runUpload(
  base: string,
  plan: UploadPlan,
  onProgress: (p: UploadProgress) => void,
  signal: AbortSignal,
): Promise<UploadFailure[]> {
  const failures: UploadFailure[] = []
  const failedDirs = new Set<string>()
  const total = plan.files.reduce((n, f) => n + f.file.size, 0)
  const sentBy = new Map<string, number>()
  let filesDone = 0
  let current = ''
  const report = () => {
    let sent = 0
    for (const v of sentBy.values()) sent += v
    onProgress({ sent, total, filesDone, filesTotal: plan.files.length, current })
  }

  for (const dir of plan.dirs) {
    if (signal.aborted) return failures
    current = dir + '/'
    report()
    try {
      await api.mkdir(`${base}/${dir}`)
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) continue
      failedDirs.add(dir)
      failures.push({ rel: dir + '/', message: messageOf(e) })
    }
  }

  // A file under a folder that could not be made would only fail again, once
  // per file; it is reported under the folder instead.
  const blocked = (rel: string) => {
    const parts = rel.split('/')
    for (let i = 1; i < parts.length; i++) if (failedDirs.has(parts.slice(0, i).join('/'))) return true
    return false
  }
  const queue = plan.files.filter((f) => {
    if (!blocked(f.rel)) return true
    filesDone++
    return false
  })

  async function worker() {
    for (;;) {
      const item = queue.shift()
      if (!item || signal.aborted) return
      current = item.rel
      report()
      try {
        await uploadFile(
          `${base}/${item.rel}`,
          item.file,
          (n) => {
            sentBy.set(item.rel, n)
            report()
          },
          signal,
        )
      } catch (e) {
        if (signal.aborted) return
        sentBy.set(item.rel, item.file.size) // so the bar still reaches the end
        failures.push({ rel: item.rel, message: messageOf(e) })
      }
      filesDone++
      report()
    }
  }

  await Promise.all(Array.from({ length: Math.min(CONCURRENCY, queue.length) }, worker))
  return failures
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : '올리지 못했습니다.'
}
