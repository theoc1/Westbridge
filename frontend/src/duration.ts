/** Formats an elapsed duration as m:ss, or h:mm:ss past the hour. */
export function formatDuration(joinedAt: string | undefined, now: number): string {
  if (joinedAt === undefined) {
    return '—'
  }
  const started = Date.parse(joinedAt)
  if (Number.isNaN(started)) {
    return '—'
  }
  const total = Math.max(0, Math.floor((now - started) / 1000))
  const seconds = total % 60
  const minutes = Math.floor(total / 60) % 60
  const hours = Math.floor(total / 3600)
  const pad = (n: number) => String(n).padStart(2, '0')
  return hours > 0
    ? `${hours}:${pad(minutes)}:${pad(seconds)}`
    : `${minutes}:${pad(seconds)}`
}

