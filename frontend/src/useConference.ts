import { useCallback, useEffect, useRef, useState } from 'react'
import { getConference } from './api.ts'
import type { Snapshot, SnapshotMessage } from './types.ts'

/** Reconnect backoff, matching the backoff the backend uses towards Asterisk. */
const RECONNECT_MIN_MS = 1_000
const RECONNECT_MAX_MS = 30_000

export interface ConferenceState {
  /** Null until the first snapshot arrives, from either the socket or REST. */
  snapshot: Snapshot | null
  /** True while the browser has a live socket to the backend. */
  connected: boolean
  /** Set when the initial REST fetch failed and no socket has opened yet. */
  error: string | null
}

function socketURL(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}/ws`
}

function parseSnapshot(data: unknown): Snapshot | null {
  if (typeof data !== 'string') {
    return null
  }
  let message: SnapshotMessage
  try {
    message = JSON.parse(data) as SnapshotMessage
  } catch {
    return null
  }
  if (message.type !== 'snapshot' || !Array.isArray(message.participants)) {
    return null
  }
  return {
    room: message.room,
    asteriskConnected: message.asteriskConnected,
    participants: message.participants,
  }
}

/**
 * useConference owns the one WebSocket the page needs. The server pushes a
 * full snapshot on connect and after every change, so there is no merging to
 * do here: the latest frame simply replaces the state.
 *
 * A REST fetch runs in parallel as a fallback, which covers the case where the
 * socket is blocked by a proxy that still passes plain HTTP. Whichever answers
 * first wins; the socket then keeps the state fresh.
 */
export function useConference(): ConferenceState {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const [connected, setConnected] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Held in a ref so the reconnect timer can be cancelled from the cleanup
  // without the effect depending on state that changes on every message.
  const socketRef = useRef<WebSocket | null>(null)
  const timerRef = useRef<number | null>(null)

  useEffect(() => {
    // Set once the effect is torn down, so a socket event or a timer that
    // fires during teardown cannot resurrect the connection or set state on
    // an unmounted component. StrictMode's double-mount makes this routine
    // rather than theoretical.
    let cancelled = false
    let attempt = 0

    const controller = new AbortController()
    getConference(controller.signal)
      .then((initial) => {
        if (cancelled) {
          return
        }
        // Only fill a gap: a snapshot already pushed over the socket is newer
        // than this response, whichever order the two happen to complete in.
        setSnapshot((current) => current ?? initial)
        setError(null)
      })
      .catch((cause: unknown) => {
        if (cancelled || controller.signal.aborted) {
          return
        }
        setError(cause instanceof Error ? cause.message : 'cannot load the conference')
      })

    const connect = () => {
      if (cancelled) {
        return
      }
      const socket = new WebSocket(socketURL())
      socketRef.current = socket

      socket.onopen = () => {
        if (cancelled) {
          return
        }
        attempt = 0
        setConnected(true)
        setError(null)
      }

      socket.onmessage = (event: MessageEvent<unknown>) => {
        if (cancelled) {
          return
        }
        const next = parseSnapshot(event.data)
        if (next !== null) {
          setSnapshot(next)
        }
      }

      // onclose fires after onerror too, so retrying from here alone covers
      // both a dropped connection and one that never opened.
      socket.onclose = () => {
        if (cancelled) {
          return
        }
        setConnected(false)
        const delay = Math.min(RECONNECT_MIN_MS * 2 ** attempt, RECONNECT_MAX_MS)
        attempt += 1
        timerRef.current = window.setTimeout(connect, delay)
      }
    }

    connect()

    return () => {
      cancelled = true
      controller.abort()
      if (timerRef.current !== null) {
        window.clearTimeout(timerRef.current)
        timerRef.current = null
      }
      // Drop the handlers before closing: onclose would otherwise schedule a
      // reconnect for a socket nobody is listening to any more.
      const socket = socketRef.current
      socketRef.current = null
      if (socket !== null) {
        socket.onopen = null
        socket.onmessage = null
        socket.onclose = null
        socket.close()
      }
    }
  }, [])

  return { snapshot, connected, error }
}

/**
 * useAsyncAction wraps a one-shot request in the pending/error state every
 * button in this app needs, and swallows a result that arrives after unmount.
 */
export function useAsyncAction(): {
  pending: boolean
  error: string | null
  clearError: () => void
  run: (action: () => Promise<unknown>) => Promise<boolean>
} {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const mounted = useRef(true)

  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])

  const clearError = useCallback(() => {
    setError(null)
  }, [])

  const run = useCallback(async (action: () => Promise<unknown>) => {
    setPending(true)
    setError(null)
    try {
      await action()
      return true
    } catch (cause: unknown) {
      if (mounted.current) {
        setError(cause instanceof Error ? cause.message : 'the request failed')
      }
      return false
    } finally {
      if (mounted.current) {
        setPending(false)
      }
    }
  }, [])

  return { pending, error, clearError, run }
}
