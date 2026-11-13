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
        xvfb xauth \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 1000 odb && useradd -u 1000 -g 1000 -M -s /usr/sbin/nologin odb \
    && mkdir -p /data && chown 1000:1000 /data

# ---- Perch 注册机 worker ----
COPY tools/perch-register/requirements.txt /tmp/perch-requirements.txt
RUN python3 -m venv /opt/perch-register \
    && /opt/perch-register/bin/pip install --no-cache-dir -r /tmp/perch-requirements.txt
COPY tools/perch-register/ /usr/local/share/perch-register/
RUN chmod -R a+rX /usr/local/share/perch-register

# ---- 真 Google Chrome（不是 Playwright 自带 Chromium）----
#
# Playwright Chromium 的 navigator.plugins 是空数组、window.chrome 是 undefined，
# 任何指纹库都直接读到。这里装 Google 官方 Chrome stable。
# 钉版本避免镜像重建时版本漂移；构建参数可覆盖。
# Chrome 会从 apt 仓库下架旧版本：142.0.7444.59 已 404，构建直接 fail。
# 157.0.8103.78 与注册机实测指纹基线一致（见 docs/deploy-signup-recon.md）。
ARG CHROME_DEB_VERSION="157.0.8103.78-1"
RUN wget -q -O /tmp/chrome.deb \
        "https://dl.google.com/linux/chrome/deb/pool/main/g/google-chrome-stable/google-chrome-stable_${CHROME_DEB_VERSION}_amd64.deb" \
    && apt-get update \
    && apt-get install -y --no-install-recommends /tmp/chrome.deb \
    && rm -f /tmp/chrome.deb \
    && rm -rf /var/lib/apt/lists/* \
    && google-chrome --version

# ---- Deploy 注册机（curl_cffi + Playwright）----
# curl_cffi 提供 TLS/JA3 指纹伪装；playwright 用于 Stripe.js 生成绑卡 token。
RUN python3 -m venv /opt/deploy-signup \
    && /opt/deploy-signup/bin/pip install --no-cache-dir curl_cffi playwright
COPY tools/deploy_registrar.py tools/deploy_payment.py tools/fp_layer.py \
     tools/proxy_bridge.py tools/gen_ua_pool.py /usr/local/share/deploy-signup/
COPY tools/data/ua_pool.json /usr/local/share/deploy-signup/data/ua_pool.json
RUN chmod -R a+rX /usr/local/share/deploy-signup

# ---- Mistral 注册机 worker（含 Playwright）----
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

# 浏览器走 xvfb 里的 headed 真 Chrome：新 headless 的 WebGL 是 SwiftShader，
# 与画像声称的真实 GPU 直接矛盾。
ENV MISTRAL_BROWSER_CHANNEL=chrome \
    DEPLOY_REGISTRAR=/usr/local/share/deploy-signup/deploy_registrar.py \
    DEPLOY_PYTHON=/opt/deploy-signup/bin/python \
    PERCH_REGISTER_PYTHON=/opt/perch-register/bin/python \
    MISTRAL_WORKER=/usr/local/share/mistral-signup/worker.py \
    MISTRAL_WORKER_PYTHON=/opt/mistral-signup/bin/python \
    MISTRAL_WORKER_ROOT=/usr/local/share/mistral-signup/signup \
    ODB_ADDR=:8790 ODB_DATA_DIR=/data
EXPOSE 8790
VOLUME ["/data"]
ENTRYPOINT ["odb"]
