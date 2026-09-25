import { useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { request } from './api.ts'
import type { Locale } from './translation.ts'
import { LocaleContext } from './i18n.ts'

export function LocaleProvider({ children }: { children: ReactNode }) {
  const [locale, applyLocale] = useState<Locale>('en')
  const revision = useRef(0)
  const setLocale = (next: Locale) => {
    revision.current++
    applyLocale(next)
  }
  useEffect(() => {
    let active = true
    const refresh = async () => {
      const current = ++revision.current
      try {
        const result = await request<{ locale: Locale }>(
          '/api/locale',
          undefined,
          false,
        )
        if (
          active &&
          current === revision.current &&
          (result.locale === 'en' || result.locale === 'ru')
        )
          applyLocale(result.locale)
      } catch {
        /* Keep the last language while the server is unavailable. */
      }
    }
    void refresh()
    const timer = window.setInterval(() => void refresh(), 5000)
    window.addEventListener('focus', refresh)
    return () => {
      active = false
      window.clearInterval(timer)
      window.removeEventListener('focus', refresh)
    }
  }, [])
  useEffect(() => {
    document.documentElement.lang = locale
    document.title =
      locale === 'ru'
        ? 'Westbridge — Управление конференциями'
        : 'Westbridge — Conference Control'
  }, [locale])
  return (
    <LocaleContext.Provider value={{ locale, setLocale }}>
      {children}
    </LocaleContext.Provider>
  )
}
