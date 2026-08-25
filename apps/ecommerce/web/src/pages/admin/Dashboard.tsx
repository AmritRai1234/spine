import { useCallback, useEffect, useMemo, useState } from "react"
import { AlertTriangle, DollarSign, Package, ShoppingCart } from "lucide-react"
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
} from "recharts"

import { Skeleton } from "@/components/ui/skeleton"
import { useSpineStateTick } from "@/hooks/use-spine"
import { adminClient } from "@/lib/admin"
import { money, moneyCompact, shortId, timeAgo } from "@/lib/format"
import { summarizeSales, useStoreSettings } from "@/lib/store"
import { statusColor, type OrderItemRow, type OrderRow, type Product } from "@/types"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"

function StatCard({
  title,
  value,
  sub,
  icon,
  accent,
}: {
  title: string
  value: string
  sub?: string
  icon: React.ReactNode
  accent?: boolean
}) {
  return (
    <Card className={accent ? "border-primary/30 bg-primary/[0.03]" : undefined}>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-sm font-medium text-muted-foreground">{title}</CardTitle>
        <span className="text-muted-foreground [&_svg]:h-4 [&_svg]:w-4">{icon}</span>
      </CardHeader>
      <CardContent>
        <div className="text-2xl font-bold tracking-tight">{value}</div>
        {sub && <p className="mt-1 text-xs text-muted-foreground">{sub}</p>}
      </CardContent>
    </Card>
  )
}

export default function Dashboard() {
  const [orders, setOrders] = useState<OrderRow[] | null>(null)
  const [items, setItems] = useState<OrderItemRow[] | null>(null)
  const [products, setProducts] = useState<Product[] | null>(null)
  const settings = useStoreSettings()

  const orderTick = useSpineStateTick("ORDER_CREATED")
  const productTick = useSpineStateTick("PRODUCT_PUBLISHED")

  const load = useCallback(async () => {
    const client = adminClient()
    const [o, i, p] = await Promise.all([
      client.queryTable("orders", { limit: 500 }),
      client.queryTable("order_items", { limit: 1000 }),
      client.queryTable("products", { limit: 500 }),
    ])
    setOrders((o.rows ?? []) as unknown as OrderRow[])
    setItems((i.rows ?? []) as unknown as OrderItemRow[])
    setProducts((p.rows ?? []) as unknown as Product[])
  }, [])

  useEffect(() => {
    load()
  }, [load, orderTick, productTick])

  const stats = useMemo(() => {
    if (!orders || !items || !products) return null
    const active = orders.filter((o) => o.status !== "cancelled")
    // Shared pipeline: cancellations + coupon discounts applied once for all
    const { revenue, series } = summarizeSales(orders, items)
    const aov = active.length > 0 ? revenue / active.length : 0
    const lowStock = products.filter((p) => Number(p.stock) < settings.low_stock_threshold)

    // Top products by server-computed line totals (excludes cancelled)
    const statusById = new Map(orders.map((o) => [o.id, o.status]))
    const byName = new Map<string, { qty: number; gross: number }>()
    for (const it of items) {
      if (statusById.get(it.order_id) === "cancelled") continue
      const g = Number(it.line_total ?? Number(it.price) * Number(it.qty))
      const cur = byName.get(it.name) ?? { qty: 0, gross: 0 }
      cur.qty += Number(it.qty)
      cur.gross += g
      byName.set(it.name, cur)
    }
    const topProducts = [...byName.entries()]
      .map(([name, v]) => ({ name, ...v }))
      .sort((a, b) => b.gross - a.gross)
      .slice(0, 5)

    return { revenue, aov, lowStock, series, topProducts }
  }, [orders, items, products, settings.low_stock_threshold])

  if (!stats) {
    return (
      <div className="space-y-6">
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {[...Array(4)].map((_, i) => <Skeleton key={i} className="h-28" />)}
        </div>
        <Skeleton className="h-72" />
        <Skeleton className="h-48" />
      </div>
    )
  }

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 space-y-6 duration-500">
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard title="Gross revenue" value={moneyCompact(stats.revenue)} icon={<DollarSign />} accent sub="excludes cancelled" />
        <StatCard title="Orders" value={String(orders!.length)} icon={<ShoppingCart />} sub={`${orders!.filter(o => o.status === "pending").length} pending`} />
        <StatCard title="Avg order value" value={moneyCompact(stats.aov)} icon={<Package />} />
        <StatCard title="Low stock" value={String(stats.lowStock.length)} icon={<AlertTriangle />} sub={stats.lowStock.length > 0 ? stats.lowStock.map(p => p.sku).slice(0, 3).join(", ") : "all healthy"} />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Sales over time</CardTitle>
        </CardHeader>
        <CardContent>
          {stats.series.length === 0 ? (
            <p className="py-16 text-center text-sm text-muted-foreground">No sales data yet — place an order on the storefront.</p>
          ) : (
            <div className="h-64">
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={stats.series} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
                  <defs>
                    <linearGradient id="revFill" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor="#10b981" stopOpacity={0.35} />
                      <stop offset="100%" stopColor="#10b981" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" className="stroke-border" vertical={false} />
                  <XAxis dataKey="day" fontSize={12} tickLine={false} axisLine={false} />
                  <Tooltip
                    formatter={(v) => [money(Number(v)), "Revenue"]}
                    contentStyle={{ borderRadius: 8, border: "1px solid var(--border)", background: "var(--card)" }}
                  />
                  <Area type="monotone" dataKey="total" stroke="#10b981" strokeWidth={2} fill="url(#revFill)" animationDuration={600} />
                </AreaChart>
              </ResponsiveContainer>
            </div>
          )}
        </CardContent>
      </Card>

      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Recent orders</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            {orders!.slice(0, 6).map((o) => (
              <div key={o.id} className="flex animate-in fade-in slide-in-from-left-2 items-center justify-between rounded-md border px-3 py-2 duration-300">
                <div className="flex min-w-0 items-center gap-3">
                  <code className="text-xs text-muted-foreground">{shortId(o.id)}</code>
                  <span className="truncate text-sm">{o.email}</span>
                </div>
                <div className="flex shrink-0 items-center gap-3">
                  <Badge variant={statusColor(o.status)}>{o.status}</Badge>
                  <span className="text-xs text-muted-foreground">{timeAgo(o.created_at)}</span>
                </div>
              </div>
            ))}
            {orders!.length === 0 && (
              <p className="py-8 text-center text-sm text-muted-foreground">No orders yet.</p>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Top products</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            {stats.topProducts.map((p, i) => (
              <div key={p.name} className="flex items-center justify-between rounded-md border px-3 py-2">
                <div className="flex min-w-0 items-center gap-3">
                  <span className="w-4 text-xs font-semibold text-muted-foreground">{i + 1}</span>
                  <span className="truncate text-sm">{p.name}</span>
                  <Badge variant="outline" className="shrink-0">{p.qty} sold</Badge>
                </div>
                <span className="shrink-0 text-sm font-medium tabular-nums">{money(p.gross)}</span>
              </div>
            ))}
            {stats.topProducts.length === 0 && (
              <p className="py-8 text-center text-sm text-muted-foreground">No sales yet.</p>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
