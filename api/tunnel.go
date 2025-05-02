package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/Diniboy1123/usque/internal"
	"github.com/songgao/water"
	"golang.org/x/time/rate"
	"golang.zx2c4.com/wireguard/tun"
)

// 包级别的缓冲池，用于复用数据包缓冲区
var packetBufferPool = sync.Pool{
	New: func() interface{} {
		// 默认分配MTU大小的缓冲区，实际使用时会根据需要调整
		return make([]byte, 2048)
	},
}

// TunnelDevice abstracts a TUN device so that we can use the same tunnel-maintenance code
// regardless of the underlying implementation.
type TunnelDevice interface {
	// ReadPacket reads a packet from the device (using the given mtu) and returns its contents.
	ReadPacket(mtu int) ([]byte, error)
	// WritePacket writes a packet to the device.
	WritePacket(pkt []byte) error
}

// TunnelStats 用于跟踪隧道性能指标
type TunnelStats struct {
	PacketsIn     uint64
	PacketsOut    uint64
	BytesIn       uint64
	BytesOut      uint64
	Errors        uint64
	Reconnections uint64
	LastReconnect time.Time
	mu            sync.Mutex
}

func (s *TunnelStats) RecordPacketIn(bytes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PacketsIn++
	s.BytesIn += uint64(bytes)
}

func (s *TunnelStats) RecordPacketOut(bytes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PacketsOut++
	s.BytesOut += uint64(bytes)
}

func (s *TunnelStats) RecordError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Errors++
}

func (s *TunnelStats) RecordReconnection() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Reconnections++
	s.LastReconnect = time.Now()
}

// NetstackAdapter wraps a tun.Device (e.g. from netstack) to satisfy TunnelDevice.
type NetstackAdapter struct {
	dev tun.Device
}

func (n *NetstackAdapter) ReadPacket(mtu int) ([]byte, error) {
	// Deprecated: 原始实现方式，直接创建新缓冲区
	// For netstack TUN devices we need to use the multi-buffer interface.
	// packetBufs := make([][]byte, 1)
	// packetBufs[0] = make([]byte, mtu)
	// sizes := make([]int, 1)
	// _, err := n.dev.Read(packetBufs, sizes, 0)
	// if err != nil {
	//     return nil, err
	// }
	// return packetBufs[0][:sizes[0]], nil

	// 从对象池获取缓冲区
	bufInterface := packetBufferPool.Get()
	packetBuf := bufInterface.([]byte)

	// 确保缓冲区有足够的大小
	if cap(packetBuf) < mtu {
		packetBuf = make([]byte, mtu)
	} else {
		packetBuf = packetBuf[:mtu]
	}

	// 为多缓冲区接口准备切片
	packetBufs := make([][]byte, 1)
	packetBufs[0] = packetBuf
	sizes := make([]int, 1)

	_, err := n.dev.Read(packetBufs, sizes, 0)
	if err != nil {
		// 错误发生时，归还缓冲区到池中
		packetBufferPool.Put(packetBuf)
		return nil, err
	}

	// 创建一个新的切片保存实际数据，并归还原始缓冲区到池中
	result := make([]byte, sizes[0])
	copy(result, packetBufs[0][:sizes[0]])
	packetBufferPool.Put(packetBuf)

	return result, nil
}

func (n *NetstackAdapter) WritePacket(pkt []byte) error {
	// Write expects a slice of packet buffers.
	_, err := n.dev.Write([][]byte{pkt}, 0)
	return err
}

// NewNetstackAdapter creates a new NetstackAdapter.
func NewNetstackAdapter(dev tun.Device) TunnelDevice {
	return &NetstackAdapter{dev: dev}
}

// WaterAdapter wraps a *water.Interface so it satisfies TunnelDevice.
type WaterAdapter struct {
	iface *water.Interface
}

func (w *WaterAdapter) ReadPacket(mtu int) ([]byte, error) {
	// Deprecated: 原始实现方式，每次都创建新缓冲区
	// buf := make([]byte, mtu)
	// n, err := w.iface.Read(buf)
	// if err != nil {
	//    return nil, err
	// }
	// return buf[:n], nil

	// 从对象池获取缓冲区
	bufInterface := packetBufferPool.Get()
	buf := bufInterface.([]byte)

	// 确保缓冲区有足够的大小
	if cap(buf) < mtu {
		buf = make([]byte, mtu)
	} else {
		buf = buf[:mtu]
	}

	n, err := w.iface.Read(buf)
	if err != nil {
		// 错误发生时，归还缓冲区到池中
		packetBufferPool.Put(buf)
		return nil, err
	}

	// 创建一个新的切片保存实际数据，并归还原始缓冲区到池中
	result := make([]byte, n)
	copy(result, buf[:n])
	packetBufferPool.Put(buf)

	return result, nil
}

func (w *WaterAdapter) WritePacket(pkt []byte) error {
	_, err := w.iface.Write(pkt)
	return err
}

// NewWaterAdapter creates a new WaterAdapter.
func NewWaterAdapter(iface *water.Interface) TunnelDevice {
	return &WaterAdapter{iface: iface}
}

// ConnectionConfig 包含连接配置选项
type ConnectionConfig struct {
	TLSConfig         *tls.Config
	KeepAlivePeriod   time.Duration
	InitialPacketSize uint16
	Endpoint          *net.UDPAddr
	MTU               int
	MaxPacketRate     float64 // 每秒最大数据包处理速率
	MaxBurst          int     // 突发处理数据包的最大数量
	ReconnectStrategy BackoffStrategy
}

// BackoffStrategy 定义重连策略接口
type BackoffStrategy interface {
	NextDelay(attempt int) time.Duration
	Reset()
}

// ExponentialBackoff 实现指数退避重连策略
type ExponentialBackoff struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Factor       float64
}

func (b *ExponentialBackoff) NextDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return b.InitialDelay
	}

	// 计算指数退避延迟
	delay := b.InitialDelay
	maxDelayInFloat := float64(b.MaxDelay) / b.Factor
	for i := 0; i < attempt && float64(delay) < maxDelayInFloat; i++ {
		delay = time.Duration(float64(delay) * b.Factor)
	}

	// 确保不超过最大延迟
	if delay > b.MaxDelay {
		delay = b.MaxDelay
	}

	// 添加随机抖动以避免雷暴问题
	jitter := time.Duration(float64(delay) * 0.1) // 10%的抖动
	delay = delay - jitter + time.Duration(float64(jitter*2)*rand.Float64())

	return delay
}

func (b *ExponentialBackoff) Reset() {
	// 重置状态（如果需要）
}

// MaintainTunnel continuously connects to the MASQUE server, then starts two
// forwarding goroutines: one forwarding from the device to the IP connection (and handling
// any ICMP reply), and the other forwarding from the IP connection to the device.
// If an error occurs in either loop, the connection is closed and a reconnect is attempted.
//
// Parameters:
//   - ctx: context.Context - The context for the connection.
//   - config: ConnectionConfig - 包含TLS配置、keepalive周期、初始包大小、endpoint、MTU等配置
//   - device: TunnelDevice - The TUN device to forward packets to and from.
//
// Deprecated: 原始函数签名
// func (OLD)MaintainTunnel(ctx context.Context, tlsConfig *tls.Config, keepalivePeriod time.Duration,
//
//	initialPacketSize uint16, endpoint *net.UDPAddr, device TunnelDevice, mtu int, reconnectDelay time.Duration) {

// MaintainTunnel continuously connects to the MASQUE server, then starts two
func MaintainTunnel(ctx context.Context, config ConnectionConfig, device TunnelDevice) {
	stats := &TunnelStats{}
	reconnectAttempt := 0

	// 创建一个限速器来控制处理速率
	limiter := rate.NewLimiter(rate.Limit(config.MaxPacketRate), config.MaxBurst)

	for {
		select {
		case <-ctx.Done():
			log.Println("Context canceled, stopping tunnel maintenance")
			return
		default:
			// 继续执行
		}

		log.Printf("Establishing MASQUE connection to %s:%d (attempt #%d)",
			config.Endpoint.IP, config.Endpoint.Port, reconnectAttempt+1)

		udpConn, tr, ipConn, rsp, err := ConnectTunnel(
			ctx,
			config.TLSConfig,
			internal.DefaultQuicConfig(config.KeepAlivePeriod, config.InitialPacketSize),
			internal.ConnectURI,
			config.Endpoint,
		)

		if err != nil {
			log.Printf("Failed to connect tunnel: %v", err)
			stats.RecordError()
			delay := config.ReconnectStrategy.NextDelay(reconnectAttempt)
			reconnectAttempt++
			log.Printf("Will retry in %v", delay)

			select {
			case <-time.After(delay):
				// 等待重连延迟
			case <-ctx.Done():
				return
			}
			continue
		}

		if rsp.StatusCode != 200 {
			log.Printf("Tunnel connection failed: %s", rsp.Status)
			stats.RecordError()

			// 清理资源
			if ipConn != nil {
				ipConn.Close()
			}
			if udpConn != nil {
				udpConn.Close()
			}
			if tr != nil {
				tr.Close()
			}

			delay := config.ReconnectStrategy.NextDelay(reconnectAttempt)
			reconnectAttempt++
			log.Printf("Will retry in %v", delay)

			select {
			case <-time.After(delay):
				// 等待重连延迟
			case <-ctx.Done():
				return
			}
			continue
		}

		// 连接成功，重置重连尝试计数和策略
		reconnectAttempt = 0
		config.ReconnectStrategy.Reset()
		stats.RecordReconnection()
		log.Println("Connected to MASQUE server")

		// 创建子上下文，用于在需要时取消转发goroutine
		forwardingCtx, cancelForwarding := context.WithCancel(ctx)
		errChan := make(chan error, 2)

		// 从设备到IP连接的转发
		go func() {
			// Deprecated: 原始实现方式
			// for {
			//     pkt, err := device.ReadPacket(mtu)
			//     if err != nil {
			//         errChan <- fmt.Errorf("failed to read from TUN device: %v", err)
			//         return
			//     }
			//     icmp, err := ipConn.WritePacket(pkt)
			//     if err != nil {
			//         errChan <- fmt.Errorf("failed to write to IP connection: %v", err)
			//         return
			//     }
			//     if len(icmp) > 0 {
			//         if err := device.WritePacket(icmp); err != nil {
			//             errChan <- fmt.Errorf("failed to write ICMP to TUN device: %v", err)
			//             return
			//         }
			//     }
			// }

			for {
				select {
				case <-forwardingCtx.Done():
					return
				default:
					// 应用速率限制
					if err := limiter.Wait(forwardingCtx); err != nil {
						if errors.Is(err, context.Canceled) {
							return
						}
						continue
					}

					pkt, err := device.ReadPacket(config.MTU)
					if err != nil {
						errChan <- fmt.Errorf("failed to read from TUN device: %v", err)
						return
					}

					stats.RecordPacketOut(len(pkt))
					icmp, err := ipConn.WritePacket(pkt)
					if err != nil {
						errChan <- fmt.Errorf("failed to write to IP connection: %v", err)
						return
					}

					if len(icmp) > 0 {
						if err := device.WritePacket(icmp); err != nil {
							errChan <- fmt.Errorf("failed to write ICMP to TUN device: %v", err)
							return
						}
						stats.RecordPacketIn(len(icmp))
					}
				}
			}
		}()

		// 从IP连接到设备的转发
		go func() {
			// Deprecated: 原始实现方式
			// buf := make([]byte, mtu)
			// for {
			//     n, err := ipConn.ReadPacket(buf, true)
			//     if err != nil {
			//         errChan <- fmt.Errorf("failed to read from IP connection: %v", err)
			//         return
			//     }
			//     if err := device.WritePacket(buf[:n]); err != nil {
			//         errChan <- fmt.Errorf("failed to write to TUN device: %v", err)
			//         return
			//     }
			// }

			for {
				select {
				case <-forwardingCtx.Done():
					return
				default:
					// 应用速率限制
					if err := limiter.Wait(forwardingCtx); err != nil {
						if errors.Is(err, context.Canceled) {
							return
						}
						continue
					}

					// 从对象池获取缓冲区
					bufInterface := packetBufferPool.Get()
					buf := bufInterface.([]byte)

					// 确保缓冲区有足够的大小
					if cap(buf) < config.MTU {
						buf = make([]byte, config.MTU)
					} else {
						buf = buf[:config.MTU]
					}

					n, err := ipConn.ReadPacket(buf, true)
					if err != nil {
						packetBufferPool.Put(buf) // 归还缓冲区
						errChan <- fmt.Errorf("failed to read from IP connection: %v", err)
						return
					}

					stats.RecordPacketIn(n)
					if err := device.WritePacket(buf[:n]); err != nil {
						packetBufferPool.Put(buf) // 归还缓冲区
						errChan <- fmt.Errorf("failed to write to TUN device: %v", err)
						return
					}

					packetBufferPool.Put(buf) // 归还缓冲区
				}
			}
		}()

		// 监控统计信息的goroutine
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-forwardingCtx.Done():
					return
				case <-ticker.C:
					log.Printf("Tunnel stats: In: %d pkts (%d bytes), Out: %d pkts (%d bytes), Errors: %d, Reconnects: %d",
						stats.PacketsIn, stats.BytesIn, stats.PacketsOut, stats.BytesOut, stats.Errors, stats.Reconnections)
				}
			}
		}()

		// 等待错误或上下文取消
		select {
		case err := <-errChan:
			log.Printf("Tunnel connection lost: %v. Reconnecting...", err)
			stats.RecordError()
		case <-ctx.Done():
			log.Println("Context canceled, stopping tunnel")
		}

		// 清理资源
		cancelForwarding()
		if ipConn != nil {
			ipConn.Close()
		}
		if udpConn != nil {
			udpConn.Close()
		}
		if tr != nil {
			tr.Close()
		}

		// 如果上下文被取消，则退出循环
		if ctx.Err() != nil {
			return
		}

		// 否则准备重连
		delay := config.ReconnectStrategy.NextDelay(reconnectAttempt)
		reconnectAttempt++
		log.Printf("Will retry in %v", delay)

		select {
		case <-time.After(delay):
			// 等待重连延迟
		case <-ctx.Done():
			return
		}
	}
}

// 使用优化后的MaintainTunnel的示例
//func Example_optimizedUsage() {
//	// 创建TUN设备
//	device := /* ... 创建或获取TUN设备 */
//
//	// 创建上下文，用于控制整个隧道生命周期
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//
//	// 配置连接
//	config := ConnectionConfig{
//		TLSConfig:         createTLSConfig(),
//		KeepAlivePeriod:   30 * time.Second,
//		InitialPacketSize: 1280,
//		Endpoint:          &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 443},
//		MTU:               1500,
//		MaxPacketRate:     5000, // 每秒5000个数据包
//		MaxBurst:          500,  // 突发最多处理500个数据包
//		ReconnectStrategy: &ExponentialBackoff{
//			InitialDelay: 1 * time.Second,
//			MaxDelay:     5 * time.Minute,
//			Factor:       2.0,
//		},
//	}
//
//	// 启动隧道维护
//	MaintainTunnel(ctx, config, device)
//}
