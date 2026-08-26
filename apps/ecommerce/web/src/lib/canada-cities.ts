/**
 * Canadian city directory for city-tier shipping — big metros first,
 * mid-size cities next (Kingston-class), expanding over time.
 *
 * cityKey is the shipping_zones lookup key: CA-<PROV>-<CITY> (city upper-
 * cased, spaces → underscores). The checkout sends this exact key after
 * normalizing the shopper's free-text city, so keys here MUST stay in sync
 * with the admin-configured rows.
 *
 * tiers: metro (biggest cities), city (mid-size, Kingston-class)
 */

export type CityTier = "metro" | "city"

export interface CaCity {
  cityKey: string
  name: string
  provCode: string // CA-XX region code (matches CA_PROVINCES)
  tier: CityTier
  /** Suggested local-tier shipping rate — cheaper than the province rate. */
  suggestion: number
}

export const CA_CITIES: CaCity[] = [
  // ── Ontario ────────────────────────────────────────────────────────────
  { cityKey: "CA-ON-TORONTO", name: "Toronto", provCode: "CA-ON", tier: "metro", suggestion: 8 },
  { cityKey: "CA-ON-OTTAWA", name: "Ottawa", provCode: "CA-ON", tier: "metro", suggestion: 9 },
  { cityKey: "CA-ON-MISSISSAUGA", name: "Mississauga", provCode: "CA-ON", tier: "metro", suggestion: 8 },
  { cityKey: "CA-ON-BRAMPTON", name: "Brampton", provCode: "CA-ON", tier: "metro", suggestion: 8 },
  { cityKey: "CA-ON-HAMILTON", name: "Hamilton", provCode: "CA-ON", tier: "city", suggestion: 9 },
  { cityKey: "CA-ON-LONDON", name: "London", provCode: "CA-ON", tier: "city", suggestion: 9 },
  { cityKey: "CA-ON-WINDSOR", name: "Windsor", provCode: "CA-ON", tier: "city", suggestion: 10 },
  { cityKey: "CA-ON-KITCHENER", name: "Kitchener", provCode: "CA-ON", tier: "city", suggestion: 9 },
  { cityKey: "CA-ON-KINGSTON", name: "Kingston", provCode: "CA-ON", tier: "city", suggestion: 10 },
  { cityKey: "CA-ON-BARRIE", name: "Barrie", provCode: "CA-ON", tier: "city", suggestion: 9 },
  { cityKey: "CA-ON-GUELPH", name: "Guelph", provCode: "CA-ON", tier: "city", suggestion: 9 },
  { cityKey: "CA-ON-ST_CATHARINES", name: "St. Catharines", provCode: "CA-ON", tier: "city", suggestion: 9 },
  // ── Quebec ─────────────────────────────────────────────────────────────
  { cityKey: "CA-QC-MONTREAL", name: "Montreal", provCode: "CA-QC", tier: "metro", suggestion: 9 },
  { cityKey: "CA-QC-QUEBEC_CITY", name: "Quebec City", provCode: "CA-QC", tier: "metro", suggestion: 10 },
  { cityKey: "CA-QC-LAVAL", name: "Laval", provCode: "CA-QC", tier: "city", suggestion: 9 },
  { cityKey: "CA-QC-GATINEAU", name: "Gatineau", provCode: "CA-QC", tier: "city", suggestion: 9 },
  // ── British Columbia ───────────────────────────────────────────────────
  { cityKey: "CA-BC-VANCOUVER", name: "Vancouver", provCode: "CA-BC", tier: "metro", suggestion: 10 },
  { cityKey: "CA-BC-SURREY", name: "Surrey", provCode: "CA-BC", tier: "metro", suggestion: 10 },
  { cityKey: "CA-BC-VICTORIA", name: "Victoria", provCode: "CA-BC", tier: "city", suggestion: 11 },
  { cityKey: "CA-BC-KAMLOOPS", name: "Kamloops", provCode: "CA-BC", tier: "city", suggestion: 12 },
  // ── Alberta ────────────────────────────────────────────────────────────
  { cityKey: "CA-AB-CALGARY", name: "Calgary", provCode: "CA-AB", tier: "metro", suggestion: 11 },
  { cityKey: "CA-AB-EDMONTON", name: "Edmonton", provCode: "CA-AB", tier: "metro", suggestion: 11 },
  { cityKey: "CA-AB-RED_DEER", name: "Red Deer", provCode: "CA-AB", tier: "city", suggestion: 12 },
  // ── Manitoba / Saskatchewan ────────────────────────────────────────────
  { cityKey: "CA-MB-WINNIPEG", name: "Winnipeg", provCode: "CA-MB", tier: "metro", suggestion: 11 },
  { cityKey: "CA-SK-SASKATOON", name: "Saskatoon", provCode: "CA-SK", tier: "metro", suggestion: 12 },
  { cityKey: "CA-SK-REGINA", name: "Regina", provCode: "CA-SK", tier: "city", suggestion: 12 },
  // ── Atlantic ───────────────────────────────────────────────────────────
  { cityKey: "CA-NS-HALIFAX", name: "Halifax", provCode: "CA-NS", tier: "metro", suggestion: 13 },
  { cityKey: "CA-NB-MONCTON", name: "Moncton", provCode: "CA-NB", tier: "city", suggestion: 13 },
  { cityKey: "CA-NB-SAINT_JOHN", name: "Saint John", provCode: "CA-NB", tier: "city", suggestion: 13 },
  { cityKey: "CA-NL-ST_JOHNS", name: "St. John's", provCode: "CA-NL", tier: "city", suggestion: 15 },
]

/** Normalize free-text city input to the lookup key ("kingston" → CA-ON-KINGSTON). */
export function cityKeyFor(provCode: string, cityName: string): string {
  const norm = cityName
    .trim()
    .toUpperCase()
    .replace(/\./g, "") // St. Catharines → ST CATHARINES
    .replace(/['’]/g, "") // St John's → ST JOHNS
    .replace(/\s+/g, "_")
    .replace(/[^A-Z0-9_]/g, "")
  return `${provCode}-${norm}`
}

/** Find a configured city matching free-text input for a province. */
export function matchCity(provCode: string, cityName: string): CaCity | undefined {
  const key = cityKeyFor(provCode, cityName)
  return CA_CITIES.find((c) => c.cityKey === key)
}
