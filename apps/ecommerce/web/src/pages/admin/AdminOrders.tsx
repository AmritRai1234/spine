import { useCallback, useEffect, useState } from "react"
import { Ban, ChevronDown, ChevronLeft, ChevronRight, Download, Eye } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useSpineStateTick } from "@/hooks/use-spine"
import { toast } from "sonner"
import { adminClient } from "@/lib/admin"
import { downloadCSV } from "@/lib/csv"
import { money, shortId, timeAgo } from "@/lib/format"
import {
  ORDER_STATUSES,
  statusColor,
  type OrderItemRow,
  type OrderRow,
} from "@/types"

const PAGE_SIZE = 25

export default function AdminOrders() {
  const [orders, setOrders] = useState<OrderRow[] | null>(null)
  const [itemsByOrder, setItemsByOrder] = useState<Record<string, OrderItemRow[]>>({})
  const [filter, setFilter] = useState<string>("all")
  const [page, setPage] = useState(0)
  const [hasMore, setHasMore] = useState(false)
  const [detail, setDetail] = useState<OrderRow | null>(null)

  const createdTick = useSpineStateTick("ORDER_CREATED")
  const statusTick = useSpineStateTick("ORDER_STATUS_CHANGED")

  // Server-side pagination: orders page from the engine (limit/offset),
  // then one targeted items query per visible order — no 500-row slurp.
  const load = useCallback(async () => {
    setOrders(null)
    const client = adminClient()
    const params: Record<string, string | number> = {
      limit: PAGE_SIZE,
      offset: page * PAGE_SIZE,
    }
    if (filter !== "all") params.where = `status:${filter}`
    try {
      const o = await client.queryTable("orders", params)
      const rows = (o.rows ?? []) as unknown as OrderRow[]
      setOrders(rows)
      setHasMore(rows.length === PAGE_SIZE)

      const itemResults = await Promise.all(
        rows.map((o) =>
          client.queryTable("order_items", { where: `order_id:${o.id}`, limit: 100 })
        )
      )
      const map: Record<string, OrderItemRow[]> = {}
      rows.forEach((o, i) => {
        map[o.id] = (itemResults[i].rows ?? []) as unknown as OrderItemRow[]
      })
      setItemsByOrder(map)
    } catch {
      setOrders([])
    }
  }, [filter, page])

  useEffect(() => {
    load()
  }, [load, createdTick, statusTick])

  useEffect(() => {
    setPage(0) // filter change resets paging
  }, [filter])

  // Phase 6: prefer the SERVER-computed order.total; older rows fall back to
  // summing line items (line_total when present, else price × qty).
  const orderTotal = useCallback(
    (orderId: string) => {
      const order = orders?.find((o) => o.id === orderId)
      const serverTotal = Number(order?.total)
      if (Number.isFinite(serverTotal) && serverTotal > 0) return serverTotal
      const lines = itemsByOrder[orderId] ?? []
      return lines.reduce((s, it) => s + Number(it.line_total ?? Number(it.price) * Number(it.qty)), 0)
    },
    [itemsByOrder, orders]
  )

  function exportCSV() {
    if (!orders || orders.length === 0) return
    downloadCSV(
      `orders-${new Date().toISOString().slice(0, 10)}.csv`,
      orders.map((o) => ({
        id: o.id,
        email: o.email,
        status: o.status,
        total: orderTotal(o.id).toFixed(2),
        coupon: o.coupon_code ?? "",
        ship_name: o.ship_name ?? "",
        address1: o.address1 ?? "",
        city: o.city ?? "",
        country: o.country ?? "",
        zip: o.zip ?? "",
        placed_at: o.created_at,
      }))
    )
    toast.success(`Exported ${orders.length} orders`)
  }

  async function setStatus(order: OrderRow, status: string) {
    await adminClient().emit("UPDATE_ORDER_STATUS", {
      id: order.id,
      status,
      actor: "admin@panel",
    })
    toast.success(`Order ${shortId(order.id)} → ${status}`)
  }

  /**
   * Cancel & restock: restores inventory per line via RESTOCK_ORDER_ITEM
   * (engine db.adjust), then flips the order to cancelled. Only offered on
   * non-cancelled orders so stock is never double-restored.
   */
  async function cancelAndRestock(order: OrderRow) {
    const lines = itemsByOrder[order.id] ?? []
    for (const line of lines) {
      await adminClient().emit("RESTOCK_ORDER_ITEM", {
        order_id: order.id,
        product_id: line.product_id,
        qty: Number(line.qty),
        actor: "admin@panel",
      })
    }
    await adminClient().emit("UPDATE_ORDER_STATUS", {
      id: order.id,
      status: "cancelled",
      actor: "admin@panel",
    })
    toast.success(`Order ${shortId(order.id)} cancelled${lines.length ? ` · ${lines.length} line(s) restocked` : ""}`)
    setDetail(null)
  }

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 space-y-4 duration-300">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-xl font-semibold tracking-tight">Orders</h2>
        <div className="flex flex-wrap items-center gap-3">
          <Tabs value={filter} onValueChange={setFilter}>
            <TabsList>
              <TabsTrigger value="all">All</TabsTrigger>
              {ORDER_STATUSES.map((s) => (
                <TabsTrigger key={s} value={s} className="capitalize">{s}</TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
          <Button variant="outline" size="sm" onClick={exportCSV}>
            <Download className="mr-1 h-4 w-4" /> CSV
          </Button>
        </div>
      </div>

      {!orders ? (
        <Skeleton className="h-96" />
      ) : (
        <Card>
          <CardContent className="pt-6">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Order</TableHead>
                  <TableHead>Customer</TableHead>
                  <TableHead>Items</TableHead>
                  <TableHead>Total</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="text-right">Placed</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {orders.map((o) => (
                  <TableRow key={o.id} className="group">
                    <TableCell>
                      <button
                        className="inline-flex items-center gap-1.5 font-medium hover:underline"
                        onClick={() => setDetail(o)}
                      >
                        <Eye className="h-3.5 w-3.5 opacity-0 transition-opacity group-hover:opacity-100" />
                        <code>{shortId(o.id)}</code>
                      </button>
                    </TableCell>
                    <TableCell>{o.email}</TableCell>
                    <TableCell className="tabular-nums">
                      {(itemsByOrder[o.id]?.length ?? 0) || "—"}
                    </TableCell>
                    <TableCell className="font-medium tabular-nums">{money(orderTotal(o.id))}</TableCell>
                    <TableCell>
                      <Badge variant={statusColor(o.status)}>{o.status}</Badge>
                    </TableCell>
                    <TableCell colSpan={2} className="text-right whitespace-nowrap text-muted-foreground">
                      <span className="mr-2 inline-flex items-center gap-2">
                        {timeAgo(o.created_at)}
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button variant="outline" size="sm" className="h-7 capitalize">
                              {o.status} <ChevronDown className="ml-1 h-3 w-3" />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuLabel>Move to</DropdownMenuLabel>
                            {ORDER_STATUSES.filter((s) => s !== o.status).map((s) => (
                              <DropdownMenuItem key={s} className="capitalize" onClick={() => setStatus(o, s)}>
                                {s}
                              </DropdownMenuItem>
                            ))}
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </span>
                    </TableCell>
                  </TableRow>
                ))}
                {orders.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={6} className="py-12 text-center text-muted-foreground">
                      No {filter === "all" ? "" : filter + " "}orders.
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>

            {/* Pager */}
            <div className="flex items-center justify-end gap-2 pt-4 text-sm text-muted-foreground">
              <span>page {page + 1}</span>
              <Button variant="outline" size="sm" disabled={page === 0} onClick={() => setPage(page - 1)}>
                <ChevronLeft className="h-4 w-4" /> Prev
              </Button>
              <Button variant="outline" size="sm" disabled={!hasMore} onClick={() => setPage(page + 1)}>
                Next <ChevronRight className="h-4 w-4" />
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      {/* Order detail sheet */}
      <Sheet open={!!detail} onOpenChange={(o) => !o && setDetail(null)}>
        <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-lg">
          <SheetHeader>
            <SheetTitle className="flex items-center gap-2">
              Order <code className="rounded bg-muted px-1.5 py-0.5 text-sm">{shortId(detail?.id, 12)}</code>
            </SheetTitle>
            <SheetDescription>
              {detail?.email} · placed {timeAgo(detail?.created_at)}
            </SheetDescription>
          </SheetHeader>
          {detail && (
            <div className="space-y-4 px-4 pb-8">
              <div className="flex items-center justify-between rounded-md border p-3">
                <span className="text-sm text-muted-foreground">Status</span>
                <Badge variant={statusColor(detail.status)}>{detail.status}</Badge>
              </div>

              {(detail.ship_name || detail.address1) && (
                <div className="rounded-md border p-3 text-sm">
                  <p className="mb-1 font-medium">Shipping address</p>
                  <p className="text-muted-foreground">{detail.ship_name}</p>
                  <p className="text-muted-foreground">{detail.address1}</p>
                  <p className="text-muted-foreground">
                    {[detail.city, detail.country, detail.zip].filter(Boolean).join(", ")}
                  </p>
                </div>
              )}

              <div className="overflow-hidden rounded-md border">
                {(itemsByOrder[detail.id] ?? []).map((it) => (
                  <div key={it.id ?? it.product_id} className="flex items-center justify-between border-b px-3 py-2 text-sm last:border-0">
                    <div>
                      <p className="font-medium">{it.name}</p>
                      <p className="text-xs text-muted-foreground">{money(it.price)} × {it.qty}</p>
                    </div>
                    <span className="font-medium tabular-nums">{money(Number(it.price) * Number(it.qty))}</span>
                  </div>
                ))}
                {(itemsByOrder[detail.id]?.length ?? 0) === 0 && (
                  <p className="px-3 py-6 text-center text-sm text-muted-foreground">No line items recorded.</p>
                )}
              </div>
              <div className="space-y-1 border-t pt-3 text-sm">
                {Number(detail.subtotal) > 0 && (
                  <div className="flex justify-between text-muted-foreground">
                    <span>Subtotal</span>
                    <span className="tabular-nums">{money(Number(detail.subtotal))}</span>
                  </div>
                )}
                {Number(detail.shipping_cost) > 0 && (
                  <div className="flex justify-between text-muted-foreground">
                    <span>Shipping</span>
                    <span className="tabular-nums">{money(Number(detail.shipping_cost))}</span>
                  </div>
                )}
                {Number(detail.tax_amount) > 0 && (
                  <div className="flex justify-between text-muted-foreground">
                    <span>Tax</span>
                    <span className="tabular-nums">{money(Number(detail.tax_amount))}</span>
                  </div>
                )}
                {Number(detail.coupon_discount) > 0 && (
                  <div className="flex justify-between text-green-600">
                    <span>Coupon {detail.coupon_code}</span>
                    <span className="tabular-nums">−{money(Number(detail.coupon_discount))}</span>
                  </div>
                )}
                <div className="flex justify-between pt-1 text-base font-semibold">
                  <span>Total</span>
                  <span>{money(orderTotal(detail.id))}</span>
                </div>
              </div>
              <div className="flex flex-wrap gap-2 pt-2">
                {ORDER_STATUSES.filter((s) => s !== detail.status).map((s) => (
                  <Button key={s} size="sm" variant={s === "cancelled" ? "destructive" : "outline"} className="capitalize" onClick={() => { setStatus(detail, s); setDetail({ ...detail, status: s }) }}>
                    Mark {s}
                  </Button>
                ))}
              </div>
              {detail.status !== "cancelled" && (
                <Button
                  variant="destructive"
                  className="w-full"
                  onClick={() => cancelAndRestock(detail)}
                >
                  <Ban className="mr-2 h-4 w-4" />
                  Cancel &amp; restock
                </Button>
              )}
            </div>
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}
