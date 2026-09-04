import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from 'react'

/**
 * In-app replacements for window.confirm and window.prompt.
 *
 * The native ones cannot be styled, read as a browser chrome rather than part
 * of this interface, and on some setups are suppressed entirely — a suppressed
 * confirm() returns false, which silently turns "delete this" into "do nothing"
 * with no way to tell. These render as the same <dialog> the rest of the app
 * uses, and resolve a promise, so a call site reads almost exactly as the
 * native call it replaces:
 *
 *   if (!(await dialogs.confirm({ title: '지울까요?' }))) return
 *   const name = await dialogs.prompt({ title: '새 이름', initial: entry.name })
 *
 * One dialog at a time is all the app needs; a second call while one is open
 * replaces it (the earlier promise resolves to the cancel value first, so no
 * caller is left waiting forever).
 */
export interface ConfirmOptions {
  title: string
  message?: string
  confirmLabel?: string
  cancelLabel?: string
  /** Draw the confirm button as a destructive action. */
  danger?: boolean
}

export interface PromptOptions {
  title: string
  message?: string
  /** Pre-filled value, e.g. the current name for a rename. */
  initial?: string
  placeholder?: string
  confirmLabel?: string
}

interface Dialogs {
  confirm(opts: ConfirmOptions): Promise<boolean>
  prompt(opts: PromptOptions): Promise<string | null>
}

const DialogsContext = createContext<Dialogs | null>(null)

/** useDialogs returns the app's confirm/prompt. Must be under DialogsProvider. */
export function useDialogs(): Dialogs {
  const ctx = useContext(DialogsContext)
  if (!ctx) throw new Error('useDialogs used outside DialogsProvider')
  return ctx
}

type Request =
  | { kind: 'confirm'; opts: ConfirmOptions; resolve: (v: boolean) => void }
  | { kind: 'prompt'; opts: PromptOptions; resolve: (v: string | null) => void }

export function DialogsProvider({ children }: { children: ReactNode }) {
  const [request, setRequest] = useState<Request | null>(null)
  // The live request, so opening a second dialog can settle the first at its
  // cancel value rather than abandoning whoever awaited it.
  const pending = useRef<Request | null>(null)
  pending.current = request

  const settle = useCallback((value: boolean | string | null) => {
    const req = pending.current
    if (!req) return
    if (req.kind === 'confirm') req.resolve(value as boolean)
    else req.resolve(value as string | null)
    pending.current = null
    setRequest(null)
  }, [])

  const cancelValue = useCallback((req: Request | null) => (req?.kind === 'prompt' ? null : false), [])

  const confirm = useCallback(
    (opts: ConfirmOptions) =>
      new Promise<boolean>((resolve) => {
        if (pending.current) pending.current.resolve(cancelValue(pending.current) as never)
        const req: Request = { kind: 'confirm', opts, resolve }
        pending.current = req
        setRequest(req)
      }),
    [cancelValue],
  )

  const prompt = useCallback(
    (opts: PromptOptions) =>
      new Promise<string | null>((resolve) => {
        if (pending.current) pending.current.resolve(cancelValue(pending.current) as never)
        const req: Request = { kind: 'prompt', opts, resolve }
        pending.current = req
        setRequest(req)
      }),
    [cancelValue],
  )

  return (
    <DialogsContext.Provider value={{ confirm, prompt }}>
      {children}
      {request && (
        <DialogView
          request={request}
          onConfirm={(v) => settle(v)}
          onCancel={() => settle(cancelValue(request))}
        />
      )}
    </DialogsContext.Provider>
  )
}

function DialogView({
  request,
  onConfirm,
  onCancel,
}: {
  request: Request
  onConfirm: (value: boolean | string) => void
  onCancel: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const [value, setValue] = useState(request.kind === 'prompt' ? (request.opts.initial ?? '') : '')

  useEffect(() => {
    ref.current?.showModal()
    // A prompt selects its pre-filled text so a full replacement is one gesture
    // and a small edit is still easy; a confirm focuses its default button.
    if (request.kind === 'prompt') inputRef.current?.select()
  }, [request])

  function submit(e: FormEvent) {
    e.preventDefault()
    if (request.kind === 'prompt') onConfirm(value)
    else onConfirm(true)
  }

  const danger = request.kind === 'confirm' && request.opts.danger
  const confirmLabel =
    request.opts.confirmLabel ?? (request.kind === 'prompt' ? '확인' : danger ? '지우기' : '확인')
  const cancelLabel = (request.kind === 'confirm' && request.opts.cancelLabel) || '취소'

  return (
    <dialog
      ref={ref}
      className="app-dialog"
      onClose={onCancel}
      onCancel={(e) => {
        e.preventDefault()
        onCancel()
      }}
    >
      <form onSubmit={submit}>
        <h3>{request.opts.title}</h3>
        {request.opts.message && <p className="muted small dialog-message">{request.opts.message}</p>}
        {request.kind === 'prompt' && (
          <input
            ref={inputRef}
            type="text"
            autoFocus
            value={value}
            placeholder={request.opts.placeholder}
            onChange={(e) => setValue(e.target.value)}
          />
        )}
        <div className="dialog-actions">
          <button type="button" className="ghost" onClick={onCancel}>
            {cancelLabel}
          </button>
          <button
            type="submit"
            className={danger ? 'danger' : ''}
            // A prompt with nothing typed has nothing to confirm.
            disabled={request.kind === 'prompt' && value.trim() === ''}
            autoFocus={request.kind === 'confirm'}
          >
            {confirmLabel}
          </button>
        </div>
      </form>
    </dialog>
  )
}
