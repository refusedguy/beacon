// Time and duration formatting shared by the sidebar and card views. Both
// views render the same values, so the rules live here once.
import { str } from "./ui_model.js"

// Coerce a wire value or ISO timestamp into milliseconds. Numbers and numeric
// strings pass through, ISO strings are parsed, and unusable values become 0.
export function timeOf(v) {
  if (v == null || v === "") return 0
  if (typeof v === "number") return Number.isFinite(v) ? v : 0
  const n = Number(v)
  if (Number.isFinite(n) && n > 0) return n
  const t = Date.parse(str(v))
  return Number.isFinite(t) ? t : 0
}

// Render a duration in the smallest readable unit.
export function elapsed(ms) {
  if (!Number.isFinite(ms) || ms < 0) return ""
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  return h < 24 ? `${h}h` : `${Math.floor(h / 24)}d`
}

// Take the HH:MM:SS part of a timestamp. Short values stay unchanged.
export function clockTime(value) {
  const t = str(value)
  return t.length >= 19 ? t.slice(11, 19) : t
}
