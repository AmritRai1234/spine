import { useEffect, useState } from "react"
import { spine } from "@/lib/spine"

/**
 * Subscribe to a Spine state broadcast over WebSocket.
 * Re-renders whenever the engine emits this state, with the latest payload.
 */
export function useSpineState(state: string): Record<string, unknown> | null {
  const [payload, setPayload] = useState<Record<string, unknown> | null>(null)

  useEffect(() => {
    const unsubscribe = spine.onState(state, (data) => setPayload(data))
    return unsubscribe
  }, [state])

  return payload
}

/** Bump counter every time a state fires — handy for refetching queries. */
export function useSpineStateTick(state: string): number {
  const [tick, setTick] = useState(0)
  useEffect(() => {
    const unsubscribe = spine.onState(state, () => setTick((t) => t + 1))
    return unsubscribe
  }, [state])
  return tick
}
