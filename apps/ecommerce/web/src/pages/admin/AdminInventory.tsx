import { useCallback, useEffect, useMemo, useState } from "react"
import { AlertTriangle, Minus, PackageSearch, Plus, Search } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import EmptyState from "@/components/EmptyState"
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
import { adminClient } from "@/lib/admin"
import { money } from "@/lib/format"
import type { Product } from "@/types"

const LOW_STOCK_THRESHOLD = 10

/**
 * Inventory — Shopify-style stock management over the products table.
 * Adjustments reuse the admin PUBLISH_PRODUCT upsert (read-modify-write on
 * stock), identical to the +/- controls on the Products page.
 */
export default function AdminInventory() {
  const [products, setProducts] = useState<Product[] | null>(null)
  const [query, setQuery] = useState("")
  const [onlyLow, setOnlyLow] = useState(false)
  const [busySku, setBusySku] = useState<string | null>(null)

  const load = useCallback(async () => {
    const res = await adminClient().queryTable("products", { limit: 500 })
    setProducts(
      ((res.rows ?? []) as unknown as Product[]).map((p) => ({
        ...p,
        price: Number(p.price),
        stock: Number(p.stock),
      })),
    )
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const visible = useMemo(() => {
    let list = products ?? []
    if (onlyLow) list = list.filter((p) => p.stock < LOW_STOCK_THRESHOLD)
    const q = query.trim().toLowerCase()
    if (q) {
      list = list.filter(
        (p) =>
          p.name.toLowerCase().includes(q) ||
          p.sku.toLowerCase().includes(q) ||
          (p.category ?? "").toLowerCase().includes(q),
      )
    }
    return [...list].sort((a, b) => a.stock - b.stock) // lowest stock first
  }, [products, query, onlyLow])

  const lowCount = useMemo(
    () => (products ?? []).filter((p) => p.stock < LOW_STOCK_THRESHOLD).length,
    [products],
  )

  async function adjust(p: Product, delta: number) {
    setBusySku(p.sku)
    const next = Math.max(0, Number(p.stock) + delta)
    try {
      await adminClient().emit("PUBLISH_PRODUCT", {
        sku: p.sku,
        name: p.name,
        price: Number(p.price),
        stock: next,
        description: p.description ?? "",
        image_url: p.image_url ?? "",
        category: p.category ?? "",
      })
      // Optimistic update — the WS state tick will reconcile.
      setProducts((prev) =>
        (prev ?? []).map((x) => (x.sku === p.sku ? { ...x, stock: next } : x)),
      )
      toast.success(`${p.name}: stock ${delta > 0 ? "+" : ""}${delta} → ${next}`)
    } catch {
      toast.error("Adjustment failed")
    } finally {
      setBusySku(null)
    }
  }

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 space-y-4 duration-300">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-xl font-semibold tracking-tight">Inventory</h2>
          <p className="text-sm text-muted-foreground">
            {products?.length ?? 0} SKUs · {lowCount} low on stock
          </p>
        </div>
        <div className="flex items-center gap-2">
          <div className="relative">
            <Search className="absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              placeholder="Search name, SKU, category…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="w-64 pl-8"
            />
          </div>
          <Button
            variant={onlyLow ? "default" : "outline"}
            size="sm"
            onClick={() => setOnlyLow((v) => !v)}
          >
            <AlertTriangle className="mr-1 h-4 w-4" /> Low stock
          </Button>
        </div>
      </div>

      {!products ? (
        <Skeleton className="h-96" />
      ) : visible.length === 0 ? (
        <EmptyState
          icon={PackageSearch}
          title={onlyLow ? "Nothing is running low" : "No inventory yet"}
          description={
            onlyLow
              ? `Every SKU has at least ${LOW_STOCK_THRESHOLD} units on hand. The filter clears itself once you restock.`
              : "Inventory mirrors your product catalog. Add products first and their stock shows up here."
          }
        />
      ) : (
        <Card>
          <CardContent className="pt-6">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Product</TableHead>
                  <TableHead>SKU</TableHead>
                  <TableHead>Category</TableHead>
                  <TableHead className="text-right">Price</TableHead>
                  <TableHead className="text-center">Stock</TableHead>
                  <TableHead className="text-right">Adjust</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {visible.map((p) => (
                  <TableRow key={p.sku}>
                    <TableCell className="font-medium">{p.name}</TableCell>
                    <TableCell>
                      <code className="rounded bg-muted px-1.5 py-0.5 text-xs">{p.sku}</code>
                    </TableCell>
                    <TableCell className="text-muted-foreground">{p.category || "—"}</TableCell>
                    <TableCell className="text-right tabular-nums">{money(p.price)}</TableCell>
                    <TableCell className="text-center">
                      <span
                        className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-semibold tabular-nums ${
                          p.stock === 0
                            ? "bg-destructive/10 text-destructive"
                            : p.stock < LOW_STOCK_THRESHOLD
                              ? "bg-amber-500/10 text-amber-600 dark:text-amber-400"
                              : "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
                        }`}
                      >
                        {p.stock === 0 && <AlertTriangle className="h-3 w-3" />}
                        {p.stock}
                      </span>
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="inline-flex items-center gap-1">
                        <Button
                          variant="outline"
                          size="icon"
                          className="h-7 w-7"
                          disabled={busySku === p.sku || p.stock === 0}
                          onClick={() => adjust(p, -1)}
                        >
                          <Minus className="h-3.5 w-3.5" />
                        </Button>
                        <Button
                          variant="outline"
                          size="icon"
                          className="h-7 w-7"
                          disabled={busySku === p.sku}
                          onClick={() => adjust(p, +1)}
                        >
                          <Plus className="h-3.5 w-3.5" />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
