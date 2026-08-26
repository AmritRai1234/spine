// Minimal typed client for the Spine engine: HTTP emit/query + WebSocket
// state subscriptions with auto-reconnect and in-band auth handshake.

export interface EmitResponse {
  status: string
  event: string
  emitted_states?: string[]
  steps_executed?: number
  error?: string
}

export interface StateMessage {
  type: "state"
  state: string
  event: string
  payload: Record<string, unknown>
  timestamp: number
}

type StateHandler = (payload: Record<string, unknown>, msg: StateMessage) => void
type ConnectionHandler = (connected: boolean) => void

export class SpineClient {
  private baseUrl: string
  private apiKey: string
  private ws: WebSocket | null = null
  private handlers = new Map<string, Set<StateHandler>>()
  private connHandlers = new Set<ConnectionHandler>()
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private disposed = false

  constructor(baseUrl = import.meta.env.SPINE_URL ?? "http://localhost:8080", apiKey = import.meta.env.SPINE_API_KEY ?? "") {
    this.baseUrl = baseUrl.replace(/\/$/, "")
    this.apiKey = apiKey
  }

  async emit(event: string, payload: Record<string, unknown> = {}): Promise<EmitResponse> {
    const res = await fetch(`${this.baseUrl}/emit`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-API-Key": this.apiKey,
      },
      body: JSON.stringify({ event, payload }),
    })
    const data = (await res.json()) as EmitResponse
    if (!res.ok || data.status === "error") {
      throw new Error(data.error ?? `emit failed (HTTP ${res.status})`)
    }
    return data
  }

  async queryTable(name: string, params: Record<string, string | number> = {}): Promise<{ count: number; rows: Record<string, unknown>[] }> {
    const qs = new URLSearchParams()
    for (const [k, v] of Object.entries(params)) qs.set(k, String(v))
    const res = await fetch(`${this.baseUrl}/tables/${encodeURIComponent(name)}?${qs}`, {
      headers: { "X-API-Key": this.apiKey },
    })
    const data = await res.json()
    if (!res.ok || data.status === "error") {
      throw new Error(data.error ?? `table query failed (HTTP ${res.status})`)
    }
    return data
  }

  /** Query the event audit log (admin). */
  async events(limit = 50): Promise<{ status: string; count: number; events: Record<string, unknown>[] }> {
    const res = await fetch(`${this.baseUrl}/events?limit=${limit}`, {
      headers: { "X-API-Key": this.apiKey },
    })
    return res.json()
  }

  /** Subscribe to a Spine state broadcast. Returns an unsubscribe function. */
  onState(state: string, handler: StateHandler): () => void {
    if (!this.handlers.has(state)) this.handlers.set(state, new Set())
    this.handlers.get(state)!.add(handler)
    this.connect()
    return () => {
      this.handlers.get(state)?.delete(handler)
    }
  }

  /** Subscribe to connection status changes. Returns an unsubscribe function. */
  onConnectionChange(handler: ConnectionHandler): () => void {
    this.connHandlers.add(handler)
    handler(this.ws !== null && this.ws.readyState === WebSocket.OPEN)
    return () => {
      this.connHandlers.delete(handler)
    }
  }

  connect() {
    if (this.ws || this.disposed || this.handlers.size === 0) return
    // Same-origin mode (baseUrl ""): the spine server serves the SPA and the
    // API on one port, so the WS endpoint derives from location.
    const isSecure = this.baseUrl.startsWith("https") || (this.baseUrl === "" && window.location.protocol === "https:")
    const host = this.baseUrl === "" ? window.location.host : this.baseUrl.replace(/^https?:\/\//, "")
    const wsUrl = `${isSecure ? "wss" : "ws"}://${host}/ws`

    const ws = new WebSocket(wsUrl)
    this.ws = ws

    ws.onopen = () => {
      // In-band auth handshake (browser-friendly)
      ws.send(JSON.stringify({ type: "auth", token: this.apiKey }))
      this.connHandlers.forEach((h) => h(true))
    }

    ws.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data as string) as StateMessage
        if (msg.type === "state") {
          this.handlers.get(msg.state)?.forEach((h) => h(msg.payload, msg))
        }
      } catch {
        // ignore malformed frames
      }
    }

    ws.onclose = () => {
      this.ws = null
      this.connHandlers.forEach((h) => h(false))
      if (!this.disposed && this.handlers.size > 0) {
        this.reconnectTimer = setTimeout(() => this.connect(), 3000)
      }
    }

    ws.onerror = () => ws.close()
  }

  dispose() {
    this.disposed = true
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer)
    this.ws?.close()
    this.ws = null
    this.handlers.clear()
    this.connHandlers.clear()
  }
}

export const spine = new SpineClient()
