/**
 * Pure-function `.env` file parser.
 *
 * Parses a raw `.env` text into valid entries and skipped lines.
 *
 * Rules:
 * - Lines starting with `#` (after trimming leading whitespace) are comments → skipped
 * - Blank lines (empty or only whitespace) → skipped
 * - Lines without `=` → `invalid / no_equals`
 * - Lines starting with `=` → `invalid / empty_key`
 * - KEY must match `^[A-Z_][A-Z0-9_]*$` → otherwise `invalid / invalid_key_format`
 * - Value: matching outer quotes (single or double) are stripped
 * - `#` inside values is preserved (not treated as a comment)
 * - Duplicate keys: last occurrence wins in `effective`; earlier occurrences marked `duplicate: true`
 */

export interface DotenvEntry {
  /** 1-based line index in the original text. */
  index: number
  /** Parsed key. */
  key: string
  /** Parsed value (outer quotes stripped if present). */
  value: string
  /** True when a later line has the same key. */
  duplicate: boolean
}

export interface DotenvSkipped {
  /** 1-based line index in the original text. */
  index: number
  /** Original raw line (trimmed). */
  rawLine: string
  /** Reason this line was skipped. */
  reason: 'comment' | 'blank' | 'no_equals' | 'empty_key' | 'invalid_key_format'
}

export interface ParseResult {
  /** All parsed entries (including duplicates, for preview). */
  entries: DotenvEntry[]
  /** Skipped lines with reasons. */
  skipped: DotenvSkipped[]
  /** Key → value map, last-writer-wins. */
  effective: Map<string, string>
}

const KEY_RE = /^[A-Z_][A-Z0-9_]*$/

/** Strip matching outer single or double quotes. */
function stripQuotes(v: string): string {
  if (v.length >= 2) {
    if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) {
      return v.slice(1, -1)
    }
  }
  return v
}

export function parseDotenv(text: string): ParseResult {
  const lines = text.split(/\r?\n/)
  const entries: DotenvEntry[] = []
  const skipped: DotenvSkipped[] = []
  const keyIndex: Map<string, number> = new Map() // key → last entry index in entries[]

  // Pass 1: classify each line
  let i = 0
  for (const raw of lines) {
    const line = raw.trim()

    // Blank line
    if (line === '') {
      if (raw !== '') {
        // Only whitespace → blank
        skipped.push({ index: i + 1, rawLine: raw, reason: 'blank' })
      } else {
        skipped.push({ index: i + 1, rawLine: '', reason: 'blank' })
      }
      i++
      continue
    }

    // Comment
    if (line.startsWith('#')) {
      skipped.push({ index: i + 1, rawLine: raw, reason: 'comment' })
      i++
      continue
    }

    // Must contain '='
    const eqIdx = line.indexOf('=')
    if (eqIdx === -1) {
      skipped.push({ index: i + 1, rawLine: raw, reason: 'no_equals' })
      i++
      continue
    }

    const key = line.slice(0, eqIdx)
    if (key === '') {
      skipped.push({ index: i + 1, rawLine: raw, reason: 'empty_key' })
      i++
      continue
    }

    if (!KEY_RE.test(key)) {
      skipped.push({ index: i + 1, rawLine: raw, reason: 'invalid_key_format' })
      i++
      continue
    }

    const rawValue = line.slice(eqIdx + 1)
    const value = stripQuotes(rawValue)

    // Mark previous occurrence as duplicate
    const prevIdx = keyIndex.get(key)
    if (prevIdx !== undefined) {
      const prev = entries[prevIdx]
      if (prev) prev.duplicate = true
    }

    const entry: DotenvEntry = { index: i + 1, key, value, duplicate: false }
    entries.push(entry)
    keyIndex.set(key, entries.length - 1)
    i++
  }

  // Pass 2: build effective map (only the last occurrence)
  const effective = new Map<string, string>()
  for (const e of entries) {
    if (!e.duplicate) {
      effective.set(e.key, e.value)
    }
  }

  return { entries, skipped, effective }
}
