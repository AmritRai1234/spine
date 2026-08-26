/**
 * Custom React hook to subscribe to a fine-grained Spine state channel.
 * Triggers a re-render ONLY when the specified state receives an update.
 *
 * @param stateName The Spine state name (e.g. 'LEAD_STATUS', 'ITEM_UPDATED')
 * @param initialValue Optional initial state payload
 */
export declare function useSpineState<T = any>(stateName: string, initialValue?: T): T | undefined;
