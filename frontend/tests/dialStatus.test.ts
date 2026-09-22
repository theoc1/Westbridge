import assert from 'node:assert/strict'
import test from 'node:test'
import { dialHasJoined } from '../src/dialStatus.ts'
import type { Participant } from '../src/types.ts'

const bob: Participant = { uniqueid: 'new-call', callerIdNum: '1002', callerIdName: 'Bob', channel: 'PJSIP/1002', admin: false, muted: false }

test('a matching join completes the pending dial, including formatted numbers', () => {
  const attempt = { number: '1 (002)', existingIds: [] }
  assert.equal(dialHasJoined(attempt, []), false)
  assert.equal(dialHasJoined(attempt, [{ ...bob, callerIdNum: '1001' }]), false)
  assert.equal(dialHasJoined(attempt, [bob]), true)
})

test('an existing call from the same number does not complete a new attempt', () => {
  const attempt = { number: '1002', existingIds: ['old-call'] }
  assert.equal(dialHasJoined(attempt, [{ ...bob, uniqueid: 'old-call' }]), false)
  assert.equal(dialHasJoined(attempt, [{ ...bob, uniqueid: 'old-call' }, bob]), true)
})
