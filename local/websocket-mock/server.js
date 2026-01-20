// Mock WebSocket server for local development
const http = require('http');
const WebSocket = require('ws');

const PORT = process.env.PORT || 3000;

// Create HTTP server for health checks
const server = http.createServer((req, res) => {
  if (req.url === '/health') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ status: 'healthy' }));
    return;
  }

  // Mock API Gateway Management API endpoint
  if (req.url.startsWith('/@connections/')) {
    const connectionId = req.url.split('/')[2];

    if (req.method === 'POST') {
      let body = '';
      req.on('data', chunk => body += chunk);
      req.on('end', () => {
        console.log(`[API] Sending to ${connectionId}:`, body.substring(0, 200));

        // Find and send to the connection
        const client = clients.get(connectionId);
        if (client && client.readyState === WebSocket.OPEN) {
          client.send(body);
          res.writeHead(200);
          res.end();
        } else {
          res.writeHead(410); // Gone
          res.end('Connection not found');
        }
      });
      return;
    }
  }

  res.writeHead(404);
  res.end('Not Found');
});

// Create WebSocket server
const wss = new WebSocket.Server({ server });
const clients = new Map();
let connectionCounter = 0;

wss.on('connection', (ws, req) => {
  const connectionId = `conn-${++connectionCounter}-${Date.now()}`;
  clients.set(connectionId, ws);

  console.log(`[WS] Client connected: ${connectionId}`);

  ws.on('message', (data) => {
    const message = data.toString();
    console.log(`[WS] Received from ${connectionId}:`, message.substring(0, 200));

    try {
      const parsed = JSON.parse(message);

      // Auto-respond to registration
      if (parsed.MessageType === 'registration') {
        ws.send(JSON.stringify({
          MessageType: 'registration_success',
          UserId: parsed.UserId,
          Message: 'Registration successful'
        }));
        console.log(`[WS] User ${parsed.UserId} registered`);
      }

      // Echo back for testing
      ws.send(JSON.stringify({
        MessageType: 'echo',
        OriginalMessage: parsed,
        Timestamp: new Date().toISOString()
      }));

    } catch (e) {
      console.error(`[WS] Invalid JSON from ${connectionId}:`, e.message);
    }
  });

  ws.on('close', () => {
    clients.delete(connectionId);
    console.log(`[WS] Client disconnected: ${connectionId}`);
  });

  ws.on('error', (error) => {
    console.error(`[WS] Error for ${connectionId}:`, error.message);
  });

  // Send welcome message
  ws.send(JSON.stringify({
    MessageType: 'welcome',
    ConnectionId: connectionId,
    Message: 'Connected to mock WebSocket server'
  }));
});

server.listen(PORT, () => {
  console.log(`Mock WebSocket server running on port ${PORT}`);
  console.log(`  Health check: http://localhost:${PORT}/health`);
  console.log(`  WebSocket: ws://localhost:${PORT}`);
  console.log(`  API endpoint: http://localhost:${PORT}/@connections/{connectionId}`);
});

// Periodic stats logging
setInterval(() => {
  console.log(`[STATS] Active connections: ${clients.size}`);
}, 60000);
