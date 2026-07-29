/**
 * Custom React hook providing an event emitter function for Spine backend routes.
 *
 * @example
 * const emit = useSpineEvent();
 * emit('SUBMIT_LEAD', { email: 'user@example.com' });
 */
export declare function useSpineEvent(): (eventName: string, payload: any) => Promise<any>;
