import { useRoomID } from '../roomContext.ts'
import { contactNumber } from '../phonebook.ts'
import { formatDuration } from '../duration.ts'
import { useEffect, useState } from 'react'
import { cancelCall, retryCall, kickParticipant, setParticipantMuted } from '../api.ts'
import type { Participant, OutgoingCall } from '../types.ts'
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
  contactNames: Map<string, string>
  participants: Participant[]
  calls: OutgoingCall[]
  socketConnected: boolean
  /** True when the roster may be out of date, i.e. the AMI link is down. */
  stale: boolean
}

export function ParticipantList({ participants, calls, stale, socketConnected, contactNames }: ParticipantListProps) {
  const now = useNow()

  if (participants.length === 0 && calls.length === 0) {
    return (
      <p className="empty-state">
        Nobody is in the conference yet. Dial in, or call a number above.
      </p>
    )
  }

  return (
    <ul className={`roster${stale ? ' roster-stale' : ''}`} aria-label="Calls and participants" tabIndex={0}>
      {calls.map(call => <CallRow key={call.id} name={contactNames.get(contactNumber(call.number))} call={call} stale={stale} socketConnected={socketConnected} duration={formatDuration(call.createdAt, now)} />)}
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
  const roomId = useRoomID()
  const { pending, error, run } = useAsyncAction()

  const onKick = async () => {
    const ok = await run(() => kickParticipant(roomId, participant.uniqueid))
    if (!ok) {
      // Leave the error visible and step back to the idle state; the row
      // usually disappears on the next snapshot when the kick did succeed.
      setConfirming(false)
    }
  }

  return (
    <li className="roster-row participant-row call-connected">
      <div className="roster-identity">
        <div className="participant-heading">
          <span className="roster-name" title={displayName(participant)}>{displayName(participant)}</span>
          <span className="roster-number" title={participant.callerIdNum}>{participant.callerIdNum || '—'}</span>
          <span className={`talking-indicator${participant.talking && !participant.muted && !disabled ? " is-talking" : ""}`} role="img" aria-label="Говорит" title="Говорит"><svg aria-hidden="true" viewBox="0 0 16 16"><path d="M2 6v4h3l4 3V3L5 6H2Zm10-2c2 2 2 6 0 8" /></svg></span>
        </div>
        {error !== null && (
          <span className="roster-error" role="alert">
            {error}
          </span>
        )}
      </div>

      <div className="roster-meta">
        <span className="call-state">Connected</span>
        {participant.admin && <span className="tag">admin</span>}
        {participant.muted && <span className="tag">muted</span>}
        <span className="roster-duration" title="Time in conference">
          {duration}
        </span>
      </div>

      <div className="roster-actions">
        <button type="button" className="button button-quiet" disabled={pending || disabled}
          aria-pressed={participant.muted}
          title={participant.muted ? 'Let this participant speak' : 'Mute this participant’s microphone; they can still hear the conference'}
          onClick={() => { void run(() => setParticipantMuted(roomId, participant.uniqueid, !participant.muted)) }}>
          {participant.muted ? 'Unmute' : 'Mute'}
        </button>
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


function CallRow({ call, stale, socketConnected, duration, name }: {
  name?: string;
  call: OutgoingCall; stale: boolean; socketConnected: boolean; duration: string
}) {
  const roomId = useRoomID()
  const { pending, error, run } = useAsyncAction()
  const failed = call.state === 'failed'
  return <li className={`roster-row participant-row ${failed ? 'call-failed' : 'call-dialing'}`}>
    <div className="roster-identity">
      <div className="participant-heading">
        <span className="roster-name">{name || call.number}</span>
        {name && <span className="roster-number">{call.number}</span>}
        <span className="call-state">{failed ? (call.reason || 'Connection failed') : call.cancelling ? 'Cancelling…' : (call.reason || 'Dialling…')}</span>
      </div>
      {error && <span role="alert" className="roster-error">{error}</span>}
    </div>
    {!failed && <span className="roster-duration" title="Time since dialling">{duration}</span>}
    <div className="roster-actions">
      {failed && <button type="button" className="button" disabled={pending || stale} onClick={() => { void run(() => retryCall(roomId, call.id)) }}>Retry</button>}
      <button type="button" className="button button-quiet" disabled={pending || call.cancelling || (failed ? !socketConnected : stale)} onClick={() => { void run(() => cancelCall(roomId, call.id)) }}>
        {pending ? 'Working…' : failed ? 'Remove' : 'Cancel'}
      </button>
    </div>
  </li>
}
