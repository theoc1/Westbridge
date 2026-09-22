/** Enqueue independently, keeping failures selected without repeating accepted calls. */
export async function callContacts(
  contacts: { id: number; number: string }[],
  invite: (number: string) => Promise<unknown>,
  accepted: (id: number) => void,
  rejected: (id: number, message: string) => void,
) {
  for (let offset = 0; offset < contacts.length; offset += 4) {
    await Promise.all(contacts.slice(offset, offset + 4).map(async contact => {
      try {
        await invite(contact.number)
        accepted(contact.id)
      } catch (cause) {
        rejected(contact.id, cause instanceof Error ? cause.message : 'Could not call')
      }
    }))
  }
}
