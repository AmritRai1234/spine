export interface SpineClientOptions {
  baseUrl: string;
  apiKey?: string;
  wsUrl?: string;
  autoReconnect?: boolean;
  reconnectIntervalMs?: number;
}

export interface EmitResponse<T = any> {
  status: string;
  event: string;
  routes_matched?: number;
  emitted_states?: string[];
  result?: T;
  error?: string;
}

export interface QueryOptions {
  limit?: number;
  offset?: number;
  cursor?: number | string;
  where?: Record<string, string>;
}

export interface TableQueryResponse<T = any> {
  status: string;
  table: string;
  count: number;
  rows: T[];
  next_cursor?: number;
  error?: string;
}

export type StateCallback<T = any> = (stateName: string, payload: T) => void;
