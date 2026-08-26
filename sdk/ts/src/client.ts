import { SpineClientOptions, EmitResponse, QueryOptions, TableQueryResponse, StateCallback } from './types';

export class SpineClient {
  private baseUrl: string;
  private apiKey?: string;
  private wsUrl: string;
  private autoReconnect: boolean;
  private autoReconnectOption: boolean; // configured value; connect() restores it
  private reconnectIntervalMs: number;
  private ws: WebSocket | null = null;
  private subscriptions: Map<string, Set<StateCallback>> = new Map();
  private isConnected: boolean = false;
  private lastSeenID: number = 0;

  constructor(options: SpineClientOptions) {
    this.baseUrl = options.baseUrl.replace(/\/$/, '');
    this.apiKey = options.apiKey;
    this.autoReconnectOption = options.autoReconnect ?? true;
    this.autoReconnect = this.autoReconnectOption;
    this.reconnectIntervalMs = options.reconnectIntervalMs ?? 3000;

    if (options.wsUrl) {
      this.wsUrl = options.wsUrl;
    } else {
      const isSecure = this.baseUrl.startsWith('https');
      const host = this.baseUrl.replace(/^https?:\/\//, '');
      this.wsUrl = `${isSecure ? 'wss' : 'ws'}://${host}/ws`;
    }
  }

  /**
   * Connect to Spine WebSocket endpoint for real-time state subscriptions.
   */
  public connect(): Promise<void> {
    // A previous disconnect() must not permanently disable reconnection:
    // every connect() (including automatic reconnects) restores the
    // configured auto-reconnect behavior.
    this.autoReconnect = this.autoReconnectOption;
    return new Promise((resolve, reject) => {
      try {
        let url = this.wsUrl;
        if (this.apiKey) {
          // The engine's wsAuthCheck reads ?token= (or X-API-Key / in-band
          // auth) — the old ?key= parameter was dead code.
          url += `?token=${encodeURIComponent(this.apiKey)}`;
        }
        this.ws = new WebSocket(url);

        this.ws.onopen = () => {
          const isReconnection = this.isConnected === false && this.lastSeenID > 0;
          this.isConnected = true;

          if (this.apiKey) {
            this.ws?.send(JSON.stringify({ type: 'auth', token: this.apiKey }));
          }

          if (isReconnection) {
            this.ws?.send(JSON.stringify({ type: 'reconnect', last_seen_id: this.lastSeenID }));
          }
          resolve();
        };

        this.ws.onmessage = (event) => {
          try {
            const data = JSON.parse(event.data);
            if (data.id && typeof data.id === 'number') {
              this.lastSeenID = Math.max(this.lastSeenID, data.id);
            }

            if (data.type === 'reconnect_ack' && Array.isArray(data.missed_events)) {
              // Audit-log rows carry {id, event, payload, emitted_states} —
              // replay dispatches the payload to every state the event
              // emitted (the old code looked for evt.state, which audit rows
              // never have — replay silently dropped everything).
              for (const evt of data.missed_events) {
                if (!evt.payload) continue;
                const states: string[] = Array.isArray(evt.emitted_states) ? evt.emitted_states : [];
                for (const state of states) {
                  const callbacks = this.subscriptions.get(state);
                  if (callbacks) callbacks.forEach((cb) => cb(state, evt.payload));
                }
              }
            } else if (data.state && data.payload) {
              const callbacks = this.subscriptions.get(data.state);
              if (callbacks) {
                callbacks.forEach((cb) => cb(data.state, data.payload));
              }
            }
          } catch (e) {
            // Ignore non-JSON or unhandled messages
          }
        };

        this.ws.onclose = () => {
          this.isConnected = false;
          if (this.autoReconnect) {
            // connect() returns a promise — unhandled rejections on repeat
            // failures would crash Node clients.
            setTimeout(() => {
              this.connect().catch(() => {});
            }, this.reconnectIntervalMs);
          }
        };

        this.ws.onerror = (err) => {
          if (!this.isConnected) {
            reject(err);
          }
        };
      } catch (err) {
        reject(err);
      }
    });
  }

  /**
   * Emit an event to the Spine backend via HTTP POST /emit.
   * @param idempotencyKey optional dedup key (sent as payload._idempotency_key)
   */
  public async emit<T = any>(event: string, payload: Record<string, any> = {}, idempotencyKey?: string): Promise<EmitResponse<T>> {
    const body: Record<string, any> = { event, payload };
    if (idempotencyKey) {
      body.payload = { ...payload, _idempotency_key: idempotencyKey };
    }
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
    };
    if (this.apiKey) {
      headers['X-API-Key'] = this.apiKey;
    }

    const response = await fetch(`${this.baseUrl}/emit`, {
      method: 'POST',
      headers,
      body: JSON.stringify(body),
    });

    if (!response.ok) {
      const errorText = await response.text();
      throw new Error(`Emit failed (${response.status}): ${errorText}`);
    }

    return response.json();
  }

  /**
   * Subscribe to real-time state broadcasts.
   */
  public subscribe<T = any>(stateName: string, callback: StateCallback<T>): () => void {
    if (!this.subscriptions.has(stateName)) {
      this.subscriptions.set(stateName, new Set());
    }
    this.subscriptions.get(stateName)!.add(callback);

    if (!this.ws && this.autoReconnect) {
      this.connect().catch(() => {});
    }

    return () => {
      const cbs = this.subscriptions.get(stateName);
      if (cbs) {
        cbs.delete(callback);
        if (cbs.size === 0) {
          this.subscriptions.delete(stateName);
        }
      }
    };
  }

  /**
   * Query rows from a table with optional cursor pagination and filters.
   */
  public async queryTable<T = any>(tableName: string, options: QueryOptions = {}): Promise<TableQueryResponse<T>> {
    const params = new URLSearchParams();
    if (options.limit) params.set('limit', options.limit.toString());
    if (options.offset) params.set('offset', options.offset.toString());
    if (options.cursor) params.set('cursor', options.cursor.toString());
    if (options.where) {
      Object.entries(options.where).forEach(([col, val]) => {
        params.append('where', `${col}:${val}`);
      });
    }

    const headers: Record<string, string> = {};
    if (this.apiKey) {
      headers['X-API-Key'] = this.apiKey;
    }

    const url = `${this.baseUrl}/tables/${tableName}?${params.toString()}`;
    const response = await fetch(url, { headers });

    if (!response.ok) {
      const errText = await response.text();
      throw new Error(`Table query failed (${response.status}): ${errText}`);
    }

    return response.json();
  }

  private async getJson<T>(path: string): Promise<T> {
    const headers: Record<string, string> = {};
    if (this.apiKey) {
      headers['X-API-Key'] = this.apiKey;
    }
    const response = await fetch(`${this.baseUrl}${path}`, { headers });
    if (!response.ok) {
      const errText = await response.text();
      throw new Error(`GET ${path} failed (${response.status}): ${errText}`);
    }
    return response.json();
  }

  /** List all tables with row counts (GET /tables). */
  public async getTables(): Promise<{ status: string; tables: { name: string; rows: number }[] }> {
    return this.getJson('/tables');
  }

  /** Query the event audit log (GET /events). */
  public async getEvents(limit = 50, event?: string): Promise<{ status: string; count: number; events: Record<string, any>[] }> {
    const params = new URLSearchParams({ limit: String(limit) });
    if (event) params.set('event', event);
    return this.getJson(`/events?${params.toString()}`);
  }

  /** Fetch the manifest schema (GET /schema). */
  public async getSchema(): Promise<Record<string, any>> {
    return this.getJson('/schema');
  }

  /** Check the engine health endpoint (GET /health). */
  public async health(): Promise<{ status: string; engine?: string; version?: number }> {
    return this.getJson('/health');
  }

  /**
   * Close the WebSocket connection and clear subscriptions.
   */
  public disconnect(): void {
    this.autoReconnect = false;
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
    this.subscriptions.clear();
  }
}
