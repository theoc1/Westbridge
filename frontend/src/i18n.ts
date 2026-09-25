import { createContext, useContext } from 'react'
import { translate, translateMessage, participantCount } from './translation.ts'
import type { Locale } from './translation.ts'

export const LocaleContext = createContext<{
  locale: Locale
  setLocale: (locale: Locale) => void
}>({ locale: 'en', setLocale: () => {} })
export function useLocale() {
  return useContext(LocaleContext)
}
export function useT() {
  const { locale } = useLocale()
  return (message: string, values?: Record<string, string | number>) =>
    values
      ? translate(locale, message, values)
      : translateMessage(locale, message)
}
export function useParticipantCount() {
  const { locale } = useLocale()
  return (count: number) => participantCount(locale, count)
}
