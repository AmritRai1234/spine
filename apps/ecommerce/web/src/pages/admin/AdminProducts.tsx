import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react"
import { ArrowUpDown, Boxes, MoreHorizontal, Pencil, Plus, Search, Trash2 } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import { useSpineStateTick } from "@/hooks/use-spine"
import { toast } from "sonner"
import { adminClient } from "@/lib/admin"
import { money, parseGallery } from "@/lib/format"
import ImageUpload from "@/components/ImageUpload"
import type { Product, ProductVariantRow } from "@/types"

type SortKey = "name" | "price" | "stock" | "sku"

const EMPTY_FORM = {
  sku: "", name: "", price: "", stock: "", category: "", description: "", image_url: "",
  gallery: [] as string[],
}

const EMPTY_VARIANT_FORM = {
  sku: "", option1_name: "Size", option1_value: "", option2_name: "", option2_value: "",
  price: "", stock: "",
}

export default function AdminProducts() {
  const [products, setProducts] = useState<Product[] | null>(null)
  const [query, setQuery] = useState("")
  const [sortKey, setSortKey] = useState<SortKey>("name")
  const [sortAsc, setSortAsc] = useState(true)
  const [editing, setEditing] = useState<Product | null>(null)
  const [retiring, setRetiring] = useState<Product | null>(null)
  const [formOpen, setFormOpen] = useState(false)
  const [variantFor, setVariantFor] = useState<Product | null>(null)

  const tick = useSpineStateTick("PRODUCT_PUBLISHED")
  const retireTick = useSpineStateTick("PRODUCT_RETIRED")
  const variantTick = useSpineStateTick("VARIANT_PUBLISHED")

  const load = useCallback(async () => {
    const res = await adminClient().queryTable("products", { limit: 500 })
    setProducts((res.rows ?? []) as unknown as Product[])
  }, [])

  useEffect(() => {
    load()
  }, [load, tick, retireTick, variantTick])

  const visible = useMemo(() => {
    if (!products) return []
    const q = query.trim().toLowerCase()
    const filtered = q
      ? products.filter((p) =>
          [p.name, p.sku, p.category].some((v) => String(v ?? "").toLowerCase().includes(q))
        )
      : products
    const dir = sortAsc ? 1 : -1
    return [...filtered].sort((a, b) => {
      switch (sortKey) {
        case "price": return (Number(a.price) - Number(b.price)) * dir
        case "stock": return (Number(a.stock) - Number(b.stock)) * dir
        case "sku": return String(a.sku).localeCompare(String(b.sku)) * dir
        default: return String(a.name).localeCompare(String(b.name)) * dir
      }
    })
  }, [products, query, sortKey, sortAsc])

  function header(key: SortKey, label: string) {
    return (
      <Button
        variant="ghost"
        size="sm"
        className="-ml-2 h-7 font-medium"
        onClick={() => {
          if (sortKey === key) setSortAsc(!sortAsc)
          else {
            setSortKey(key)
            setSortAsc(true)
          }
        }}
      >
        {label}
        <ArrowUpDown className="ml-1 h-3 w-3" />
      </Button>
    )
  }

  async function adjustStock(p: Product, delta: number) {
    const next = Math.max(0, Number(p.stock) + delta)
    await adminClient().emit("PUBLISH_PRODUCT", {
      sku: p.sku,
      name: p.name,
      price: Number(p.price),
      stock: next,
      description: p.description ?? "",
      image_url: p.image_url ?? "",
      category: p.category ?? "",
    })
    toast.success(`${p.name}: stock ${delta > 0 ? "+" : ""}${delta} → ${next}`)
  }

  async function confirmRetire() {
    if (!retiring) return
    await adminClient().emit("RETIRE_PRODUCT", { sku: retiring.sku })
    toast.success(`Retired ${retiring.name}`)
    setRetiring(null)
  }

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 space-y-4 duration-300">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-xl font-semibold tracking-tight">Products</h2>
          <p className="text-sm text-muted-foreground">{visible.length} items</p>
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
          <Button onClick={() => { setEditing(null); setFormOpen(true) }}>
            <Plus className="mr-1 h-4 w-4" /> New product
          </Button>
        </div>
      </div>

      {!products ? (
        <Skeleton className="h-96" />
      ) : (
        <Card>
          <CardContent className="pt-6">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b text-left text-muted-foreground">
                  <th className="pb-2 pl-2 font-medium">Image</th>
                  <th className="pb-2 font-medium">{header("sku", "SKU")}</th>
                  <th className="pb-2 font-medium">{header("name", "Name")}</th>
                  <th className="hidden pb-2 font-medium md:table-cell">Category</th>
                  <th className="pb-2 font-medium">{header("price", "Price")}</th>
                  <th className="pb-2 font-medium">{header("stock", "Stock")}</th>
                  <th className="pb-2 pr-2" />
                </tr>
              </thead>
              <tbody>
                {visible.map((p) => (
                  <tr key={p.id} className="group border-b transition-colors last:border-0 hover:bg-muted/50">
                    <td className="py-2 pl-2">
                      {p.image_url ? (
                        <img src={p.image_url} alt={p.name} className="h-10 w-10 rounded-md border object-cover" />
                      ) : (
                        <div className="flex h-10 w-10 items-center justify-center rounded-md border bg-muted text-xs text-muted-foreground">
                          {String(p.name ?? "?").slice(0, 1)}
                        </div>
                      )}
                    </td>
                    <td><code className="text-xs">{p.sku}</code></td>
                    <td className="max-w-48 truncate py-2 font-medium">{p.name}</td>
                    <td className="hidden md:table-cell">
                      {p.category ? <Badge variant="outline">{p.category}</Badge> : <span className="text-muted-foreground">—</span>}
                    </td>
                    <td>{money(p.price)}</td>
                    <td>
                      <div className="flex items-center gap-1.5">
                        <Button variant="outline" size="icon" className="h-6 w-6" onClick={() => adjustStock(p, -1)}>−</Button>
                        <span className={`w-8 text-center tabular-nums ${Number(p.stock) < 10 ? "font-semibold text-destructive" : ""}`}>
                          {p.stock}
                        </span>
                        <Button variant="outline" size="icon" className="h-6 w-6" onClick={() => adjustStock(p, +1)}>+</Button>
                      </div>
                    </td>
                    <td className="pr-2 text-right">
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button variant="ghost" size="icon" className="h-8 w-8">
                            <MoreHorizontal className="h-4 w-4" />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem onClick={() => { setEditing(p); setFormOpen(true) }}>
                            <Pencil className="mr-2 h-4 w-4" /> Edit
                          </DropdownMenuItem>
                          <DropdownMenuItem onClick={() => setVariantFor(p)}>
                            <Boxes className="mr-2 h-4 w-4" /> Variants
                          </DropdownMenuItem>
                          <DropdownMenuItem variant="destructive" onClick={() => setRetiring(p)}>
                            <Trash2 className="mr-2 h-4 w-4" /> Retire
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </td>
                  </tr>
                ))}
                {visible.length === 0 && (
                  <tr><td colSpan={7} className="py-12 text-center text-muted-foreground">No products match.</td></tr>
                )}
              </tbody>
            </table>
          </CardContent>
        </Card>
      )}

      {/* Create / edit sheet */}
      <ProductSheet
        open={formOpen}
        onOpenChange={setFormOpen}
        product={editing}
      />

      {/* Variant matrix editor */}
      <VariantSheet
        product={variantFor}
        onOpenChange={(o) => !o && setVariantFor(null)}
      />

      {/* Retire confirmation */}
      <Dialog open={!!retiring} onOpenChange={(o) => !o && setRetiring(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Retire “{retiring?.name}”?</DialogTitle>
            <DialogDescription>
              This removes the product from the catalog permanently.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRetiring(null)}>Cancel</Button>
            <Button variant="destructive" onClick={confirmRetire}>Retire product</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function ProductSheet({
  open,
  onOpenChange,
  product,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  product: Product | null
}) {
  const [form, setForm] = useState(EMPTY_FORM)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (open) {
      setForm(product ? {
        sku: product.sku, name: product.name, price: String(product.price),
        stock: String(product.stock), category: product.category ?? "",
        description: product.description ?? "", image_url: product.image_url ?? "",
        gallery: parseGallery(product.gallery),
      } : EMPTY_FORM)
    }
  }, [open, product])

  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm({ ...form, [k]: e.target.value })

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      const res = await adminClient().emit("PUBLISH_PRODUCT", {
        sku: form.sku.trim(),
        name: form.name.trim(),
        price: Number(form.price) || 0,
        stock: Math.trunc(Number(form.stock)) || 0,
        description: form.description.trim(),
        image_url: form.image_url.trim(),
        category: form.category.trim(),
        gallery: form.gallery,
      })
      if (res.status === "ok") {
        toast.success(product ? `Updated ${form.name}` : `Published ${form.name}`)
        onOpenChange(false)
      } else {
        toast.error(res.error ?? "Publish failed")
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>{product ? "Edit product" : "New product"}</SheetTitle>
          <SheetDescription>Publishes via upsert — SKU is the identity.</SheetDescription>
        </SheetHeader>
        <form onSubmit={submit} className="space-y-3 px-4 pb-6">
          <Field label="SKU"><Input value={form.sku} onChange={set("sku")} disabled={!!product} placeholder="spine-hat" required /></Field>
          <Field label="Name"><Input value={form.name} onChange={set("name")} required /></Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Price"><Input type="number" step="0.01" min="0" value={form.price} onChange={set("price")} required /></Field>
            <Field label="Stock"><Input type="number" min="0" value={form.stock} onChange={set("stock")} required /></Field>
          </div>
          <Field label="Category"><Input value={form.category} onChange={set("category")} placeholder="apparel" /></Field>

          <div className="space-y-3">
            <ImageUpload
              label="Primary image"
              value={form.image_url || null}
              onChange={(v) => setForm({ ...form, image_url: v ?? "" })}
            />
            <Input
              value={form.image_url.startsWith("data:") ? "" : form.image_url}
              onChange={(e) => setForm({ ...form, image_url: e.target.value })}
              placeholder="…or paste an image URL (https://…)"
              className="mt-1"
            />
            <div>
              <span className="mb-1.5 block text-sm font-medium">Gallery (up to 4)</span>
              <div className="grid grid-cols-4 gap-2">
                {form.gallery.map((g, i) => (
                  <div key={i} className="relative overflow-hidden rounded-md border">
                    <img src={g} alt="" className="h-16 w-full object-cover" />
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="absolute right-0.5 top-0.5 h-6 w-6 bg-background/80"
                      onClick={() => setForm({ ...form, gallery: form.gallery.filter((_, j) => j !== i) })}
                    >
                      <Trash2 className="h-3 w-3" />
                    </Button>
                  </div>
                ))}
                {form.gallery.length < 4 && (
                  <ImageUpload
                    label=""
                    value={null}
                    className="h-16"
                    onChange={(v) => v && setForm({ ...form, gallery: [...form.gallery, v] })}
                  />
                )}
              </div>
            </div>
          </div>

          <Field label="Description">
            <textarea
              value={form.description}
              onChange={(e) => setForm({ ...form, description: e.target.value })}
              rows={3}
              className="border-input placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 flex w-full rounded-md border bg-transparent px-3 py-2 text-sm shadow-xs focus-visible:ring-[3px] focus-visible:outline-none"
            />
          </Field>
          <SheetFooter className="px-0 pt-2">
            <Button type="submit" disabled={busy} className="w-full">
              {product ? "Save changes" : "Publish product"}
            </Button>
          </SheetFooter>
        </form>
      </SheetContent>
    </Sheet>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="text-sm font-medium">{label}</span>
      {children}
    </label>
  )
}

/**
 * Variant matrix editor — per-SKU rows under a product (option1 × option2,
 * own price + stock). Publishes via PUBLISH_VARIANT (upsert by SKU).
 */
function VariantSheet({
  product,
  onOpenChange,
}: {
  product: Product | null
  onOpenChange: (o: boolean) => void
}) {
  const [variants, setVariants] = useState<ProductVariantRow[] | null>(null)
  const [form, setForm] = useState(EMPTY_VARIANT_FORM)
  const [busy, setBusy] = useState(false)
  const variantTick = useSpineStateTick("VARIANT_PUBLISHED")

  useEffect(() => {
    if (!product) return
    setVariants(null)
    setForm(EMPTY_VARIANT_FORM)
    ;(async () => {
      try {
        const res = await adminClient().queryTable("product_variants", {
          where: `product_id:${product.id}`,
          limit: 200,
        })
        setVariants((res.rows ?? []) as unknown as ProductVariantRow[])
      } catch {
        setVariants([])
      }
    })()
  }, [product, variantTick])

  if (!product) return null
  const productId = product.id

  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm({ ...form, [k]: e.target.value })

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      const res = await adminClient().emit("PUBLISH_VARIANT", {
        sku: form.sku.trim(),
        product_id: productId,
        option1_name: form.option1_name.trim() || "Size",
        option1_value: form.option1_value.trim(),
        ...(form.option2_name.trim() && form.option2_value.trim()
          ? { option2_name: form.option2_name.trim(), option2_value: form.option2_value.trim() }
          : {}),
        price: Number(form.price) || 0,
        stock: Math.trunc(Number(form.stock)) || 0,
        actor: "admin@panel",
      })
      if (res.status === "ok") {
        toast.success(`Variant ${form.sku} saved`)
        setForm(EMPTY_VARIANT_FORM)
      } else {
        toast.error(res.error ?? "Variant save failed")
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet open={!!product} onOpenChange={onOpenChange}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <Boxes className="h-4 w-4" /> Variants — {product.name}
          </SheetTitle>
          <SheetDescription>
            One row per option combination (Size × Color). Each variant carries
            its own SKU, price and stock; checkout enforces them server-side.
          </SheetDescription>
        </SheetHeader>

        <div className="space-y-4 px-4 pb-6">
          {variants === null ? (
            <Skeleton className="h-32" />
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b text-left text-muted-foreground">
                  <th className="pb-2 font-medium">SKU</th>
                  <th className="pb-2 font-medium">Options</th>
                  <th className="pb-2 text-right font-medium">Price</th>
                  <th className="pb-2 text-right font-medium">Stock</th>
                </tr>
              </thead>
              <tbody>
                {variants.map((v) => (
                  <tr key={v.id} className="border-b last:border-0">
                    <td className="py-2"><code className="text-xs">{v.sku}</code></td>
                    <td className="py-2 text-muted-foreground">
                      {[v.option1_value, v.option2_value].filter(Boolean).join(" / ") || "—"}
                    </td>
                    <td className="py-2 text-right tabular-nums">{money(Number(v.price))}</td>
                    <td className="py-2 text-right tabular-nums">{v.stock}</td>
                  </tr>
                ))}
                {variants.length === 0 && (
                  <tr>
                    <td colSpan={4} className="py-6 text-center text-muted-foreground">
                      No variants yet — add the first option combination below.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          )}

          <form onSubmit={submit} className="space-y-3 rounded-md border p-3">
            <p className="text-sm font-medium">Add variant</p>
            <div className="grid grid-cols-2 gap-3">
              <Field label="SKU"><Input value={form.sku} onChange={set("sku")} placeholder="tee-s" required /></Field>
              <Field label="Price"><Input type="number" step="0.01" min="0" value={form.price} onChange={set("price")} required /></Field>
              <Field label="Option 1 name"><Input value={form.option1_name} onChange={set("option1_name")} placeholder="Size" /></Field>
              <Field label="Option 1 value"><Input value={form.option1_value} onChange={set("option1_value")} placeholder="S" required /></Field>
              <Field label="Option 2 name (optional)"><Input value={form.option2_name} onChange={set("option2_name")} placeholder="Color" /></Field>
              <Field label="Option 2 value (optional)"><Input value={form.option2_value} onChange={set("option2_value")} placeholder="Black" /></Field>
              <Field label="Stock"><Input type="number" min="0" value={form.stock} onChange={set("stock")} required /></Field>
            </div>
            <Button type="submit" disabled={busy} className="w-full">
              <Plus className="mr-1 h-4 w-4" /> Save variant
            </Button>
          </form>
        </div>
      </SheetContent>
    </Sheet>
  )
}
