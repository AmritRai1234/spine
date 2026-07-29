import express from 'express';
import http from 'http';

const app = express();
app.use(express.json());

const PORT = 3000;

// In-memory data stores simulating database & state
const leads: Array<{ email: string; name: string; timestamp: number }> = [];
const items: Record<string, { value: string; timestamp: number }> = {};
let latestStatus = { updated: false, status: 'IDLE', timestamp: Date.now() };

// Static HTML page for browser tests
app.get('/', (req, res) => {
  res.send(`
    <!DOCTYPE html>
    <html lang="en">
    <head>
      <meta charset="UTF-8">
      <title>Traditional Website</title>
      <style>body { font-family: sans-serif; padding: 2rem; background: #0f172a; color: #fff; }</style>
    </head>
    <body>
      <h1>Traditional Website (REST + Polling)</h1>
      <div id="status">Status: Initializing...</div>
      <script>
        // Simulating traditional polling loop
        setInterval(async () => {
          try {
            const res = await fetch('/api/status-poll');
            const data = await res.json();
            document.getElementById('status').innerText = 'Status: ' + data.status + ' (' + new Date(data.timestamp).toLocaleTimeString() + ')';
          } catch(e) {}
        }, 500);
      </script>
    </body>
    </html>
  `);
});

// REST Controller: Submit Lead
app.post('/api/lead', (req, res) => {
  const { email, name } = req.body || {};
  leads.push({ email: email || 'test@example.com', name: name || 'User', timestamp: Date.now() });
  latestStatus = { updated: true, status: `Lead received for ${email}`, timestamp: Date.now() };
  res.status(201).json({ success: true, message: 'Lead recorded', count: leads.length });
});

// REST Controller: Update Item
app.post('/api/item', (req, res) => {
  const { id, value } = req.body || {};
  items[id || '1'] = { value: value || 'default', timestamp: Date.now() };
  latestStatus = { updated: true, status: `Item ${id} updated`, timestamp: Date.now() };
  res.json({ success: true, id, value });
});

// Traditional Polling Endpoint
app.get('/api/status-poll', (req, res) => {
  res.json(latestStatus);
});

// Health check
app.get('/health', (req, res) => {
  res.json({ status: 'ok', server: 'Traditional REST' });
});

const server = http.createServer(app);

if (require.main === module) {
  server.listen(PORT, () => {
    console.log(`[TRADITIONAL SERVER] Running at http://localhost:${PORT}`);
  });
}

export { app, server };
