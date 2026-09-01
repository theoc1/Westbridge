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
  // Two links can be down, and they mean different things: a dead socket is a
  // stale page, a dead AMI link is a stale backend. Report the nearer break.
  const status = !socketConnected
    ? { className: 'offline', label: 'Disconnected from the server' }
    : asteriskConnected
      ? { className: 'online', label: 'Connected to Asterisk' }
      : { className: 'offline', label: 'Asterisk unreachable' }

  return (
    <header className="status-bar">
      <div className="status-bar-title">
        <h1>Conference {room ?? '—'}</h1>
        <p className="status-bar-count">
          {participantCount === 1
            ? '1 participant'
            : `${participantCount} participants`}
        </p>
      </div>
      <p className={`status-link status-link-${status.className}`}>
        <span className="status-dot" aria-hidden="true" />
        {status.label}
      </p>
    </header>
  )
}
