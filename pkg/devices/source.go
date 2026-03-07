// Package devices 提供设备热插拔检测功能
// 本文件定义事件源接口别名
package devices

import "github.com/sipeed/picoclaw/pkg/devices/events"

// EventSource 事件源接口别名
// 定义在 events 包中，此处重新导出以便使用
type EventSource = events.EventSource
