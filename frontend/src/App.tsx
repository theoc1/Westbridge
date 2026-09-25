import { useEffect, useState } from 'react'
import { request } from './api.ts'
import { RoomContext } from './roomContext.ts'
import type { Room } from './rooms.ts'
import { Phonebook } from './components/Phonebook.tsx'
import { contactNumber, usePhonebook } from './phonebook.ts'
import { AuthGate } from './auth.tsx'
import { AddParticipantForm } from './components/AddParticipantForm.tsx'
import { ParticipantList } from './components/ParticipantList.tsx'
import { StatusBar } from './components/StatusBar.tsx'
import { useConference } from './useConference.ts'

export default function App() {
  return (
    <AuthGate>
      <RoomSelection />
    </AuthGate>
  )
}

function Conference({ room }: { room: Room }) {
  const { snapshot, connected, error } = useConference(room.id)
  const book = usePhonebook()
  const names = new Map(book.contacts.map((c) => [c.number, c.name]))
  const participants = (snapshot?.participants ?? []).map((p) => ({
    ...p,
    callerIdName: names.get(contactNumber(p.callerIdNum)) ?? p.callerIdName,
  }))

  // Either link being down means the roster on screen may no longer match the
  // room, so both put the page into the same stale state.
  const asteriskConnected = snapshot?.asteriskConnected ?? false
  const stale = !connected || !asteriskConnected

  return (
    <main className="app conference-app">
      <StatusBar
        room={`${room.name} · ${room.number}`}
        participantCount={snapshot?.participants.length ?? 0}
        asteriskConnected={asteriskConnected}
        socketConnected={connected}
      />

      <div className="conference-layout">
        <Phonebook book={book} disabled={stale || room.state !== 'ready'} />
        <section className="conference-panel" aria-label="Conference controls">
          {snapshot === null ? (
            <p className="empty-state" role="status">
              {error ?? 'Loading the conference…'}
            </p>
          ) : (
            <>
              {stale && (
                <p className="banner" role="alert">
                  {connected
                    ? 'The server has lost its connection to Asterisk. The list below is the last known state and may be out of date.'
                    : 'Disconnected from the server. Reconnecting…'}
                </p>
              )}
              <AddParticipantForm disabled={stale || room.state !== 'ready'} />
              <ParticipantList
                participants={participants}
                contactNames={names}
                calls={snapshot.calls ?? []}
                stale={stale}
                socketConnected={connected}
              />
            </>
          )}
        </section>
      </div>
    </main>
  )
}

function RoomSelection() {
  const [rooms, setRooms] = useState<Room[]>([])
  const [selected, setSelected] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  useEffect(() => {
    let active = true
    const refresh = async () => {
      try {
        const next = await request<Room[]>('/api/rooms')
        if (active) {
          setRooms(next)
          setSelected((old) =>
            next.some((r) => r.id === old) ? old : (next[0]?.id ?? ''),
          )
          setError(null)
        }
      } catch (cause) {
        if (active) {
          setRooms([])
          setError(cause instanceof Error ? cause.message : 'Cannot load rooms')
        }
      } finally {
        if (active) setLoading(false)
      }
    }
    void refresh()
    const interval = window.setInterval(() => void refresh(), 3000)
    const changed = () => void refresh()
    window.addEventListener('westbridge-rooms-changed', changed)
    return () => {
      active = false
      window.clearInterval(interval)
      window.removeEventListener('westbridge-rooms-changed', changed)
    }
  }, [])
  const room = rooms.find((r) => r.id === selected)
  return (
    <>
      <div className="room-selector">
        <label>
          Conference{' '}
          <select
            value={selected}
            onChange={(e) => setSelected(e.target.value)}
          >
            {rooms.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name} · {r.number}
              </option>
            ))}
          </select>
        </label>
        {room && room.state !== 'ready' && (
          <span role="status">
            {room.state}
            {room.error ? `: ${room.error}` : ''}
          </span>
        )}
      </div>
      {error && (
        <p role="alert" className="banner">
          {error}
        </p>
      )}
      {room ? (
        <RoomContext.Provider key={room.id} value={room.id}>
          <Conference room={room} />
        </RoomContext.Provider>
      ) : (
        <main className="app">
          <p>
            {loading
              ? 'Loading rooms…'
              : 'No rooms assigned. Ask an administrator for access.'}
          </p>
        </main>
      )}
    </>
  )
}
