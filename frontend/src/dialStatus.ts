import type { Participant } from './types.ts'

export interface DialAttempt {
  number: string
  existingIds: string[]
}

function normalized(number: string): string {
  return number.trim().replace(/[\s\-().]/g, '')
}

/** Only a new participant can complete this attempt; an existing call cannot. */
export function dialHasJoined(attempt: DialAttempt, participants: Participant[]): boolean {
  return participants.some(participant =>
    normalized(participant.callerIdNum) === normalized(attempt.number) &&
    !attempt.existingIds.includes(participant.uniqueid),
  )
}
