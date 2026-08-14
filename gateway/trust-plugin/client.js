// 网关注入的信任插件：connection.isLoopback 置 true（配置平面放行，机制见 trust_plugin.go）
const pluginID = 'dsh-gateway-trust'
window.__ModuleLoader__.load({
  id: pluginID,
  factory: (require) => {
    var module = { exports: {} }
    var exports = module.exports
    Object.defineProperty(exports, Symbol.toStringTag, { value: 'Module' })
    exports.name = pluginID
    exports.inject = ['connection']
    exports.apply = (ctx) => {
      // inject 保证已提供；改共享句柄，下游按绑定时刻读取
      ctx.get('connection').isLoopback = true
    }
    return module.exports
  },
})
