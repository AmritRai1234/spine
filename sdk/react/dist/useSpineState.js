import { useState, useEffect } from 'react';
import { useSpineContext } from './SpineContext';
/**
 * Custom React hook to subscribe to a fine-grained Spine state channel.
 * Triggers a re-render ONLY when the specified state receives an update.
 *
 * @param stateName The Spine state name (e.g. 'LEAD_STATUS', 'ITEM_UPDATED')
 * @param initialValue Optional initial state payload
 */
export function useSpineState(stateName, initialValue) {
    const { subscribe } = useSpineContext();
    const [state, setState] = useState(initialValue);
    useEffect(() => {
        const unsubscribe = subscribe(stateName, (data) => {
            setState(data);
        });
        return unsubscribe;
    }, [stateName, subscribe]);
    return state;
}
