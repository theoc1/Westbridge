import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  translate,
  translateMessage,
  participantCount,
} from '../src/translation.ts'

test('English fallback, Russian messages and interpolation preserve supplied names', () => {
  assert.equal(translate('en', 'Create room'), 'Create room')
  assert.equal(translate('ru', 'Create room'), 'Создать комнату')
  assert.equal(
    translate('ru', 'Edit {name}', { name: 'Save' }),
    'Изменить Save',
  )
  assert.equal(
    translateMessage('ru', 'invalid login or password'),
    'Неверный логин или пароль',
  )
  assert.equal(
    translateMessage(
      'ru',
      'invalid room or user assignment: number must be 100–9999',
    ),
    'Номер комнаты должен быть от 100 до 9999',
  )
  assert.equal(
    translateMessage('ru', 'unexpected provider diagnostic'),
    'unexpected provider diagnostic',
  )
})
test('Russian participant counts use one, few and many forms', () => {
  for (const [n, expected] of [
    [0, '0 участников'],
    [1, '1 участник'],
    [2, '2 участника'],
    [5, '5 участников'],
    [11, '11 участников'],
    [21, '21 участник'],
    [22, '22 участника'],
  ] as const)
    assert.equal(participantCount('ru', n), expected)
  assert.equal(participantCount('en', 1), '1 participant')
  assert.equal(participantCount('en', 2), '2 participants')
})
