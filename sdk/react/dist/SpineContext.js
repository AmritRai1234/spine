import { jsx as _jsx } from "react/jsx-runtime";
import { createContext, useContext, useEffect, useState, useRef } from 'react';
const SpineContext = createContext(null);
export const SpineProvider = ({ url, children }) => {
    const [connected, setConnected] = useState(false);
    const listenersRef = useRef(new Map());
    const wsRef = useRef(null);
    // Normalize URLs
    const httpUrl = url.replace(/^ws:/, 'http:').replace(/^wss:/, 'https:').replace(/\/ws$/, '');
    const wsUrl = url.startsWith('http')
        ? url.replace(/^http:/, 'ws:').replace(/^https:/, 'wss:') + '/ws'
        : url;
    useEffect(() => {
        let ws;
        let reconnectTimeout;
        const connect = () => {
            try {
                ws = new WebSocket(wsUrl);
                wsRef.current = ws;
                ws.onopen = () => {
                    setConnected(true);
                    console.log('[Spine React SDK] ⚡ Connected to Spine server at', wsUrl);
                };
                ws.onmessage = (event) => {
                    try {
                        const data = JSON.parse(event.data);
                        const stateName = data.state || data.event;
                        if (stateName && listenersRef.current.has(stateName)) {
                            const callbacks = listenersRef.current.get(stateName);
                            callbacks?.forEach((cb) => cb(data.payload || data));
                        }
                    }
                    catch (err) {
                        console.error('[Spine React SDK] Error parsing Spine message:', err);
                    }
                };
                ws.onclose = () => {
                    setConnected(false);
                    wsRef.current = null;
                    // Auto reconnect after 2 seconds
                    reconnectTimeout = setTimeout(connect, 2000);
                };
                ws.onerror = () => {
                    setConnected(false);
                };
            }
            catch (e) {
                setConnected(false);
            }
        };
        connect();
        return () => {
            if (reconnectTimeout)
                clearTimeout(reconnectTimeout);
            if (wsRef.current)
                wsRef.current.close();
        };
    }, [wsUrl]);
    // Emit event via HTTP POST /emit
    const emit = async (eventName, payload) => {
        try {
            const response = await fetch(`${httpUrl}/emit`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ event: eventName, payload }),
            });
            return await response.json();
        }
        catch (err) {
            console.error(`[Spine React SDK] Failed to emit event ${eventName}:`, err);
            throw err;
        }
    };
    // Subscribe component listener to Spine state channel
    const subscribe = (stateName, listener) => {
        if (!listenersRef.current.has(stateName)) {
            listenersRef.current.set(stateName, new Set());
        }
        listenersRef.current.get(stateName).add(listener);
        return () => {
            const set = listenersRef.current.get(stateName);
            if (set) {
                set.delete(listener);
                if (set.size === 0)
                    listenersRef.current.delete(stateName);
            }
        };
    };
    return (_jsx(SpineContext.Provider, { value: { connected, emit, subscribe, serverUrl: httpUrl }, children: children }));
};
export const useSpineContext = () => {
    const context = useContext(SpineContext);
    if (!context) {
        throw new Error('useSpineContext must be used within a <SpineProvider>');
    }
    return context;
};
