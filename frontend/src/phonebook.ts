import { useCallback, useEffect, useRef, useState } from 'react'
import { request } from './api.ts'

export interface Contact { id: number; name: string; number: string }
export function contactNumber(number: string): string {
  return number.trim().replace(/[ ().-]/g, '')
}
export function usePhonebook() {
  const [contacts, setContacts] = useState<Contact[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const generation = useRef(0)
  const reload = useCallback(async () => {
    const current = ++generation.current
    try {
      const next = await request<Contact[]>('/api/contacts')
      if (current === generation.current) { setContacts(next); setError(null) }
    } catch (cause) {
      if (current === generation.current) setError(cause instanceof Error ? cause.message : 'Could not load phonebook')
    } finally { if (current === generation.current) setLoading(false) }
  }, [])
  useEffect(() => {
    // The loader updates state only after an HTTP response.
    // eslint-disable-next-line react/set-state-in-effect
    void reload()
    const focus = () => { void reload() }
    window.addEventListener('focus', focus)
    // This is a request generation counter, not a DOM ref; invalidate pending reads.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    return () => { generation.current++; window.removeEventListener('focus', focus) }
  }, [reload])
  const save = async (id: number | null, name: string, number: string) => {
    const contact = await request<Contact>(id === null ? '/api/contacts' : `/api/contacts/${id}`, {
      method: id === null ? 'POST' : 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, number }),
    })
    generation.current++
    setContacts(previous => [...previous.filter(c => c.id !== contact.id), contact].sort((a, b) => a.name.localeCompare(b.name)))
    setLoading(false)
    return contact
  }
  const remove = async (id: number) => {
    await request<void>(`/api/contacts/${id}`, { method: 'DELETE', headers: { 'Content-Type': 'application/json' } })
    generation.current++
    setContacts(previous => previous.filter(c => c.id !== id))
    setLoading(false)
  }
  return { contacts, loading, error, reload, save, remove }
}
