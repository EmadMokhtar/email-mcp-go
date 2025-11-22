# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make

# Set working directory
WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w" \
    -o email-mcp \
    ./cmd/email-mcp

# Final stage
FROM alpine:latest

# Install ca-certificates for HTTPS connections
RUN apk --no-cache add ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 emailmcp && \
    adduser -D -u 1000 -G emailmcp emailmcp

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/email-mcp .

# Copy .env.example as template (users can override with volume mount)
COPY .env.example .

# Change ownership
RUN chown -R emailmcp:emailmcp /app

# Switch to non-root user
USER emailmcp

# Expose port for HTTP mode (optional)
EXPOSE 8080

# Set default command (stdio mode)
# Users can override with docker run command for HTTP mode
CMD ["/app/email-mcp"]

