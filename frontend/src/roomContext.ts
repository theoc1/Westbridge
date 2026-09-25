import { createContext, useContext } from 'react'

export const RoomContext = createContext<string | null>(null)
export function useRoomID(): string {
  const id = useContext(RoomContext)
  if (!id) throw new Error('Conference controls require a room')
  return id
}
