import { useState } from 'react'
import { request } from '../api.ts'
import { useLocale, useT } from '../i18n.ts'
import type { Locale } from '../translation.ts'
import { useAsyncAction } from '../useConference.ts'

export function LanguageSettings() {
  const t = useT()
  const { locale, setLocale } = useLocale()
  const [draft, setSelected] = useState<Locale | null>(null)
  const selected = draft ?? locale
  const [saved, setSaved] = useState(false)
  const { pending, error, run } = useAsyncAction()
  return (
    <form
      className="app language-settings"
      onSubmit={(event) => {
        event.preventDefault()
        setSaved(false)
        void run(async () => {
          const result = await request<{ locale: Locale }>(
            '/api/admin/locale',
            {
              method: 'PUT',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ locale: selected }),
            },
          )
          setLocale(result.locale)
          setSelected(null)
          setSaved(true)
        })
      }}
    >
      <label htmlFor="application-language">{t('Application language')}</label>
      <select
        id="application-language"
        className="add-form-input"
        value={selected}
        disabled={pending}
        onChange={(event) => {
          setSelected(event.target.value as Locale)
          setSaved(false)
        }}
      >
        <option value="en">English</option>
        <option value="ru">Русский</option>
      </select>
      <button className="button" disabled={pending}>
        {t('Save language')}
      </button>
      <p className="add-form-note">
        {t('Language applies to everyone, including the sign-in screen.')}
      </p>
      {saved && <p role="status">{t('Language saved.')}</p>}
      {error && (
        <p role="alert" className="add-form-error">
          {t(error)}
        </p>
      )}
    </form>
  )
}
