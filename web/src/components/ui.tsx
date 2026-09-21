import { type ButtonHTMLAttributes, type InputHTMLAttributes, type SelectHTMLAttributes, type ReactNode, forwardRef } from 'react'
import { Loader2 } from 'lucide-react'
import { cx } from '@/lib/utils'

/* ---------- Button ---------- */
type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'outline'
type ButtonSize = 'sm' | 'md' | 'icon'

const buttonVariants: Record<ButtonVariant, string> = {
  primary:
    'bg-primary text-primary-foreground hover:brightness-110 shadow-sm shadow-primary/20',
  secondary: 'bg-card hover:bg-card-hover text-foreground border border-border',
  ghost: 'bg-transparent hover:bg-card text-foreground',
  danger: 'bg-danger/10 text-danger border border-danger/30 hover:bg-danger/20',
  outline: 'bg-transparent border border-border text-foreground hover:bg-card',
}

const buttonSizes: Record<ButtonSize, string> = {
  sm: 'h-8 px-3 text-xs gap-1.5',
  md: 'h-9 px-4 text-sm gap-2',
  icon: 'h-8 w-8',
}

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  size?: ButtonSize
  loading?: boolean
}

export function Button({
  variant = 'primary',
  size = 'md',
  loading,
  className,
  children,
  disabled,
  ...props
}: ButtonProps) {
  return (
    <button
      className={cx(
        'inline-flex items-center justify-center rounded-lg font-medium transition-colors',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/60',
        'disabled:opacity-50 disabled:pointer-events-none cursor-pointer',
        buttonVariants[variant],
        buttonSizes[size],
        className,
      )}
      disabled={disabled || loading}
      {...props}
    >
      {loading && <Loader2 className="h-4 w-4 animate-spin" />}
      {children}
    </button>
  )
}

/* ---------- Card ---------- */
export function Card({
  className,
  children,
  onClick,
}: {
  className?: string
  children: ReactNode
  onClick?: () => void
}) {
  return (
    <div
      className={cx('rounded-xl border border-border bg-card shadow-card', className)}
      onClick={onClick}
    >
      {children}
    </div>
  )
}

export function CardHeader({
  title,
  subtitle,
  action,
}: {
  title: ReactNode
  subtitle?: ReactNode
  action?: ReactNode
}) {
  return (
    <div className="flex items-start justify-between gap-4 px-5 py-4 border-b border-border">
      <div className="min-w-0">
        <h3 className="text-sm font-semibold text-foreground">{title}</h3>
        {subtitle && <p className="mt-0.5 text-xs text-muted-foreground">{subtitle}</p>}
      </div>
      {action}
    </div>
  )
}

/* ---------- Badge ---------- */
type BadgeVariant = 'default' | 'success' | 'warning' | 'danger' | 'primary' | 'muted'

const badgeVariants: Record<BadgeVariant, string> = {
  default: 'bg-card text-foreground border border-border',
  success: 'bg-success/10 text-success border border-success/30',
  warning: 'bg-warning/10 text-warning border border-warning/30',
  danger: 'bg-danger/10 text-danger border border-danger/30',
  primary: 'bg-primary/10 text-primary border border-primary/30',
  muted: 'bg-muted text-muted-foreground border border-border',
}

export function Badge({
  variant = 'default',
  className,
  title,
  children,
}: {
  variant?: BadgeVariant
  className?: string
  title?: string
  children: ReactNode
}) {
  return (
    <span
      title={title}
      className={cx(
        'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-medium leading-4',
        badgeVariants[variant],
        className,
      )}
    >
      {children}
    </span>
  )
}

export function statusVariant(running: boolean): BadgeVariant {
  return running ? 'success' : 'muted'
}

/* ---------- Inputs ---------- */
export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  ({ className, ...props }, ref) => (
    <input
      ref={ref}
      className={cx(
        'h-9 w-full rounded-lg border border-border bg-background px-3 text-sm text-foreground',
        'placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-primary/50',
        className,
      )}
      {...props}
    />
  ),
)
Input.displayName = 'Input'

export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement>>(
  ({ className, children, ...props }, ref) => (
    <select
      ref={ref}
      className={cx(
        'h-9 rounded-lg border border-border bg-background px-2.5 text-sm text-foreground',
        'focus:outline-none focus:ring-2 focus:ring-primary/50',
        className,
      )}
      {...props}
    >
      {children}
    </select>
  ),
)
Select.displayName = 'Select'

export function Label({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <label className={cx('block text-xs font-medium text-muted-foreground mb-1.5', className)}>
      {children}
    </label>
  )
}

/* ---------- Spinner ---------- */
export function Spinner({ className }: { className?: string }) {
  return <Loader2 className={cx('h-5 w-5 animate-spin text-muted-foreground', className)} />
}

export function Loading({ label = 'Loading…' }: { label?: string }) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 py-16 text-muted-foreground">
      <Spinner className="h-6 w-6" />
      <span className="text-sm">{label}</span>
    </div>
  )
}

/* ---------- Empty state ---------- */
export function EmptyState({
  title,
  hint,
  action,
}: {
  title: string
  hint?: string
  action?: ReactNode
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-xl border border-dashed border-border py-16 text-center">
      <p className="text-sm font-medium text-foreground">{title}</p>
      {hint && <p className="max-w-md text-xs text-muted-foreground">{hint}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  )
}

/* ---------- Toggle ---------- */
export function Toggle({
  checked,
  onChange,
  disabled,
}: {
  checked: boolean
  onChange: (v: boolean) => void
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      // The track stays small, but the button itself is padded out to a
      // comfortable touch target; the negative margin keeps the layout
      // footprint identical to the unpadded switch.
      className="-m-2 shrink-0 cursor-pointer p-2 disabled:opacity-50"
    >
      <span
        className={cx(
          'relative block h-5 w-9 rounded-full transition-colors',
          checked ? 'bg-primary' : 'bg-muted',
        )}
      >
        <span
          className={cx(
            'absolute left-0 top-0.5 h-4 w-4 rounded-full bg-white transition-transform',
            checked ? 'translate-x-[18px]' : 'translate-x-0.5',
          )}
        />
      </span>
    </button>
  )
}

/* ---------- Modal ---------- */
export function Modal({
  open,
  onClose,
  title,
  children,
  wide,
}: {
  open: boolean
  onClose: () => void
  title: ReactNode
  children: ReactNode
  wide?: boolean
}) {
  if (!open) return null
  return (
    <div
      className="fixed inset-0 z-50 flex items-end justify-center sm:items-start sm:p-8"
      role="dialog"
      aria-modal="true"
    >
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      <div
        className={cx(
          // Phones get a bottom sheet that never exceeds the visible viewport;
          // larger screens get a centred card. `dvh` tracks the mobile URL bar.
          'relative z-10 flex max-h-[92dvh] w-full flex-col overflow-hidden rounded-t-2xl border border-border bg-card shadow-2xl',
          'sm:max-h-[85dvh] sm:rounded-xl',
          wide ? 'sm:max-w-4xl' : 'sm:max-w-lg',
        )}
      >
        <div className="flex shrink-0 items-center justify-between gap-3 border-b border-border px-4 py-3.5 sm:px-5">
          <h2 className="min-w-0 truncate text-sm font-semibold text-foreground">{title}</h2>
          <button
            onClick={onClose}
            className="shrink-0 rounded-md p-2 text-muted-foreground hover:bg-card-hover hover:text-foreground sm:p-1"
            aria-label="Close"
          >
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M18 6 6 18M6 6l12 12" />
            </svg>
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4 sm:px-5">{children}</div>
      </div>
    </div>
  )
}

/* ---------- Confirm dialog ---------- */
export function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  message,
  confirmLabel = 'Confirm',
  danger,
  loading,
}: {
  open: boolean
  onClose: () => void
  onConfirm: () => void
  title: string
  message: ReactNode
  confirmLabel?: string
  danger?: boolean
  loading?: boolean
}) {
  if (!open) return null
  return (
    <div className="fixed inset-0 z-[60] flex items-center justify-center p-4" role="alertdialog">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      <div className="relative z-10 w-full max-w-md rounded-xl border border-border bg-card p-5 shadow-2xl">
        <h2 className="text-sm font-semibold text-foreground">{title}</h2>
        <div className="mt-2 text-sm text-muted-foreground">{message}</div>
        <div className="mt-5 flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={loading}>
            Cancel
          </Button>
          <Button variant={danger ? 'danger' : 'primary'} onClick={onConfirm} loading={loading}>
            {confirmLabel}
          </Button>
        </div>
      </div>
    </div>
  )
}
