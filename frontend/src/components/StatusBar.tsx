import { useParticipantCount, useT } from '../i18n.ts'
interface StatusBarProps {
  room: string | null
  participantCount: number
  /** The backend's AMI link to Asterisk. */
  asteriskConnected: boolean
  /** This browser's WebSocket to the backend. */
  socketConnected: boolean
}

export function StatusBar({
  room,
  participantCount,
  asteriskConnected,
  socketConnected,
}: StatusBarProps) {
  const t = useT()
  const countText = useParticipantCount()

  // Two links can be down, and they mean different things: a dead socket is a
  // stale page, a dead AMI link is a stale backend. Report the nearer break.
  const status = !socketConnected
    ? { className: 'offline', label: t('Disconnected from the server') }
    : asteriskConnected
      ? { className: 'online', label: t('Connected to Asterisk') }
      : { className: 'offline', label: t('Asterisk unreachable') }

  return (
    <header className="status-bar">
      <div className="status-bar-title">
        <h1>
          {t('Conference')} {room ?? '—'}
        </h1>
        <p className="status-bar-count">{countText(participantCount)}</p>
      </div>
      <p className={`status-link status-link-${status.className}`}>
        <span className="status-dot" aria-hidden="true" />
        {status.label}
      </p>
    </header>
  )
}
