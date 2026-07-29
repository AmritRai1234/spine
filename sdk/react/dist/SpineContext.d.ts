import React from 'react';
export interface SpineContextValue {
    connected: boolean;
    emit: (eventName: string, payload: any) => Promise<any>;
    subscribe: (stateName: string, listener: (data: any) => void) => () => void;
    serverUrl: string;
}
export interface SpineProviderProps {
    url: string;
    children: React.ReactNode;
}
export declare const SpineProvider: React.FC<SpineProviderProps>;
export declare const useSpineContext: () => SpineContextValue;
