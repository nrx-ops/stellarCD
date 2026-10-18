import { ReactNode } from 'react'

// Form building blocks shared by the Astral and Galaxy forms.

export const input =
  'block w-full rounded-md border px-3 py-2 text-sm placeholder:text-slate-400 focus:outline-none focus:ring-1 disabled:bg-slate-50 disabled:text-slate-500'

export function Field({ label, help, error, inherited, children, required }: {
  label: string
  help?: ReactNode
  error?: string
  inherited?: string
  children: ReactNode
  required?: boolean
}) {
  return (
    <label className="block">
      <span className="text-sm font-medium text-slate-800">{label}{required && <span className="text-red-500"> *</span>}</span>
      <div className="mt-1">{children}</div>
      {inherited && !error && <span className="mt-1 block text-xs text-slate-400">Inherited: {inherited}</span>}
      {help && !error && <span className="mt-1 block text-xs text-slate-500">{help}</span>}
      {error && <span className="mt-1 block text-xs text-red-600">{error}</span>}
    </label>
  )
}

export function cls(error?: string) {
  return `${input} ${error ? 'border-red-400 focus:border-red-500 focus:ring-red-500' : 'border-slate-300 focus:border-indigo-500 focus:ring-indigo-500'}`
}

export function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <fieldset className="grid grid-cols-1 gap-5 border-t border-slate-200 py-6 md:grid-cols-[12rem_1fr]">
      <legend className="sr-only">{title}</legend>
      <h2 className="text-sm font-semibold text-slate-900">{title}</h2>
      <div className="grid max-w-2xl grid-cols-1 gap-5 sm:grid-cols-2">{children}</div>
    </fieldset>
  )
}

export function SecretSelect({ value, onChange, secrets, inherits, error }: {
  value: string
  onChange: (v: string) => void
  secrets: string[]
  inherits?: string
  error?: string
}) {
  return (
    <select value={value} onChange={(e) => onChange(e.target.value)} className={cls(error)}>
      <option value="">{inherits ? `Inherit from the Galaxy (${inherits})` : 'None'}</option>
      {secrets.map((s) => <option key={s} value={s}>{s}</option>)}
      {value && !secrets.includes(value) && <option value={value}>{value} (missing)</option>}
    </select>
  )
}
