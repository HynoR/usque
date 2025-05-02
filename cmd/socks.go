package cmd

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"runtime"
	"sync"

	"time"

	"github.com/Diniboy1123/usque/api"
	"github.com/Diniboy1123/usque/config"
	"github.com/Diniboy1123/usque/internal"
	"github.com/spf13/cobra"
	"github.com/things-go/go-socks5"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// socksCmd 命令。
var socksCmd = &cobra.Command{
	Use:   "socks",
	Short: "Expose Warp as a SOCKS5 proxy",
	Long:  "Dual-stack SOCKS5 proxy with optional authentication. Doesn't require elevated privileges.",
	Run:   runSocksCmd,
}

func init() {
	socksCmd.Flags().StringP("bind", "b", "0.0.0.0", "Address to bind the SOCKS proxy to")
	socksCmd.Flags().StringP("port", "p", "1080", "Port to listen on for SOCKS proxy")
	socksCmd.Flags().StringP("username", "u", "", "Username for proxy authentication (specify both username and password to enable)")
	socksCmd.Flags().StringP("password", "w", "", "Password for proxy authentication (specify both username and password to enable)")
	socksCmd.Flags().IntP("connect-port", "P", 443, "Used port for MASQUE connection")
	socksCmd.Flags().StringArrayP("dns", "d", []string{"9.9.9.9", "149.112.112.112", "2620:fe::fe", "2620:fe::9"}, "DNS servers to use inside the MASQUE tunnel")
	socksCmd.Flags().DurationP("dns-timeout", "t", 2*time.Second, "Timeout for DNS queries on a specific server (tries the next server if exceeded)")
	socksCmd.Flags().BoolP("ipv6", "6", false, "Use IPv6 for MASQUE connection")
	socksCmd.Flags().BoolP("no-tunnel-ipv4", "F", false, "Disable IPv4 inside the MASQUE tunnel")
	socksCmd.Flags().BoolP("no-tunnel-ipv6", "S", false, "Disable IPv6 inside the MASQUE tunnel")
	socksCmd.Flags().StringP("sni-address", "s", internal.ConnectSNI, "SNI address to use for MASQUE connection")
	socksCmd.Flags().DurationP("keepalive-period", "k", 30*time.Second, "Keepalive period for MASQUE connection")
	socksCmd.Flags().IntP("mtu", "m", 1280, "MTU for MASQUE connection")
	socksCmd.Flags().Uint16P("initial-packet-size", "i", 1242, "Initial packet size for MASQUE connection")
	socksCmd.Flags().DurationP("reconnect-delay", "r", 1*time.Second, "Delay between reconnect attempts")

	// 把 socksCmd 注册到根命令（如果有的话）
	rootCmd.AddCommand(socksCmd)
}

// runSocksCmd 是 socksCmd 的执行逻辑
func runSocksCmd(cmd *cobra.Command, args []string) {
	// 1. 建议在应用启动时设置 GOMAXPROCS，保证并发时能够利用多核 CPU。
	runtime.GOMAXPROCS(runtime.NumCPU())

	if !config.ConfigLoaded {
		cmd.Println("Config not loaded. Please register first.")
		return
	}

	sni, err := cmd.Flags().GetString("sni-address")
	if err != nil {
		cmd.Printf("Failed to get SNI address: %v\n", err)
		return
	}

	privKey, err := config.AppConfig.GetEcPrivateKey()
	if err != nil {
		cmd.Printf("Failed to get private key: %v\n", err)
		return
	}
	peerPubKey, err := config.AppConfig.GetEcEndpointPublicKey()
	if err != nil {
		cmd.Printf("Failed to get public key: %v\n", err)
		return
	}

	cert, err := internal.GenerateCert(privKey, &privKey.PublicKey)
	if err != nil {
		cmd.Printf("Failed to generate cert: %v\n", err)
		return
	}

	tlsConfig, err := api.PrepareTlsConfig(privKey, peerPubKey, cert, sni)
	if err != nil {
		cmd.Printf("Failed to prepare TLS config: %v\n", err)
		return
	}

	keepalivePeriod, err := cmd.Flags().GetDuration("keepalive-period")
	if err != nil {
		cmd.Printf("Failed to get keepalive period: %v\n", err)
		return
	}
	initialPacketSize, err := cmd.Flags().GetUint16("initial-packet-size")
	if err != nil {
		cmd.Printf("Failed to get initial packet size: %v\n", err)
		return
	}

	bindAddress, err := cmd.Flags().GetString("bind")
	if err != nil {
		cmd.Printf("Failed to get bind address: %v\n", err)
		return
	}

	port, err := cmd.Flags().GetString("port")
	if err != nil {
		cmd.Printf("Failed to get port: %v\n", err)
		return
	}

	connectPort, err := cmd.Flags().GetInt("connect-port")
	if err != nil {
		cmd.Printf("Failed to get connect port: %v\n", err)
		return
	}

	var endpoint *net.UDPAddr
	if ipv6, err := cmd.Flags().GetBool("ipv6"); err == nil && !ipv6 {
		endpoint = &net.UDPAddr{
			IP:   net.ParseIP(config.AppConfig.EndpointV4),
			Port: connectPort,
		}
	} else {
		endpoint = &net.UDPAddr{
			IP:   net.ParseIP(config.AppConfig.EndpointV6),
			Port: connectPort,
		}
	}

	tunnelIPv4, err := cmd.Flags().GetBool("no-tunnel-ipv4")
	if err != nil {
		cmd.Printf("Failed to get no tunnel IPv4: %v\n", err)
		return
	}

	tunnelIPv6, err := cmd.Flags().GetBool("no-tunnel-ipv6")
	if err != nil {
		cmd.Printf("Failed to get no tunnel IPv6: %v\n", err)
		return
	}

	var localAddresses []netip.Addr
	if !tunnelIPv4 {
		v4, err := netip.ParseAddr(config.AppConfig.IPv4)
		if err != nil {
			cmd.Printf("Failed to parse IPv4 address: %v\n", err)
			return
		}
		localAddresses = append(localAddresses, v4)
	}
	if !tunnelIPv6 {
		v6, err := netip.ParseAddr(config.AppConfig.IPv6)
		if err != nil {
			cmd.Printf("Failed to parse IPv6 address: %v\n", err)
			return
		}
		localAddresses = append(localAddresses, v6)
	}

	dnsServers, err := cmd.Flags().GetStringArray("dns")
	if err != nil {
		cmd.Printf("Failed to get DNS servers: %v\n", err)
		return
	}

	var dnsAddrs []netip.Addr
	for _, dns := range dnsServers {
		addr, err := netip.ParseAddr(dns)
		if err != nil {
			cmd.Printf("Failed to parse DNS server: %v\n", err)
			return
		}
		dnsAddrs = append(dnsAddrs, addr)
	}

	dnsTimeout, err := cmd.Flags().GetDuration("dns-timeout")
	if err != nil {
		cmd.Printf("Failed to get DNS timeout: %v\n", err)
		return
	}

	mtu, err := cmd.Flags().GetInt("mtu")
	if err != nil {
		cmd.Printf("Failed to get MTU: %v\n", err)
		return
	}
	if mtu != 1280 {
		log.Println("Warning: MTU is not the default 1280. This is not supported. Packet loss and other issues may occur.")
	}

	var username string
	var password string
	if u, err := cmd.Flags().GetString("username"); err == nil && u != "" {
		username = u
	}
	if p, err := cmd.Flags().GetString("password"); err == nil && p != "" {
		password = p
	}

	//reconnectDelay, err := cmd.Flags().GetDuration("reconnect-delay")
	//if err != nil {
	//	cmd.Printf("Failed to get reconnect delay: %v\n", err)
	//	return
	//}

	// 2. 创建 TUN 设备
	tunDev, tunNet, err := netstack.CreateNetTUN(localAddresses, dnsAddrs, mtu)
	if err != nil {
		cmd.Printf("Failed to create virtual TUN device: %v\n", err)
		return
	}
	defer tunDev.Close()

	// 配置连接
	configTunnel := api.ConnectionConfig{
		TLSConfig:         tlsConfig,
		KeepAlivePeriod:   keepalivePeriod,
		InitialPacketSize: initialPacketSize,
		Endpoint:          endpoint,
		MTU:               mtu,
		MaxPacketRate:     8192,
		MaxBurst:          1024,
		ReconnectStrategy: &api.ExponentialBackoff{
			InitialDelay: 1 * time.Second,
			MaxDelay:     5 * time.Minute,
			Factor:       2.0,
		},
	}

	// 3. 后台协程维护 MASQUE 隧道连接
	go api.MaintainTunnelV2(
		context.Background(),
		configTunnel,
		api.NewNetstackAdapter(tunDev),
	)

	// 4. 构建 Socks5.Server
	//   - Dial 使用 tunNet 的 DialContext
	//   - Resolver 使用并行 DNS + 缓存
	dnsResolver := &ParallelCachedDNSResolver{
		TunNet:   tunNet,
		DNSAddrs: dnsAddrs,
		Timeout:  dnsTimeout,
		TTL:      300 * time.Second, // DNS 缓存生存时间，可视需求调整
	}

	var server *socks5.Server
	if username == "" || password == "" {
		server = socks5.NewServer(
			socks5.WithLogger(socks5.NewLogger(log.New(os.Stdout, "socks5: ", log.LstdFlags))),
			socks5.WithDial(func(ctx context.Context, network, addr string) (net.Conn, error) {
				log.Printf("[Socks] Connecting to (%s) %s", network, addr)
				return tunNet.DialContext(ctx, network, addr)
			}),
			socks5.WithResolver(dnsResolver),
		)
	} else {
		server = socks5.NewServer(
			socks5.WithLogger(socks5.NewLogger(log.New(os.Stdout, "socks5: ", log.LstdFlags))),
			socks5.WithDial(func(ctx context.Context, network, addr string) (net.Conn, error) {
				return tunNet.DialContext(ctx, network, addr)
			}),
			socks5.WithResolver(dnsResolver),
			socks5.WithAuthMethods(
				[]socks5.Authenticator{
					socks5.UserPassAuthenticator{
						Credentials: socks5.StaticCredentials{
							username: password,
						},
					},
				},
			),
		)
	}

	// 5. 启动 SOCKS5 代理
	log.Printf("SOCKS proxy listening on %s:%s", bindAddress, port)
	if err := server.ListenAndServe("tcp", net.JoinHostPort(bindAddress, port)); err != nil {
		cmd.Printf("Failed to start SOCKS proxy: %v\n", err)
		return
	}
}

// ======================= 并行 + 缓存 DNS 解析示例 =======================

// ParallelCachedDNSResolver 使用隧道内的 DNS 服务器做解析；
// 支持并发向多个 DNS 服务器同时发起请求，并对结果进行缓存。
type ParallelCachedDNSResolver struct {
	TunNet   *netstack.Net
	DNSAddrs []netip.Addr
	Timeout  time.Duration
	TTL      time.Duration // DNS 缓存的生存时间

	// 缓存结构：map[域名] => { 解析结果, 过期时间 }
	// sync.Map 用于并发安全
	cache sync.Map
}

// dnsCacheEntry 用于存储缓存的解析结果
type dnsCacheEntry struct {
	ip       net.IP
	expireAt time.Time
}

// Resolve 并行向多个 DNS 服务器发起查询，在收到第一个成功结果后就返回，并缓存结果。
func (r *ParallelCachedDNSResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	// 1. 如果缓存中已有且未过期，直接返回
	if ip, ok := r.getFromCache(name); ok {
		return ctx, ip, nil
	}

	// 2. 并行向多个 DNS 服务器发起查询
	ip, err := r.resolveParallel(ctx, name)
	if err != nil {
		return ctx, nil, fmt.Errorf("all DNS servers failed for name %s: %v", name, err)
	}

	// 3. 缓存结果
	r.setCache(name, ip)
	return ctx, ip, nil
}

// 并行解析，任何一个 DNS 服务器返回成功即返回
func (r *ParallelCachedDNSResolver) resolveParallel(ctx context.Context, name string) (net.IP, error) {
	type result struct {
		ip  net.IP
		err error
	}
	resultCh := make(chan result, 1)

	// 可 cancel 的上下文
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for _, dnsAddr := range r.DNSAddrs {
		wg.Add(1)
		go func(addr netip.Addr) {
			defer wg.Done()
			// 构造 resolver 与超时
			dialer := func(ctx context.Context, network, address string) (net.Conn, error) {
				return r.TunNet.DialContext(ctx, "udp", net.JoinHostPort(addr.String(), "53"))
			}
			resolver := &net.Resolver{PreferGo: true, Dial: dialer}
			ctxTimeout, cancelTimeout := context.WithTimeout(ctx, r.Timeout)
			defer cancelTimeout()

			ips, err := resolver.LookupIP(ctxTimeout, "ip", name)
			if err == nil && len(ips) > 0 {
				// 尝试写入第一个结果
				select {
				case resultCh <- result{ip: ips[0], err: nil}:
					cancel() // 拿到第一个就 cancel 其他
				default:
				}
			} else {
				// 如果还没收到任何结果，就写入错误
				select {
				case resultCh <- result{ip: nil, err: fmt.Errorf("server %s: %w", addr, err)}:
				default:
				}
			}
		}(dnsAddr)
	}

	// 等待所有 goroutine 结束后关闭 channel
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// 取第一个写入的结果
	if res, ok := <-resultCh; ok {
		return res.ip, res.err
	}
	return nil, fmt.Errorf("all DNS queries failed")
}

// 从缓存中获取
func (r *ParallelCachedDNSResolver) getFromCache(name string) (cachedIP net.IP, found bool) {
	value, ok := r.cache.Load(name)
	if !ok {
		return nil, false
	}
	entry, _ := value.(dnsCacheEntry)
	if time.Now().After(entry.expireAt) {
		// 缓存过期，删除
		r.cache.Delete(name)
		return nil, false
	}
	return entry.ip, true
}

// 设置缓存
func (r *ParallelCachedDNSResolver) setCache(name string, ip net.IP) {
	r.cache.Store(name, dnsCacheEntry{
		ip:       ip,
		expireAt: time.Now().Add(r.TTL),
	})
}
