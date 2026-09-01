import { useState } from 'react'
import type { FormEvent } from 'react'
import { addParticipant } from '../api.ts'
import { useAsyncAction } from '../useConference.ts'

/**
 * Digits with at most a leading "+", after formatting noise is stripped. This
 * mirrors conference.NormalizeNumber on the backend so a typo is caught before
 * a call is placed — the backend still validates, this is only for the message.
 */
const FORMATTING = /[\s\-().]/g
const VALID_NUMBER = /^\+?\d{1,23}$/

function validate(raw: string): string | null {
  const trimmed = raw.trim()
  if (trimmed === '') {
    return 'Enter a number to dial'
  }
  if (!VALID_NUMBER.test(trimmed.replace(FORMATTING, ''))) {
    return 'A number is digits, optionally starting with "+"'
  }
  return null
}

interface AddParticipantFormProps {
  /** Dialling is pointless with no AMI link; the form says so instead of
   * letting the request fail with a 503. */
  disabled: boolean
}

export function AddParticipantForm({ disabled }: AddParticipantFormProps) {
  const [number, setNumber] = useState('')
  const [localError, setLocalError] = useState<string | null>(null)
  const [dialing, setDialing] = useState<string | null>(null)
  const { pending, error, clearError, run } = useAsyncAction()

  const onSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const invalid = validate(number)
    setLocalError(invalid)
    if (invalid !== null) {
      return
    }
    clearError()
    const dialed = number.trim()
    const ok = await run(() => addParticipant(dialed))
    if (ok) {
      // 202 only means Asterisk took the call. Say that rather than implying
      // the callee is already in the room; they show up in the roster if they
      // answer.
      setDialing(dialed)
      setNumber('')
    }
  }

  const message = localError ?? error

  return (
    <form className="add-form" onSubmit={onSubmit} noValidate>
      <label className="add-form-label" htmlFor="number">
        Add a participant
      </label>
      <div className="add-form-row">
        <input
          id="number"
          className="add-form-input"
          type="tel"
          inputMode="tel"
          autoComplete="off"
          placeholder="1002"
          value={number}
          disabled={disabled || pending}
          onChange={(event) => {
            setNumber(event.target.value)
            setLocalError(null)
            setDialing(null)
          }}
        />
        <button type="submit" className="button" disabled={disabled || pending}>
          {pending ? 'Calling…' : 'Call'}
        </button>
      </div>
      {message !== null && (
        <p className="add-form-error" role="alert">
          {message}
        </p>
      )}
      {message === null && dialing !== null && (
        <p className="add-form-note" role="status">
          Calling {dialing}. They join the conference once they answer.
        </p>
      )}
      {disabled && (
        <p className="add-form-note">
          Dialling is unavailable while Asterisk is unreachable.
        </p>
      )}
    </form>
  )
}
