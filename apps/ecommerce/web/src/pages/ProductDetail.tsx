import { useEffect, useMemo, useState } from "react"
import { ArrowLeft, Minus, Plus, ShoppingCart } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { useSpineStateTick } from "@/hooks/use-spine"
import { toast } from "sonner"
import { spine } from "@/lib/spine"
import { getCartId } from "@/lib/cart"
import { money, parseGallery } from "@/lib/format"
import type { Product, ProductVariantRow } from "@/types"

interface ProductDetailProps {
  productId: string
  onBack: () => void
  onAddToCart: (p: Product) => void
}

/**
 * Full product view — live stock via PRODUCT_PUBLISHED / STOCK_ADJUSTED
 * broadcasts, quantity picker, add-to-cart. When the product has variants
 * (product_variants detail rows), an option matrix (Size × Color) drives the
 * displayed price + stock and the cart line carries variant_id.
 */
export default function ProductDetail({ productId, onBack, onAddToCart }: ProductDetailProps) {
  const [product, setProduct] = useState<Product | null>(null)
  const [variants, setVariants] = useState<ProductVariantRow[]>([])
  const [selectedVariantId, setSelectedVariantId] = useState<string | null>(null)
  const [qty, setQty] = useState(1)
  const [imgIndex, setImgIndex] = useState(0)

  const pubTick = useSpineStateTick("PRODUCT_PUBLISHED")
  const stockTick = useSpineStateTick("STOCK_ADJUSTED")
  const variantTick = useSpineStateTick("VARIANT_PUBLISHED")

  useEffect(() => {
    ;(async () => {
      try {
        const res = await spine.queryTable("products", {
          where: `id:${productId}`,
          limit: 1,
        })
        const rows = (res.rows ?? []) as unknown as Product[]
        setProduct(rows[0] ?? null)
      } catch {
        /* keep last */
      }
    })()
  }, [productId, pubTick, stockTick])

  // Load the variant matrix; keep the selection valid (default: first
  // in-stock variant).
  useEffect(() => {
    ;(async () => {
      try {
        const res = await spine.queryTable("product_variants", {
          where: `product_id:${productId}`,
          limit: 200,
        })
        const rows = (res.rows ?? []) as unknown as ProductVariantRow[]
        setVariants(rows)
        setSelectedVariantId((cur) => {
          if (cur && rows.some((v) => v.id === cur)) return cur
          return rows.find((v) => Number(v.stock) > 0)?.id ?? rows[0]?.id ?? null
        })
      } catch {
        /* keep last */
      }
    })()
  }, [productId, variantTick, stockTick])

  const selectedVariant = useMemo(
    () => variants.find((v) => v.id === selectedVariantId) ?? null,
    [variants, selectedVariantId]
  )

  // Image gallery: primary + gallery column (data URLs or external URLs)
  const images = useMemo(() => {
    if (!product) return []
    return [product.image_url, ...parseGallery(product.gallery)].filter(Boolean) as string[]
  }, [product])

  const hasVariants = variants.length > 0
  const option1Name = variants[0]?.option1_name ?? ""
  const option2Name = variants[0]?.option2_name ?? ""
  const option1Values = useMemo(
    () => [...new Set(variants.map((v) => v.option1_value).filter((v): v is string => Boolean(v)))],
    [variants]
  )
  const option2Values = useMemo(
    () => [...new Set(variants.map((v) => v.option2_value).filter((v): v is string => Boolean(v)))],
    [variants]
  )

  function pickOption(option: 1 | 2, value: string) {
    const match = variants.find((v) =>
      option === 1
        ? v.option1_value === value && v.option2_value === (selectedVariant?.option2_value ?? "")
        : v.option2_value === value && v.option1_value === (selectedVariant?.option1_value ?? "")
    )
    if (match) setSelectedVariantId(match.id)
  }

  if (!product) {
    return (
      <div className="mx-auto max-w-4xl space-y-4">
        <Button variant="ghost" size="sm" onClick={onBack}>
          <ArrowLeft className="mr-1 h-4 w-4" /> Back to catalog
        </Button>
        <Skeleton className="aspect-video w-full" />
      </div>
    )
  }

  // Displayed price/stock come from the selected variant when one exists
  const price = hasVariants && selectedVariant ? Number(selectedVariant.price) : Number(product.price)
  const stock = hasVariants && selectedVariant ? Number(selectedVariant.stock) : Number(product.stock)
  const inStock = stock > 0
  const variantLabel = selectedVariant
    ? [selectedVariant.option1_value, selectedVariant.option2_value].filter(Boolean).join(" / ")
    : ""

  async function addToCart() {
    const res = await spine.emit("ADD_TO_CART", {
      cart_id: getCartId(),
      product_id: product!.id,
      variant_id: selectedVariant?.id ?? "",
      variant_label: variantLabel,
      name: product!.name,
      price,
      qty,
    })
    if (res.status !== "ok") {
      toast.error(res.error ?? "Could not add to cart")
      return
    }
    onAddToCart(product!)
    toast.success(`${qty} × ${product!.name}${variantLabel ? ` (${variantLabel})` : ""} added to cart`)
  }

  return (
    <div className="mx-auto max-w-4xl space-y-4">
      <Button variant="ghost" size="sm" onClick={onBack}>
        <ArrowLeft className="mr-1 h-4 w-4" /> Back to catalog
      </Button>

      <div className="grid gap-6 md:grid-cols-2">
        <Card className="overflow-hidden">
          {images.length > 0 ? (
            <>
              <img
                src={images[Math.min(imgIndex, images.length - 1)]}
                alt={product.name}
                loading="lazy"
                className="aspect-square w-full object-cover"
              />
              {images.length > 1 && (
                <div className="flex gap-2 border-t p-3">
                  {images.map((src, i) => (
                    <button
                      key={i}
                      onClick={() => setImgIndex(i)}
                      className={`h-16 w-16 shrink-0 overflow-hidden rounded-md border transition-colors ${
                        imgIndex === i ? "ring-2 ring-primary" : "opacity-70 hover:opacity-100"
                      }`}
                    >
                      <img src={src} alt={`${product.name} view ${i + 1}`} loading="lazy" className="h-full w-full object-cover" />
                    </button>
                  ))}
                </div>
              )}
            </>
          ) : (
            <div className="flex aspect-square items-center justify-center bg-muted text-5xl text-muted-foreground">
              {String(product.name ?? "?").slice(0, 1)}
            </div>
          )}
        </Card>

        <div className="space-y-5">
          <div>
            {product.category && (
              <Badge variant="outline" className="mb-2">{product.category}</Badge>
            )}
            <h2 className="text-3xl font-bold tracking-tight">{product.name}</h2>
            <p className="mt-1 text-sm text-muted-foreground">
              SKU: {selectedVariant?.sku ?? product.sku}
              {hasVariants && <span className="ml-2">· {variants.length} variants</span>}
            </p>
          </div>

          <div className="flex items-center gap-3">
            <span className="text-3xl font-bold">{money(price)}</span>
            <Badge variant={inStock ? "secondary" : "destructive"}>
              {inStock ? `${stock} in stock` : "out of stock"}
            </Badge>
          </div>

          {hasVariants && (
            <div className="space-y-3">
              {option1Name && (
                <div>
                  <p className="mb-1.5 text-sm font-medium">{option1Name}</p>
                  <div className="flex flex-wrap gap-2">
                    {option1Values.map((v) => (
                      <button
                        key={v}
                        onClick={() => pickOption(1, v)}
                        className={`rounded-md border px-3 py-1.5 text-sm transition-colors ${
                          selectedVariant?.option1_value === v
                            ? "border-primary bg-primary text-primary-foreground"
                            : "hover:bg-muted"
                        }`}
                      >
                        {v}
                      </button>
                    ))}
                  </div>
                </div>
              )}
              {option2Name && (
                <div>
                  <p className="mb-1.5 text-sm font-medium">{option2Name}</p>
                  <div className="flex flex-wrap gap-2">
                    {option2Values.map((v) => (
                      <button
                        key={v}
                        onClick={() => pickOption(2, v)}
                        className={`rounded-md border px-3 py-1.5 text-sm transition-colors ${
                          selectedVariant?.option2_value === v
                            ? "border-primary bg-primary text-primary-foreground"
                            : "hover:bg-muted"
                        }`}
                      >
                        {v}
                      </button>
                    ))}
                  </div>
                </div>
              )}
            </div>
          )}

          {product.description && (
            <p className="text-sm leading-relaxed text-muted-foreground">{product.description}</p>
          )}

          <div className="flex flex-wrap items-center gap-3 pt-2">
            <div className="flex items-center gap-1">
              <Button
                variant="outline"
                size="icon"
                className="h-9 w-9"
                disabled={qty <= 1}
                onClick={() => setQty((q) => q - 1)}
              >
                <Minus className="h-4 w-4" />
              </Button>
              <span className="w-10 text-center font-medium tabular-nums">{qty}</span>
              <Button
                variant="outline"
                size="icon"
                className="h-9 w-9"
                disabled={qty >= stock}
                onClick={() => setQty((q) => q + 1)}
              >
                <Plus className="h-4 w-4" />
              </Button>
            </div>
            <Button className="flex-1" disabled={!inStock} onClick={addToCart}>
              <ShoppingCart className="mr-2 h-4 w-4" />
              Add to cart — {money(price * qty)}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
