.PHONY: build build-lambda build-worker test deploy clean

# Variables
AWS_REGION ?= us-east-1
ENVIRONMENT ?= dev
STACK_NAME = messanger-$(ENVIRONMENT)

# Build all
build: build-lambda build-worker

# Build Lambda functions
build-lambda:
	@echo "Building Lambda functions..."
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-w -s" -o ./bin/connect/bootstrap ./cmd/lambda/connect
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-w -s" -o ./bin/disconnect/bootstrap ./cmd/lambda/disconnect
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-w -s" -o ./bin/message/bootstrap ./cmd/lambda/message
	@echo "Lambda functions built successfully"

# Build worker Docker image
build-worker:
	@echo "Building worker Docker image..."
	docker build -t messanger-worker:latest -f cmd/worker/Dockerfile .
	@echo "Worker image built successfully"

# Run tests
test:
	go test -v ./...

# Deploy to AWS
deploy: build
	@echo "Deploying to AWS..."
	sam deploy \
		--template-file infrastructure/template.yaml \
		--stack-name $(STACK_NAME) \
		--region $(AWS_REGION) \
		--capabilities CAPABILITY_IAM \
		--parameter-overrides \
			Environment=$(ENVIRONMENT) \
			AnthropicApiKey=$(ANTHROPIC_API_KEY) \
			GeminiApiKey=$(GEMINI_API_KEY) \
			NewsApiKey=$(NEWS_API_KEY) \
		--no-confirm-changeset
	@echo "Deployment complete"

# Push worker image to ECR
push-worker:
	@echo "Pushing worker image to ECR..."
	aws ecr get-login-password --region $(AWS_REGION) | docker login --username AWS --password-stdin $(AWS_ACCOUNT_ID).dkr.ecr.$(AWS_REGION).amazonaws.com
	docker tag messanger-worker:latest $(AWS_ACCOUNT_ID).dkr.ecr.$(AWS_REGION).amazonaws.com/messanger-worker:latest
	docker push $(AWS_ACCOUNT_ID).dkr.ecr.$(AWS_REGION).amazonaws.com/messanger-worker:latest
	@echo "Worker image pushed successfully"

# Full deployment (Lambda + Worker)
deploy-all: deploy push-worker
	@echo "Full deployment complete"

# Clean build artifacts
clean:
	rm -rf ./bin
	rm -rf .aws-sam
	go clean

# Local development
dev:
	@echo "Starting local development environment..."
	sam local start-api --template-file infrastructure/template.yaml

# Get outputs from deployed stack
outputs:
	@aws cloudformation describe-stacks --stack-name $(STACK_NAME) --region $(AWS_REGION) --query 'Stacks[0].Outputs' --output table

# Download dependencies
deps:
	go mod download
	go mod tidy
