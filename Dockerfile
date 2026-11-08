FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /odb ./cmd/odb

# Debian slim：Playwright 官方 Chromium 是 glibc 编译，Alpine(musl) 跑不起来
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    PYTHONUNBUFFERED=1 \
    PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates tzdata wget curl python3 python3-venv python3-pip \
        libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libcups2 \
        libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 \
        libxrandr2 libgbm1 libpango-1.0-0 libcairo2 libasound2 \
        libatspi2.0-0 fonts-liberation \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 1000 odb && useradd -u 1000 -g 1000 -M -s /usr/sbin/nologin odb \
    && mkdir -p /data && chown 1000:1000 /data

# ---- Perch 注册机 worker ----
COPY tools/perch-register/requirements.txt /tmp/perch-requirements.txt
RUN python3 -m venv /opt/perch-register \
    && /opt/perch-register/bin/pip install --no-cache-dir -r /tmp/perch-requirements.txt
COPY tools/perch-register/ /usr/local/share/perch-register/
RUN chmod -R a+rX /usr/local/share/perch-register

# ---- Mistral 注册机 worker（含 Playwright + Chromium）----
RUN python3 -m venv /opt/mistral-signup \
    && /opt/mistral-signup/bin/pip install --no-cache-dir requests playwright
COPY tools/mistral/ /usr/local/share/mistral-signup/
RUN chmod -R a+rX /usr/local/share/mistral-signup

# Chromium 以 root 装到公共路径，运行时由 rootless 用户只读访问
RUN /opt/mistral-signup/bin/playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/* \
    && chmod -R a+rX /opt/ms-playwright

USER 1000:1000
COPY --from=build /odb /usr/local/bin/odb

ENV PERCH_REGISTER_PYTHON=/opt/perch-register/bin/python \
    MISTRAL_WORKER=/usr/local/share/mistral-signup/worker.py \
    MISTRAL_WORKER_PYTHON=/opt/mistral-signup/bin/python \
    MISTRAL_WORKER_ROOT=/usr/local/share/mistral-signup/signup \
    ODB_ADDR=:8790 ODB_DATA_DIR=/data
EXPOSE 8790
VOLUME ["/data"]
ENTRYPOINT ["odb"]
