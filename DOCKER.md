# Docker Quick Reference - Email MCP Server

## 🚀 Quick Start

### Build and Run (Stdio Mode)
```bash
make docker/build
make docker/run
```

### Build and Run (HTTP Mode)
```bash
make docker/build
make docker/run-http
# Access at http://localhost:8080
```

### Using Docker Compose
```bash
docker compose up
```

## 📋 Common Commands

### Building
| Command                                 | Description            |
|-----------------------------------------|------------------------|
| `make docker/build`                     | Build the Docker image |
| `docker build -t email-mcp-go:latest .` | Build manually         |

### Running
| Command                                                                                                  | Description                     |
|----------------------------------------------------------------------------------------------------------|---------------------------------|
| `make docker/run`                                                                                        | Run in stdio mode (interactive) |
| `make docker/run-http`                                                                                   | Run in HTTP mode on port 8080   |
| `docker run -it --rm --env-file .env email-mcp-go:latest`                                                | Run stdio mode manually         |
| `docker run -d -p 8080:8080 --env-file .env email-mcp-go:latest /app/email-mcp -http -addr 0.0.0.0:8080` | Run HTTP mode manually          |


### Management
| Command                             | Description                  |
|-------------------------------------|------------------------------|
| `make docker/clean`                 | Remove images and containers |
| `make docker/test`                  | Run Docker tests             |
| `docker images email-mcp-go`        | List built images            |
| `docker ps`                         | List running containers      |
| `docker logs <container-id>`        | View container logs          |
| `docker exec -it <container-id> sh` | Shell into container         |

## 🔧 Configuration

### Environment File (.env)
```env
IMAP_HOST=imap.gmail.com
IMAP_PORT=993
IMAP_USERNAME=your-email@gmail.com
IMAP_PASSWORD=your-app-password
IMAP_TLS=true

SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_USERNAME=your-email@gmail.com
SMTP_PASSWORD=your-app-password
SMTP_TLS=true
```

### Modes

#### Stdio Mode (Default)
```bash
# Docker
docker run -it --rm --env-file .env email-mcp-go:latest
```

#### HTTP Mode
```bash
# Docker
docker run -d -p 8080:8080 --env-file .env \
  email-mcp-go:latest /app/email-mcp -http -addr 0.0.0.0:8080
```

## 🐛 Troubleshooting

### View Logs
```bash
# Docker
docker logs <container-name>
docker logs -f <container-name>  # Follow logs
```

### Debug in Container
```bash
# Docker
docker exec -it <container-name> sh

# Docker Compose
docker compose exec email-mcp sh
```

### Common Issues

**Permission denied (logs)**
```bash
# Check .env file permissions
chmod 600 .env
```

**Port already in use**
```bash
# Change port mapping
docker run -p 8081:8080 ...

# Or in docker/compose.yml
ports:
  - "8081:8080"
```

**Container exits immediately**
```bash
# Check logs for errors
docker logs <container-name>

# Verify .env file
docker run --rm -it --env-file .env email-mcp-go:latest sh -c 'env | grep IMAP'
```

## 📦 Registry Operations

### Tag Image
```bash
docker tag email-mcp-go:latest yourusername/email-mcp-go:latest
docker tag email-mcp-go:latest yourusername/email-mcp-go:v1.0.0
```

### Push to Registry
```bash
docker push yourusername/email-mcp-go:latest
docker push yourusername/email-mcp-go:v1.0.0
```

### Pull from Registry
```bash
docker pull yourusername/email-mcp-go:latest
docker run -it --rm --env-file .env yourusername/email-mcp-go:latest
```

## 🎯 Best Practices

### Security
1. **Never commit .env file**
   ```bash
   # It's already in .gitignore
   git status  # Verify .env is not tracked
   ```

2. **Use read-only filesystem** (optional)
   ```bash
   docker run --read-only --tmpfs /tmp --env-file .env email-mcp-go:latest
   ```

3. **Limit resources**
   ```bash
   docker run --memory="256m" --cpus="0.5" --env-file .env email-mcp-go:latest
   ```

### Production
1. **Use specific tags** (not `latest`)
   ```bash
   docker tag email-mcp-go:latest email-mcp-go:1.0.0
   ```

2. **Health checks** (HTTP mode)
   ```yaml
   healthcheck:
     test: ["CMD", "wget", "--spider", "http://localhost:8080/health"]
     interval: 30s
     timeout: 10s
     retries: 3
   ```

3. **Restart policy**
   ```bash
   docker run -d --restart=unless-stopped --env-file .env email-mcp-go:latest
   ```

## 🔄 Development Workflow

### Make Changes and Rebuild
```bash
# Edit code
vim cmd/email-mcp/main.go

# Rebuild and run
make docker/build docker/run
```

### Test Changes
```bash
make docker/test
```

### View Image Size
```bash
docker images email-mcp-go:latest
```

### Clean Up Development Artifacts
```bash
make docker/clean
docker system prune -a  # Clean all unused images
```

## 📚 Additional Resources

- **Main Documentation**: [README.md](README.md)

## 💡 Tips

### Speed Up Builds
```bash
# Use BuildKit
DOCKER_BUILDKIT=1 docker build -t email-mcp-go:latest .
```

### Multi-Architecture Builds
```bash
docker buildx create --use
docker buildx build --platform linux/amd64,linux/arm64 -t email-mcp-go:latest .
```

### Inspect Image
```bash
docker inspect email-mcp-go:latest
docker history email-mcp-go:latest
```

### Container Stats
```bash
docker stats <container-name>
```

---

For complete documentation, see [DOCKER.md](DOCKER.md)

