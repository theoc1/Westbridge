import assert from 'node:assert/strict'
import test from 'node:test'
import { formatDuration } from '../src/duration.ts'

test('unknown conference join time renders as a dash', () => {
  assert.equal(formatDuration(undefined, Date.now()), '—')
})

test('observed join time renders elapsed time', () => {
  const joined = '2026-09-01T10:00:00Z'
  assert.equal(formatDuration(joined, Date.parse(joined) + 65000), '1:05')
  assert.equal(formatDuration(joined, Date.parse(joined) + 3661000), '1:01:01')
})
