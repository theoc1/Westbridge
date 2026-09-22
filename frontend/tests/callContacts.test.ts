import assert from 'node:assert/strict'
import test from 'node:test'
import { callContacts } from '../src/callContacts.ts'

test('group dial starts concurrent requests and preserves only rejected selections', async () => {
  const contacts = Array.from({ length: 7 }, (_, id) => ({ id, number: String(1000 + id) }))
  const selected = new Set(contacts.map(c => c.id))
  const errors = new Map<number, string>()
  const dialled: string[] = []
  let active = 0
  let peak = 0
  await callContacts(contacts, async number => {
    dialled.push(number)
    active++; peak = Math.max(peak, active)
    await new Promise(resolve => setTimeout(resolve, 5))
    active--
    if (number === '1002') throw new Error('Asterisk unavailable')
  }, id => selected.delete(id), (id, message) => errors.set(id, message))
  assert.equal(peak, 4)
  assert.deepEqual(dialled, contacts.map(c => c.number))
  assert.deepEqual([...selected], [2])
  assert.equal(errors.get(2), 'Asterisk unavailable')
  const retry: string[] = []
  await callContacts(contacts.filter(c => selected.has(c.id)), async number => { retry.push(number) }, id => selected.delete(id), () => assert.fail())
  assert.deepEqual(retry, ['1002'])
  assert.equal(selected.size, 0)
})
