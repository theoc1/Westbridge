import { useT } from './i18n.ts'
import { Fragment, useEffect, useRef, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import { ApiError, request } from './api.ts'
import { useAsyncAction } from './useConference.ts'
import { LanguageSettings } from './components/LanguageSettings.tsx'
import { RoomAdmin } from './components/RoomAdmin.tsx'
import { UserAdmin } from './components/UserAdmin.tsx'

export interface User {
  id: number
  login: string
  role: 'admin' | 'user'
  enabled: boolean
}
export const AUTH_EXPIRED = 'westbridge-auth-expired'

export function AuthGate({
  children,
}: {
  children: (settings: boolean, closeSettings: () => void) => ReactNode
}) {
  const t = useT()

  const [user, setUser] = useState<User | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [admin, setAdmin] = useState(false)
  const [settings, setSettings] = useState(false)
  const generation = useRef(0)
  const { pending, error, run } = useAsyncAction()

  useEffect(() => {
    let active = true
    const expired = () => {
      generation.current++
      setUser(null)
      setAdmin(false)
      setSettings(false)
    }
    window.addEventListener(AUTH_EXPIRED, expired)
    const check = async () => {
      const id = ++generation.current
      try {
        const current = await request<User>('/api/auth/me', undefined, false)
        if (active && id === generation.current) {
          setUser(current)
          setLoadError(null)
        }
      } catch (cause) {
        if (active && id === generation.current) {
          if (cause instanceof ApiError && cause.status === 401) {
            expired()
            setLoadError(null)
          } else
            setLoadError(
              cause instanceof Error
                ? cause.message
                : 'Cannot check your session',
            )
        }
      } finally {
        if (active) setLoading(false)
      }
    }
    void check()
    // A failed/expired socket dispatches AUTH_EXPIRED as well; this also catches
    // revocation while the admin screen is open and no conference socket exists.
    const interval = window.setInterval(() => {
      void check()
    }, 15_000)
    const focus = () => {
      void check()
    }
    window.addEventListener('focus', focus)
    return () => {
      active = false
      window.clearInterval(interval)
      window.removeEventListener('focus', focus)
      window.removeEventListener(AUTH_EXPIRED, expired)
    }
  }, [])

  if (loading)
    return (
      <main className="app">
        <p role="status">{t('Loading…')}</p>
      </main>
    )
  if (!user)
    return (
      <Login
        onLogin={(next) => {
          generation.current++
          setUser(next)
          setAdmin(false)
          setSettings(false)
          setLoadError(null)
        }}
        serverError={t(loadError ?? '')}
      />
    )

  return (
    <>
      <header className="account-bar">
        <strong>Westbridge</strong>
        <span>{user.login}</span>
        {user.role === 'admin' && (
          <button
            className="button button-quiet"
            onClick={() => {
              setAdmin(!admin)
              setSettings(false)
            }}
          >
            {admin ? t('Conferences') : t('Administration')}
          </button>
        )}
        <button
          className="button button-quiet"
          aria-pressed={settings}
          onClick={() => {
            setSettings(!settings)
            setAdmin(false)
          }}
        >
          {settings ? t('Conferences') : t('Settings')}
        </button>
        <button
          className="button button-quiet"
          disabled={pending}
          onClick={async () => {
            if (
              await run(() =>
                request<void>('/api/auth/logout', {
                  method: 'POST',
                  headers: { 'Content-Type': 'application/json' },
                  body: '{}',
                }),
              )
            ) {
              window.dispatchEvent(new Event(AUTH_EXPIRED))
            }
          }}
        >
          {t('Sign out')}
        </button>
        {error && (
          <span role="alert" className="add-form-error">
            {t(error ?? '')}
          </span>
        )}
      </header>
      {admin && user.role === 'admin' && <AdminPanel user={user} />}
      <div hidden={admin && user.role === 'admin'}>
        <Fragment key={user.id}>
          {children(settings, () => setSettings(false))}
        </Fragment>
      </div>
    </>
  )
}

function Login({
  onLogin,
  serverError,
}: {
  onLogin: (user: User) => void
  serverError: string | null
}) {
  const t = useT()

  const [login, setLogin] = useState('')
  const [password, setPassword] = useState('')
  const { pending, error, run } = useAsyncAction()
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    await run(async () => {
      const user = await request<User>('/api/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ login, password }),
      })
      setPassword('')
      onLogin(user)
    })
  }
  return (
    <main className="app login-page">
      <h1>Westbridge</h1>
      <form className="add-form" onSubmit={submit}>
        <h2>{t('Sign in')}</h2>
        <label htmlFor="login">{t('Login')}</label>
        <input
          className="add-form-input"
          id="login"
          autoComplete="username"
          required
          maxLength={64}
          value={login}
          onChange={(e) => setLogin(e.target.value)}
          disabled={pending}
          autoFocus
        />
        <label htmlFor="password">{t('Password')}</label>
        <input
          className="add-form-input"
          id="password"
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          disabled={pending}
        />
        <button className="button" disabled={pending}>
          {pending ? t('Signing in…') : t('Sign in')}
        </button>
        {(error || serverError) && (
          <p className="add-form-error" role="alert">
            {t(error || serverError || '')}
          </p>
        )}
      </form>
    </main>
  )
}

function AdminPanel({ user }: { user: User }) {
  const t = useT()

  const [tab, setTab] = useState<'users' | 'rooms' | 'application'>('rooms')
  return (
    <>
      <nav className="admin-tabs" aria-label={t('Administration')}>
        <button
          className="button button-quiet"
          aria-pressed={tab === 'rooms'}
          onClick={() => setTab('rooms')}
        >
          {t('Rooms')}
        </button>
        <button
          className="button button-quiet"
          aria-pressed={tab === 'users'}
          onClick={() => setTab('users')}
        >
          {t('Users')}
        </button>
        <button
          className="button button-quiet"
          aria-pressed={tab === 'application'}
          onClick={() => setTab('application')}
        >
          {t('Application')}
        </button>
      </nav>
      {tab === 'rooms' && <RoomAdmin />}
      {tab === 'users' && <UserAdmin currentUser={user} />}
      {tab === 'application' && <LanguageSettings />}
    </>
  )
}
