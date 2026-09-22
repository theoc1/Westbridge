import { formatDuration } from '../duration.ts'
import { useEffect, useState } from 'react'
import { kickParticipant } from '../api.ts'
import type { Participant } from '../types.ts'
import { useAsyncAction } from '../useConference.ts'

/** Ticks the "time in conference" column without re-rendering on every frame. */
function useNow(intervalMs = 1_000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => {
      setNow(Date.now())
    }, intervalMs)
    return () => {
      window.clearInterval(id)
    }
  }, [intervalMs])
  return now
}

function displayName(p: Participant): string {
  // Asterisk fills unknown caller IDs with these placeholders; showing them
  // verbatim is worse than showing nothing.
  const name = p.callerIdName.trim()
  if (name === '' || name === '<unknown>' || name === 'unknown') {
    return 'Unknown participant'
  }
  return name
}

interface ParticipantListProps {
  participants: Participant[]
  /** True when the roster may be out of date, i.e. the AMI link is down. */
  stale: boolean
}

export function ParticipantList({ participants, stale }: ParticipantListProps) {
  const now = useNow()

  if (participants.length === 0) {
    return (
      <p className="empty-state">
        Nobody is in the conference yet. Dial in, or call a number above.
      </p>
    )
  }

  return (
    <ul className={`roster${stale ? ' roster-stale' : ''}`}>
      {participants.map((participant) => (
        <ParticipantRow
          key={participant.uniqueid}
          participant={participant}
          disabled={stale}
          duration={formatDuration(participant.joinedAt, now)}
        />
      ))}
    </ul>
  )
}

interface ParticipantRowProps {
  participant: Participant
  disabled: boolean
  duration: string
}

function ParticipantRow({ participant, disabled, duration }: ParticipantRowProps) {
  // Kicking drops a live call, so it takes a second click. The confirmation is
  // inline rather than a window.confirm() so it cannot block the render loop
  // or get suppressed by the browser.
  const [confirming, setConfirming] = useState(false)
  const { pending, error, run } = useAsyncAction()

  const onKick = async () => {
    const ok = await run(() => kickParticipant(participant.uniqueid))
    if (!ok) {
      // Leave the error visible and step back to the idle state; the row
      // usually disappears on the next snapshot when the kick did succeed.
      setConfirming(false)
    }
  }

  return (
    <li className="roster-row participant-row">
      <div className="roster-identity">
        <div className="participant-heading">
          <span className="roster-name">{displayName(participant)}</span>
          <span className="roster-number">{participant.callerIdNum || '—'}</span>
        </div>
        {error !== null && (
          <span className="roster-error" role="alert">
            {error}
          </span>
        )}
      </div>

      <div className="roster-meta">
        {participant.admin && <span className="tag">admin</span>}
        {participant.muted && <span className="tag">muted</span>}
        <span className="roster-duration" title="Time in conference">
          {duration}
        </span>
      </div>

      <div className="roster-actions">
        {confirming ? (
          <>
            <button
              type="button"
              className="button button-danger"
              disabled={pending || disabled}
              onClick={onKick}
            >
              {pending ? 'Kicking…' : 'Confirm'}
            </button>
            <button
              type="button"
              className="button button-quiet"
              disabled={pending}
              onClick={() => {
                setConfirming(false)
              }}
            >
              Cancel
            </button>
          </>
        ) : (
          <button
            type="button"
            className="button button-danger"
            disabled={pending || disabled}
            onClick={() => {
              setConfirming(true)
            }}
          >
            Kick
          </button>
        )}
      </div>
    </li>
  )
}
