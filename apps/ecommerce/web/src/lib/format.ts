// Currency formatting with a runtime-swappable symbol (store_settings).
// The admin Settings page pushes the configured symbol in via setCurrencySymbol.

let currencySymbol = "$"

export function setCurrencySymbol(symbol: string) {
  currencySymbol = symbol || "$"
}

export function getCurrencySymbol(): string {
  return currencySymbol
}

export function money(n: number | string | undefined): string {
  const v = Number(n ?? 0)
  return currencySymbol + v.toFixed(2)
}

/** Compact currency for KPI tiles: $12.4k */
export function moneyCompact(n: number): string {
  if (Math.abs(n) >= 1_000_000) return currencySymbol + (n / 1_000_000).toFixed(1) + "M"
  if (Math.abs(n) >= 1_000) return currencySymbol + (n / 1_000).toFixed(1) + "k"
  return currencySymbol + n.toFixed(2)
}

/** Product gallery column → string[] (tolerates both JSON-string and array
 * read-back shapes, and plain URLs). */
export function parseGallery(v: unknown): string[] {
  if (Array.isArray(v)) return v.filter((x): x is string => typeof x === "string")
  if (typeof v === "string" && v.startsWith("[")) {
    try {
      const arr = JSON.parse(v)
      if (Array.isArray(arr)) return arr.filter((x): x is string => typeof x === "string")
    } catch {
      /* not JSON — treat as a single URL */
    }
  }
  if (typeof v === "string" && v) return [v]
  return []
}

export function dayOf(ts: string | number | undefined): string {
  if (!ts) return ""
  const d = new Date(ts)
  if (isNaN(d.getTime())) return String(ts)
  return d.toISOString().slice(0, 10)
}

export function timeAgo(ts: string | number | undefined): string {
  if (!ts) return ""
  const then = new Date(ts).getTime()
  if (isNaN(then)) return String(ts)
  const s = Math.max(1, Math.floor((Date.now() - then) / 1000))
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

export function shortId(id: string | undefined, len = 8): string {
  return String(id ?? "").slice(0, len)
}
