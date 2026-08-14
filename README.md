# dsh-gateway

dsh web + 认证网关的一体镜像，`docker compose up` 即用。登录入口固定路径 `/login`，带 bcrypt 密码、会话和 CSRF 防护。

## 快速开始

```bash
# 1. 改 docker-compose.yml 里的 AUTH_USER / AUTH_PASSWORD
# 2. 启动（自动从 GHCR 拉取镜像）
docker compose up -d
```

浏览器打开 `http://宿主机IP:8080/login` 即可登录。

## 配置项

| 配置 | 说明 |
|---|---|
| `AUTH_USER` / `AUTH_PASSWORD` | 登录用户名/密码，不填启动报错 |
| `ports` | `8080` 认证入口（http），`8443` 自签 HTTPS（证书只含 localhost/127.0.0.1，供本机/调试） |
| `./data` → `/data` | 唯一的持久化点：dsh 的配置、会话、日志全在里面，删掉重来 |

公网访问请用 frp + nginx 反代 8080（正式证书，cookie 的 Secure 标志自动适配），不要直接开放端口。

## 更新镜像

镜像由 GitHub Actions 构建推送到 GHCR，dsh 发新版自动重建（amd64 + arm64）。镜像不会自动拉取：

```bash
docker compose pull && docker compose up -d
```
