import { Phonebook } from './components/Phonebook.tsx'
import { contactNumber, usePhonebook } from './phonebook.ts'
import { AuthGate } from './auth.tsx'
import { AddParticipantForm } from './components/AddParticipantForm.tsx'
import { ParticipantList } from './components/ParticipantList.tsx'
import { StatusBar } from './components/StatusBar.tsx'
import { useConference } from './useConference.ts'

export default function App() {
 return <AuthGate><Conference /></AuthGate>
}

function Conference() {
  const { snapshot, connected, error } = useConference()
  const book = usePhonebook()
  const names = new Map(book.contacts.map(c => [c.number, c.name]))
  const participants = (snapshot?.participants ?? []).map(p => ({ ...p, callerIdName: names.get(contactNumber(p.callerIdNum)) ?? p.callerIdName }))

  // Either link being down means the roster on screen may no longer match the
  // room, so both put the page into the same stale state.
  const asteriskConnected = snapshot?.asteriskConnected ?? false
  const stale = !connected || !asteriskConnected

  return (
    <main className="app conference-app">
      <StatusBar
        room={snapshot?.room ?? null}
        participantCount={snapshot?.participants.length ?? 0}
        asteriskConnected={asteriskConnected}
        socketConnected={connected}
      />

      <div className="conference-layout">
      <Phonebook book={book} disabled={stale} />
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
          <AddParticipantForm disabled={stale} />
          <ParticipantList participants={participants} contactNames={names} calls={snapshot.calls ?? []} stale={stale} socketConnected={connected} />
        </>
      )}
      </section>
      </div>
    </main>
  )
}
