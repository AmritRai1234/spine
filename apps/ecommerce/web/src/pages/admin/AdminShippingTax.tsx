import { useCallback, useEffect, useState, type FormEvent } from "react"
import { Globe2, Percent, Save, Truck } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
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

interface RateRow {
  country: string
  rate: number
  created_at?: string
}

/**
 * Shipping zones + tax rules — the config that powers SERVER-side order
 * totals. Zones are flat rates per order; tax rules are percentages of the
 * subtotal. A country with no row gets free shipping / 0% tax (documented
 * default); use country "*" for a catch-all rule.
 */
export default function AdminShippingTax() {
  const [zones, setZones] = useState<RateRow[] | null>(null)
  const [taxes, setTaxes] = useState<RateRow[] | null>(null)

  const [zoneCountry, setZoneCountry] = useState("")
  const [zoneRate, setZoneRate] = useState("")
  const [taxCountry, setTaxCountry] = useState("")
  const [taxRate, setTaxRate] = useState("")

  const zoneTick = useSpineStateTick("SHIPPING_ZONE_SAVED")
  const taxTick = useSpineStateTick("TAX_RULE_SAVED")

  const load = useCallback(async () => {
    const client = adminClient()
    const [z, t] = await Promise.all([
      client.queryTable("shipping_zones", { limit: 100 }),
      client.queryTable("tax_rules", { limit: 100 }),
    ])
    setZones((z.rows ?? []) as unknown as RateRow[])
    setTaxes((t.rows ?? []) as unknown as RateRow[])
  }, [])

  useEffect(() => {
    load()
  }, [load, zoneTick, taxTick])

  async function saveZone(e: FormEvent) {
    e.preventDefault()
    const country = zoneCountry.trim().toUpperCase()
    const rate = Number(zoneRate)
    if (!country || !isFinite(rate) || rate < 0) return
    const res = await adminClient().emit("SAVE_SHIPPING_ZONE", {
      country,
      rate,
      actor: "admin@panel",
    })
    if (res.status === "ok") {
      toast.success(`Shipping zone ${country} = ${money(rate)}`)
      setZoneCountry("")
      setZoneRate("")
    } else {
      toast.error(res.error ?? "Zone save failed")
    }
  }

  async function saveTax(e: FormEvent) {
    e.preventDefault()
    const country = taxCountry.trim().toUpperCase()
    const rate = Number(taxRate)
    if (!country || !isFinite(rate) || rate < 0) return
    const res = await adminClient().emit("SAVE_TAX_RULE", {
      country,
      rate,
      actor: "admin@panel",
    })
    if (res.status === "ok") {
      toast.success(`Tax rule ${country} = ${rate}%`)
      setTaxCountry("")
      setTaxRate("")
    } else {
      toast.error(res.error ?? "Tax rule save failed")
    }
  }

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 w-full space-y-4 duration-300">
      <div>
        <h2 className="text-xl font-semibold tracking-tight">Shipping &amp; Tax</h2>
        <p className="text-sm text-muted-foreground">
          Server-calculated at checkout — shoppers never send dollar amounts.
          Missing country = free shipping / 0% tax. Use{" "}
          <code className="rounded bg-muted px-1">*</code> for a catch-all rule.
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Truck className="h-4 w-4" /> Shipping zones
          </CardTitle>
          <CardDescription>Flat rate per order, keyed by country.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <form onSubmit={saveZone} className="flex items-end gap-3">
            <label className="flex-1 space-y-1.5">
              <span className="text-sm font-medium">Country (or * for default)</span>
              <Input
                value={zoneCountry}
                onChange={(e) => setZoneCountry(e.target.value)}
                placeholder="US"
                className="uppercase placeholder:normal-case"
                required
              />
            </label>
            <label className="w-40 space-y-1.5">
              <span className="text-sm font-medium">Rate ($)</span>
              <Input
                type="number"
                min="0"
                step="0.01"
                value={zoneRate}
                onChange={(e) => setZoneRate(e.target.value)}
                placeholder="5.99"
                required
              />
            </label>
            <Button type="submit" variant="outline">
              <Save className="mr-1 h-4 w-4" /> Save zone
            </Button>
          </form>

          {zones === null ? (
            <Skeleton className="h-24" />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Country</TableHead>
                  <TableHead className="text-right">Rate</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {zones.map((z) => (
                  <TableRow key={z.country}>
                    <TableCell className="font-medium">
                      {z.country === "*" ? <span className="italic">default (*)</span> : z.country}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{money(Number(z.rate))}</TableCell>
                  </TableRow>
                ))}
                {zones.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={2} className="py-8 text-center text-muted-foreground">
                      No zones — free shipping everywhere until you add one.
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Percent className="h-4 w-4" /> Tax rules
          </CardTitle>
          <CardDescription>Percentage of subtotal, keyed by country.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <form onSubmit={saveTax} className="flex items-end gap-3">
            <label className="flex-1 space-y-1.5">
              <span className="text-sm font-medium">Country (or * for default)</span>
              <Input
                value={taxCountry}
                onChange={(e) => setTaxCountry(e.target.value)}
                placeholder="US"
                className="uppercase placeholder:normal-case"
                required
              />
            </label>
            <label className="w-40 space-y-1.5">
              <span className="text-sm font-medium">Rate (%)</span>
              <Input
                type="number"
                min="0"
                step="0.01"
                value={taxRate}
                onChange={(e) => setTaxRate(e.target.value)}
                placeholder="8.25"
                required
              />
            </label>
            <Button type="submit" variant="outline">
              <Save className="mr-1 h-4 w-4" /> Save rule
            </Button>
          </form>

          {taxes === null ? (
            <Skeleton className="h-24" />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Country</TableHead>
                  <TableHead className="text-right">Rate</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {taxes.map((t) => (
                  <TableRow key={t.country}>
                    <TableCell className="font-medium">
                      {t.country === "*" ? <span className="italic">default (*)</span> : t.country}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{Number(t.rate)}%</TableCell>
                  </TableRow>
                ))}
                {taxes.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={2} className="py-8 text-center text-muted-foreground">
                      No tax rules — no tax collected anywhere until you add one.
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <p className="flex items-center gap-2 text-xs text-muted-foreground">
        <Globe2 className="h-3.5 w-3.5" />
        Totals are recomputed at PLACE_ORDER from these rules + persisted line
        items; the storefront only displays them.
      </p>
    </div>
  )
}
