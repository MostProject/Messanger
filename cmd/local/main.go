// Local development server - runs everything in one process for testing
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/MostProject/Messanger/internal/handlers"
	"github.com/MostProject/Messanger/internal/health"
	"github.com/MostProject/Messanger/internal/models"
	"github.com/MostProject/Messanger/internal/observability"
	"github.com/MostProject/Messanger/internal/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/gorilla/websocket"
)

var (
	wsPort     = "1738"
	healthPort = "8080"
)

func init() {
	if p := os.Getenv("HEALTH_PORT"); p != "" {
		healthPort = p
	}
}

// MockStore provides in-memory storage for local testing
type MockStore struct {
	connections  map[string]*models.Connection
	conversations map[string][]models.Conversation
	reportJobs   map[string]*models.ReportJob
	mu           sync.RWMutex
}

func NewMockStore() *MockStore {
	return &MockStore{
		connections:   make(map[string]*models.Connection),
		conversations: make(map[string][]models.Conversation),
		reportJobs:    make(map[string]*models.ReportJob),
	}
}

// Storage interface for the local server
type Storage interface {
	SaveConnection(ctx context.Context, conn *models.Connection) error
	DeleteConnection(ctx context.Context, connectionID string) error
	GetConnectionByUserID(ctx context.Context, userID int) (*models.Connection, error)
	GetAllConnections(ctx context.Context) ([]models.Connection, error)
	SaveMessage(ctx context.Context, msg *models.IncomingMessage) (int, error)
	GetConversation(ctx context.Context, user1, user2, limit int, lastMsgID int) ([]models.BroadcastMessage, bool, error)
}

// LocalServer runs a local development server
type LocalServer struct {
	store     *MockStore      // In-memory store (always used for connections map)
	dynamo    *storage.DynamoDBStore // Optional DynamoDB for persistence
	useDynamo bool
	wsConns   map[string]*websocket.Conn
	wsConnsMu sync.RWMutex
	upgrader  websocket.Upgrader
	logger    *observability.Logger
	health    *health.Server
}

func NewLocalServer() *LocalServer {
	server := &LocalServer{
		store:   NewMockStore(),
		wsConns: make(map[string]*websocket.Conn),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		logger: observability.NewLogger("local-server", observability.LevelDebug),
		health: health.NewServer(healthPort, "local-dev"),
	}

	// Check if DynamoDB should be used
	if os.Getenv("USE_DYNAMODB") == "true" {
		ctx := context.Background()
		endpoint := os.Getenv("AWS_ENDPOINT")

		var cfg aws.Config
		var err error

		if endpoint != "" {
			// Local development with LocalStack
			cfg, err = config.LoadDefaultConfig(ctx,
				config.WithRegion("us-east-1"),
				config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
			)
			if err == nil {
				dynamoClient := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
					o.BaseEndpoint = aws.String(endpoint)
				})
				server.dynamo = storage.NewDynamoDBStore(dynamoClient)
				server.useDynamo = true
				server.logger.Info(ctx, "DynamoDB storage enabled", map[string]interface{}{
					"endpoint": endpoint,
				})
			}
		} else {
			// Real AWS
			cfg, err = config.LoadDefaultConfig(ctx)
			if err == nil {
				dynamoClient := dynamodb.NewFromConfig(cfg)
				server.dynamo = storage.NewDynamoDBStore(dynamoClient)
				server.useDynamo = true
				server.logger.Info(ctx, "DynamoDB storage enabled (AWS)", nil)
			}
		}

		if err != nil {
			server.logger.Error(ctx, "Failed to initialize DynamoDB, using in-memory storage", err, nil)
		}
	}

	return server
}

func (s *LocalServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error(context.Background(), "WebSocket upgrade failed", err, nil)
		return
	}

	connID := fmt.Sprintf("conn-%d", time.Now().UnixNano())
	ctx := observability.WithConnectionID(context.Background(), connID)

	s.wsConnsMu.Lock()
	s.wsConns[connID] = conn
	s.wsConnsMu.Unlock()

	s.logger.Info(ctx, "WebSocket client connected", map[string]interface{}{
		"connection_id": connID,
		"remote_addr":   r.RemoteAddr,
	})

	// Send welcome message
	conn.WriteJSON(map[string]interface{}{
		"MessageType":  "welcome",
		"ConnectionId": connID,
		"Message":      "Connected to local development server",
	})

	defer func() {
		s.wsConnsMu.Lock()
		delete(s.wsConns, connID)
		s.wsConnsMu.Unlock()
		conn.Close()
		s.logger.Info(ctx, "WebSocket client disconnected", nil)
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				s.logger.Error(ctx, "WebSocket read error", err, nil)
			}
			break
		}

		s.handleMessage(ctx, connID, message)
	}
}

func (s *LocalServer) handleMessage(ctx context.Context, connID string, message []byte) {
	var base models.BaseMessage
	if err := json.Unmarshal(message, &base); err != nil {
		s.logger.Error(ctx, "Invalid message format", err, nil)
		return
	}

	s.logger.Info(ctx, "Received message", map[string]interface{}{
		"type": base.MessageType,
		"size": len(message),
	})

	switch base.MessageType {
	case models.TypeRegistration:
		s.handleRegistration(ctx, connID, message)
	case models.TypeActiveClients:
		s.handleActiveClients(ctx, connID)
	case models.TypeConversationRequest:
		s.handleConversationRequest(ctx, connID, message)
	case models.TypePong:
		s.logger.Debug(ctx, "Received pong", nil)
	default:
		s.handleChatMessage(ctx, connID, message)
	}
}

func (s *LocalServer) handleRegistration(ctx context.Context, connID string, message []byte) {
	var reg models.RegistrationMessage
	if err := json.Unmarshal(message, &reg); err != nil {
		s.sendError(connID, "registration_error", "Invalid registration format")
		return
	}

	// C# client sends ProgramId as the user identifier
	// Use ProgramId as UserID if UserID is not provided
	userID := reg.UserID
	if userID == 0 {
		userID = reg.ProgramID
	}

	ctx = observability.WithUserID(ctx, userID)

	conn := &models.Connection{
		ConnectionID: connID,
		UserID:       userID,
		SystemID:     reg.SystemID,
		ProgramID:    reg.ProgramID,
		Username:     reg.Username,
		ConnectedAt:  time.Now().UTC(),
		LastPingAt:   time.Now().UTC(),
	}

	s.store.mu.Lock()
	s.store.connections[connID] = conn
	s.store.mu.Unlock()

	// Send response in format expected by C# client
	s.sendToConnection(connID, map[string]interface{}{
		"MessageType": "registration_response",
		"Success":     true,
		"Message":     "Registration successful",
		"UserId":      userID,
	})

	s.logger.Info(ctx, "Client registered", map[string]interface{}{
		"user_id":    userID,
		"program_id": reg.ProgramID,
		"system_id":  reg.SystemID,
		"username":   reg.Username,
	})
}

func (s *LocalServer) handleActiveClients(ctx context.Context, connID string) {
	s.store.mu.RLock()
	// C# client expects ActiveProgramIds (list of ints), not UserStatus objects
	programIds := make([]int, 0, len(s.store.connections))
	for _, conn := range s.store.connections {
		programIds = append(programIds, conn.ProgramID)
	}
	s.store.mu.RUnlock()

	s.sendToConnection(connID, map[string]interface{}{
		"MessageType":      "active_clients_response",
		"ActiveProgramIds": programIds,
		"TotalCount":       len(programIds),
		"Success":          true,
		"ErrorMessage":     "",
	})
}

// ConversationRequest message structure
type ConversationRequest struct {
	MessageType   string `json:"MessageType"`
	WithProgramId int    `json:"WithProgramId"`
}

func (s *LocalServer) handleConversationRequest(ctx context.Context, connID string, message []byte) {
	var req ConversationRequest
	if err := json.Unmarshal(message, &req); err != nil {
		s.logger.Error(ctx, "Invalid conversation request", err, nil)
		return
	}

	// Get the requesting user's ID from the connection
	s.store.mu.RLock()
	conn := s.store.connections[connID]
	s.store.mu.RUnlock()

	var requestingUserID int
	if conn != nil {
		requestingUserID = conn.ProgramID
	}

	s.logger.Info(ctx, "Conversation request", map[string]interface{}{
		"from_user":       requestingUserID,
		"with_program_id": req.WithProgramId,
	})

	var messages []models.BroadcastMessage

	// Fetch from DynamoDB if enabled
	if s.useDynamo && s.dynamo != nil && requestingUserID > 0 {
		var err error
		messages, _, err = s.dynamo.GetConversation(ctx, requestingUserID, req.WithProgramId, 50, 0)
		if err != nil {
			s.logger.Error(ctx, "Failed to fetch conversation from DynamoDB", err, nil)
			messages = []models.BroadcastMessage{}
		}
	} else {
		messages = []models.BroadcastMessage{}
	}

	// C# expects: MessageType, WithProgramId, Messages (array), Success, ErrorMessage, UnseenMessageCount
	s.sendToConnection(connID, map[string]interface{}{
		"MessageType":        "conversation_response",
		"WithProgramId":      req.WithProgramId,
		"Messages":           messages,
		"Success":            true,
		"ErrorMessage":       "",
		"UnseenMessageCount": 0,
	})
}

func (s *LocalServer) handleChatMessage(ctx context.Context, connID string, message []byte) {
	var msg models.IncomingMessage
	if err := json.Unmarshal(message, &msg); err != nil {
		s.sendError(connID, "message_error", "Invalid message format")
		return
	}

	// Generate message ID or save to DynamoDB
	var msgID int
	if s.useDynamo && s.dynamo != nil {
		var err error
		msgID, err = s.dynamo.SaveMessage(ctx, &msg)
		if err != nil {
			s.logger.Error(ctx, "Failed to save message to DynamoDB", err, nil)
			msgID = int(time.Now().UnixNano() % 1000000)
		}
	} else {
		msgID = int(time.Now().UnixNano() % 1000000)
	}

	s.logger.Info(ctx, "Chat message", map[string]interface{}{
		"from":       msg.MesajGonderenKullaniciID,
		"to":         msg.MesajAliciKullaniciID,
		"content":    truncate(msg.MesajIcerik, 50),
		"message_id": msgID,
		"persisted":  s.useDynamo,
	})

	// Send confirmation
	s.sendToConnection(connID, map[string]interface{}{
		"MessageType": "message_sent",
		"MessageId":   msgID,
	})

	// Route to recipient
	broadcast := models.BroadcastMessage{
		MesajID:                  msgID,
		MesajGonderenKullaniciID: msg.MesajGonderenKullaniciID,
		MesajAliciKullaniciID:    msg.MesajAliciKullaniciID,
		MesajIcerik:              msg.MesajIcerik,
		MesajGonderilenTarih:     msg.MesajGonderilenTarih,
		IsSeenByRecipient:        false,
	}

	// Find recipient connection and deliver message
	s.store.mu.RLock()
	delivered := false
	for cid, conn := range s.store.connections {
		if conn.ProgramID == msg.MesajAliciKullaniciID {
			if err := s.sendToConnection(cid, broadcast); err != nil {
				s.logger.Error(ctx, "Failed to deliver message", err, map[string]interface{}{
					"recipient_id": msg.MesajAliciKullaniciID,
				})
			} else {
				s.logger.Info(ctx, "Message delivered", map[string]interface{}{
					"from":         msg.MesajGonderenKullaniciID,
					"to":           msg.MesajAliciKullaniciID,
					"recipient_conn": cid,
				})
				delivered = true
			}
			break
		}
	}
	if !delivered && msg.MesajAliciKullaniciID != handlers.AIClientID {
		s.logger.Warn(ctx, "Recipient not connected", map[string]interface{}{
			"recipient_id": msg.MesajAliciKullaniciID,
		})
	}
	s.store.mu.RUnlock()

	// Check if message is to AI
	if msg.MesajAliciKullaniciID == handlers.AIClientID {
		s.handleAIMessage(ctx, connID, msg, msgID)
	}
}

func (s *LocalServer) handleAIMessage(ctx context.Context, connID string, msg models.IncomingMessage, msgID int) {
	s.logger.Info(ctx, "AI message received", map[string]interface{}{
		"query": truncate(msg.MesajIcerik, 100),
	})

	// Send placeholder
	s.sendToConnection(connID, map[string]interface{}{
		"MessageType":        "report_placeholder",
		"RequestId":          fmt.Sprintf("ai_%d", msgID),
		"StatusMessage":      "Processing with AI",
		"ProgressPercentage": 30,
		"CurrentStep":        "Analyzing query",
	})

	// Simulate AI response after delay
	go func() {
		time.Sleep(2 * time.Second)

		response := &models.ReportMessage{
			MessageType:              models.TypeReport,
			MesajGonderenKullaniciID: handlers.AIClientID,
			MesajAliciKullaniciID:    msg.MesajGonderenKullaniciID,
			MesajGonderilenTarih:     time.Now().Format("2006-01-02 15:04:05"),
			RequestID:                fmt.Sprintf("ai_%d", msgID),
			ReportQuery:              msg.MesajIcerik,
			WebApplication:           fmt.Sprintf("<html><body><h1>Mock AI Response</h1><p>Query: %s</p><p>This is a local development mock response.</p></body></html>", msg.MesajIcerik),
			IsSuccessful:             true,
			ProcessingTimeMs:         2000,
		}

		s.sendToConnection(connID, response)
		s.logger.Info(ctx, "AI response sent", nil)
	}()
}

func (s *LocalServer) sendToConnection(connID string, data interface{}) error {
	s.wsConnsMu.RLock()
	conn, ok := s.wsConns[connID]
	s.wsConnsMu.RUnlock()

	if !ok {
		return fmt.Errorf("connection not found: %s", connID)
	}

	return conn.WriteJSON(data)
}

func (s *LocalServer) sendError(connID string, errType string, errMsg string) {
	s.sendToConnection(connID, map[string]string{
		"MessageType":  "error",
		"ErrorType":    errType,
		"ErrorMessage": errMsg,
	})
}

func (s *LocalServer) startPingLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.wsConnsMu.RLock()
			for connID, conn := range s.wsConns {
				err := conn.WriteJSON(map[string]interface{}{
					"MessageType": "ping",
					"Timestamp":   time.Now().UnixMilli(),
					"PingId":      fmt.Sprintf("ping-%d", time.Now().UnixNano()),
				})
				if err != nil {
					s.logger.Warn(ctx, "Failed to send ping", map[string]interface{}{
						"connection_id": connID,
						"error":         err.Error(),
					})
				}
			}
			s.wsConnsMu.RUnlock()
		}
	}
}

func (s *LocalServer) Run(ctx context.Context) error {
	// Start health check server
	go func() {
		if err := s.health.Start(); err != nil && err != http.ErrServerClosed {
			s.logger.Error(ctx, "Health server error", err, nil)
		}
	}()

	// Start ping loop
	go s.startPingLoop(ctx)

	// WebSocket server
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`
<!DOCTYPE html>
<html>
<head><title>Messanger Local Dev</title></head>
<body>
<h1>Messanger Local Development Server</h1>
<p>WebSocket endpoint: <code>ws://localhost:` + wsPort + `/ws</code></p>
<p>Health check: <a href="http://localhost:` + healthPort + `/health">http://localhost:` + healthPort + `/health</a></p>
<h2>Test Console</h2>
<div id="console" style="background:#f0f0f0;padding:10px;height:300px;overflow:auto;font-family:monospace;"></div>
<input type="text" id="input" style="width:80%" placeholder="Enter JSON message...">
<button onclick="send()">Send</button>
<script>
var ws = new WebSocket('ws://localhost:` + wsPort + `/ws');
var console = document.getElementById('console');
function log(msg) { console.innerHTML += msg + '<br>'; console.scrollTop = console.scrollHeight; }
ws.onopen = function() { log('Connected'); };
ws.onmessage = function(e) { log('← ' + e.data); };
ws.onclose = function() { log('Disconnected'); };
ws.onerror = function(e) { log('Error: ' + e); };
function send() {
  var msg = document.getElementById('input').value;
  log('→ ' + msg);
  ws.send(msg);
  document.getElementById('input').value = '';
}
document.getElementById('input').addEventListener('keypress', function(e) {
  if (e.key === 'Enter') send();
});
// Auto-register
setTimeout(function() {
  var reg = JSON.stringify({MessageType:'registration',SystemId:'local',ProgramId:1,UserId:1,Username:'LocalUser'});
  log('→ ' + reg);
  ws.send(reg);
}, 500);
</script>
</body>
</html>`))
	})

	wsServer := &http.Server{
		Addr:    ":" + wsPort,
		Handler: mux,
	}

	s.logger.Info(ctx, "Local development server started", map[string]interface{}{
		"websocket_port": wsPort,
		"health_port":    healthPort,
	})
	fmt.Printf("\n")
	fmt.Printf("🚀 Local Development Server Running\n")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("  Web UI:     http://localhost:%s/\n", wsPort)
	fmt.Printf("  WebSocket:  ws://localhost:%s/ws\n", wsPort)
	fmt.Printf("  Health:     http://localhost:%s/health\n", healthPort)
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	go func() {
		<-ctx.Done()
		s.logger.Info(ctx, "Shutting down servers...", nil)
		wsServer.Shutdown(context.Background())
		s.health.Stop(context.Background())
	}()

	return wsServer.ListenAndServe()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func main() {
	server := NewLocalServer()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Println("\nShutting down...")
		cancel()
	}()

	if err := server.Run(ctx); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}
