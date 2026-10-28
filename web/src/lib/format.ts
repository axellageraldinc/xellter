/** The formatting helpers the panel uses everywhere. */

/** idr formats rupiah: whole rupiah for big amounts, no trailing zeros on the small ones. */
export function idr(n: number | null | undefined): string {
  if (n == null) return '–'
  if (n === 0) return '0'
  if (n >= 100) return Math.round(n).toLocaleString('en-US')
  const s = (n >= 1 ? n.toFixed(2) : n.toFixed(4)).replace(/0+$/, '')
  return s.endsWith('.') ? s.slice(0, -1) : s
}

/** num formats a count with thin separators. */
export function num(n: number | null | undefined): string {
  return (n ?? 0).toLocaleString('en-US')
}

/** human turns a number of seconds into a short phrase. */
export function human(s: number): string {
  if (s < 90) return Math.round(s) + ' s'
  if (s < 5400) return Math.round(s / 60) + ' min'
  if (s < 172800) return Math.round(s / 3600) + ' h'
  return Math.round(s / 86400) + ' days'
}

/** ago says how long ago a moment was. */
export function ago(t: string | undefined | null): string {
  if (!t || t.startsWith('0001')) return 'never'
  return human(Math.max(0, (Date.now() - new Date(t).getTime()) / 1000)) + ' ago'
}

/** left says how long is left until a moment. */
export function left(t: string | undefined | null): string {
  if (!t || t.startsWith('0001')) return ''
  const s = (new Date(t).getTime() - Date.now()) / 1000
  return s <= 0 ? '' : human(s)
}

/** rp formats a price the way the owner reads it: Rp100k, Rp1.2M, Rp500. */
export function rp(n: number | null | undefined): string {
  if (n == null) return '—'
  if (n === 0) return 'Rp0'
  if (n >= 1e6) return 'Rp' + (n / 1e6).toFixed(n % 1e6 ? 1 : 0) + 'M'
  if (n >= 1e3) return 'Rp' + Math.round(n / 1e3) + 'k'
  return 'Rp' + idr(n)
}

/** ctx formats a context window: 1.0M, 512k, 0. */
export function ctx(n: number): string {
  if (!n) return '—'
  if (n >= 1e6) return (n / 1e6).toFixed(1).replace(/\.0$/, '') + 'M'
  return Math.round(n / 1e3) + 'k'
}

/** pl makes a count read as a phrase: "1 key", "3 keys". */
export function pl(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

/** secs formats a wait given in milliseconds: 0.8 s, 12 s. */
export function secs(ms: number): string {
  if (!ms) return '—'
  const s = ms / 1000
  return (s < 10 ? s.toFixed(1) : Math.round(s).toString()) + ' s'
}

/** when says when a moment (unix milliseconds) was, relative to now. */
export function when(ms: number): string {
  const s = Math.max(0, (Date.now() - ms) / 1000)
  return human(s) + ' ago'
}
