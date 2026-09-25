// Wire shapes shared with the Go backend. These mirror conference.Participant
// and conference.Snapshot in internal/conference; change one and change both.

export interface Participant {
  uniqueid: string
  channel: string
  callerIdNum: string
  callerIdName: string
  admin: boolean
  muted: boolean
  talking: boolean
  /** Observed bridge join time (RFC 3339); absent when unknown. */
  joinedAt?: string
}

export interface OutgoingCall {
  id: string
  number: string
  state: 'dialing' | 'failed'
  reason?: string
  createdAt: string
  cancelling?: boolean
}

export interface Snapshot {
  roomId: string
  roomName: string
  room: string
  /** False while the backend has no working AMI link; the roster is stale. */
  asteriskConnected: boolean
  participants: Participant[]
  calls: OutgoingCall[]
}

/**
 * The only kind of frame the server sends. The snapshot fields are inlined
 * alongside "type", so a message is a Snapshot with one extra property.
 */
export interface SnapshotMessage extends Snapshot {
  type: 'snapshot'
}

export interface AddParticipantResponse {
  actionId: string
}
