import { useRoomID } from '../roomContext.ts'
import { useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { callContacts } from '../callContacts.ts'
import { addParticipant } from '../api.ts'
import type { Contact, usePhonebook } from '../phonebook.ts'

export function Phonebook({ book, disabled }: { book: ReturnType<typeof usePhonebook>; disabled: boolean }) {
  const roomId = useRoomID()
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [editor, setEditor] = useState<Contact | 'new' | null>(null)
  const [name, setName] = useState('')
  const [number, setNumber] = useState('')
  const [pending, setPending] = useState(false)
  const [calling, setCalling] = useState(false)
  const callLock = useRef(false)
  const [error, setError] = useState<string | null>(null)
  const [callErrors, setCallErrors] = useState<Record<number, string>>({})
  const [deleting, setDeleting] = useState<number | null>(null)
  const chosen = book.contacts.filter(c => selected.has(c.id))
  const open = (contact: Contact | 'new') => {
    setEditor(contact); setName(contact === 'new' ? '' : contact.name); setNumber(contact === 'new' ? '' : contact.number); setError(null); setDeleting(null)
  }
  const save = async (event: FormEvent) => {
    event.preventDefault(); setPending(true); setError(null)
    try { await book.save(editor === 'new' || editor === null ? null : editor.id, name, number); setEditor(null) }
    catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not save contact') }
    finally { setPending(false) }
  }
  const remove = async (id: number) => {
    setPending(true); setError(null)
    try {
      await book.remove(id)
      setSelected(previous => { const next = new Set(previous); next.delete(id); return next })
      setDeleting(null)
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not delete contact') }
    finally { setPending(false) }
  }
  const callSelected = async () => {
    if (callLock.current || disabled || chosen.length === 0) return
    callLock.current = true; setCalling(true); setCallErrors({})
    try {
      await callContacts(chosen, number => addParticipant(roomId, number),
        id => setSelected(previous => { const next = new Set(previous); next.delete(id); return next }),
        (id, message) => setCallErrors(previous => ({ ...previous, [id]: message })),
      )
    } finally { callLock.current = false; setCalling(false) }
  }
  return <aside className="phonebook" aria-label="Personal phonebook">
    <div className="phonebook-heading"><h2>Phonebook</h2><button className="button button-quiet" disabled={pending || calling} onClick={() => open('new')}>Add contact</button></div>
    {editor !== null && <form className="add-form" onSubmit={save}>
      <h3>{editor === 'new' ? 'New contact' : 'Edit contact'}</h3>
      <label htmlFor="contact-name">Name</label>
      <input id="contact-name" className="add-form-input" autoFocus required maxLength={100} value={name} disabled={pending} onChange={e => setName(e.target.value)} />
      <label htmlFor="contact-number">Number</label>
      <input id="contact-number" className="add-form-input" type="tel" required maxLength={64} value={number} disabled={pending} onChange={e => setNumber(e.target.value)} />
      <div className="add-form-row"><button className="button" disabled={pending}>{pending ? 'Saving…' : 'Save'}</button><button type="button" className="button button-quiet" disabled={pending} onClick={() => { setEditor(null); setError(null) }}>Cancel</button></div>
    </form>}
    {error && <p role="alert" className="add-form-error">{error}</p>}
    {book.error && <div role="alert" className="add-form-error">{book.error} <button className="button button-quiet" onClick={() => { void book.reload() }}>Reload</button></div>}
    {book.loading ? <p role="status">Loading phonebook…</p> : book.contacts.length === 0 ? <p className="empty-state">Your phonebook is empty.</p> : <>
      <div className="phonebook-toolbar">
        <label><input type="checkbox" aria-label="Select all contacts" disabled={calling} checked={chosen.length === book.contacts.length} onChange={e => setSelected(e.target.checked ? new Set(book.contacts.map(c => c.id)) : new Set())} /> All</label>
        <button className="button" disabled={disabled || calling || pending || editor !== null || chosen.length === 0} onClick={() => { void callSelected() }}>{calling ? 'Calling…' : `Call selected (${chosen.length})`}</button>
      </div>
      <ul className="contact-list" aria-label="Contacts" tabIndex={0}>{book.contacts.map(contact => <li key={contact.id} className="contact-row">
        <label className="contact-identity"><input type="checkbox" aria-label={`Select ${contact.name}`} disabled={calling} checked={selected.has(contact.id)} onChange={e => { const checked = e.target.checked; setSelected(previous => { const next = new Set(previous); if (checked) next.add(contact.id); else next.delete(contact.id); return next }) }} /><span><strong title={contact.name}>{contact.name}</strong><span className="roster-number" title={contact.number}>{contact.number}</span></span></label>
        <div className="contact-actions">{deleting === contact.id ? <><button className="button button-danger" disabled={pending} onClick={() => { void remove(contact.id) }}>Delete?</button><button className="button button-quiet" disabled={pending} onClick={() => setDeleting(null)}>Cancel</button></> : <><button className="button button-quiet icon-button" title="Edit" aria-label={`Edit ${contact.name}`} disabled={pending || calling} onClick={() => open(contact)}><svg aria-hidden="true" viewBox="0 0 20 20"><path d="m13 3 4 4M3 17l4-1L17 6a2.8 2.8 0 0 0-4-4L3 12v5Z" /></svg></button><button className="button button-quiet icon-button" title="Delete" aria-label={`Delete ${contact.name}`} disabled={pending || calling} onClick={() => { setDeleting(contact.id); setEditor(null); setError(null) }}><svg aria-hidden="true" viewBox="0 0 20 20"><path d="M3 5h14M7 5V2h6v3M5 5l1 13h8l1-13M8 8v7m4-7v7" /></svg></button></>}</div>
        {callErrors[contact.id] && <span role="alert" className="roster-error">{callErrors[contact.id]}</span>}
      </li>)}</ul>
    </>}
  </aside>
}
