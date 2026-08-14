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
| `./workspace` → `/root` | dsh 工作区（cwd + HOME，新建会话默认落在这里）+ 配置（`/root/.dsh`：settings、凭据、profiles），删掉重来 |
| `./ssh` → `/root/.ssh` | ssh 密钥（放你的 id_ed25519 等，权限 600） |

工作目录就是 `/root`（挂载的 `./workspace`），Web UI 里不用点 "Choose workspace"，直接 Continue 即可；目录选择器默认也从 `/root` 开始，选工作区就选 `/root` 下的项目目录。

终端用户可用 nvm 切换 node 版本（nvm 在 `/opt/nvm`，登录 shell 自动注入；`nvm install 22 && nvm use 22`）；装的版本在容器重建后丢失，需重装。

公网访问请用 frp + nginx 反代 8080（正式证书，cookie 的 Secure 标志自动适配），不要直接开放端口。

## 更新镜像

镜像由 GitHub Actions 构建推送到 GHCR，dsh 发新版自动重建（amd64 + arm64）。镜像不会自动拉取：

```bash
docker compose pull && docker compose up -d
```
