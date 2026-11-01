/*
 * Package port 定义应用层依赖的外部能力端口。
 *
 * 约定：端口只描述能力与错误语义，不出现任何供应商 SDK 类型；
 * 付费能力（视觉识别、邮件投递、对象存储）必须先经此层，禁止用例直接调用 SDK。
 * 参考：docs/ARCHITECTURE.md#52-代码结构与依赖规则、docs/ARCHITECTURE.md#816-付费第三方服务的端口与-fake-策略
 */
package port
