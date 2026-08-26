import { useCallback, useEffect, useState, type FormEvent } from "react"
import { CalendarClock, Pause, Play, Plus, RefreshCw, Repeat, XCircle } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { useSpineStateTick } from "@/hooks/use-spine"
import { toast } from "sonner"
import { adminClient } from "@/lib/admin"
import { money } from "@/lib/format"

interface PlanRow {
  id: string
  name: string
  interval_months: number
  percent_off: number
}

interface SubscriptionRow {
  id: string
  status: string
  email: string
  name: string
  plan_name: string
  unit_price: number
  qty: number
  interval_months: number
  next_run_at: string
  created_at: string
}

const INTERVALS = [1, 2, 3, 6, 12]

function intervalLabel(months: number) {
  return months === 1 ? "monthly" : `every ${months} months`
}

/**
 * AdminSubscriptions — selling plans (Marketing-style section) + the live
 * subscriber list. Plans are reusable across products; the Edit Product
 * sheet attaches them. Subscribers can be paused / resumed / cancelled.
 */
export default function AdminSubscriptions() {
  const [plans, setPlans] = useState<PlanRow[] | null>(null)
  const [subs, setSubs] = useState<SubscriptionRow[] | null>(null)

  const [planName, setPlanName] = useState("")
  const [planInterval, setPlanInterval] = useState("1")
  const [planPercentOff, setPlanPercentOff] = useState("10")
  const [busy, setBusy] = useState(false)

  const planTick = useSpineStateTick("SUBSCRIPTION_PLAN_SAVED")
  const subTick = useSpineStateTick("SUBSCRIPTION_UPDATED")
  const renewTick = useSpineStateTick("ORDER_CREATED")

  const load = useCallback(async () => {
    try {
      const [p, s] = await Promise.all([
        adminClient().queryTable("subscription_plans", { limit: 200 }),
        adminClient().queryTable("subscriptions", { limit: 500 }),
      ])
      setPlans((p.rows ?? []) as unknown as PlanRow[])
      setSubs((s.rows ?? []) as unknown as SubscriptionRow[])
    } catch {
      setPlans((cur) => cur ?? [])
      setSubs((cur) => cur ?? [])
    }
  }, [])

  useEffect(() => {
    load()
  }, [load, planTick, subTick, renewTick])

  async function savePlan(e: FormEvent) {
    e.preventDefault()
    if (busy) return
    const months = Math.trunc(Number(planInterval))
    if (!planName.trim() || months < 1) return
    setBusy(true)
    try {
      const res = await adminClient().emit("SAVE_SUBSCRIPTION_PLAN", {
        name: planName.trim(),
        interval_months: months,
        percent_off: Number(planPercentOff) || 0,
      })
      if (res.status === "ok") {
        toast.success(`Plan “${planName.trim()}” saved`)
        setPlanName("")
      } else {
        toast.error(res.error ?? "Plan save failed")
      }
    } finally {
      setBusy(false)
    }
  }

  async function toggle(sub: SubscriptionRow, action: string) {
    const res = await adminClient().emit("TOGGLE_SUBSCRIPTION", { id: sub.id, action })
    if (res.status === "ok") {
      toast.success(`Subscription ${action === "reactivate" ? "reactivated" : action + (action.endsWith("e") ? "d" : "ed")}`)
    } else {
      toast.error(res.error ?? `${action} failed`)
    }
  }

  const activeCount = (subs ?? []).filter((s) => s.status === "active").length

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 space-y-4 duration-300">
      <div>
        <h2 className="text-xl font-semibold tracking-tight">Subscriptions</h2>
        <p className="text-sm text-muted-foreground">
          Selling plans are reusable across products — attach them in the product editor.
          {" "}{activeCount} active subscription{activeCount === 1 ? "" : "s"}.
        </p>
      </div>

      {/* ── Selling plans ─────────────────────────────────────────── */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Repeat className="h-4 w-4" /> Selling plans
          </CardTitle>
          <CardDescription>
            A plan sets how often customers are charged and their discount vs the one-time price.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <form onSubmit={savePlan} className="flex flex-wrap items-end gap-2">
            <label className="min-w-48 flex-1 space-y-1">
              <span className="text-xs text-muted-foreground">Plan name</span>
              <Input value={planName} onChange={(e) => setPlanName(e.target.value)} placeholder="Monthly refill" required />
            </label>
            <label className="space-y-1">
              <span className="text-xs text-muted-foreground">Deliver every</span>
              <select
                value={planInterval}
                onChange={(e) => setPlanInterval(e.target.value)}
                className="h-9 w-32 rounded-md border border-input bg-transparent px-2 text-sm"
              >
                {INTERVALS.map((m) => (
                  <option key={m} value={m}>{intervalLabel(m)}</option>
                ))}
              </select>
            </label>
            <label className="space-y-1">
              <span className="text-xs text-muted-foreground">Discount %</span>
              <Input type="number" min="0" max="90" step="1" value={planPercentOff} onChange={(e) => setPlanPercentOff(e.target.value)} className="w-24" />
            </label>
            <Button type="submit" disabled={busy}>
              <Plus className="mr-1 h-4 w-4" /> Save plan
            </Button>
          </form>

          {!plans ? (
            <Skeleton className="h-24" />
          ) : plans.length === 0 ? (
            <p className="py-4 text-center text-sm text-muted-foreground">No plans yet — create your first one above.</p>
          ) : (
            <div className="rounded-md border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Name</TableHead>
                    <TableHead>Cadence</TableHead>
                    <TableHead>Discount</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {plans.map((p) => (
                    <TableRow key={p.id}>
                      <TableCell className="font-medium">{p.name}</TableCell>
                      <TableCell>{intervalLabel(Number(p.interval_months))}</TableCell>
                      <TableCell>
                        {Number(p.percent_off) > 0 ? (
                          <Badge variant="secondary">{Number(p.percent_off)}% off one-time price</Badge>
                        ) : (
                          <span className="text-muted-foreground">same as one-time</span>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      {/* ── Subscriber list ───────────────────────────────────────── */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <CalendarClock className="h-4 w-4" /> Subscribers
          </CardTitle>
          <CardDescription>
            Auto-renewing orders fire when a subscription comes due (checked hourly).
          </CardDescription>
        </CardHeader>
        <CardContent>
          {!subs ? (
            <Skeleton className="h-40" />
          ) : subs.length === 0 ? (
            <div className="py-8 text-center">
              <Repeat className="mx-auto h-10 w-10 text-muted-foreground/50" />
              <p className="mt-3 font-medium">No subscribers yet</p>
              <p className="mt-1 text-sm text-muted-foreground">
                Turn on “Subscribe &amp; save” for a product and attach a plan — shoppers will appear here.
              </p>
            </div>
          ) : (
            <div className="overflow-x-auto rounded-md border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Customer</TableHead>
                    <TableHead>Product</TableHead>
                    <TableHead>Plan</TableHead>
                    <TableHead>Per cycle</TableHead>
                    <TableHead>Next run</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead className="text-right">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {subs.map((s) => {
                    const status = s.status ?? "active"
                    return (
                      <TableRow key={s.id}>
                        <TableCell>{s.email}</TableCell>
                        <TableCell className="max-w-40 truncate">{s.name}{Number(s.qty) > 1 ? ` × ${s.qty}` : ""}</TableCell>
                        <TableCell>
                          {s.plan_name}
                          <span className="ml-1 text-xs text-muted-foreground">{intervalLabel(Number(s.interval_months))}</span>
                        </TableCell>
                        <TableCell className="tabular-nums">{money(Number(s.unit_price) * Number(s.qty))}</TableCell>
                        <TableCell className="whitespace-nowrap text-xs tabular-nums">
                          {status === "cancelled" ? "—" : new Date(s.next_run_at).toLocaleString()}
                        </TableCell>
                        <TableCell>
                          <Badge
                            variant={
                              status === "active" ? "secondary"
                                : status === "paused" ? "outline"
                                : "destructive"
                            }
                          >
                            {status}
                          </Badge>
                        </TableCell>
                        <TableCell className="text-right">
                          <div className="inline-flex gap-1">
                            {status === "active" && (
                              <>
                                <Button variant="outline" size="icon" className="h-7 w-7" title="Pause" onClick={() => toggle(s, "pause")}>
                                  <Pause className="h-3.5 w-3.5" />
                                </Button>
                                <Button variant="ghost" size="icon" className="h-7 w-7 text-destructive hover:text-destructive" title="Cancel" onClick={() => toggle(s, "cancel")}>
                                  <XCircle className="h-3.5 w-3.5" />
                                </Button>
                              </>
                            )}
                            {status === "paused" && (
                              <>
                                <Button variant="outline" size="icon" className="h-7 w-7" title="Resume" onClick={() => toggle(s, "resume")}>
                                  <Play className="h-3.5 w-3.5" />
                                </Button>
                                <Button variant="ghost" size="icon" className="h-7 w-7 text-destructive hover:text-destructive" title="Cancel" onClick={() => toggle(s, "cancel")}>
                                  <XCircle className="h-3.5 w-3.5" />
                                </Button>
                              </>
                            )}
                            {status === "cancelled" && (
                              <Button variant="outline" size="sm" className="h-7" onClick={() => toggle(s, "reactivate")}>
                                <RefreshCw className="mr-1 h-3 w-3" /> Reactivate
                              </Button>
                            )}
                          </div>
                        </TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
