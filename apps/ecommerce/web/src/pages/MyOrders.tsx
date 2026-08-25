import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react"
import { ChevronDown, PackageSearch } from "lucide-react"

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
import { useSpineStateTick } from "@/hooks/use-spine"
import { spine } from "@/lib/spine"
import { money, timeAgo } from "@/lib/format"
import { statusColor, type OrderItemRow, type OrderRow } from "@/types"

const EMAIL_KEY = "spine_shopper_email"

/**
 * My Orders — email-keyed shopper order tracking. No accounts needed:
 * enter the email you ordered with and watch statuses update live via
 * the ORDER_STATUS_CHANGED broadcast.
 */
export default function MyOrders() {
  const [emailInput, setEmailInput] = useState(() => localStorage.getItem(EMAIL_KEY) ?? "")
  const [email, setEmail] = useState(() => localStorage.getItem(EMAIL_KEY) ?? "")
  const [orders, setOrders] = useState<OrderRow[] | null>(null)
  const [expanded, setExpanded] = useState<string | null>(null)

  const statusTick = useSpineStateTick("ORDER_STATUS_CHANGED")
  const createdTick = useSpineStateTick("ORDER_CREATED")

  const load = useCallback(async () => {
    if (!email) {
      setOrders([])
      return
    }
    try {
      const res = await spine.queryTable("orders", {
        where: `email:${email}`,
        limit: 50,
      })
      setOrders((res.rows ?? []) as unknown as OrderRow[])
    } catch {
      /* keep last */
    }
  }, [email])

  useEffect(() => {
    load()
  }, [load, statusTick, createdTick])

  function submit(e: FormEvent) {
    e.preventDefault()
    const normalized = emailInput.trim().toLowerCase()
    setEmail(normalized)
    setOrders(null)
    if (normalized) localStorage.setItem(EMAIL_KEY, normalized)
  }

  const sorted = useMemo(() => {
    if (!orders) return []
    return [...orders].sort((a, b) => String(b.created_at).localeCompare(String(a.created_at)))
  }, [orders])

  if (!email) {
    return (
      <Card className="mx-auto max-w-md">
        <CardHeader>
          <div className="flex items-center gap-2">
            <PackageSearch className="h-6 w-6" />
            <CardTitle>My Orders</CardTitle>
          </div>
          <CardDescription>
            Enter the email you ordered with to track your purchases.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit} className="flex gap-2">
            <Input
              type="email"
              placeholder="you@example.com"
              value={emailInput}
              onChange={(e) => setEmailInput(e.target.value)}
              required
              autoFocus
            />
            <Button type="submit">Find orders</Button>
          </form>
        </CardContent>
      </Card>
    )
  }

  return (
    <div className="mx-auto max-w-2xl space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-xl font-semibold tracking-tight">My Orders</h2>
          <p className="text-sm text-muted-foreground">{email} · live status updates</p>
        </div>
        <Button variant="ghost" size="sm" onClick={() => { setEmail(""); setOrders([]) }}>
          Change email
        </Button>
      </div>

      {!orders ? (
        <p className="py-8 text-center text-sm text-muted-foreground">Loading…</p>
      ) : sorted.length === 0 ? (
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            No orders for this email yet.
          </CardContent>
        </Card>
      ) : (
        sorted.map((o) => (
          <OrderCard
            key={o.id}
            order={o}
            expanded={expanded === o.id}
            onToggle={() => setExpanded(expanded === o.id ? null : o.id)}
          />
        ))
      )}
    </div>
  )
}

function OrderCard({
  order,
  expanded,
  onToggle,
}: {
  order: OrderRow
  expanded: boolean
  onToggle: () => void
}) {
  const [items, setItems] = useState<OrderItemRow[] | null>(null)

  useEffect(() => {
    if (!expanded || items !== null) return
    ;(async () => {
      try {
        const res = await spine.queryTable("order_items", {
          where: `order_id:${order.id}`,
          limit: 100,
        })
        setItems((res.rows ?? []) as unknown as OrderItemRow[])
      } catch {
        setItems([])
      }
    })()
  }, [expanded, items, order.id])

  // Phase 6: the order row carries SERVER-computed totals; older rows (or
  // partial fetches) fall back to summing the line items.
  const serverTotal = Number(order.total)
  const hasServerTotal = Number.isFinite(serverTotal) && serverTotal > 0
  const lineSum = (items ?? []).reduce(
    (s, it) => s + Number(it.line_total ?? Number(it.price) * Number(it.qty)),
    0
  )
  const discount = Number(order.coupon_discount ?? 0)
  const total = hasServerTotal ? serverTotal : lineSum

  return (
    <Card>
      <button className="w-full text-left focus:outline-none" onClick={onToggle}>
        <CardHeader className="pb-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="flex items-center gap-2 text-base">
              Order <code className="rounded bg-muted px-1.5 py-0.5 text-xs">{order.id.slice(0, 8)}</code>
              <ChevronDown className={`h-4 w-4 transition-transform ${expanded ? "rotate-180" : ""}`} />
            </CardTitle>
            <div className="flex items-center gap-2">
              <span className="text-xs text-muted-foreground">{timeAgo(order.created_at)}</span>
              <Badge variant={statusColor(order.status)}>{order.status}</Badge>
            </div>
          </div>
          {discount > 0 && (
            <CardDescription>Coupon {order.coupon_code} — −{money(discount)}</CardDescription>
          )}
        </CardHeader>
      </button>
      {expanded && (
        <CardContent className="space-y-1 border-t pt-3">
          {items === null ? (
            <p className="py-3 text-sm text-muted-foreground">Loading items…</p>
          ) : (
            <>
              {items.map((it) => (
                <div key={it.product_id + String(it.id)} className="flex items-center justify-between py-1 text-sm">
                  <span>
                    {it.name} <span className="text-muted-foreground">× {it.qty}</span>
                  </span>
                  <span className="tabular-nums">{money(Number(it.price) * Number(it.qty))}</span>
                </div>
              ))}
              {items.length === 0 && (
                <p className="py-3 text-sm text-muted-foreground">No line items recorded.</p>
              )}
              {hasServerTotal && (
                <div className="space-y-1 border-t pt-2 text-sm text-muted-foreground">
                  <div className="flex justify-between">
                    <span>Subtotal</span>
                    <span className="tabular-nums">{money(Number(order.subtotal))}</span>
                  </div>
                  <div className="flex justify-between">
                    <span>Shipping</span>
                    <span className="tabular-nums">{money(Number(order.shipping_cost))}</span>
                  </div>
                  <div className="flex justify-between">
                    <span>Tax</span>
                    <span className="tabular-nums">{money(Number(order.tax_amount))}</span>
                  </div>
                  {discount > 0 && (
                    <div className="flex justify-between text-green-600">
                      <span>Coupon {order.coupon_code}</span>
                      <span className="tabular-nums">−{money(discount)}</span>
                    </div>
                  )}
                </div>
              )}
              <div className="flex justify-between border-t pt-2 text-sm font-semibold">
                <span>Total</span>
                <span>{money(total)}</span>
              </div>
            </>
          )}
        </CardContent>
      )}
    </Card>
  )
}
