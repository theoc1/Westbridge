// Wire shapes shared with the Go backend. These mirror conference.Participant
// and conference.Snapshot in internal/conference; change one and change both.

export interface Participant {
  uniqueid: string
  channel: string
  callerIdNum: string
  callerIdName: string
  admin: boolean
  muted: boolean
  /** RFC 3339 timestamp of the moment the channel joined the bridge. */
  joinedAt: string
}

export interface Snapshot {
  room: string
  /** False while the backend has no working AMI link; the roster is stale. */
  asteriskConnected: boolean
  participants: Participant[]
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
