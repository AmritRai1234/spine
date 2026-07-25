import { useState, useEffect, useRef, useCallback } from 'react'

/* ===== Types ===== */
interface PayloadField { name: string; field_type: string }
interface EmitDef { event: string; fields?: PayloadField[] }
interface ListenDef { state: string; fields?: PayloadField[] }
interface NodeDef { name: string; owns_files?: string[]; emits?: EmitDef[]; listens?: ListenDef[] }
interface RouteDef { on_event: string; steps: { action: string; table?: string }[]; emit_state?: string }
interface Schema { spine_version: number; db_tables: string[]; nodes: NodeDef[]; routes: RouteDef[] }

interface FeedItem {
  id: number
  type: 'state' | 'emit' | 'error'
  title: string
  payload?: Record<string, unknown>
  time: Date
}

/* ===== Hook: WebSocket ===== */
function useSpineWS(onMessage: (data: FeedItem) => void) {
  const [connected, setConnected] = useState(false)
  const idRef = useRef(0)
  const onMsgRef = useRef(onMessage)
  onMsgRef.current = onMessage

  useEffect(() => {
    let ws: WebSocket | null = null
    let timer: ReturnType<typeof setTimeout> | null = null
    let alive = true

    function connect() {
      if (!alive) return
      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      const url = `${proto}//${window.location.host}/ws`
      ws = new WebSocket(url)

      ws.onopen = () => setConnected(true)
      ws.onclose = () => {
        setConnected(false)
        if (alive) timer = setTimeout(connect, 3000)  // reconnect silently
      }
      ws.onmessage = (e) => {
        try {
          const data = JSON.parse(e.data)
          if (data.type === 'state') {
            onMsgRef.current({
              id: ++idRef.current,
              type: 'state',
              title: `⚡ ${data.state}`,
              payload: data.payload,
              time: new Date(data.timestamp),
            })
          }
        } catch {}
      }
    }

    connect()
    return () => { alive = false; if (timer) clearTimeout(timer); ws?.close() }
  }, [])

  return connected
}

/* ===== Hook: Schema ===== */
function useSchema() {
  const [schema, setSchema] = useState<Schema | null>(null)
  useEffect(() => {
    fetch('/schema').then(r => r.json()).then(setSchema).catch(() => {})
  }, [])
  return schema
}

/* ===== Components ===== */

function StatCard({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="stat-card">
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
    </div>
  )
}

function EmitPanel({ schema, onEmit }: { schema: Schema | null; onEmit: (item: FeedItem) => void }) {
  const events = schema?.nodes.flatMap(n => n.emits?.map(e => e.event) ?? []) ?? []
  const unique = [...new Set(events)]

  const [event, setEvent] = useState(unique[0] ?? '')
  const [payload, setPayload] = useState('{\n  "email": "user@example.com"\n}')
  const [sending, setSending] = useState(false)
  const idRef = useRef(1000)

  useEffect(() => {
    if (unique.length > 0 && !event) setEvent(unique[0])
  }, [unique])

  // Auto-fill payload template when event changes
  useEffect(() => {
    if (!schema) return
    for (const node of schema.nodes) {
      const emit = node.emits?.find(e => e.event === event)
      if (emit?.fields?.length) {
        const tpl: Record<string, string> = {}
        for (const f of emit.fields) {
          tpl[f.name] = f.field_type === 'number' ? '0' as unknown as string : `example_${f.name}`
        }
        setPayload(JSON.stringify(tpl, null, 2))
        return
      }
    }
  }, [event, schema])

  const handleSubmit = async () => {
    setSending(true)
    try {
      const body = { event, payload: JSON.parse(payload) }
      const res = await fetch('/emit', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      const data = await res.json()
      onEmit({
        id: ++idRef.current,
        type: res.ok ? 'emit' : 'error',
        title: res.ok ? `→ ${event}` : `✗ ${event}`,
        payload: data,
        time: new Date(),
      })
    } catch (err: unknown) {
      onEmit({
        id: ++idRef.current,
        type: 'error',
        title: `✗ ${event}`,
        payload: { error: String(err) },
        time: new Date(),
      })
    }
    setSending(false)
  }

  return (
    <div className="card">
      <div className="card-header">
        <span className="card-title"><span className="card-title-icon">📡</span> Emit Event</span>
      </div>
      <div className="card-body">
        <div className="form-group">
          <label className="form-label">Event</label>
          <select className="form-select" value={event} onChange={e => setEvent(e.target.value)}>
            {unique.map(ev => <option key={ev} value={ev}>{ev}</option>)}
          </select>
        </div>
        <div className="form-group">
          <label className="form-label">Payload (JSON)</label>
          <textarea className="form-textarea" value={payload} onChange={e => setPayload(e.target.value)} rows={5} spellCheck={false} />
        </div>
        <button className="btn-emit" onClick={handleSubmit} disabled={sending}>
          {sending ? 'Emitting...' : `Emit ${event}`}
        </button>
      </div>
    </div>
  )
}

function EventFeed({ items }: { items: FeedItem[] }) {
  const feedRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (feedRef.current) feedRef.current.scrollTop = 0
  }, [items.length])

  return (
    <div className="card" style={{ flex: 1 }}>
      <div className="card-header">
        <span className="card-title"><span className="card-title-icon">⚡</span> Live Event Feed</span>
        <span className="card-badge" style={{ background: 'var(--accent-glow)', color: 'var(--accent)' }}>
          {items.length} events
        </span>
      </div>
      <div className="feed" ref={feedRef}>
        {items.length === 0 && (
          <div className="feed-empty">No events yet — emit one from the panel</div>
        )}
        {items.map(item => (
          <div key={item.id} className="feed-item">
            <div className={`feed-dot ${item.type}`} />
            <div className="feed-content">
              <div className="feed-title">{item.title}</div>
              <div className="feed-time">{item.time.toLocaleTimeString()}</div>
              {item.payload && (
                <div className="feed-payload">{JSON.stringify(item.payload, null, 2)}</div>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}

function SchemaPanel({ schema }: { schema: Schema | null }) {
  if (!schema) return <div className="card"><div className="card-body feed-empty">Loading schema...</div></div>

  return (
    <div className="card">
      <div className="card-header">
        <span className="card-title"><span className="card-title-icon">🗺️</span> Schema</span>
        <span className="card-badge" style={{ background: 'var(--cyan-dim)', color: 'var(--cyan)' }}>
          v{schema.spine_version}
        </span>
      </div>
      <div className="card-body">
        <div className="schema-section">
          <div className="schema-section-title">Nodes ({schema.nodes.length})</div>
          {schema.nodes.map(node => (
            <div key={node.name} className="node-card">
              <div className="node-name">◆ {node.name}</div>
              <div className="node-tags">
                {node.emits?.map(e => <span key={e.event} className="tag tag-emit">{e.event}</span>)}
                {node.listens?.map(l => <span key={l.state} className="tag tag-listen">{l.state}</span>)}
              </div>
            </div>
          ))}
        </div>
        <div className="schema-section">
          <div className="schema-section-title">Routes ({schema.routes.length})</div>
          {schema.routes.map((r, i) => (
            <div key={i} className="node-card">
              <div className="node-tags">
                <span className="tag tag-emit">{r.on_event}</span>
                <span style={{ color: 'var(--text-muted)', fontSize: 11 }}>→</span>
                {r.steps.map((s, j) => (
                  <span key={j} className="tag tag-route">{s.action}{s.table ? `:${s.table}` : ''}</span>
                ))}
                {r.emit_state && <>
                  <span style={{ color: 'var(--text-muted)', fontSize: 11 }}>→</span>
                  <span className="tag tag-listen">{r.emit_state}</span>
                </>}
              </div>
            </div>
          ))}
        </div>
        <div className="schema-section">
          <div className="schema-section-title">Tables ({schema.db_tables?.length ?? 0})</div>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
            {schema.db_tables?.map(t => <span key={t} className="tag tag-table">{t}</span>)}
          </div>
        </div>
      </div>
    </div>
  )
}

/* ===== App ===== */
export default function App() {
  const [feed, setFeed] = useState<FeedItem[]>([])
  const [emitCount, setEmitCount] = useState(0)
  const [stateCount, setStateCount] = useState(0)
  const [errorCount, setErrorCount] = useState(0)

  const addItem = useCallback((item: FeedItem) => {
    setFeed(prev => [item, ...prev].slice(0, 200))
    if (item.type === 'state') setStateCount(c => c + 1)
    else if (item.type === 'emit') setEmitCount(c => c + 1)
    else if (item.type === 'error') setErrorCount(c => c + 1)
  }, [])

  const connected = useSpineWS(addItem)
  const schema = useSchema()

  return (
    <div className="app">
      <header className="header">
        <div className="header-left">
          <span className="logo">⚡ SPINE</span>
          <span className="logo-tag">Dashboard</span>
        </div>
        <div className="connection-badge">
          <div className={`connection-dot ${connected ? 'connected' : 'disconnected'}`} />
          <span>{connected ? 'WebSocket Connected' : 'Disconnected'}</span>
        </div>
      </header>

      <div className="stats-bar">
        <StatCard label="Events Emitted" value={emitCount} />
        <StatCard label="States Received" value={stateCount} />
        <StatCard label="Errors" value={errorCount} />
        <StatCard label="Nodes" value={schema?.nodes.length ?? '—'} />
      </div>

      <div className="grid">
        <div className="sidebar">
          <EmitPanel schema={schema} onEmit={addItem} />
          <SchemaPanel schema={schema} />
        </div>
        <div className="main-content">
          <EventFeed items={feed} />
        </div>
      </div>
    </div>
  )
}
