import { SpineClientOptions, EmitResponse, QueryOptions, TableQueryResponse, StateCallback } from './types';

export class SpineClient {
  private baseUrl: string;
  private apiKey?: string;
  private wsUrl: string;
  private autoReconnect: boolean;
  private reconnectIntervalMs: number;
  private ws: WebSocket | null = null;
  private subscriptions: Map<string, Set<StateCallback>> = new Map();
  private isConnected: boolean = false;
  private lastSeenID: number = 0;

  constructor(options: SpineClientOptions) {
    this.baseUrl = options.baseUrl.replace(/\/$/, '');
    this.apiKey = options.apiKey;
    this.autoReconnect = options.autoReconnect ?? true;
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
    return new Promise((resolve, reject) => {
      try {
        let url = this.wsUrl;
        if (this.apiKey) {
          url += `?key=${encodeURIComponent(this.apiKey)}`;
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
              for (const evt of data.missed_events) {
                if (evt.state && evt.payload) {
                  const callbacks = this.subscriptions.get(evt.state);
                  if (callbacks) callbacks.forEach((cb) => cb(evt.state, evt.payload));
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
            setTimeout(() => this.connect(), this.reconnectIntervalMs);
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
   */
  public async emit<T = any>(event: string, payload: Record<string, any> = {}): Promise<EmitResponse<T>> {
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
    };
    if (this.apiKey) {
      headers['X-API-Key'] = this.apiKey;
    }

    const response = await fetch(`${this.baseUrl}/emit`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ event, payload }),
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
