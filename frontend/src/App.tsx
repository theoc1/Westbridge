import { useT } from './i18n.ts'
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
      {(settings, closeSettings) => (
        <RoomSelection settings={settings} closeSettings={closeSettings} />
      )}
    </AuthGate>
  )
}

function Conference({ room }: { room: Room }) {
  const t = useT()

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
        <section
          className="conference-panel"
          aria-label={t('Conference controls')}
        >
          {snapshot === null ? (
            <p className="empty-state" role="status">
              {t(error ?? 'Loading the conference…')}
            </p>
          ) : (
            <>
              {stale && (
                <p className="banner" role="alert">
                  {connected
                    ? t(
                        'The server has lost its connection to Asterisk. The list below is the last known state and may be out of date.',
                      )
                    : t('Disconnected from the server. Reconnecting…')}
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

function RoomSelection({
  settings,
  closeSettings,
}: {
  settings: boolean
  closeSettings: () => void
}) {
  const t = useT()

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
          setSelected((old) => old || (next[0]?.id ?? ''))
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
      {settings && (
        <ConferenceSettings
          rooms={rooms}
          selected={selected}
          onCancel={closeSettings}
          onApply={(id) => {
            if (rooms.some((room) => room.id === id)) {
              setSelected(id)
              closeSettings()
            }
          }}
        />
      )}
      {!settings && room && room.state !== 'ready' && (
        <p className="banner" role="status">
          {t(room.state)}
          {room.error ? `: ${t(room.error)}` : ''}
        </p>
      )}
      {error && (
        <p role="alert" className="banner">
          {t(error ?? '')}
        </p>
      )}
      <div hidden={settings}>
        {room ? (
          <RoomContext.Provider key={room.id} value={room.id}>
            <Conference room={room} />
          </RoomContext.Provider>
        ) : (
          <main className="app">
            <p>
              {loading
                ? t('Loading rooms…')
                : rooms.length
                  ? t('Choose a conference in Settings.')
                  : t('No rooms assigned. Ask an administrator for access.')}
            </p>
          </main>
        )}
      </div>
    </>
  )
}

function ConferenceSettings({
  rooms,
  selected,
  onApply,
  onCancel,
}: {
  rooms: Room[]
  selected: string
  onApply: (id: string) => void
  onCancel: () => void
}) {
  const t = useT()
  const [draft, setDraft] = useState(selected)
  const current = rooms.find((room) => room.id === selected)
  const target = rooms.find((room) => room.id === draft)
  return (
    <main className="app">
      <h1>{t('Settings')}</h1>
      <form
        className="add-form"
        onSubmit={(event) => {
          event.preventDefault()
          if (target) onApply(target.id)
        }}
      >
        <h2>{t('Conference')}</h2>
        <p>
          {t('Current conference')}:{' '}
          {current ? `${current.number} · ${current.name}` : '—'}
        </p>
        <label htmlFor="settings-conference">{t('Select conference')}</label>
        <select
          id="settings-conference"
          className="add-form-input"
          value={target ? draft : ''}
          onChange={(event) => setDraft(event.target.value)}
        >
          <option value="" disabled>
            {t('Select conference')}
          </option>
          {rooms.map((room) => (
            <option key={room.id} value={room.id}>
              {room.number} · {room.name}
            </option>
          ))}
        </select>
        <p className="add-form-note">
          {t(
            'Switching conferences does not disconnect existing calls. The change applies only after confirmation.',
          )}
        </p>
        <div className="add-form-row">
          <button className="button" disabled={!target || draft === selected}>
            {t('Switch conference')}
          </button>
          <button
            type="button"
            className="button button-quiet"
            onClick={onCancel}
          >
            {t('Cancel')}
          </button>
        </div>
      </form>
    </main>
  )
}
