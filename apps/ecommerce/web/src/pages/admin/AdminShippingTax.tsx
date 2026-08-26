import { useCallback, useEffect, useMemo, useState } from "react"
import { AlertTriangle, Globe2, Landmark, Percent, Save, Truck, Wand2 } from "lucide-react"
import { toast } from "sonner"

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
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { useSpineStateTick } from "@/hooks/use-spine"
import { adminClient } from "@/lib/admin"
import { money } from "@/lib/format"
import { CA_PROVINCES, OTHER_REGIONS, PROVINCE_TIER, SHIP_TIERS, type ShipTier } from "@/lib/canada"
import { CA_CITIES } from "@/lib/canada-cities"

interface RateRow {
  country: string
  rate: number
  created_at?: string
}

/**
 * Shipping & Tax — Canada-first configuration.
 *
 * Every Canadian province gets its official sales-tax rate (2025, verified
 * against Canada.ca and TaxTips.ca) and a Canada Post-tiered shipping rate,
 * edited inline in the tables below. The checkout engine resolves the
 * shopper's rate by region code (CA-ON) → country code (US) → "*" default,
 * so the same tables still serve international orders.
 */
export default function AdminShippingTax() {
  const [zones, setZones] = useState<RateRow[] | null>(null)
  const [taxes, setTaxes] = useState<RateRow[] | null>(null)

  // Tax table — one draft rate per province row (prefilled from server/official)
  const [taxDrafts, setTaxDrafts] = useState<Record<string, string>>({})
  // Canada shipping form — province + per-city draft rates
  const [shipProv, setShipProv] = useState("CA-ON")
  const [provRateDraft, setProvRateDraft] = useState("")
  const [cityRateDrafts, setCityRateDrafts] = useState<Record<string, string>>({})
  // International forms
  const [intlZoneRegion, setIntlZoneRegion] = useState("US")
  const [intlZoneRate, setIntlZoneRate] = useState("")
  const [intlTaxRegion, setIntlTaxRegion] = useState("US")
  const [intlTaxRate, setIntlTaxRate] = useState("")
  const [bulkBusy, setBulkBusy] = useState(false)

  const zoneTick = useSpineStateTick("SHIPPING_ZONE_SAVED")
  const taxTick = useSpineStateTick("TAX_RULE_SAVED")
  const [loadError, setLoadError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoadError(null)
    try {
      const client = adminClient()
      const [z, t] = await Promise.all([
        client.queryTable("shipping_zones", { limit: 100 }),
        client.queryTable("tax_rules", { limit: 100 }),
      ])
      setZones((z.rows ?? []) as unknown as RateRow[])
      setTaxes((t.rows ?? []) as unknown as RateRow[])
    } catch (e) {
      // Keep prior data (if any) but surface why the tables are empty/stale.
      setLoadError(e instanceof Error ? e.message : "failed to load rates")
    }
  }, [])

  useEffect(() => {
    load()
  }, [load, zoneTick, taxTick])

  const zoneMap = useMemo(() => {
    const m = new Map<string, number>()
    for (const z of zones ?? []) m.set(z.country, Number(z.rate))
    return m
  }, [zones])

  const taxMap = useMemo(() => {
    const m = new Map<string, number>()
    for (const t of taxes ?? []) m.set(t.country, Number(t.rate))
    return m
  }, [taxes])

  const selectedShipProv = CA_PROVINCES.find((p) => p.code === shipProv)

  async function saveZone(region: string, rate: number, label: string) {
    const res = await adminClient().emit("SAVE_SHIPPING_ZONE", {
      country: region,
      rate,
      actor: "admin@panel",
    })
    if (res.status === "ok") {
      toast.success(`Shipping — ${label}: ${money(rate)}`)
    } else {
      toast.error(res.error ?? "Zone save failed")
    }
  }

  async function saveTax(region: string, rate: number, label: string) {
    const res = await adminClient().emit("SAVE_TAX_RULE", {
      country: region,
      rate,
      actor: "admin@panel",
    })
    if (res.status === "ok") {
      toast.success(`Tax — ${label}: ${rate}%`)
    } else {
      toast.error(res.error ?? "Tax save failed")
    }
  }

  /** One-click: every province gets its official 2025 sales-tax rate. */
  async function applyOfficialTaxRates() {
    setBulkBusy(true)
    try {
      for (const p of CA_PROVINCES) {
        const res = await adminClient().emit("SAVE_TAX_RULE", {
          country: p.code,
          rate: p.rate,
          actor: "admin@panel",
        })
        if (res.status !== "ok") {
          toast.error(`${p.name}: ${res.error ?? "save failed"}`)
          return
        }
      }
      toast.success(`Applied official 2025 rates to all ${CA_PROVINCES.length} provinces/territories`)
    } finally {
      setBulkBusy(false)
    }
  }

  /** One-click: Canada Post-tier shipping per province. */
  async function applySuggestedShipping() {
    setBulkBusy(true)
    try {
      for (const p of CA_PROVINCES) {
        const tier: ShipTier = PROVINCE_TIER[p.code] ?? "national"
        const rate = SHIP_TIERS[tier].suggestion
        const res = await adminClient().emit("SAVE_SHIPPING_ZONE", {
          country: p.code,
          rate,
          actor: "admin@panel",
        })
        if (res.status !== "ok") {
          toast.error(`${p.name}: ${res.error ?? "save failed"}`)
          return
        }
      }
      toast.success(`Applied Canada Post-tier shipping to all ${CA_PROVINCES.length} provinces/territories`)
    } finally {
      setBulkBusy(false)
    }
  }

  const missingTax = CA_PROVINCES.filter((p) => !taxMap.has(p.code))
  const missingZone = CA_PROVINCES.filter((p) => !zoneMap.has(p.code))
  const intlZones = (zones ?? []).filter((z) => !z.country.startsWith("CA"))
  const intlTaxes = (taxes ?? []).filter((t) => !t.country.startsWith("CA-"))

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 w-full space-y-4 duration-300">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-xl font-semibold tracking-tight">Shipping &amp; Tax</h2>
          <p className="max-w-2xl text-sm text-muted-foreground">
            Canada-first configuration. Shoppers are charged by province — the engine resolves
            their region code (e.g. <code className="rounded bg-muted px-1">CA-ON</code>) → country →{" "}
            <code className="rounded bg-muted px-1">*</code> default, server-side at checkout.
          </p>
        </div>
        <div className="flex gap-2">
          <Button size="sm" variant="outline" onClick={applySuggestedShipping} disabled={bulkBusy}>
            <Truck className="mr-1 h-4 w-4" /> Apply suggested shipping
          </Button>
          <Button size="sm" onClick={applyOfficialTaxRates} disabled={bulkBusy}>
            <Wand2 className="mr-1 h-4 w-4" /> Apply official 2025 tax rates
          </Button>
        </div>
      </div>

      {(loadError || missingTax.length > 0 || missingZone.length > 0) && (
        <Card className={loadError ? "border-destructive/40 bg-destructive/5" : "border-amber-500/40 bg-amber-500/5"}>
          <CardContent className="flex flex-wrap items-center gap-2 py-3 text-sm">
            {loadError ? (
              <>
                <AlertTriangle className="h-4 w-4 text-destructive" />
                <span>
                  Couldn't load rates: <strong>{loadError}</strong>. Your admin key may be stale —{" "}
                  <button className="underline" onClick={() => { location.hash = "#/admin"; location.reload() }}>
                    unlock again
                  </button>{" "}
                  or check that the backend is running.
                </span>
                <Button size="sm" variant="outline" className="ml-auto" onClick={load}>Retry</Button>
              </>
            ) : (
              <>
                <AlertTriangle className="h-4 w-4 text-amber-500" />
                <span>
                  {missingTax.length > 0 && (
                    <>
                      <strong>{missingTax.length}</strong> province(s) missing tax rates (
                      {missingTax.slice(0, 4).map((p) => p.code.replace("CA-", "")).join(", ")}
                      {missingTax.length > 4 ? "…" : ""})
                    </>
                  )}
                  {missingTax.length > 0 && missingZone.length > 0 && " · "}
                  {missingZone.length > 0 && (
                    <>
                      <strong>{missingZone.length}</strong> missing shipping zones
                    </>
                  )}
                  {" — these regions get 0% tax / free shipping until configured."}
                </span>
              </>
            )}
          </CardContent>
        </Card>
      )}

      {/* ── Canada — Tax ─────────────────────────────────────────────── */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Percent className="h-4 w-4" /> Canadian sales tax by province
          </CardTitle>
          <CardDescription>
            Official 2025 general rates (HST / GST+PST / GST+QST), editable inline. Applies to the order subtotal.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {taxes === null ? (
            <Skeleton className="h-64" />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Province / territory</TableHead>
                  <TableHead className="hidden sm:table-cell">System</TableHead>
                  <TableHead className="w-28">Charged rate (%)</TableHead>
                  <TableHead className="w-20" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {CA_PROVINCES.map((p) => {
                  const configured = taxMap.has(p.code)
                  const draft = taxDrafts[p.code] ?? (configured ? String(taxMap.get(p.code)) : String(p.rate))
                  return (
                    <TableRow key={p.code} className={configured ? "" : "bg-amber-500/5"}>
                      <TableCell>
                        <span className="font-medium">{p.name}</span>
                        <span className="ml-2 hidden text-xs text-muted-foreground lg:inline">{p.breakdown}</span>
                      </TableCell>
                      <TableCell className="hidden sm:table-cell">
                        <Badge variant="outline">{p.system}</Badge>
                      </TableCell>
                      <TableCell>
                        <Input
                          type="number"
                          min="0"
                          step="0.001"
                          value={draft}
                          onChange={(e) => setTaxDrafts((m) => ({ ...m, [p.code]: e.target.value }))}
                          className="h-8 tabular-nums"
                          aria-label={`Tax rate for ${p.name}`}
                        />
                      </TableCell>
                      <TableCell className="text-right">
                        <Button
                          variant="outline"
                          size="sm"
                          className="h-8"
                          onClick={() => {
                            const rate = Number(draft)
                            if (!isFinite(rate) || rate < 0) return
                            saveTax(p.code, rate, p.name)
                          }}
                        >
                          Save
                        </Button>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          )}
          {intlTaxes.length > 0 && (
            <p className="mt-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
              <Landmark className="h-3.5 w-3.5" />
              International tax rules:{" "}
              {intlTaxes.map((t) => (
                <Badge key={t.country} variant="secondary">
                  {t.country === "*" ? "default (*)" : t.country} · {Number(t.rate)}%
                </Badge>
              ))}
            </p>
          )}
        </CardContent>
      </Card>

      {/* ── Canada — Shipping ────────────────────────────────────────── */}
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <CardTitle className="flex items-center gap-2 text-base">
                <Truck className="h-4 w-4" /> Canadian shipping
              </CardTitle>
              <CardDescription>
                City (big cities) → province → country → default — the most specific rate wins.
                Unlisted cities automatically get the province rate.
              </CardDescription>
            </div>
            <Select value={shipProv} onValueChange={(v: string) => { setShipProv(v); setProvRateDraft("") }}>
              <SelectTrigger className="w-64">
                <SelectValue placeholder="Choose a province" />
              </SelectTrigger>
              <SelectContent>
                {CA_PROVINCES.map((p) => {
                  const tier = SHIP_TIERS[PROVINCE_TIER[p.code] ?? "national"]
                  return (
                    <SelectItem key={p.code} value={p.code}>
                      {p.name} — {tier.label}
                    </SelectItem>
                  )
                })}
              </SelectContent>
            </Select>
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          {zones === null ? (
            <Skeleton className="h-48" />
          ) : (
            <>
              {/* Province row */}
              <div className="flex flex-wrap items-center gap-3 rounded-lg border p-3">
                <div className="min-w-40 flex-1">
                  <p className="text-sm font-medium">{selectedShipProv?.name ?? shipProv}</p>
                  <p className="text-xs text-muted-foreground">
                    {SHIP_TIERS[PROVINCE_TIER[shipProv] ?? "national"].note} — everyone in the province
                  </p>
                </div>
                {zoneMap.has(shipProv) ? (
                  <Badge variant="secondary">province rate: {money(zoneMap.get(shipProv)!)}</Badge>
                ) : (
                  <Badge variant="destructive">no rate — free shipping</Badge>
                )}
                <Input
                  type="number"
                  min="0"
                  step="0.01"
                  value={provRateDraft}
                  onChange={(e) => setProvRateDraft(e.target.value)}
                  className="h-9 w-28"
                  placeholder={`suggested ${SHIP_TIERS[PROVINCE_TIER[shipProv] ?? "national"].suggestion}`}
                  aria-label={`Shipping rate for ${selectedShipProv?.name ?? shipProv}`}
                />
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    const rate = Number(provRateDraft)
                    if (!isFinite(rate) || rate < 0) return
                    saveZone(shipProv, rate, selectedShipProv?.name ?? shipProv)
                  }}
                >
                  <Save className="mr-1 h-3.5 w-3.5" /> Save
                </Button>
              </div>

              {/* City rows for this province */}
              {CA_CITIES.filter((c) => c.provCode === shipProv).length > 0 && (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>City</TableHead>
                      <TableHead className="hidden sm:table-cell">Scope</TableHead>
                      <TableHead className="w-28">Rate ($)</TableHead>
                      <TableHead className="w-20" />
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {CA_CITIES.filter((c) => c.provCode === shipProv).map((c) => {
                      const configured = zoneMap.has(c.cityKey)
                      const draft = cityRateDrafts[c.cityKey] ?? (configured ? String(zoneMap.get(c.cityKey)) : String(c.suggestion))
                      return (
                        <TableRow key={c.cityKey}>
                          <TableCell>
                            <span className="font-medium">{c.name}</span>
                            <span className="ml-2 text-xs text-muted-foreground">overrides province</span>
                          </TableCell>
                          <TableCell className="hidden sm:table-cell">
                            {configured ? (
                              <Badge variant="secondary">local rate</Badge>
                            ) : (
                              <span className="text-xs italic text-muted-foreground">uses province rate</span>
                            )}
                          </TableCell>
                          <TableCell>
                            <Input
                              type="number"
                              min="0"
                              step="0.01"
                              value={draft}
                              onChange={(e) => setCityRateDrafts((m) => ({ ...m, [c.cityKey]: e.target.value }))}
                              className="h-8"
                              aria-label={`Shipping rate for ${c.name}`}
                            />
                          </TableCell>
                          <TableCell className="text-right">
                            <Button
                              variant="outline"
                              size="sm"
                              className="h-8"
                              onClick={() => {
                                const rate = Number(draft)
                                if (!isFinite(rate) || rate < 0) return
                                saveZone(c.cityKey, rate, `${c.name} (local)`)
                              }}
                            >
                              Save
                            </Button>
                          </TableCell>
                        </TableRow>
                      )
                    })}
                  </TableBody>
                </Table>
              )}
              {CA_CITIES.filter((c) => c.provCode === shipProv).length === 0 && (
                <p className="rounded-lg border border-dashed p-4 text-center text-sm text-muted-foreground">
                  No city rates listed for {selectedShipProv?.name ?? shipProv} — everyone gets the province rate.
                </p>
              )}
            </>
          )}
        </CardContent>
      </Card>

      {/* ── International ────────────────────────────────────────────── */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Globe2 className="h-4 w-4" /> International
          </CardTitle>
          <CardDescription>
            Country-level rules for orders outside Canada. Unlisted countries fall back to the
            <code className="mx-1 rounded bg-muted px-1">*</code> default.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-6 md:grid-cols-2">
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault()
              const rate = Number(intlZoneRate)
              if (!isFinite(rate) || rate < 0) return
              const label = OTHER_REGIONS.find((r) => r.code === intlZoneRegion)?.name ?? intlZoneRegion
              saveZone(intlZoneRegion, rate, label)
              setIntlZoneRate("")
            }}
          >
            <span className="text-sm font-medium">Shipping zone</span>
            <div className="flex items-end gap-2">
              <Select value={intlZoneRegion} onValueChange={setIntlZoneRegion}>
                <SelectTrigger className="w-44">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {OTHER_REGIONS.map((r) => (
                    <SelectItem key={r.code} value={r.code}>{r.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Input
                type="number"
                min="0"
                step="0.01"
                value={intlZoneRate}
                onChange={(e) => setIntlZoneRate(e.target.value)}
                placeholder="12.99"
                className="w-28"
                required
              />
              <Button type="submit" variant="outline" size="sm">
                <Save className="mr-1 h-3.5 w-3.5" /> Save
              </Button>
            </div>
            {intlZones.length > 0 && (
              <div className="space-y-1.5 pt-1">
                {intlZones.map((z) => (
                  <div key={z.country} className="flex items-center justify-between rounded border px-3 py-1.5 text-sm">
                    <span className="font-medium">{z.country === "*" ? <span className="italic">Default (everywhere else)</span> : OTHER_REGIONS.find((r) => r.code === z.country)?.name ?? z.country}</span>
                    <span className="tabular-nums text-muted-foreground">{money(Number(z.rate))}</span>
                  </div>
                ))}
              </div>
            )}
          </form>
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault()
              const rate = Number(intlTaxRate)
              if (!isFinite(rate) || rate < 0) return
              const label = OTHER_REGIONS.find((r) => r.code === intlTaxRegion)?.name ?? intlTaxRegion
              saveTax(intlTaxRegion, rate, label)
              setIntlTaxRate("")
            }}
          >
            <span className="text-sm font-medium">Tax rule</span>
            <div className="flex items-end gap-2">
              <Select value={intlTaxRegion} onValueChange={setIntlTaxRegion}>
                <SelectTrigger className="w-44">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {OTHER_REGIONS.map((r) => (
                    <SelectItem key={r.code} value={r.code}>{r.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Input
                type="number"
                min="0"
                step="0.01"
                value={intlTaxRate}
                onChange={(e) => setIntlTaxRate(e.target.value)}
                placeholder="0"
                className="w-28"
                required
              />
              <Button type="submit" variant="outline" size="sm">
                <Save className="mr-1 h-3.5 w-3.5" /> Save
              </Button>
            </div>
            {intlTaxes.length > 0 && (
              <div className="space-y-1.5 pt-1">
                {intlTaxes.map((t) => (
                  <div key={t.country} className="flex items-center justify-between rounded border px-3 py-1.5 text-sm">
                    <span className="font-medium">{t.country === "*" ? <span className="italic">Default (everywhere else)</span> : OTHER_REGIONS.find((r) => r.code === t.country)?.name ?? t.country}</span>
                    <span className="tabular-nums text-muted-foreground">{Number(t.rate)}%</span>
                  </div>
                ))}
              </div>
            )}
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
