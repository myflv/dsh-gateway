# 多阶段构建：Node+dsh 安装（带编译工具链） → Go 编译 → 精简运行时（纯 Debian，可 apt 装软件）

# 阶段 0：Node 基座（纯 Debian + Node 官方预编译包）
FROM debian:bookworm-slim AS nodebase

ARG NODE_VERSION=26.7.0

# libatomic1 是 Node 官方二进制的依赖
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        libatomic1 \
        xz-utils \
    && rm -rf /var/lib/apt/lists/*

# Node 解压到 /usr/local，bin 落在默认 PATH，无需任何 ENV 配置
# node 包名用 x64/arm64，buildx 的 TARGETARCH 是 amd64/arm64，需映射
ARG TARGETARCH
RUN NODE_ARCH=$( [ "${TARGETARCH:-amd64}" = "amd64" ] && echo x64 || echo "${TARGETARCH:-amd64}" ) \
    && curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${NODE_ARCH}.tar.xz" \
        | tar -xJ -C /usr/local --strip-components=1

RUN node --version && npm --version

# 阶段 1：安装 dsh（node-pty 需要编译工具链）
FROM nodebase AS build

ARG NPM_REGISTRY=https://registry.npmjs.org
# CI 以 --build-arg 覆盖
ARG DSH_VERSION=0.1.0-rc.6

# node-pty 是原生模块，安装时需要编译工具链
RUN apt-get update \
    && apt-get install -y --no-install-recommends python3 make g++ \
    && rm -rf /var/lib/apt/lists/*

# npm 全局安装到 /usr/local，运行时 COPY 路径稳定
RUN npm install -g --no-audit --no-fund --registry=${NPM_REGISTRY} @deepseek-ai/dsh@${DSH_VERSION}

# 阶段 2：编译 dsh-gateway（静态二进制）
FROM golang:1.26-alpine AS gateway-build

ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /src
# 模块下载单独成层，源码改动不重下依赖
COPY gateway/go.mod gateway/go.sum ./
RUN go mod download
COPY gateway/ ./
# 静态编译（CGO_ENABLED=0），运行时零 glibc 依赖
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/dsh-gateway .

# 阶段 3：运行时（纯 Debian，可继续 apt 装软件）
FROM nodebase AS runtime

# 要额外装的软件加在这一行
RUN apt-get update \
    && apt-get install -y --no-install-recommends git openssh-client \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=gateway-build /out/dsh-gateway /usr/local/bin/dsh-gateway

# dsh-gateway 直接作 PID 1：内建 dsh 子进程守护与信号转发，无需 shell 脚本和 init
ENTRYPOINT ["/usr/local/bin/dsh-gateway"]
# 容器内接线（与 compose 的 ports/volume 配套）：0.0.0.0 绑定是 bridge 端口发布的前提
CMD ["-listen", "0.0.0.0:8080",
     "-tls-listen", "0.0.0.0:8443",
     "-backend", "http://127.0.0.1:3080",
     "-dsh-bin", "/usr/local/lib/node_modules/@deepseek-ai/dsh/lib/bin.js",
     "-data-dir", "/root"]

VOLUME /root
EXPOSE 8080 8443
