import { useCallback, useEffect, useState, type FormEvent } from "react"
import { MailCheck, MailX, Send } from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useSpineStateTick } from "@/hooks/use-spine"
import { adminClient } from "@/lib/admin"
import { timeAgo } from "@/lib/format"

interface Subscriber {
  email: string
  unsubscribed: number
  created_at: string
}

export default function AdminMarketing() {
  const [subscribers, setSubscribers] = useState<Subscriber[] | null>(null)
  const [subject, setSubject] = useState("")
  const [body, setBody] = useState("")
  const [sending, setSending] = useState(false)
  const savedTick = useSpineStateTick("SUBSCRIBER_SAVED")

  const load = useCallback(async () => {
    setSubscribers(null)
    try {
      const res = await adminClient().queryTable("subscribers", { limit: 500 })
      const rows = (res.rows ?? []) as unknown as Subscriber[]
      rows.sort((a, b) => String(b.created_at).localeCompare(String(a.created_at)))
      setSubscribers(rows)
    } catch {
      setSubscribers([])
    }
  }, [])

  useEffect(() => {
    load()
  }, [load, savedTick])

  async function sendCampaign(e: FormEvent) {
    e.preventDefault()
    if (!subject.trim() || !body.trim()) return
    setSending(true)
    try {
      const res = await adminClient().emit("SEND_CAMPAIGN", {
        subject: subject.trim(),
        body: body.trim(),
      })
      if (res.status === "ok") {
        toast.success(`Campaign queued — "${subject.trim()}"`)
        setSubject("")
        setBody("")
      } else {
        toast.error("Campaign rejected by the engine")
      }
    } catch {
      toast.error("Could not reach the engine")
    } finally {
      setSending(false)
    }
  }

  if (!subscribers) return <Skeleton className="h-96" />

  const active = subscribers.filter((s) => !Number(s.unsubscribed))

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 grid gap-4 duration-300 lg:grid-cols-[1fr_1.1fr]">
      {/* Campaign composer */}
      <Card>
        <CardHeader>
          <CardTitle>New campaign</CardTitle>
          <CardDescription>
            Sends to all {active.length} active subscriber(s) over SMTP — configure{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">SMTP_HOST</code> in the
            backend env to enable delivery.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={sendCampaign} className="space-y-3">
            <Input
              placeholder="Subject — {{email}} personalizes per recipient"
              value={subject}
              onChange={(e) => setSubject(e.target.value)}
              required
            />
            <textarea
              placeholder="Message body — one line per paragraph"
              className="flex min-h-32 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
              value={body}
              onChange={(e) => setBody(e.target.value)}
              required
            />
            <Button type="submit" disabled={sending || active.length === 0}>
              <Send className="mr-1 h-4 w-4" />
              {sending ? "Sending…" : `Send to ${active.length}`}
            </Button>
          </form>
        </CardContent>
      </Card>

      {/* Subscriber list */}
      <Card>
        <CardHeader>
          <CardTitle>Subscribers</CardTitle>
          <CardDescription>
            {active.length} active · {subscribers.length - active.length} opted out
          </CardDescription>
        </CardHeader>
        <CardContent>
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b text-left text-muted-foreground">
                <th className="pb-2 pl-2 font-medium">Email</th>
                <th className="pb-2 font-medium">Status</th>
                <th className="pb-2 pr-2 text-right font-medium">Joined</th>
              </tr>
            </thead>
            <tbody>
              {subscribers.map((s) => (
                <tr key={s.email} className="border-b last:border-0">
                  <td className="py-2.5 pl-2 font-medium">{s.email}</td>
                  <td>
                    {Number(s.unsubscribed) ? (
                      <Badge variant="outline" className="gap-1 text-muted-foreground">
                        <MailX className="h-3 w-3" /> opted out
                      </Badge>
                    ) : (
                      <Badge variant="secondary" className="gap-1">
                        <MailCheck className="h-3 w-3" /> active
                      </Badge>
                    )}
                  </td>
                  <td className="pr-2 text-right text-muted-foreground">{timeAgo(String(s.created_at))}</td>
                </tr>
              ))}
              {subscribers.length === 0 && (
                <tr>
                  <td colSpan={3} className="py-12 text-center text-muted-foreground">
                    No subscribers yet — the storefront footer form fills this list.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </CardContent>
      </Card>
    </div>
  )
}
