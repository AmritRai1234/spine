import { useCallback, useEffect, useMemo, useState } from "react"
import { Package, Search, ShoppingCart } from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useSpineStateTick } from "@/hooks/use-spine"
import { spine } from "@/lib/spine"
import { getCartId } from "@/lib/cart"
import { money } from "@/lib/format"
import type { Product } from "@/types"

interface CatalogProps {
  onAddToCart: (p: Product) => void
  onViewProduct: (p: Product) => void
}

/** Live product grid with search + category chips — refetches on PUBLISH_PRODUCT. */
export default function Catalog({ onAddToCart, onViewProduct }: CatalogProps) {
  const [products, setProducts] = useState<Product[] | null>(null)
  const [query, setQuery] = useState("")
  const [category, setCategory] = useState<string>("all")

  // Live refresh: engine broadcasts PRODUCT_PUBLISHED after admin upserts
  const tick = useSpineStateTick("PRODUCT_PUBLISHED")
  const stockTick = useSpineStateTick("STOCK_ADJUSTED")

  const load = useCallback(async () => {
    try {
      const res = await spine.queryTable("products", { limit: 200 })
      setProducts((res.rows ?? []) as unknown as Product[])
    } finally {
      if (products === null) setProducts([])
    }
  }, [products])

  useEffect(() => {
    load()
  }, [load, tick, stockTick])

  const categories = useMemo(() => {
    if (!products) return []
    return [...new Set(products.map((p) => String(p.category ?? "")).filter(Boolean))].sort()
  }, [products])

  const visible = useMemo(() => {
    if (!products) return []
    const q = query.trim().toLowerCase()
    return products.filter((p) => {
      const inCategory = category === "all" || String(p.category ?? "") === category
      const inQuery =
        !q ||
        [p.name, p.sku, p.description].some((v) => String(v ?? "").toLowerCase().includes(q))
      return inCategory && inQuery
    })
  }, [products, query, category])

  async function addToCart(p: Product) {
    const res = await spine.emit("ADD_TO_CART", {
      cart_id: getCartId(),
      product_id: p.id,
      variant_id: "",
      variant_label: "",
      name: p.name,
      price: p.price,
      qty: 1,
    })
    if (res.status !== "ok") {
      toast.error(res.error ?? "Could not add to cart")
      return
    }
    onAddToCart(p)
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <Package className="h-6 w-6" />
          <h2 className="text-2xl font-semibold tracking-tight">All products</h2>
          {products && (
            <span className="text-sm text-muted-foreground">
              {visible.length} of {products.length}
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          <div className="relative">
            <Search className="absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              placeholder="Search products…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="w-56 pl-8"
            />
          </div>
        </div>
      </div>

      {categories.length > 0 && (
        <div className="flex flex-wrap gap-2">
          <button onClick={() => setCategory("all")} className="focus:outline-none">
            <Badge variant={category === "all" ? "default" : "outline"}>All</Badge>
          </button>
          {categories.map((c) => (
            <button key={c} onClick={() => setCategory(c)} className="focus:outline-none">
              <Badge variant={category === c ? "default" : "outline"}>{c}</Badge>
            </button>
          ))}
        </div>
      )}

      {!products ? (
        <div id="catalog-grid" className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-5 2xl:grid-cols-6">
          {[...Array(10)].map((_, i) => <Skeleton key={i} className="h-64" />)}
        </div>
      ) : visible.length === 0 ? (
        <Card>
          <CardHeader>
            <CardTitle>{query || category !== "all" ? "No matches" : "No products yet"}</CardTitle>
            <CardDescription>
              {query || category !== "all" ? (
                "Try a different search or category."
              ) : (
                <>
                  Publish the first product with the seed script or an admin
                  <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">PUBLISH_PRODUCT</code>
                  event.
                </>
              )}
            </CardDescription>
          </CardHeader>
        </Card>
      ) : (
        <div id="catalog-grid" className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-5 2xl:grid-cols-6">
          {visible.map((p) => (
            <Card key={p.id} className="flex flex-col transition-shadow hover:shadow-md">
              <div
                className="cursor-pointer overflow-hidden rounded-t-xl bg-muted"
                onClick={() => onViewProduct(p)}
              >
                {p.image_url ? (
                  <img
                    src={p.image_url}
                    alt={p.name}
                    loading="lazy"
                    className="aspect-square w-full object-cover transition-transform hover:scale-105"
                  />
                ) : (
                  <div className="flex aspect-square items-center justify-center text-3xl text-muted-foreground">
                    {String(p.name ?? "?").slice(0, 1)}
                  </div>
                )}
              </div>
              <CardHeader className="pb-2">
                {p.category && (
                  <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
                    {p.category}
                  </p>
                )}
                <CardTitle
                  className="cursor-pointer text-base hover:underline"
                  onClick={() => onViewProduct(p)}
                >
                  {p.name}
                </CardTitle>
                <CardDescription>SKU: {p.sku}</CardDescription>
              </CardHeader>
              <CardContent className="flex-1">
                <div className="flex items-center gap-2">
                  <span className="text-lg font-bold">{money(Number(p.price))}</span>
                  <Badge variant={Number(p.stock) > 0 ? "secondary" : "destructive"}>
                    {Number(p.stock) > 0 ? `${p.stock} in stock` : "out of stock"}
                  </Badge>
                </div>
              </CardContent>
              <CardFooter className="gap-2">
                <Button
                  variant="outline"
                  className="flex-1"
                  onClick={() => onViewProduct(p)}
                >
                  Details
                </Button>
                <Button
                  className="flex-1"
                  disabled={Number(p.stock) <= 0}
                  onClick={() => addToCart(p)}
                >
                  <ShoppingCart className="mr-2 h-4 w-4" />
                  Add
                </Button>
              </CardFooter>
            </Card>
          ))}
        </div>
      )}
    </div>
  )
}
