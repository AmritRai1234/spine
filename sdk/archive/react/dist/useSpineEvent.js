import { useSpineContext } from './SpineContext';
/**
 * Custom React hook providing an event emitter function for Spine backend routes.
 *
 * @example
 * const emit = useSpineEvent();
 * emit('SUBMIT_LEAD', { email: 'user@example.com' });
 */
export function useSpineEvent() {
    const { emit } = useSpineContext();
    return emit;
}
