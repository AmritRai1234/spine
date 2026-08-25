import { useCallback, useEffect, useMemo, useState } from "react"
import {
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts"

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useSpineStateTick } from "@/hooks/use-spine"
import { adminClient } from "@/lib/admin"
import { money } from "@/lib/format"
import {
  ORDER_STATUSES,
  type OrderItemRow,
  type OrderRow,
} from "@/types"

const PIE_COLORS = ["#8b5cf6", "#06b6d4", "#f59e0b", "#10b981", "#ef4444"]

type Metric = "revenue" | "units"

export default function AdminAnalytics() {
  const [orders, setOrders] = useState<OrderRow[] | null>(null)
  const [items, setItems] = useState<OrderItemRow[]>([])
  const [metric, setMetric] = useState<Metric>("revenue")
  const orderTick = useSpineStateTick("ORDER_CREATED")

  const load = useCallback(async () => {
    const client = adminClient()
    const [o, i] = await Promise.all([
      client.queryTable("orders", { limit: 500 }),
      client.queryTable("order_items", { limit: 1000 }),
    ])
    setOrders((o.rows ?? []) as unknown as OrderRow[])
    setItems((i.rows ?? []) as unknown as OrderItemRow[])
  }, [])

  useEffect(() => {
    load()
  }, [load, orderTick])

  const topProducts = useMemo(() => {
    if (!items) return []
    const byProduct = new Map<string, { name: string; units: number; revenue: number }>()
    for (const it of items) {
      const key = it.product_id
      const agg = byProduct.get(key) ?? { name: it.name, units: 0, revenue: 0 }
      agg.units += Number(it.qty)
      agg.revenue += Number(it.price) * Number(it.qty)
      byProduct.set(key, agg)
    }
    return [...byProduct.values()]
      .sort((a, b) => (metric === "revenue" ? b.revenue - a.revenue : b.units - a.units))
      .slice(0, 6)
  }, [items, metric])

  const statusSplit = useMemo(() => {
    if (!orders) return []
    const counts = new Map<string, number>()
    for (const o of orders) counts.set(o.status, (counts.get(o.status) ?? 0) + 1)
    return ORDER_STATUSES
      .map((s) => ({ name: s, value: counts.get(s) ?? 0 }))
      .filter((d) => d.value > 0)
  }, [orders])

  if (!orders || !items) return <Skeleton className="h-96" />

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 space-y-4 duration-300">
      <div className="flex items-center justify-between">
        <h2 className="text-xl font-semibold tracking-tight">Analytics</h2>
        <Tabs value={metric} onValueChange={(v) => setMetric(v as Metric)}>
          <TabsList>
            <TabsTrigger value="revenue">Revenue</TabsTrigger>
            <TabsTrigger value="units">Units</TabsTrigger>
          </TabsList>
        </Tabs>
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader><CardTitle>Top products</CardTitle></CardHeader>
          <CardContent>
            {topProducts.length === 0 ? (
              <p className="py-16 text-center text-sm text-muted-foreground">No sales yet.</p>
            ) : (
              <div className="h-72">
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart data={topProducts} layout="vertical" margin={{ left: 12 }}>
                    <CartesianGrid strokeDasharray="3 3" horizontal={false} className="stroke-border" />
                    <XAxis
                      type="number"
                      fontSize={12}
                      tickLine={false}
                      axisLine={false}
                      tickFormatter={(v: number) => (metric === "revenue" ? money(v) : String(v))}
                    />
                    <YAxis type="category" dataKey="name" width={110} fontSize={11} tickLine={false} axisLine={false} />
                    <Tooltip
                      formatter={(v) => [metric === "revenue" ? money(Number(v)) : `${v} units`, metric]}
                      contentStyle={{ borderRadius: 8, border: "1px solid var(--border)", background: "var(--card)" }}
                    />
                    <Bar dataKey={metric === "revenue" ? "revenue" : "units"} radius={[0, 6, 6, 0]} animationDuration={500}>
                      {topProducts.map((_, i) => (
                        <Cell key={i} fill={PIE_COLORS[i % PIE_COLORS.length]} />
                      ))}
                    </Bar>
                  </BarChart>
                </ResponsiveContainer>
              </div>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader><CardTitle>Order statuses</CardTitle></CardHeader>
          <CardContent>
            {statusSplit.length === 0 ? (
              <p className="py-16 text-center text-sm text-muted-foreground">No orders yet.</p>
            ) : (
              <>
                <div className="h-52">
                  <ResponsiveContainer width="100%" height="100%">
                    <PieChart>
                      <Pie
                        data={statusSplit}
                        dataKey="value"
                        nameKey="name"
                        innerRadius={55}
                        outerRadius={85}
                        paddingAngle={3}
                        animationDuration={600}
                      >
                        {statusSplit.map((_, i) => (
                          <Cell key={i} fill={PIE_COLORS[i % PIE_COLORS.length]} />
                        ))}
                      </Pie>
                      <Tooltip contentStyle={{ borderRadius: 8, border: "1px solid var(--border)", background: "var(--card)" }} />
                    </PieChart>
                  </ResponsiveContainer>
                </div>
                <div className="mt-2 flex flex-wrap justify-center gap-x-4 gap-y-1">
                  {statusSplit.map((s, i) => (
                    <span key={s.name} className="flex items-center gap-1.5 text-xs text-muted-foreground capitalize">
                      <span className="h-2.5 w-2.5 rounded-full" style={{ background: PIE_COLORS[i % PIE_COLORS.length] }} />
                      {s.name} ({s.value})
                    </span>
                  ))}
                </div>
              </>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
