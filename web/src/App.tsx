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
        if (alive) timer = setTimeout(connect, 3000)
      }
      ws.onmessage = (e) => {
        try {
          const data = JSON.parse(e.data)
          if (data.type === 'state') {
            onMsgRef.current({
              id: ++idRef.current,
              type: 'state',
              title: data.state,
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

/* ===== Component: Emit Console ===== */
function EmitConsole({ schema, onEmit }: { schema: Schema | null; onEmit: (item: FeedItem) => void }) {
  const events = schema?.nodes.flatMap(n => n.emits?.map(e => e.event) ?? []) ?? []
  const uniqueEvents = [...new Set(events)]

  const [event, setEvent] = useState(uniqueEvents[0] ?? '')
  const [payload, setPayload] = useState('{\n  "email": "user@example.com"\n}')
  const [sending, setSending] = useState(false)
  const idRef = useRef(1000)

  useEffect(() => {
    if (uniqueEvents.length > 0 && !event) setEvent(uniqueEvents[0])
  }, [uniqueEvents])

  useEffect(() => {
    if (!schema) return
    for (const node of schema.nodes) {
      const emit = node.emits?.find(e => e.event === event)
      if (emit?.fields?.length) {
        const tpl: Record<string, string> = {}
        for (const f of emit.fields) {
          tpl[f.name] = f.field_type === 'number' ? '0' as unknown as string : `sample_${f.name}`
        }
        setPayload(JSON.stringify(tpl, null, 2))
        return
      }
    }
  }, [event, schema])

  const handleSubmit = async () => {
    if (!event || sending) return
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
        title: event,
        payload: data,
        time: new Date(),
      })
    } catch (err: unknown) {
      onEmit({
        id: ++idRef.current,
        type: 'error',
        title: event,
        payload: { error: String(err) },
        time: new Date(),
      })
    }
    setSending(false)
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
      e.preventDefault()
      handleSubmit()
    }
  }

  return (
    <div className="panel">
      <div className="panel-header">
        <span className="panel-title">EMIT CONSOLE</span>
        <span className="panel-tag">POST /emit</span>
      </div>
      <div className="panel-body">
        <div className="field-group">
          <div className="field-label">Target Event</div>
          <select className="select-control" value={event} onChange={e => setEvent(e.target.value)}>
            {uniqueEvents.length === 0 && <option value="">(No events declared)</option>}
            {uniqueEvents.map(ev => <option key={ev} value={ev}>{ev}</option>)}
          </select>
        </div>
        <div className="field-group">
          <div className="field-label">
            <span>Payload</span>
            <span className="field-hint">Press ⌘+Enter to submit</span>
          </div>
          <textarea
            className="code-textarea"
            value={payload}
            onChange={e => setPayload(e.target.value)}
            onKeyDown={handleKeyDown}
            rows={5}
            spellCheck={false}
          />
        </div>
        <button className="btn-primary" onClick={handleSubmit} disabled={sending || !event}>
          {sending ? 'Emitting...' : `Dispatch ${event}`}
        </button>
      </div>
    </div>
  )
}

/* ===== Component: Event Stream ===== */
function EventStream({ items, onClear }: { items: FeedItem[]; onClear: () => void }) {
  const [filter, setFilter] = useState<'all' | 'emit' | 'state' | 'error'>('all')
  const feedRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (feedRef.current) feedRef.current.scrollTop = 0
  }, [items.length])

  const filteredItems = items.filter(item => {
    if (filter === 'all') return true
    return item.type === filter
  })

  return (
    <div className="panel" style={{ flex: 1 }}>
      <div className="panel-header">
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <span className="panel-title">LIVE EVENT STREAM</span>
          <div className="tab-group">
            {(['all', 'emit', 'state', 'error'] as const).map(tab => (
              <button
                key={tab}
                className={`tab-btn ${filter === tab ? 'active' : ''}`}
                onClick={() => setFilter(tab)}
              >
                {tab.toUpperCase()}
              </button>
            ))}
          </div>
        </div>
        <button className="btn-ghost" onClick={onClear}>Clear</button>
      </div>
      <div className="feed-stream" ref={feedRef}>
        {filteredItems.length === 0 && (
          <div className="empty-state">No events recorded in stream</div>
        )}
        {filteredItems.map(item => (
          <div key={item.id} className="feed-row">
            <div className="feed-row-header">
              <div className="feed-meta">
                <span className={`badge-tag badge-${item.type}`}>{item.type}</span>
                <span className="feed-title">{item.title}</span>
              </div>
              <span className="feed-timestamp">{item.time.toLocaleTimeString()}</span>
            </div>
            {item.payload && (
              <pre className="code-block">{JSON.stringify(item.payload, null, 2)}</pre>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}

/* ===== Component: Schema Inspector ===== */
function SchemaInspector({ schema }: { schema: Schema | null }) {
  if (!schema) {
    return (
      <div className="panel">
        <div className="panel-header"><span className="panel-title">MANIFEST SCHEMA</span></div>
        <div className="panel-body empty-state">Loading schema...</div>
      </div>
    )
  }

  return (
    <div className="panel">
      <div className="panel-header">
        <span className="panel-title">MANIFEST SCHEMA</span>
        <span className="panel-tag">v{schema.spine_version}</span>
      </div>
      <div className="panel-body">
        <div className="schema-block">
          <div className="schema-label">NODES ({schema.nodes.length})</div>
          {schema.nodes.map(node => (
            <div key={node.name} className="tree-item">
              <div className="tree-item-title">{node.name}</div>
              <div className="tree-tags">
                {node.emits?.map(e => (
                  <span key={e.event} className="tag-badge" style={{ color: 'var(--blue)' }}>emits:{e.event}</span>
                ))}
                {node.listens?.map(l => (
                  <span key={l.state} className="tag-badge" style={{ color: 'var(--emerald)' }}>listens:{l.state}</span>
                ))}
              </div>
            </div>
          ))}
        </div>

        <div className="schema-block">
          <div className="schema-label">ROUTES ({schema.routes.length})</div>
          {schema.routes.map((r, i) => (
            <div key={i} className="tree-item">
              <div className="tree-tags">
                <span className="tag-badge" style={{ color: 'var(--blue)' }}>{r.on_event}</span>
                <span style={{ color: 'var(--text-dim)' }}>→</span>
                {r.steps.map((s, j) => (
                  <span key={j} className="tag-badge">{s.action}{s.table ? `:${s.table}` : ''}</span>
                ))}
                {r.emit_state && (
                  <>
                    <span style={{ color: 'var(--text-dim)' }}>→</span>
                    <span className="tag-badge" style={{ color: 'var(--emerald)' }}>{r.emit_state}</span>
                  </>
                )}
              </div>
            </div>
          ))}
        </div>

        <div className="schema-block">
          <div className="schema-label">TABLES ({schema.db_tables?.length ?? 0})</div>
          <div className="tree-tags">
            {schema.db_tables?.map(t => (
              <span key={t} className="tag-badge">{t}</span>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}

/* ===== Main App ===== */
export default function App() {
  const [feed, setFeed] = useState<FeedItem[]>([])
  const [emitCount, setEmitCount] = useState(0)
  const [stateCount, setStateCount] = useState(0)
  const [errorCount, setErrorCount] = useState(0)

  const addItem = useCallback((item: FeedItem) => {
    setFeed(prev => [item, ...prev].slice(0, 300))
    if (item.type === 'state') setStateCount(c => c + 1)
    else if (item.type === 'emit') setEmitCount(c => c + 1)
    else if (item.type === 'error') setErrorCount(c => c + 1)
  }, [])

  const connected = useSpineWS(addItem)
  const schema = useSchema()

  return (
    <div className="app">
      <header className="header">
        <div className="brand">
          <span className="logo-text">SPINE // ENGINE</span>
          <span className="brand-badge">v2.0.0</span>
        </div>
        <div className="status-pill">
          <div className={`status-dot ${connected ? 'active' : 'inactive'}`} />
          <span>{connected ? 'CONNECTED (WS: LIVE)' : 'DISCONNECTED'}</span>
        </div>
      </header>

      <div className="metrics-strip">
        <div className="metric-card">
          <span className="metric-label">Events Emitted</span>
          <span className="metric-value">{emitCount}</span>
        </div>
        <div className="metric-card">
          <span className="metric-label">States Broadcast</span>
          <span className="metric-value">{stateCount}</span>
        </div>
        <div className="metric-card">
          <span className="metric-label">Active Nodes</span>
          <span className="metric-value">{schema?.nodes.length ?? '0'}</span>
        </div>
        <div className="metric-card">
          <span className="metric-label">Registered Routes</span>
          <span className="metric-value">{schema?.routes.length ?? '0'}</span>
        </div>
      </div>

      <div className="grid-layout">
        <div className="col-sidebar">
          <EmitConsole schema={schema} onEmit={addItem} />
          <SchemaInspector schema={schema} />
        </div>
        <div className="col-main">
          <EventStream items={feed} onClear={() => setFeed([])} />
        </div>
      </div>
    </div>
  )
}
