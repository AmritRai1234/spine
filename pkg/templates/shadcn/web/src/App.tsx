import { useCallback, useEffect, useState, type FormEvent } from "react"
import { Plus, Radio, RefreshCw } from "lucide-react"

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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { useSpineStateTick } from "@/hooks/use-spine"
import { spine } from "@/lib/spine"

interface TaskRow {
  id: string
  title: string
  created_at: string
}

export default function App() {
  const [title, setTitle] = useState("")
  const [tasks, setTasks] = useState<TaskRow[]>([])
  const [loading, setLoading] = useState(false)
  const [connected, setConnected] = useState(false)

  // Re-fetch the tasks table every time the engine broadcasts TASK_CREATED
  const tick = useSpineStateTick("TASK_CREATED")

  const refresh = useCallback(async () => {
    setLoading(true)
    try {
      const res = await spine.queryTable("tasks", { limit: 50 })
      setTasks((res.rows ?? []) as unknown as TaskRow[])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refresh()
  }, [refresh, tick])

  // Reflect WS connection state in the header badge
  useEffect(() => spine.onConnectionChange(setConnected), [])

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (!title.trim()) return
    await spine.emit("NEW_TASK", { title: title.trim() })
    setTitle("")
    // State broadcast triggers refetch via tick; refresh is instant fallback
    void refresh()
  }

  return (
    <main className="mx-auto flex min-h-screen max-w-3xl flex-col gap-6 p-6">
      <header className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Spine Tasks</h1>
          <p className="text-muted-foreground text-sm">
            Declarative backend · shadcn/ui frontend · Vite HMR
          </p>
        </div>
        <Badge variant={connected ? "default" : "secondary"}>
          <Radio className="size-3" />
          {connected ? "Live" : "Connecting…"}
        </Badge>
      </header>

      <Card>
        <CardHeader>
          <CardTitle>New task</CardTitle>
          <CardDescription>
            Emits <code className="text-xs">NEW_TASK</code> → Spine persists and
            broadcasts <code className="text-xs">TASK_CREATED</code> over
            WebSocket.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="flex gap-2">
            <Input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="What needs doing?"
              aria-label="Task title"
            />
            <Button type="submit" disabled={!title.trim()}>
              <Plus /> Add
            </Button>
          </form>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <div className="flex flex-col gap-1.5">
            <CardTitle>Tasks</CardTitle>
            <CardDescription>
              Live table — updates arrive via WebSocket state broadcasts.
            </CardDescription>
          </div>
          <Button variant="outline" size="sm" onClick={() => void refresh()} disabled={loading}>
            <RefreshCw className={loading ? "animate-spin" : ""} /> Refresh
          </Button>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Task</TableHead>
                <TableHead className="text-right">Created</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {tasks.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={2} className="text-muted-foreground h-16 text-center">
                    No tasks yet — add one above.
                  </TableCell>
                </TableRow>
              ) : (
                tasks.map((task) => (
                  <TableRow key={task.id}>
                    <TableCell>{task.title}</TableCell>
                    <TableCell className="text-muted-foreground text-right text-xs">
                      {new Date(task.created_at).toLocaleString()}
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </main>
  )
}
