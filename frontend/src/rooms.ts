export interface Room {
  id: string
  number: string
  name: string
  state: 'pending' | 'ready' | 'deleting'
  error?: string
}
