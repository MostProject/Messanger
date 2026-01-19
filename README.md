# Messanger

A Go serverless messaging server with Claude Code worker for MostJewelery.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        SERVERLESS (Go)                         │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐     │
│  │ API Gateway  │───▶│   Lambda     │───▶│  DynamoDB    │     │
│  │  WebSocket   │    │  Handlers    │    │  (messages)  │     │
│  └──────────────┘    └──────┬───────┘    └──────────────┘     │
│                             │                                  │
│                             ▼                                  │
│                      ┌──────────────┐                         │
│                      │  SQS Queue   │                         │
│                      │ (report jobs)│                         │
│                      └──────┬───────┘                         │
└─────────────────────────────┼───────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    CONTAINER (ECS Fargate)                     │
│  ┌──────────────────────────────────────────────────────────┐ │
│  │                  Claude Code Worker                       │ │
│  │  - Polls SQS for report requests                         │ │
│  │  - Spawns Claude Code CLI instances                      │ │
│  │  - Returns results to DynamoDB/S3                        │ │
│  └──────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

## Features

- **WebSocket API** - Real-time bidirectional communication
- **Message Routing** - P2P and broadcast messaging
- **AI Integration** - Claude Code for report generation
- **Image Support** - S3-backed image uploads with presigned URLs
- **Announcements** - System and news announcements
- **Health Monitoring** - CloudWatch metrics and logging

## Project Structure

```
.
├── cmd/
│   ├── lambda/           # Lambda function handlers
│   │   ├── connect/      # WebSocket $connect handler
│   │   ├── disconnect/   # WebSocket $disconnect handler
│   │   └── message/      # WebSocket $default handler
│   └── worker/           # Claude Code worker service
├── internal/
│   ├── handlers/         # Message processing logic
│   ├── models/           # Data models
│   ├── queue/            # SQS integration
│   ├── services/         # Business logic services
│   └── storage/          # DynamoDB and S3 storage
├── infrastructure/       # AWS SAM templates
├── Makefile
└── README.md
```

## Prerequisites

- Go 1.22+
- AWS CLI configured
- AWS SAM CLI
- Docker (for worker)

## Quick Start

### Build

```bash
# Download dependencies
make deps

# Build Lambda functions
make build-lambda

# Build worker Docker image
make build-worker
```

### Deploy

```bash
# Set environment variables
export ANTHROPIC_API_KEY=your_key
export GEMINI_API_KEY=your_key
export NEWS_API_KEY=your_key
export AWS_ACCOUNT_ID=123456789012

# Deploy to AWS
make deploy ENVIRONMENT=dev

# Push worker image to ECR
make push-worker
```

### Local Development

```bash
# Start local API
make dev
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | API key for Claude Code |
| `GEMINI_API_KEY` | API key for Google Gemini |
| `NEWS_API_KEY` | API key for NewsAPI.org |
| `ENVIRONMENT` | Deployment environment (dev/staging/prod) |

## Message Types

| Type | Description |
|------|-------------|
| `registration` | Client registration |
| `conversation_request` | Fetch conversation history |
| `active_clients_request` | List online users |
| `pong` | Ping response |
| `image_upload` | Upload image |
| `image_download_request` | Download image |
| `report_request` | Generate report |
| `report_modification` | Modify report |
| `schema_response` | Database schema info |
| `error_log` | Client error report |

## Cost Estimate (1000 concurrent clients)

| Service | Monthly Cost |
|---------|--------------|
| API Gateway WebSocket | $12 |
| Lambda | $15 |
| DynamoDB | $40 |
| S3 | $5 |
| ECS Fargate (worker) | $35 |
| SQS | $1 |
| Data Transfer | $45 |
| Secrets Manager | $1 |
| **Total** | **~$154/mo** |

## License

Proprietary - MostJewelery
