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
	dataDir = flag.String("data-dir", "/data", "dsh 配置目录（DSH_HOME，即原 ~/.dsh 的语义），仅 -dsh-bin 非空时使用")
	workDir = flag.String("work-dir", "/root", "dsh web 工作目录（cwd，终端打开位置），仅 -dsh-bin 非空时使用")
)

const restartDelay = 2 * time.Second // dsh web 崩溃后的重启间隔

// 认证入口固定路径：/login、/logout。
// dsh web 服务端只注册了 /plugins 和 /api 两个前缀路由，其余全走 SPA
// catch-all（实测 POST /login 返回 405），固定路径不会冲突；
// 且路径不再随重启变化，旧标签页永远不会失效
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

	// 其余所有路径：会话有效才反代到后端应用
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

// supervise 以子进程方式启动 dsh web：崩溃自动重启；退出信号转发给子进程后再退出。
// 僵尸进程由 Go 运行时自动回收（SIGCHLD 处理器 wait4 循环），所以不需要 init 进程
func supervise(sigCh <-chan os.Signal, backendURL *url.URL) {
	// 纯代理模式（未传 -dsh-bin）：常驻等退出信号，Ctrl+C 可正常退出
	if *dshBin == "" {
		<-sigCh
		return
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}
	if err := os.MkdirAll(*workDir, 0o755); err != nil {
		log.Fatalf("创建工作目录失败: %v", err)
	}
	log.Printf("数据目录: %s，工作目录: %s", *dataDir, *workDir)

	// 子进程接线与 -backend 同一来源：dsh web 必须监听在代理的上游地址上
	host := backendURL.Hostname()
	port := backendURL.Port()
	if port == "" {
		port = "80"
	}
	args := []string{*dshBin, "web", "--host", host, "--port", port}
	// dsh 配置目录用官方 DSH_HOME 指定（默认才是 ~/.dsh），HOME 保持用户原样（ssh/nvm/bashrc 全走 /root）
	env := append(os.Environ(), "DSH_HOME="+*dataDir)

	for {
		log.Printf("启动 dsh web (%s:%s) ...", host, port)
		cmd := exec.Command("node", args...)
		cmd.Dir = *workDir // dsh 的 cwd（终端打开位置）
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
