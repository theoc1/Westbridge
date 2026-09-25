import { ru } from './locales/ru.ts'

export type Locale = 'en' | 'ru'
export function translate(
  locale: Locale,
  message: string,
  values: Record<string, string | number> = {},
): string {
  const text = locale === 'ru' ? (ru[message] ?? message) : message
  return text.replace(/\{(\w+)\}/g, (match, key: string) =>
    String(values[key] ?? match),
  )
}
export function participantCount(locale: Locale, count: number): string {
  if (locale === 'en')
    return `${count} ${count === 1 ? 'participant' : 'participants'}`
  const form = new Intl.PluralRules('ru').select(count)
  return `${count} ${form === 'one' ? 'участник' : form === 'few' ? 'участника' : 'участников'}`
}
// API/domain messages stay language-neutral at the transport boundary. Known
// messages are localized when displayed; unknown diagnostics remain intact.
export function translateMessage(locale: Locale, message: string): string {
  if (
    locale === 'ru' &&
    (message.startsWith('conference: invalid number') ||
      message.startsWith('invalid contact: conference: invalid number'))
  )
    return 'Некорректный номер: используйте не более 24 символов, только цифры и необязательный «+» в начале'
  const range =
    /^invalid room or user assignment: number must be (\d+)–(\d+)$/.exec(
      message,
    )
  if (locale === 'ru' && range)
    return `Номер комнаты должен быть от ${range[1]} до ${range[2]}`
  const status = /^request failed with status (\d+)$/.exec(message)
  if (locale === 'ru' && status) return `Ошибка запроса: ${status[1]}`
  return translate(locale, message)
}
