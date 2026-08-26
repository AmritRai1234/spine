import { spawn } from 'child_process';
import path from 'path';
import fs from 'fs';
import axios from 'axios';
import autocannon from 'autocannon';
import WebSocket from 'ws';

// Honest comparative benchmark runner: every reported number is measured
// (or explicitly labeled as an analytical value). Tests that previously
// returned hardcoded constants with pre-declared winners (memory per
// connection, "internal bus latency", FCP/INP, JS heap) were removed — they
// measured nothing.

const SPINE_PORT = 8080;
const TRADITIONAL_PORT = 3000;

const SPINE_URL = `http://localhost:${SPINE_PORT}`;
const TRADITIONAL_URL = `http://localhost:${TRADITIONAL_PORT}`;

interface TestResult {
  metric: string;
  spine: string;
  traditional: string;
  winner: string;
  notes: string;
}

const results: TestResult[] = [];

async function delay(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function waitForServer(url: string, maxRetries = 40): Promise<boolean> {
  for (let i = 0; i < maxRetries; i++) {
    try {
      await axios.get(url, { timeout: 1000 });
      return true;
    } catch {
      await delay(250);
    }
  }
  return false;
}

function runAutocannon(url: string, method: 'GET' | 'POST' = 'GET', body?: any, connections = 50, duration = 4) {
  return new Promise<autocannon.Result>((resolve, reject) => {
    const opts: autocannon.Options = {
      url,
      method,
      connections,
      duration,
      headers: { 'content-type': 'application/json' },
      body: body ? JSON.stringify(body) : undefined,
    };
    autocannon(opts, (err, res) => {
      if (err) reject(err);
      else resolve(res);
    });
  });
}

async function medianOf<T>(samples: T[], value: (t: T) => number): Promise<number> {
  const vals = samples.map(value).sort((a, b) => a - b);
  return vals[Math.floor(vals.length / 2)];
}

async function main() {
  console.log('===============================================================');
  console.log(' SPINE vs TRADITIONAL BACKEND: BENCHMARK SUITE RUNNER');
  console.log(' (every number below is measured; nothing is hardcoded)');
  console.log('===============================================================\n');

  // 1. Start Traditional Server
  console.log('📌 Starting Traditional REST Server (Express)...');
  const tradProc = spawn('npx', ['ts-node', path.join(__dirname, 'traditional-server.ts')], {
    stdio: 'ignore',
    shell: true,
  });
  tradProc.on('error', (err) => {
    console.error('❌ Failed to start the traditional server:', err.message);
    process.exit(1);
  });

  // 2. Start Spine Server — the Makefile builds to ./bin/spine (there is no
  // binary at the repo root; the old path crashed the runner out of the box).
  const spineBinary = path.join(__dirname, '..', 'bin', 'spine');
  if (!fs.existsSync(spineBinary)) {
    console.error(`❌ Spine binary not found at ${spineBinary}`);
    console.error('   Build it first:  make build   (or: go build -tags sqlite_fts5 -o bin/spine ./cmd/spine)');
    tradProc.kill();
    process.exit(1);
  }
  console.log('📌 Starting Spine Event Engine Server...');
  const spineManifest = path.join(__dirname, 'spine-app.spine');
  // --allow-no-auth: this is a local benchmark harness; the engine refuses to
  // start unauthenticated otherwise.
  const spineProc = spawn(spineBinary, ['serve', spineManifest, '--port', String(SPINE_PORT), '--allow-no-auth'], {
    stdio: 'ignore',
  });
  spineProc.on('error', (err) => {
    console.error('❌ Failed to start the Spine server:', err.message);
    tradProc.kill();
    process.exit(1);
  });

  try {
    console.log('⏳ Waiting for servers to be ready...');
    const tradReady = await waitForServer(`${TRADITIONAL_URL}/health`);
    const spineReady = await waitForServer(`${SPINE_URL}/health`);

    if (!tradReady) throw new Error('Traditional Server failed to start on port 3000');
    if (!spineReady) throw new Error('Spine Server failed to start on port 8080');

    console.log('✅ Both servers are online and responsive!\n');

    // ------------------------------------------------------------------------
    // TEST 1: Single Request Latency & TTFB (5 samples, median — a single
    // unwarmed sample was noise before)
    // ------------------------------------------------------------------------
    console.log('📊 Running Test 1: Single Request Latency & Time to First Byte (TTFB)...');

    const spineTTFBSamples: number[] = [];
    const tradTTFBSamples: number[] = [];
    for (let i = 0; i < 5; i++) {
      let t0 = performance.now();
      await axios.get(`${SPINE_URL}/health`);
      spineTTFBSamples.push(performance.now() - t0);

      t0 = performance.now();
      await axios.get(`${TRADITIONAL_URL}/health`);
      tradTTFBSamples.push(performance.now() - t0);
    }
    const spineTTFB = await medianOf(spineTTFBSamples, (v) => v);
    const tradTTFB = await medianOf(tradTTFBSamples, (v) => v);

    results.push({
      metric: 'Test 1: Single Request Latency (TTFB, median of 5)',
      spine: `${spineTTFB.toFixed(2)} ms`,
      traditional: `${tradTTFB.toFixed(2)} ms`,
      winner: spineTTFB <= tradTTFB ? 'Spine Engine' : 'Traditional REST',
      notes: 'Median round-trip over 5 samples',
    });

    // ------------------------------------------------------------------------
    // TEST 2 + 3: High Concurrency Throughput + P99 (warmup run, then measure)
    // ------------------------------------------------------------------------
    console.log('📊 Running Test 2/3: Throughput + P99 under 50 connections...');
    await runAutocannon(`${SPINE_URL}/health`, 'GET', undefined, 50, 2); // warmup
    await runAutocannon(`${TRADITIONAL_URL}/health`, 'GET', undefined, 50, 2); // warmup

    const spineRPSRes = await runAutocannon(`${SPINE_URL}/health`, 'GET', undefined, 50, 5);
    const tradRPSRes = await runAutocannon(`${TRADITIONAL_URL}/health`, 'GET', undefined, 50, 5);

    const spineRPS = Math.round(spineRPSRes.requests.average);
    const tradRPS = Math.round(tradRPSRes.requests.average);

    results.push({
      metric: 'Test 2: Throughput (Req/sec)',
      spine: `${spineRPS.toLocaleString()} RPS`,
      traditional: `${tradRPS.toLocaleString()} RPS`,
      winner: spineRPS >= tradRPS ? 'Spine Engine' : 'Traditional REST',
      notes: '5s run after 2s warmup, 50 concurrent connections',
    });

    const spineP99 = spineRPSRes.latency.p99;
    const tradP99 = tradRPSRes.latency.p99;

    results.push({
      metric: 'Test 3: P99 Tail Latency under Load',
      spine: `${spineP99} ms`,
      traditional: `${tradP99} ms`,
      winner: spineP99 <= tradP99 ? 'Spine Engine' : 'Traditional REST',
      notes: '99th percentile response time during the measured run',
    });

    // ------------------------------------------------------------------------
    // TEST 4: Concurrent Database Write Latency (measured on both sides)
    // ------------------------------------------------------------------------
    console.log('📊 Running Test 4: Concurrent Database Write Performance...');
    const spinePayload = { event: 'SUBMIT_LEAD', payload: { email: 'benchmark@spine.dev', name: 'Perf User' } };
    const tradPayload = { email: 'benchmark@spine.dev', name: 'Perf User' };

    await runAutocannon(`${SPINE_URL}/emit`, 'POST', spinePayload, 20, 2); // warmup
    await runAutocannon(`${TRADITIONAL_URL}/api/lead`, 'POST', tradPayload, 20, 2); // warmup

    const spineWriteRes = await runAutocannon(`${SPINE_URL}/emit`, 'POST', spinePayload, 20, 5);
    const tradWriteRes = await runAutocannon(`${TRADITIONAL_URL}/api/lead`, 'POST', tradPayload, 20, 5);

    const spineWriteRPS = Math.round(spineWriteRes.requests.average);
    const tradWriteRPS = Math.round(tradWriteRes.requests.average);

    results.push({
      metric: 'Test 4: DB Write Throughput',
      spine: `${spineWriteRPS.toLocaleString()} Writes/sec`,
      traditional: `${tradWriteRPS.toLocaleString()} Writes/sec`,
      winner: spineWriteRPS >= tradWriteRPS ? 'Spine Engine' : 'Traditional REST',
      notes: '5s run after 2s warmup, 20 concurrent connections',
    });

    // ------------------------------------------------------------------------
    // TEST 5: Real-Time WebSocket Push vs. HTTP Polling Delay
    // The Spine side is MEASURED (emit → broadcast → client receive). The
    // traditional side is the analytical expected delay of a 500ms poll
    // (interval/2) — that is the correct expectation, not a measurement.
    // ------------------------------------------------------------------------
    console.log('📊 Running Test 5: Real-time State Delivery (WS Push vs Polling)...');

    const spineWsLatency = await new Promise<number>((resolve) => {
      let settled = false;
      const finish = (v: number) => {
        if (settled) return;
        settled = true;
        resolve(v);
      };
      const ws = new WebSocket(`ws://localhost:${SPINE_PORT}/ws`);
      let startTime = 0;
      ws.on('open', async () => {
        startTime = performance.now();
        try {
          await axios.post(`${SPINE_URL}/emit`, { event: 'UPDATE_ITEM', payload: { id: 'item-1', value: 'high-speed' } });
        } catch {
          finish(Number.NaN);
          ws.close();
        }
      });
      ws.on('message', () => {
        finish(performance.now() - startTime);
        ws.close();
      });
      ws.on('error', () => finish(Number.NaN));
      // If the broadcast never arrives, report a failure — never a fake value.
      setTimeout(() => finish(Number.NaN), 3000);
    });

    const pollIntervalMs = 500;
    const tradPollingLatency = pollIntervalMs / 2; // analytical expected avg delay for 500ms polling

    if (Number.isNaN(spineWsLatency)) {
      results.push({
        metric: 'Test 5: Real-time State Push Latency',
        spine: 'MEASUREMENT FAILED (no broadcast received)',
        traditional: `${tradPollingLatency} ms (500ms polling avg)`,
        winner: '—',
        notes: 'Spine WS push could not be measured in this environment',
      });
    } else {
      results.push({
        metric: 'Test 5: Real-time State Push Latency',
        spine: `${spineWsLatency.toFixed(2)} ms (measured WS push)`,
        traditional: `${tradPollingLatency} ms (500ms polling avg, analytical)`,
        winner: spineWsLatency <= tradPollingLatency ? 'Spine Engine' : 'Traditional REST',
        notes: 'Emit→broadcast→client receive; polling side is the expected average delay',
      });
    }

    // ------------------------------------------------------------------------
    // PRINT COMPARATIVE REPORT TABLE
    // ------------------------------------------------------------------------
    console.log('\n========================================================================================');
    console.log(' 📈 BENCHMARK RESULTS SUMMARY: SPINE ENGINE vs. TRADITIONAL BACKEND');
    console.log('========================================================================================\n');

    console.table(
      results.map((r) => ({
        'Performance Test': r.metric,
        'Spine Engine': r.spine,
        'Traditional REST': r.traditional,
        'Top Performer': r.winner,
      }))
    );

    console.log('\nNotes:');
    console.log(' - Test 1-4 measure HTTP behavior; Test 5 measures WS push (Spine) vs polling expectation.');
    console.log(' - For in-process engine microbenchmarks (emit enqueue, E2E latency), run:');
    console.log('     go test -tags sqlite_fts5 ./tests/ -bench=. -benchmem -run=^$');
    console.log('   Those numbers are also what the README badges reference.');

  } catch (err: any) {
    console.error('❌ Benchmark error:', err.message);
    process.exitCode = 1;
  } finally {
    console.log('\n🧹 Cleaning up test servers...');
    spineProc.kill();
    tradProc.kill();
  }
}

main();
