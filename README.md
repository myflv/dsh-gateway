# dsh-gateway

dsh web + 认证网关的一体镜像，`docker compose up` 即用（登录入口 `/plugins/dsh-gateway-auth/login`，带 bcrypt 密码、会话和 CSRF 防护）。

## 快速开始

```bash
# 1. 改 docker-compose.yml 里的 AUTH_USER / AUTH_PASSWORD
docker compose up -d
```

打开 `http://宿主机IP:8080/plugins/dsh-gateway-auth/login` 登录。工作目录就是 `/root`（挂载的 `./workspace`），Web UI 直接 Continue 即可，无需手动选工作区。

dsh 0.1.2 起 Web 自带启动令牌认证（未带 `?token=` 的首页会 401）。网关从 `dsh web` 的 stdout 捕获该令牌，登录成功后自动带上，无需打开 dsh 打印的 URL。

## 配置

| 项 | 说明 |
|---|---|
| `AUTH_USER` / `AUTH_PASSWORD` | 登录用户名/密码，不填启动报错 |
| `./workspace` → `/root` | 工作区 + dsh 配置（`/root/.dsh`）+ ssh 密钥（`/root/.ssh`，权限 600），删掉重来 |
| `8443` 端口 | 自签 HTTPS（证书只含 localhost/127.0.0.1，供本机/调试） |

终端用户可用 nvm 切换 node 版本（内置在 `/opt/nvm`，登录 shell 自动注入；`nvm install 22 && nvm use 22`），装的版本重建容器后需重装。

公网访问请用 frp + nginx 反代 8080（正式证书，cookie 的 Secure 标志自动适配），不要直接开放端口。

## 更新

镜像由 GitHub Actions 构建推送 GHCR，dsh 发新版自动重建（amd64 + arm64）；镜像不会自动拉取：

```bash
docker compose pull && docker compose up -d
```
