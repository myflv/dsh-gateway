/**
 * dsh-gateway trust 插件（浏览器端，由网关 /plugins/dsh-gateway-trust/client.js 下发）。
 *
 * 上游把配置平面（settings/credentials/agentPreset 等）钉死 loopback 同源：
 * 前端 isLoopback 由 location.hostname 判定，非 loopback 时配置作用域降级
 * memory 空壳（插件配置即空白）。本部署经网关会话认证反向代理访问（dsh 只见
 * loopback Host，后端 fence 已全过），fence 无实际防御对象——网关注入本插件
 * 把 isLoopback 置 true，配置作用域恢复 host。
 *
 * 注册契约（client-modules/system.ts）：bundle 执行时须自注册
 * window.__ModuleLoader__.load({id, factory})，factory 收 require 返回模块
 * 导出（name/inject/apply），由 cordis loader 消费。包装形态与真实 bundle
 * （tsdown 产物）一致：module/exports + Symbol.toStringTag。
 *
 * 激活顺序由框架保证：inject: ['connection'] 使本 fiber 在 connection 服务
 * 提供前保持 PENDING（boot.tsx：fiber inject waiting owns activation order）。
 * 直接改共享句柄的属性而非重新 provide：句柄是 client-connection 提供的同一
 * 对象引用，下游消费者（settings-scope 等）按绑定时刻读取，原地修改所有人可见。
 */
window.__ModuleLoader__.load({
  id: 'dsh-gateway-trust',
  factory: (require) => {
    var module = { exports: {} }
    var exports = module.exports
    Object.defineProperty(exports, Symbol.toStringTag, { value: 'Module' })
    exports.name = 'dsh-gateway-trust'
    exports.inject = ['connection']
    exports.apply = (ctx) => {
      const conn = ctx.get('connection')
      if (conn === undefined) throw new Error('dsh-gateway-trust: connection service missing')
      conn.isLoopback = true
    }
    return module.exports
  },
})
