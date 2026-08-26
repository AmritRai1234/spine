import type { LucideIcon } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"

interface EmptyStateProps {
  icon: LucideIcon
  title: string
  description: string
  actionLabel?: string
  onAction?: () => void
  /** Optional secondary hint line under the action (e.g. a learn-more link). */
  hint?: string
}

/**
 * Shopify-style illustrated empty state: centered icon medallion, headline,
 * supporting copy, and a single primary action. Used when an admin table has
 * no rows yet (first-run store) so the panel never shows a bare empty table.
 */
export default function EmptyState({ icon: Icon, title, description, actionLabel, onAction, hint }: EmptyStateProps) {
  return (
    <Card className="border-dashed">
      <CardContent className="flex flex-col items-center gap-3 px-6 py-16 text-center">
        <div className="flex h-20 w-20 items-center justify-center rounded-full bg-primary/10">
          <Icon className="h-9 w-9 text-primary" strokeWidth={1.5} />
        </div>
        <h3 className="mt-2 text-lg font-semibold tracking-tight">{title}</h3>
        <p className="max-w-sm text-sm leading-relaxed text-muted-foreground">{description}</p>
        {actionLabel && onAction && (
          <Button size="sm" className="mt-3" onClick={onAction}>
            {actionLabel}
          </Button>
        )}
        {hint && <p className="mt-1 text-xs text-muted-foreground">{hint}</p>}
      </CardContent>
    </Card>
  )
}
