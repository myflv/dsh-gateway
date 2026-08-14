# 多阶段构建：Node+dsh 安装（带编译工具链） → Go 编译 → 精简运行时（纯 Debian，可 apt 装软件）

# 阶段 0：Node 基座（纯 Debian + nvm 管理的 Node，系统与终端用户共用同一套）
FROM debian:bookworm-slim AS nodebase

ARG NVM_VERSION=0.40.3
ARG NVM_NODE_VERSION=26.7.0

# libatomic1 是 Node 官方二进制的依赖
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        libatomic1 \
        xz-utils \
    && rm -rf /var/lib/apt/lists/*

# nvm 装 /opt/nvm（/root 是工作区卷挂载点，放 /root/.nvm 会被卷遮蔽丢 node）
ENV NVM_DIR=/opt/nvm

# nvm 是 shell 函数，需 source 后使用；终端用户的登录 shell 由 /etc/profile.d 注入
RUN mkdir -p /opt/nvm && NVM_DIR=/opt/nvm curl -fsSL "https://raw.githubusercontent.com/nvm-sh/nvm/v${NVM_VERSION}/install.sh" | bash

RUN bash -lc 'source "$NVM_DIR/nvm.sh" && nvm install "$NVM_NODE_VERSION" && nvm alias default "$NVM_NODE_VERSION"'

# 守护进程不能依赖交互 shell，路径固化到 ENV（终端用户则用 nvm 自选版本）
ENV PATH=/opt/nvm/versions/node/v${NVM_NODE_VERSION}/bin:${PATH}

# 终端用户的登录 shell（HOME=/root 在卷里，镜像 /root/.bashrc 被遮蔽，nvm 由 profile.d 注入）
RUN printf 'export NVM_DIR=/opt/nvm\n[ -s "$NVM_DIR/nvm.sh" ] && \\. "$NVM_DIR/nvm.sh"\n' > /etc/profile.d/nvm.sh

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

# nvm 下 npm 全局 prefix 带版本号，固定 --prefix 让运行时 COPY 路径稳定
RUN npm install -g --prefix /usr/local --no-audit --no-fund --registry=${NPM_REGISTRY} @deepseek-ai/dsh@${DSH_VERSION}

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
COPY --from=gateway-build /out/dsh-gateway /opt/dsh-gateway

# dsh-gateway 直接作 PID 1：内建 dsh 子进程守护与信号转发，无需 shell 脚本和 init
ENTRYPOINT ["/opt/dsh-gateway"]
# 容器内接线（与 compose 的 ports 配套）：只列与默认值不同的参数；0.0.0.0 绑定是 bridge 端口发布的前提
# -work-dir /root：/root 是工作区卷挂载点（cwd + HOME，配置派生 /root/.dsh），选择器默认目录就是工作区
CMD ["-listen", "0.0.0.0:8080", "-tls-listen", "0.0.0.0:8443", "-dsh-bin", "/usr/local/lib/node_modules/@deepseek-ai/dsh/lib/bin.js", "-work-dir", "/root"]

EXPOSE 8080 8443
