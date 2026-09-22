import type { AddParticipantResponse, Snapshot } from './types.ts'

/**
 * ApiError carries the backend's own message. Every failed call answers with
 * {"error": "..."}, and those messages ("participant is no longer in the
 * conference", "not connected to Asterisk") are written to be shown to a
 * human, so they are surfaced verbatim rather than replaced with a generic
 * "request failed".
 */
export class ApiError extends Error {
  readonly status: number

  constructor(message: string, status: number, options?: ErrorOptions) {
    super(message, options)
    this.name = 'ApiError'
    this.status = status
  }
}

export async function request<T>(path: string, init?: RequestInit, notifyUnauthorized = true): Promise<T> {
  let response: Response
  try {
    response = await fetch(path, init)
  } catch (cause) {
    // fetch only rejects when the request never reached the server.
    throw new ApiError('cannot reach the server', 0, { cause })
  }

  if (notifyUnauthorized && response.status === 401 && path !== "/api/auth/login") {
 window.dispatchEvent(new Event("westbridge-auth-expired"))
 }
 if (!response.ok) {
    throw new ApiError(await errorMessage(response), response.status)
  }
  if (response.status === 204) {
    return undefined as T
  }
  return (await response.json()) as T
}

/** Pulls {"error": "..."} out of a failed response, with fallbacks for a
 * response that is not ours to parse — a proxy's HTML error page, say. */
async function errorMessage(response: Response): Promise<string> {
  try {
    const body: unknown = await response.json()
    if (
      typeof body === 'object' &&
      body !== null &&
      'error' in body &&
      typeof body.error === 'string' &&
      body.error !== ''
    ) {
      return body.error
    }
  } catch {
    // Fall through to the status line.
  }
  return `request failed with status ${response.status}`
}

export function getConference(signal?: AbortSignal): Promise<Snapshot> {
  return request<Snapshot>('/api/conference', { signal })
}

/**
 * Queues an outbound call. Resolving only means Asterisk accepted the
 * Originate; the callee appears in the roster if and when they answer.
 */
export function addParticipant(number: string): Promise<AddParticipantResponse> {
  return request<AddParticipantResponse>('/api/conference/participants', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ number }),
  })
}

export function kickParticipant(uniqueid: string): Promise<void> {
  return request<void>(
    `/api/conference/participants/${encodeURIComponent(uniqueid)}`,
    { method: 'DELETE', headers: { 'Content-Type': 'application/json' } },
  )
}


export function cancelCall(id: string): Promise<void> {
  return request<void>(`/api/conference/calls/${encodeURIComponent(id)}`, {
    method: 'DELETE', headers: { 'Content-Type': 'application/json' },
  })
}

export function retryCall(id: string): Promise<AddParticipantResponse> {
  return request<AddParticipantResponse>(`/api/conference/calls/${encodeURIComponent(id)}/retry`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}',
  })
}

export function setParticipantMuted(uniqueid: string, muted: boolean): Promise<void> {
  return request<void>(`/api/conference/participants/${encodeURIComponent(uniqueid)}/mute`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ muted }),
  })
}
