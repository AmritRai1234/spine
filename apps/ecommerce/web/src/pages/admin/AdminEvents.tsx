import { useCallback, useEffect, useState } from "react"
import { RefreshCw } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { adminClient } from "@/lib/admin"
import { timeAgo } from "@/lib/format"

interface EventLogRow {
  id: number
  event: string
  payload: Record<string, unknown>
  emitted_states?: string[]
  created_at: string
}

export default function AdminEvents() {
  const [events, setEvents] = useState<EventLogRow[] | null>(null)
  const [type, setType] = useState("all")
  const [auto, setAuto] = useState(true)

  const load = useCallback(async () => {
    const body = await adminClient().events(100)
    setEvents((body.events ?? []) as unknown as EventLogRow[])
  }, [])

  useEffect(() => {
    load()
    if (!auto) return
    const t = setInterval(load, 3000)
    return () => clearInterval(t)
  }, [load, auto])

  const types = events ? [...new Set(events.map((e) => e.event))].sort() : []
  const visible = type === "all" || !events ? events : events.filter((e) => e.event === type)

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 space-y-4 duration-300">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-xl font-semibold tracking-tight">Event Log</h2>
        <div className="flex items-center gap-2">
          <Tabs value={type} onValueChange={setType}>
            <TabsList className="max-w-md flex-wrap h-auto">
              <TabsTrigger value="all">All</TabsTrigger>
              {types.slice(0, 6).map((t) => (
                <TabsTrigger key={t} value={t} className="text-xs">{t}</TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
          <Button variant={auto ? "secondary" : "outline"} size="sm" onClick={() => setAuto(!auto)}>
            <RefreshCw className={`mr-1 h-4 w-4 ${auto ? "animate-spin [animation-duration:3s]" : ""}`} />
            {auto ? "Live" : "Paused"}
          </Button>
        </div>
      </div>

      {!visible ? (
        <Skeleton className="h-96" />
      ) : (
        <Card>
          <CardContent className="space-y-1.5 pt-6">
            {visible.map((ev) => (
              <div
                key={ev.id}
                className="animate-in fade-in slide-in-from-right-2 flex items-start justify-between gap-4 rounded-md border px-3 py-2 duration-300 hover:bg-muted/50"
              >
                <div className="min-w-0 space-y-0.5">
                  <div className="flex items-center gap-2">
                    <Badge variant="outline">{ev.event}</Badge>
                    {ev.emitted_states?.map((s) => (
                      <Badge key={s} variant="secondary">→ {s}</Badge>
                    ))}
                  </div>
                  <code className="block truncate text-xs text-muted-foreground">
                    {JSON.stringify(ev.payload)}
                  </code>
                </div>
                <span className="shrink-0 text-xs text-muted-foreground">{timeAgo(ev.created_at)}</span>
              </div>
            ))}
            {visible.length === 0 && (
              <p className="py-12 text-center text-sm text-muted-foreground">No events recorded.</p>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  )
}
