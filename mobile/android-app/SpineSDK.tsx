import React, { createContext, useContext, useEffect, useState, useRef } from 'react';

export interface SpineContextValue {
  connected: boolean;
  emit: (eventName: string, payload: any) => Promise<any>;
  subscribe: (stateName: string, listener: (data: any) => void) => () => void;
  serverUrl: string;
}

const SpineContext = createContext<SpineContextValue | null>(null);

export interface SpineProviderProps {
  url: string;
  children: React.ReactNode;
}

export const SpineProvider: React.FC<SpineProviderProps> = ({ url, children }) => {
  const [connected, setConnected] = useState(false);
  const listenersRef = useRef<Map<string, Set<(data: any) => void>>>(new Map());
  const wsRef = useRef<WebSocket | null>(null);

  const httpUrl = url.replace(/^ws:/, 'http:').replace(/^wss:/, 'https:').replace(/\/ws$/, '');
  const wsUrl = url.startsWith('http')
    ? url.replace(/^http:/, 'ws:').replace(/^https:/, 'wss:') + '/ws'
    : url;

  useEffect(() => {
    let ws: WebSocket;
    let reconnectTimeout: any;

    const connect = () => {
      try {
        ws = new WebSocket(wsUrl);
        wsRef.current = ws;

        ws.onopen = () => {
          setConnected(true);
          console.log('[Spine React Mobile SDK] ⚡ Connected to Spine server at', wsUrl);
        };

        ws.onmessage = (event) => {
          try {
            const data = JSON.parse(event.data);
            const stateName = data.state || data.event;
            if (stateName && listenersRef.current.has(stateName)) {
              const callbacks = listenersRef.current.get(stateName);
              callbacks?.forEach((cb) => cb(data.payload || data));
            }
          } catch (err) {
            console.error('[Spine React Mobile SDK] Error parsing message:', err);
          }
        };

        ws.onclose = () => {
          setConnected(false);
          wsRef.current = null;
          reconnectTimeout = setTimeout(connect, 2000);
        };

        ws.onerror = () => {
          setConnected(false);
        };
      } catch (e) {
        setConnected(false);
      }
    };

    connect();

    return () => {
      if (reconnectTimeout) clearTimeout(reconnectTimeout);
      if (wsRef.current) wsRef.current.close();
    };
  }, [wsUrl]);

  const emit = async (eventName: string, payload: any) => {
    try {
      const response = await fetch(`${httpUrl}/emit`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ event: eventName, payload }),
      });
      return await response.json();
    } catch (err) {
      console.error(`[Spine React Mobile SDK] Failed to emit event ${eventName}:`, err);
      throw err;
    }
  };

  const subscribe = (stateName: string, listener: (data: any) => void) => {
    if (!listenersRef.current.has(stateName)) {
      listenersRef.current.set(stateName, new Set());
    }
    listenersRef.current.get(stateName)!.add(listener);

    return () => {
      const set = listenersRef.current.get(stateName);
      if (set) {
        set.delete(listener);
        if (set.size === 0) listenersRef.current.delete(stateName);
      }
    };
  };

  return (
    <SpineContext.Provider value={{ connected, emit, subscribe, serverUrl: httpUrl }}>
      {children}
    </SpineContext.Provider>
  );
};

export const useSpineContext = (): SpineContextValue => {
  const context = useContext(SpineContext);
  if (!context) {
    throw new Error('useSpineContext must be used within a <SpineProvider>');
  }
  return context;
};

export function useSpineState<T = any>(stateName: string, initialValue?: T): T | undefined {
  const { subscribe } = useSpineContext();
  const [state, setState] = useState<T | undefined>(initialValue);

  useEffect(() => {
    const unsubscribe = subscribe(stateName, (data: T) => {
      setState(data);
    });
    return unsubscribe;
  }, [stateName, subscribe]);

  return state;
}

export function useSpineEvent() {
  const { emit } = useSpineContext();
  return emit;
}
