package main

import (
	_ "embed" // go:embed 所需
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
)

//go:embed static/login.html
var loginTemplate string // 登录页模板，启动时由编译器嵌入

var (
	listen         = flag.String("listen", "127.0.0.1:8080", "监听地址（容器内接线见 Dockerfile CMD，本机运行默认回环）")
	backend        = flag.String("backend", "http://127.0.0.1:3080", "上游地址（dsh web 子进程也按此 host:port 启动）")
	insecureCookie = flag.Bool("insecure-cookie", false, "本地 http 调试时关闭 cookie 的 Secure 标志")
	tlsListen      = flag.String("tls-listen", "", "自签 HTTPS 监听地址（如 0.0.0.0:8443），空则关闭")

	// dsh web 子进程守护（容器内由 Dockerfile CMD 传入；本机单独用 dsh-gateway 时留空 = 纯代理）
	dshBin  = flag.String("dsh-bin", "", "dsh web 的 bin.js 路径，非空则作为子进程守护")
	workDir = flag.String("work-dir", "/root", "dsh 工作目录（cwd，工作区根目录；配置固定在其下 .dsh/），仅 -dsh-bin 非空时使用")
)

const restartDelay = 2 * time.Second // dsh web 崩溃后的重启间隔

// 认证入口固定 /login、/logout：dsh 只注册 /plugins、/api 前缀，SPA catch-all 不冲突
func main() {
	log.SetPrefix("[dsh-gateway] ")
	flag.Parse()

	user := os.Getenv("AUTH_USER")
	if user == "" {
		log.Fatal("需要环境变量 AUTH_USER（登录用户名）")
	}
	pass := os.Getenv("AUTH_PASSWORD")
	if pass == "" {
		log.Fatal("需要环境变量 AUTH_PASSWORD（登录密码）")
	}

	backendURL, err := url.Parse(*backend)
	if err != nil {
		log.Fatal(err)
	}

	// 容器内即时哈希，不落盘
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		log.Fatal(err)
	}
	authUser, authHash = user, string(hash)

	// 显式 mux：HTTP 与自签 HTTPS 共用同一套 handler
	mux := http.NewServeMux()
	mux.HandleFunc(loginPath, handleLogin)
	mux.HandleFunc(logoutPath, handleLogout)

	// 其余所有路径：数据面（/api、/plugins）会话有效才反代，页面壳与静态资源公开
	mux.Handle(homePath, requireAuth(proxyHandler(backendURL)))

	log.Printf("认证用户: %s", user)
	log.Printf("listening on %s -> %s", *listen, *backend)
	log.Printf("auth portal: http://%s%s", *listen, loginPath)

	// 本进程是容器 PID 1：docker stop 的 SIGTERM 只发给它，由 supervise 转发给 dsh web
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	// 自签 HTTPS（可选）：直连访问时的安全上下文，免 nginx
	if *tlsListen != "" {
		go func() {
			log.Fatal(startTLS(*tlsListen, mux))
		}()
	}
	go func() {
		log.Fatal(http.ListenAndServe(*listen, mux))
	}()

	supervise(sigCh, backendURL)
}

// supervise：以子进程启动 dsh web，崩溃自动重启，信号转发后退出（Go 运行时自动回收僵尸，无需 init）
func supervise(sigCh <-chan os.Signal, backendURL *url.URL) {
	// 纯代理模式（未传 -dsh-bin）：常驻等退出信号，Ctrl+C 可正常退出
	if *dshBin == "" {
		<-sigCh
		return
	}

	// 子进程接线与 -backend 同一来源：dsh web 必须监听在代理的上游地址上
	host := backendURL.Hostname()
	port := backendURL.Port()
	if port == "" {
		port = "80"
	}
	args := []string{*dshBin, "web", "--host", host, "--port", port}
	// 配置目录固定为工作区下的 .dsh/（标准布局：工作区根目录放项目文件，.dsh/ 放配置）
	dshHome := filepath.Join(*workDir, ".dsh")
	if err := os.MkdirAll(*workDir, 0o755); err != nil {
		log.Fatalf("创建工作目录失败: %v", err)
	}
	if err := os.MkdirAll(dshHome, 0o755); err != nil {
		log.Fatalf("创建配置目录失败: %v", err)
	}
	log.Printf("工作区: %s，配置目录: %s", *workDir, dshHome)

	// dsh 配置目录用官方 DSH_HOME 指定（默认才是 ~/.dsh），HOME 保持用户原样（ssh/nvm/bashrc 全走 /root）
	// os/exec 对重复键保留最后一个，追加的 DSH_HOME 优先
	env := append(os.Environ(), "DSH_HOME="+dshHome)

	for {
		log.Printf("启动 dsh web (%s:%s) ...", host, port)
		cmd := exec.Command("node", args...)
		cmd.Dir = *workDir // dsh 的 cwd：工作区根目录（ssh 走 HOME=/root/.ssh，不受 cwd 影响）
		cmd.Env = env
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			log.Fatalf("启动 dsh web 失败: %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		select {
		case sig := <-sigCh:
			// 转发信号后等 dsh 退出，容器停止时优雅收尾
			log.Printf("收到 %s，转发给 dsh web", sig)
			cmd.Process.Signal(sig)
			<-done
			log.Printf("已停止")
			return
		case err := <-done:
			log.Printf("dsh web 意外退出(%v)，%s 后重启", err, restartDelay)
			select {
			case sig := <-sigCh:
				log.Printf("收到 %s，退出", sig)
				return
			case <-time.After(restartDelay):
			}
		}
	}
}
