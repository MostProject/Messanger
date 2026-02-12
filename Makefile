.PHONY: build build-lambda build-worker test deploy clean

# Load .env file if it exists
ifneq (,$(wildcard ./.env))
    include .env
    export
endif

# Variables
AWS_REGION ?= us-east-1
ENVIRONMENT ?= dev
APP_NAME ?= messanger
STACK_NAME = $(APP_NAME)-$(ENVIRONMENT)
AWS_PROFILE ?= default

# Build all
build: build-lambda build-worker

# Build Lambda functions (use Git Bash or WSL on Windows)
build-lambda:
	@echo "Building Lambda functions..."
ifeq ($(OS),Windows_NT)
	@if not exist bin\connect mkdir bin\connect
	@if not exist bin\disconnect mkdir bin\disconnect
	@if not exist bin\message mkdir bin\message
	powershell -Command "$$env:GOOS='linux'; $$env:GOARCH='arm64'; $$env:CGO_ENABLED='0'; go build -ldflags='-w -s' -o ./bin/connect/bootstrap ./cmd/lambda/connect"
	powershell -Command "$$env:GOOS='linux'; $$env:GOARCH='arm64'; $$env:CGO_ENABLED='0'; go build -ldflags='-w -s' -o ./bin/disconnect/bootstrap ./cmd/lambda/disconnect"
	powershell -Command "$$env:GOOS='linux'; $$env:GOARCH='arm64'; $$env:CGO_ENABLED='0'; go build -ldflags='-w -s' -o ./bin/message/bootstrap ./cmd/lambda/message"
else
	@mkdir -p bin/connect bin/disconnect bin/message
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-w -s" -o ./bin/connect/bootstrap ./cmd/lambda/connect
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-w -s" -o ./bin/disconnect/bootstrap ./cmd/lambda/disconnect
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-w -s" -o ./bin/message/bootstrap ./cmd/lambda/message
endif
	@echo "Lambda functions built successfully"

# Build worker Docker image (x86_64 for compatibility with claude-code)
build-worker:
	@echo "Building worker Docker image..."
	docker buildx build --platform linux/amd64 -t messanger-worker:latest -f cmd/worker/Dockerfile . --load
	@echo "Worker image built successfully"

# Run tests
test:
	go test -v ./...

# Deploy to AWS
deploy: build check-keys
	@echo "Deploying to AWS..."
	sam deploy \
		--template-file infrastructure/template.yaml \
		--stack-name $(STACK_NAME) \
		--region $(AWS_REGION) \
		--capabilities CAPABILITY_IAM \
		--s3-bucket $(SAM_BUCKET) \
		--parameter-overrides \
			"Environment=$(ENVIRONMENT)" \
			"AnthropicApiKey=$(ANTHROPIC_API_KEY)" \
			"GeminiApiKey=$(GEMINI_API_KEY)" \
			"NewsApiKey=$(NEWS_API_KEY)" \
			"SubnetId=$(SUBNET_ID)" \
			"VpcId=$(VPC_ID)" \
			"AppName=$(APP_NAME)" \
			"HealthPort=$(HEALTH_PORT)" \
			"DomainName=$(DOMAIN_NAME)" \
			"HostedZoneId=$(HOSTED_ZONE_ID)" \
		--no-confirm-changeset \
		--disable-rollback
	@echo "Deployment complete"

# Check required environment variables
check-keys:
ifndef ANTHROPIC_API_KEY
	$(error ANTHROPIC_API_KEY is not set in .env)
endif
ifndef GEMINI_API_KEY
	$(error GEMINI_API_KEY is not set in .env)
endif
ifndef NEWS_API_KEY
	$(error NEWS_API_KEY is not set in .env)
endif

# Push worker image to ECR
push-worker:
	@echo "Pushing worker image to ECR..."
	aws ecr get-login-password --region $(AWS_REGION) --profile $(AWS_PROFILE) | docker login --username AWS --password-stdin $(AWS_ACCOUNT_ID).dkr.ecr.$(AWS_REGION).amazonaws.com
	docker tag messanger-worker:latest $(AWS_ACCOUNT_ID).dkr.ecr.$(AWS_REGION).amazonaws.com/$(APP_NAME)-worker:latest
	docker push $(AWS_ACCOUNT_ID).dkr.ecr.$(AWS_REGION).amazonaws.com/$(APP_NAME)-worker:latest
	@echo "Worker image pushed successfully"

# Force ECS to deploy new image
redeploy-worker:
	@echo "Forcing ECS service to deploy new image..."
	aws ecs update-service \
		--cluster $(APP_NAME)-worker-cluster-$(ENVIRONMENT) \
		--service $(APP_NAME)-worker-$(ENVIRONMENT) \
		--force-new-deployment \
		--region $(AWS_REGION)
	@echo "Deployment started. Use 'make worker-status' to monitor"

# Check worker service status
worker-status:
	@aws ecs describe-services \
		--cluster $(APP_NAME)-worker-cluster-$(ENVIRONMENT) \
		--services $(APP_NAME)-worker-$(ENVIRONMENT) \
		--region $(AWS_REGION) \
		--query 'services[0].{Status:status,Running:runningCount,Desired:desiredCount}' \
		--output table

# View worker logs
worker-logs:
	aws logs tail /ecs/$(APP_NAME)-worker-$(ENVIRONMENT) --region $(AWS_REGION) --follow

# Build, push, and redeploy worker
update-worker: build-worker push-worker redeploy-worker

# Full deployment (Lambda + Worker)
deploy-all: deploy push-worker
	@echo "Full deployment complete"

# Clean build artifacts
clean:
ifeq ($(OS),Windows_NT)
	if exist bin rmdir /s /q bin
	if exist .aws-sam rmdir /s /q .aws-sam
else
	rm -rf ./bin
	rm -rf .aws-sam
endif
	go clean

# Local development (no Docker required)
local:
	@echo "Starting local development server..."
	go run ./cmd/local

# Local development with SAM (requires Docker)
dev:
	@echo "Starting local development environment with SAM..."
	sam local start-api --template-file infrastructure/template.yaml

# Run local server with hot reload (requires air: go install github.com/cosmtrek/air@latest)
local-watch:
	@echo "Starting local development server with hot reload..."
	air -c .air.toml

# Run worker locally (requires AWS credentials or LocalStack)
run-worker:
	@echo "Starting worker locally..."
	WEBSOCKET_API_ENDPOINT=http://localhost:1738 \
	REPORT_QUEUE_URL=http://localhost:4566/000000000000/messanger-report-queue \
	ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY} \
	ENVIRONMENT=local \
	LOG_LEVEL=debug \
	go run ./cmd/worker

# Docker compose for full local stack
docker-up:
	docker-compose up -d
	@echo "LocalStack starting... waiting for health"
	@sleep 10
	@echo "Services ready!"

docker-down:
	docker-compose down

docker-logs:
	docker-compose logs -f

# Get outputs from deployed stack
outputs:
	@aws cloudformation describe-stacks --stack-name $(STACK_NAME) --region $(AWS_REGION) --query 'Stacks[0].Outputs' --output table

# Download dependencies
deps:
	go mod download
	go mod tidy

# Install development tools
dev-tools:
	go install github.com/cosmtrek/air@latest
	@echo "Air installed for hot reload (run: make local-watch)"

# Run all tests with coverage
test-coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Lint code
lint:
	golangci-lint run ./...

# Format code
fmt:
	go fmt ./...
	goimports -w .

delete-stack:
	@echo "Deleting CloudFormation stack $(STACK_NAME)..."
	aws cloudformation delete-stack --stack-name $(STACK_NAME) --region $(AWS_REGION) --profile $(AWS_PROFILE)
	@echo "Stack deletion initiated."

# Fix stuck/failed CloudFormation stack
fix-stack:
	@echo "Attempting to fix CloudFormation stack $(STACK_NAME)..."
	@aws cloudformation describe-stacks --stack-name $(STACK_NAME) --region $(AWS_REGION) --profile $(AWS_PROFILE) --query "Stacks[0].StackStatus" --output text
	aws cloudformation rollback-stack --stack-name $(STACK_NAME) --region $(AWS_REGION) --profile $(AWS_PROFILE) || true
	@echo "Waiting for rollback..."
	aws cloudformation wait stack-rollback-complete --stack-name $(STACK_NAME) --region $(AWS_REGION) --profile $(AWS_PROFILE) || true
	@echo "Stack status:"
	@aws cloudformation describe-stacks --stack-name $(STACK_NAME) --region $(AWS_REGION) --profile $(AWS_PROFILE) --query "Stacks[0].StackStatus" --output text

# Check stack status
stack-status:
	@aws cloudformation describe-stacks --stack-name $(STACK_NAME) --region $(AWS_REGION) --profile $(AWS_PROFILE) --query "Stacks[0].{Status:StackStatus,Reason:StackStatusReason}" --output table

# View stack events (errors)
stack-events:
	@aws cloudformation describe-stack-events --stack-name $(STACK_NAME) --region $(AWS_REGION) --profile $(AWS_PROFILE) --query "StackEvents[?ResourceStatus=='CREATE_FAILED' || ResourceStatus=='UPDATE_FAILED'].[LogicalResourceId,ResourceStatusReason]" --output table