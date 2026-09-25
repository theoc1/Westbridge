import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { request } from '../api.ts'
import type { User } from '../auth.tsx'
import type { Room } from '../rooms.ts'
import { useAsyncAction } from '../useConference.ts'

const headers = { 'Content-Type': 'application/json' }
export function RoomAdmin() {
  const [rooms, setRooms] = useState<Room[]>([])
  const [users, setUsers] = useState<User[]>([])
  const [name, setName] = useState('')
  const [number, setNumber] = useState('')
  const [loadError, setLoadError] = useState<string | null>(null)
  const [editing, setEditing] = useState<string | null>(null)
  const { pending, error, run } = useAsyncAction()
  useEffect(() => {
    let active = true
    const refresh = async () => {
      try {
        const [next, people] = await Promise.all([
          request<Room[]>('/api/admin/rooms'),
          request<User[]>('/api/users'),
        ])
        if (active) {
          setRooms(next)
          setUsers(people)
          setLoadError(null)
        }
      } catch (cause) {
        if (active)
          setLoadError(
            cause instanceof Error ? cause.message : 'Cannot load rooms',
          )
      }
    }
    void refresh()
    const timer = window.setInterval(() => void refresh(), 2000)
    return () => {
      active = false
      window.clearInterval(timer)
    }
  }, [])
  const create = async (e: FormEvent) => {
    e.preventDefault()
    if (
      await run(async () => {
        const room = await request<Room>('/api/admin/rooms', {
          method: 'POST',
          headers,
          body: JSON.stringify({ name, number }),
        })
        setRooms((old) => [...old, room])
      })
    ) {
      setName('')
      setNumber('')
    }
  }
  return (
    <main className="app admin-page">
      <h1>Rooms</h1>
      <p className="add-form-note">
        Administrators can control all rooms. Assign other users explicitly.
        Busy rooms cannot be deleted.
      </p>
      <form className="add-form" onSubmit={create}>
        <h2>Create room</h2>
        <label htmlFor="room-name">Name</label>
        <input
          id="room-name"
          className="add-form-input"
          value={name}
          onChange={(e) => setName(e.target.value)}
          required
          maxLength={100}
          disabled={pending}
        />
        <label htmlFor="room-number">Dial-in number</label>
        <input
          id="room-number"
          className="add-form-input"
          value={number}
          onChange={(e) => setNumber(e.target.value)}
          required
          inputMode="numeric"
          pattern="[1-9][0-9]*"
          disabled={pending}
        />
        <button className="button" disabled={pending}>
          Create room
        </button>
      </form>
      {(error || loadError) && (
        <p role="alert" className="add-form-error">
          {error || loadError}
        </p>
      )}
      <ul className="room-admin-list">
        {rooms.map((room) => (
          <li key={room.id} className="room-admin-row">
            <div className="room-admin-summary">
              <strong>{room.name}</strong>
              <span>{room.number}</span>
              <span role="status">{room.state}</span>
              <button
                className="button button-quiet"
                disabled={room.state === 'deleting'}
                onClick={() => setEditing(editing === room.id ? null : room.id)}
              >
                Users
              </button>
              <button
                className="button button-danger"
                disabled={pending || room.state === 'deleting'}
                onClick={() => {
                  if (
                    window.confirm(
                      `Delete room ${room.name} (${room.number})? Active calls will not be disconnected.`,
                    )
                  )
                    void run(async () => {
                      const next = await request<Room>(
                        `/api/admin/rooms/${room.id}`,
                        { method: 'DELETE', headers },
                      )
                      setRooms((old) =>
                        old.map((r) => (r.id === next.id ? next : r)),
                      )
                      setEditing(null)
                    })
                }}
              >
                Delete
              </button>
            </div>
            {room.error && (
              <p role="alert" className="add-form-error">
                {room.error} — synchronization retries automatically.
              </p>
            )}
            {room.state === 'deleting' && (
              <p>
                Admission is closing. Waiting for any calls already in progress
                to end.
              </p>
            )}
            {editing === room.id && (
              <RoomGrants key={room.id} room={room} users={users} />
            )}
          </li>
        ))}
      </ul>
      {rooms.length === 0 && !loadError && <p>No rooms yet.</p>}
    </main>
  )
}
function RoomGrants({ room, users }: { room: Room; users: User[] }) {
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [loaded, setLoaded] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  const { pending, error, run } = useAsyncAction()
  useEffect(() => {
    let active = true
    request<number[]>(`/api/admin/rooms/${room.id}/users`)
      .then((ids) => {
        if (active) {
          setSelected(new Set(ids))
          setLoaded(true)
        }
      })
      .catch((cause: unknown) => {
        if (active)
          setLoadError(
            cause instanceof Error ? cause.message : 'Cannot load access',
          )
      })
    return () => {
      active = false
    }
  }, [room.id])
  return (
    <form
      className="room-grants"
      onSubmit={(e) => {
        e.preventDefault()
        void run(async () => {
          await request(`/api/admin/rooms/${room.id}/users`, {
            method: 'PUT',
            headers,
            body: JSON.stringify({ userIds: [...selected] }),
          })
          setSaved(true)
        })
      }}
    >
      <fieldset disabled={!loaded || pending}>
        <legend>Users allowed in {room.name}</legend>
        {users
          .filter((u) => u.role !== 'admin')
          .map((user) => (
            <label key={user.id}>
              <input
                type="checkbox"
                checked={selected.has(user.id)}
                onChange={(e) => {
                  setSaved(false)
                  setSelected((old) => {
                    const next = new Set(old)
                    if (e.target.checked) next.add(user.id)
                    else next.delete(user.id)
                    return next
                  })
                }}
              />
              {user.login}
              {!user.enabled ? ' (disabled)' : ''}
            </label>
          ))}
        <button className="button" type="submit">
          Save access
        </button>
      </fieldset>
      {saved && <p role="status">Access saved.</p>}
      {(error || loadError) && (
        <p role="alert" className="add-form-error">
          {error || loadError}
        </p>
      )}
    </form>
  )
}
