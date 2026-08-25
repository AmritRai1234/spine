import { useCallback, useEffect, useMemo, useState } from "react"
import { ChevronRight, Download } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import { useSpineStateTick } from "@/hooks/use-spine"
import { toast } from "sonner"
import { adminClient } from "@/lib/admin"
import { downloadCSV } from "@/lib/csv"
import { money, timeAgo } from "@/lib/format"
import { statusColor, type OrderItemRow, type OrderRow } from "@/types"

interface CustomerAgg {
  email: string
  orders: number
  spend: number
  lastOrder: string
  firstOrder: string
}

const PAGE = 200

/** Walk an entire table through engine-side pagination (limit/offset loop). */
async function fetchAllPaged(
  client: ReturnType<typeof adminClient>,
  table: string,
  pageSize = PAGE
): Promise<Record<string, unknown>[]> {
  const out: Record<string, unknown>[] = []
  let offset = 0
  for (;;) {
    const res = await client.queryTable(table, { limit: pageSize, offset })
    const rows = res.rows ?? []
    out.push(...rows)
    if (rows.length < pageSize) break
    offset += pageSize
  }
  return out
}

export default function AdminCustomers() {
  const [orders, setOrders] = useState<OrderRow[] | null>(null)
  const [items, setItems] = useState<OrderItemRow[]>([])
  const [drillDown, setDrillDown] = useState<CustomerAgg | null>(null)
  const orderTick = useSpineStateTick("ORDER_CREATED")

  const load = useCallback(async () => {
    setOrders(null)
    const client = adminClient()
    try {
      const [oRows, iRows] = await Promise.all([
        fetchAllPaged(client, "orders"),
        fetchAllPaged(client, "order_items"),
      ])
      setOrders(oRows as unknown as OrderRow[])
      setItems(iRows as unknown as OrderItemRow[])
    } catch {
      setOrders([])
    }
  }, [])

  useEffect(() => {
    load()
  }, [load, orderTick])

  const customers = useMemo<CustomerAgg[]>(() => {
    if (!orders) return []
    const statusById = new Map(orders.map((o) => [o.id, o]))
    const byEmail = new Map<string, CustomerAgg>()
    for (const it of items) {
      const order = statusById.get(it.order_id)
      if (!order || order.status === "cancelled") continue
      let agg = byEmail.get(order.email)
      if (!agg) {
        agg = { email: order.email, orders: 0, spend: 0, lastOrder: order.created_at, firstOrder: order.created_at }
        byEmail.set(order.email, agg)
      }
      agg.orders += 1
      agg.spend += Number(it.price) * Number(it.qty)
      if (order.created_at > agg.lastOrder) agg.lastOrder = order.created_at
      if (order.created_at < agg.firstOrder) agg.firstOrder = order.created_at
    }
    return [...byEmail.values()].sort((a, b) => b.spend - a.spend)
  }, [orders, items])

  const drillDownOrders = useMemo(() => {
    if (!drillDown || !orders) return []
    return orders
      .filter((o) => o.email === drillDown.email)
      .sort((a, b) => String(b.created_at).localeCompare(String(a.created_at)))
  }, [drillDown, orders])

  function exportCSV() {
    if (customers.length === 0) return
    downloadCSV(
      `customers-${new Date().toISOString().slice(0, 10)}.csv`,
      customers.map((c) => ({
        email: c.email,
        orders: c.orders,
        lifetime_spend: c.spend.toFixed(2),
        first_order: c.firstOrder,
        last_order: c.lastOrder,
      }))
    )
    toast.success(`Exported ${customers.length} customers`)
  }

  if (!orders) return <Skeleton className="h-96" />

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 space-y-4 duration-300">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-xl font-semibold tracking-tight">Customers</h2>
          <p className="text-sm text-muted-foreground">Derived from order emails · {customers.length} total</p>
        </div>
        <Button variant="outline" size="sm" onClick={exportCSV} disabled={customers.length === 0}>
          <Download className="mr-1 h-4 w-4" /> CSV
        </Button>
      </div>
      <Card>
        <CardContent className="pt-6">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b text-left text-muted-foreground">
                <th className="pb-2 pl-2 font-medium">Email</th>
                <th className="pb-2 font-medium">Orders</th>
                <th className="pb-2 font-medium">Lifetime spend</th>
                <th className="hidden pb-2 font-medium sm:table-cell">First order</th>
                <th className="pb-2 pr-2 text-right font-medium">Last order</th>
              </tr>
            </thead>
            <tbody>
              {customers.map((c) => (
                <tr
                  key={c.email}
                  className="cursor-pointer border-b transition-colors last:border-0 hover:bg-muted/50"
                  onClick={() => setDrillDown(c)}
                >
                  <td className="py-2.5 pl-2 font-medium">{c.email}</td>
                  <td className="tabular-nums">{c.orders}</td>
                  <td className="font-semibold tabular-nums">{money(c.spend)}</td>
                  <td className="hidden text-muted-foreground sm:table-cell">{timeAgo(c.firstOrder)}</td>
                  <td className="pr-2 text-right text-muted-foreground">{timeAgo(c.lastOrder)}</td>
                </tr>
              ))}
              {customers.length === 0 && (
                <tr><td colSpan={5} className="py-12 text-center text-muted-foreground">No customers yet.</td></tr>
              )}
            </tbody>
          </table>
        </CardContent>
      </Card>

      {/* Customer drill-down */}
      <Sheet open={!!drillDown} onOpenChange={(o) => !o && setDrillDown(null)}>
        <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-lg">
          <SheetHeader>
            <SheetTitle>{drillDown?.email}</SheetTitle>
            <SheetDescription>
              {drillDown && `${drillDown.orders} order(s) · ${money(drillDown.spend)} lifetime`}
            </SheetDescription>
          </SheetHeader>
          <div className="space-y-3 px-4 pb-8">
            {drillDownOrders.map((o) => {
              const lines = items.filter((it) => it.order_id === o.id)
              const gross = lines.reduce((s, it) => s + Number(it.price) * Number(it.qty), 0)
              const pct = Number(o.coupon_discount ?? 0)
              const total = Math.max(0, gross - (gross * pct) / 100)
              return (
                <div key={o.id} className="rounded-md border p-3 text-sm">
                  <div className="mb-2 flex items-center justify-between">
                    <span className="flex items-center gap-1.5">
                      <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
                      <code className="text-xs">{o.id.slice(0, 8)}</code>
                      {o.coupon_code && <Badge variant="outline">{o.coupon_code} −{pct}%</Badge>}
                    </span>
                    <span className="flex items-center gap-2">
                      <span className="text-xs text-muted-foreground">{timeAgo(o.created_at)}</span>
                      <Badge variant={statusColor(o.status)}>{o.status}</Badge>
                    </span>
                  </div>
                  {lines.map((it) => (
                    <div key={it.product_id + String(it.id)} className="flex justify-between py-0.5 text-xs text-muted-foreground">
                      <span>{it.name} × {it.qty}</span>
                      <span className="tabular-nums">{money(Number(it.price) * Number(it.qty))}</span>
                    </div>
                  ))}
                  <div className="mt-1 flex justify-between border-t pt-1.5 font-medium">
                    <span>Total</span>
                    <span className="tabular-nums">{money(total)}</span>
                  </div>
                </div>
              )
            })}
            {drillDown && drillDownOrders.length === 0 && (
              <p className="py-8 text-center text-sm text-muted-foreground">No orders found.</p>
            )}
          </div>
        </SheetContent>
      </Sheet>
    </div>
  )
}
