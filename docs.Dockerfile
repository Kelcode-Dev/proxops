# syntax=docker/dockerfile:1

FROM python:3.13-alpine AS builder

WORKDIR /src

COPY mkdocs.yml .
COPY docs/ docs/

# Pinned to the version the CI docs job + local quality gate run, so the
# container build and the GitHub Pages build produce the same site. (The
# site uses the readthedocs theme; mkdocs-material is installed for
# compatibility with downstream image builds and is not referenced by
# mkdocs.yml.)
RUN pip install --no-cache-dir "mkdocs==1.4.2" mkdocs-material \
    && mkdocs build --strict

FROM docker.io/library/nginx:1.31-alpine AS production

# Set labels for image metadata
LABEL org.opencontainers.image.title="proxops-docs" \
  org.opencontainers.image.description="ProxOps Documentation Site" \
  org.opencontainers.image.source="https://github.com/GizzmoShifu/proxmox-operator" \
  org.opencontainers.image.vendor="GizzmoShifu" \
  org.opencontainers.image.author="GizzmoShifu"

# Create non-root user and group simultaneously
RUN addgroup -g 1001 -S nginx-user && \
  adduser -u 1001 -S nginx-user -G nginx-user

# Copy static assets cleanly to the root directory defined in your config
COPY --from=builder /src/site /usr/share/nginx/html

# Copy your optimized server configuration block
COPY config/nginx.conf /etc/nginx/conf.d/default.conf

# Pre-create runtime paths and assign explicit non-root ownership to bypass default permission locks
RUN mkdir -p /var/cache/nginx /var/lib/nginx/tmp /var/log/nginx && \
  touch /var/run/nginx.pid && \
  chown -R nginx-user:nginx-user /usr/share/nginx/html /var/run/nginx.pid /var/cache/nginx /var/lib/nginx/tmp /var/log/nginx && \
  chmod -R 755 /usr/share/nginx/html

# Expose the non-privileged HTTP routing port defined in your listen directive
EXPOSE 8080

# Drop system privileges entirely
USER nginx-user

# Start the daemon in the foreground for clean container metrics collection
CMD ["nginx", "-g", "daemon off;"]
