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

# nvm 是 shell 函数，需 source 后使用（install.sh 需要目标目录已存在）
RUN mkdir -p /opt/nvm && curl -fsSL "https://raw.githubusercontent.com/nvm-sh/nvm/v${NVM_VERSION}/install.sh" | bash

RUN bash -lc 'source "$NVM_DIR/nvm.sh" && nvm install "$NVM_NODE_VERSION" && nvm alias default "$NVM_NODE_VERSION"'

# 守护进程不能依赖交互 shell，路径固化到 ENV（终端用户则用 nvm 自选版本）
ENV PATH=$NVM_DIR/versions/node/v${NVM_NODE_VERSION}/bin:${PATH}

# 终端用户 shell 注入 nvm（/root 是卷挂载点，镜像 /root/.bashrc 被遮蔽）：profile.d 覆盖登录 shell，bash.bashrc 覆盖交互 shell，两者互补
RUN printf '[ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh"\n' > /etc/profile.d/nvm.sh
RUN cat >> /etc/bash.bashrc <<'EOF'
[ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh"
EOF

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

# 自装软件统一 /opt（与 nvm、dsh-gateway 并列），npm 全局 prefix 固定为 /opt，
# 运行时 COPY 路径稳定；pnpm 是 dsh plugin（profile 插件管理，转发 pnpm）的前置
RUN npm install -g --prefix /opt --no-audit --no-fund --registry=${NPM_REGISTRY} @deepseek-ai/dsh@${DSH_VERSION} \
    && npm install -g --prefix /opt --no-audit --no-fund pnpm

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

# /opt/bin 进 PATH：docker exec 与网关 spawn 的子进程（继承容器 ENV）
ENV PATH=/opt/bin:${PATH}

# ssh 登录 shell 不继承镜像 ENV，profile.d 注入（与 nvm.sh 并列）
RUN printf 'export PATH=/opt/bin:$PATH\n' > /etc/profile.d/dsh-path.sh

# 要额外装的软件加在这一行
RUN apt-get update \
    && apt-get install -y --no-install-recommends git openssh-client \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /opt/lib/node_modules /opt/lib/node_modules
# npm 全局包的 bin 链接（dsh、pnpm）在 /opt/bin，漏拷则容器内 dsh 命令缺失
COPY --from=build /opt/bin /opt/bin
COPY --from=gateway-build /out/dsh-gateway /opt/dsh-gateway

# dsh-gateway 直接作 PID 1：内建 dsh 子进程守护与信号转发，无需 shell 脚本和 init
ENTRYPOINT ["/opt/dsh-gateway"]
# 容器内接线（与 compose 的 ports 配套）：只列与默认值不同的参数；0.0.0.0 绑定是 bridge 端口发布的前提
CMD ["-listen", "0.0.0.0:8080", "-tls-listen", "0.0.0.0:8443", "-dsh-bin", "/opt/lib/node_modules/@deepseek-ai/dsh/lib/bin.js"]

EXPOSE 8080 8443
