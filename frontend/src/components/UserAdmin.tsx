import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { request } from '../api.ts'
import { AUTH_EXPIRED } from '../auth.tsx'
import type { User } from '../auth.tsx'
import { useAsyncAction } from '../useConference.ts'

const jsonHeaders = { 'Content-Type': 'application/json' }
export function UserAdmin({ currentUser }: { currentUser: User }) {
  const [users, setUsers] = useState<User[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const refresh = useCallback(async () => {
    try { setUsers(await request<User[]>('/api/users')); setLoadError(null) }
    catch (cause) { setLoadError(cause instanceof Error ? cause.message : 'Cannot load users') }
    finally { setLoading(false) }
  }, [])
  useEffect(() => {
    let active = true
    request<User[]>('/api/users').then(next => { if (active) setUsers(next) })
      .catch((cause: unknown) => { if (active) setLoadError(cause instanceof Error ? cause.message : 'Cannot load users') })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [])
  return <main className="app admin-page">
    <h1>Users</h1>
    <p className="add-form-note">All active users can manage the conference. Administrators can also manage users.</p>
    <CreateUser onSaved={refresh} />
    {loading && <p role="status">Loading users…</p>}
    {loadError && <div role="alert"><p className="add-form-error">{loadError}</p><button className="button button-quiet" onClick={() => void refresh()}>Retry</button></div>}
    <ul className="roster" aria-label="Users">
      {users.map(user => <UserRow key={user.id} user={user} self={user.id === currentUser.id} onSaved={refresh} />)}
    </ul>
  </main>
}
function CreateUser({ onSaved }: { onSaved: () => Promise<void> }) {
  const [login, setLogin] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState('user')
  const [message, setMessage] = useState('')
  const { pending, error, run } = useAsyncAction()
  const submit = async (event: FormEvent) => {
    event.preventDefault(); setMessage('')
    if (await run(() => request<User>('/api/users', { method: 'POST', headers: jsonHeaders, body: JSON.stringify({ login, password, role }) }))) {
      setMessage(`User ${login} created`); setLogin(''); setPassword(''); setRole('user'); await onSaved()
    }
  }
  return <form className="add-form" onSubmit={submit}>
    <h2>Add user</h2>
    <label htmlFor="new-login">Login</label>
    <input className="add-form-input" id="new-login" required maxLength={64} autoComplete="off" value={login} onChange={e => setLogin(e.target.value)} disabled={pending} />
    <label htmlFor="new-password">Password (at least 12 characters)</label>
    <input className="add-form-input" id="new-password" type="password" required minLength={12} autoComplete="new-password" value={password} onChange={e => setPassword(e.target.value)} disabled={pending} />
    <label htmlFor="new-role">Role</label>
    <select className="add-form-input" id="new-role" value={role} onChange={e => setRole(e.target.value)} disabled={pending}><option value="user">User</option><option value="admin">Administrator</option></select>
    <button className="button" disabled={pending}>{pending ? 'Creating…' : 'Create user'}</button>
    {error && <p role="alert" className="add-form-error">{error}</p>}
    {message && <p role="status" className="add-form-note">{message}</p>}
  </form>
}
function UserRow({ user, self, onSaved }: { user: User; self: boolean; onSaved: () => Promise<void> }) {
  const [editing, setEditing] = useState(false)
  const [password, setPassword] = useState('')
  const [role, setRole] = useState(user.role)
  const [enabled, setEnabled] = useState(user.enabled)
  const [message, setMessage] = useState('')
  const { pending, error, run } = useAsyncAction()
  return <li className="roster-row user-row">
    <div className="roster-identity"><strong>{user.login}{self ? ' (you)' : ''}</strong><span className="roster-number">{user.role === 'admin' ? 'Administrator' : 'User'} · {user.enabled ? 'Active' : 'Disabled'}</span></div>
    {!editing && <button className="button button-quiet" onClick={() => { setRole(user.role); setEnabled(user.enabled); setPassword(''); setMessage(''); setEditing(true) }}>Manage</button>}
    {editing && <form className="user-edit" onSubmit={async event => {
      event.preventDefault()
      const update = { ...(role !== user.role ? { role } : {}), ...(enabled !== user.enabled ? { enabled } : {}), ...(password ? { password } : {}) }
      if (Object.keys(update).length === 0) { setEditing(false); return }
      if (await run(() => request<User>(`/api/users/${user.id}`, { method: 'PATCH', headers: jsonHeaders, body: JSON.stringify(update) }))) {
        setPassword(''); setEditing(false); setMessage('Saved. Previous sessions have been signed out.')
        if (self) window.dispatchEvent(new Event(AUTH_EXPIRED))
        else await onSaved()
      }
    }}>
      <label htmlFor={`role-${user.id}`}>Role</label>
      <select className="add-form-input" id={`role-${user.id}`} value={role} onChange={e => setRole(e.target.value as User['role'])} disabled={pending}><option value="user">User</option><option value="admin">Administrator</option></select>
      <label><input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} disabled={pending} /> Active</label>
      <label htmlFor={`password-${user.id}`}>New password (leave empty to keep)</label>
      <input className="add-form-input" type="password" id={`password-${user.id}`} autoComplete="new-password" minLength={12} value={password} onChange={e => setPassword(e.target.value)} disabled={pending} />
      <p className="add-form-note">Saving changes signs this user out of all sessions.{self ? ' You will need to sign in again.' : ''}</p>
      <div className="add-form-row"><button className="button" disabled={pending}>Save changes</button><button type="button" className="button button-quiet" disabled={pending} onClick={() => { setEditing(false); setPassword('') }}>Cancel</button></div>
      {error && <p className="add-form-error" role="alert">{error}</p>}
    </form>}
    {message && <p role="status" className="add-form-note">{message}</p>}
  </li>
}
