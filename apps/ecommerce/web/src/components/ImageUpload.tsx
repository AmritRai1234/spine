import { useRef, useState } from "react"
import { ImagePlus, RefreshCw, Trash2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { toast } from "sonner"

interface ImageUploadProps {
  /** Label above the control. */
  label: string
  /** Current value: a data URL (uploaded) or an external https:// URL. */
  value: string | null
  onChange: (dataUrl: string | null) => void
  /** Optional size override for the empty-state drop zone (e.g. "h-16"). */
  className?: string
}

const MAX_DIM = 1200
const JPEG_QUALITY = 0.8

/**
 * Client-side image upload: pick or drag-drop a file, downscale to ≤1200px,
 * compress to JPEG and return a data URL. Images are stored IN the product
 * row (schema evolution), so there is no separate file server — the same
 * emit that publishes the product carries the image. Small PNGs (e.g.
 * transparent logos) pass through untouched.
 */
function compressImage(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error("read failed"))
    reader.onload = () => {
      const img = new Image()
      img.onerror = () => reject(new Error("not an image"))
      img.onload = () => {
        if (file.type === "image/png" && file.size < 200 * 1024) {
          resolve(reader.result as string)
          return
        }
        const scale = Math.min(1, MAX_DIM / Math.max(img.width, img.height))
        const w = Math.max(1, Math.round(img.width * scale))
        const h = Math.max(1, Math.round(img.height * scale))
        const canvas = document.createElement("canvas")
        canvas.width = w
        canvas.height = h
        const ctx = canvas.getContext("2d")
        if (!ctx) {
          reject(new Error("canvas unavailable"))
          return
        }
        ctx.drawImage(img, 0, 0, w, h)
        resolve(canvas.toDataURL("image/jpeg", JPEG_QUALITY))
      }
      img.src = reader.result as string
    }
    reader.readAsDataURL(file)
  })
}

export default function ImageUpload({ label, value, onChange, className }: ImageUploadProps) {
  const inputRef = useRef<HTMLInputElement>(null)
  const [dragging, setDragging] = useState(false)

  async function handleFile(file: File | undefined) {
    if (!file) return
    if (!file.type.startsWith("image/")) {
      toast.error("Please choose an image file")
      return
    }
    try {
      const dataUrl = await compressImage(file)
      onChange(dataUrl)
      toast.success("Image ready")
    } catch {
      toast.error("Could not read that image — try another file")
    }
  }

  return (
    <div>
      {label && <span className="mb-1.5 block text-sm font-medium">{label}</span>}
      {value ? (
        <div className="space-y-2">
          <div className="relative overflow-hidden rounded-md border">
            <img src={value} alt={label || "upload"} className="h-32 w-full object-cover" />
          </div>
          <div className="flex gap-2">
            <Button type="button" variant="outline" size="sm" onClick={() => inputRef.current?.click()}>
              <RefreshCw className="mr-1 h-3.5 w-3.5" /> Replace
            </Button>
            <Button type="button" variant="ghost" size="sm" className="text-destructive" onClick={() => onChange(null)}>
              <Trash2 className="mr-1 h-3.5 w-3.5" /> Remove
            </Button>
          </div>
        </div>
      ) : (
        <button
          type="button"
          onClick={() => inputRef.current?.click()}
          onDragOver={(e) => { e.preventDefault(); setDragging(true) }}
          onDragLeave={() => setDragging(false)}
          onDrop={(e) => { e.preventDefault(); setDragging(false); handleFile(e.dataTransfer.files?.[0]) }}
          className={`flex h-24 w-full flex-col items-center justify-center gap-1 rounded-md border border-dashed text-muted-foreground transition-colors hover:bg-muted/50 ${
            dragging ? "border-primary bg-muted/50" : ""
          } ${className ?? ""}`}
        >
          <ImagePlus className="h-5 w-5" />
          <span className="px-2 text-center text-xs">Click or drop an image — resized to ≤1200px</span>
        </button>
      )}
      <input
        ref={inputRef}
        type="file"
        accept="image/*"
        className="hidden"
        onChange={(e) => { handleFile(e.target.files?.[0]); e.target.value = "" }}
      />
    </div>
  )
}
