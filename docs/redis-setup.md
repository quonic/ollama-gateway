# Redis setup for shared rate limiting

This document covers installing Redis for the gateway's optional Redis-backed rate limiter, with both Docker and non-Docker options.

## Option 1: Run Redis with Docker

If you prefer containers, you can start Redis with Docker:

```bash
docker run --name ollama-gateway-redis -p 6379:6379 -d redis:7-alpine
```

## Option 2: Install Redis from apt (Ubuntu/Debian)

1. Update the package index:

   ```bash
   sudo apt update
   ```

2. Install Redis:

   ```bash
   sudo apt install redis-server
   ```

3. Start and enable the service:

   ```bash
   sudo systemctl enable redis-server
   sudo systemctl start redis-server
   ```

4. Verify the service is listening:

   ```bash
   redis-cli ping
   ```

   A healthy Redis instance returns:

   ```text
   PONG
   ```

## Option 3: Install Redis from source (manual build)

If you prefer not to use a package manager, you can build Redis from source:

```bash
wget http://download.redis.io/redis-stable.tar.gz
tar xzf redis-stable.tar.gz
cd redis-stable
make
sudo make install
```

Then start Redis with:

```bash
redis-server --daemonize yes
```

## Configure the gateway to use Redis

Add the following to your gateway config under `rate_limiting`:

```yaml
rate_limiting:
  default_rate: 10.0
  default_burst: 50
  ttl: 1h
  backend: redis
  redis_addr: "127.0.0.1:6379"
  redis_timeout_sec: 2
  redis_fallback_to_local: true
```

Then restart the gateway.

## Notes

- If Redis is not reachable, the gateway logs a warning and continues in local-only mode for that process.
- The default `local` backend is still the simplest choice for a single gateway instance.
- Redis is intended for multi-instance deployments that need shared rate-limit state.
