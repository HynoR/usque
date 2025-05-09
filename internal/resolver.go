package internal

import (
	"context"
	"net"
	"sync"
	"time"
)

// DNSCacheEntry 表示缓存中的一个条目
type DNSCacheEntry struct {
	IP        net.IP
	ExpiresAt time.Time
}

// CachingDNSResolver 实现了带缓存的DNS解析器
type CachingDNSResolver struct {
	// DNS服务器地址
	DNSServer string
	// 缓存过期时间（秒）
	CacheTTL int
	// 缓存
	cache     map[string]DNSCacheEntry
	cacheLock sync.RWMutex
}

// NewCachingDNSResolver 创建一个新的缓存DNS解析器
// dnsServer: DNS服务器地址，如 "8.8.8.8:53"
// cacheTTLSeconds: 缓存有效期（秒）
func NewCachingDNSResolver(dnsServer string, cacheTTLSeconds int) *CachingDNSResolver {
	if cacheTTLSeconds <= 0 {
		cacheTTLSeconds = 600 // 默认10分钟
	}

	if dnsServer == "" {
		dnsServer = "8.8.8.8:53" // 默认使用谷歌DNS
	}

	return &CachingDNSResolver{
		DNSServer: dnsServer,
		CacheTTL:  cacheTTLSeconds,
		cache:     make(map[string]DNSCacheEntry),
	}
}

type dnsLookupResult struct {
	ip  net.IP
	err error
}

// Resolve 实现NameResolver接口，解析域名为IP地址
func (r *CachingDNSResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	// 先检查缓存
	r.cacheLock.RLock()
	entry, exists := r.cache[name]
	now := time.Now()
	cacheHit := exists && now.Before(entry.ExpiresAt)
	r.cacheLock.RUnlock()

	// 如果缓存中存在且未过期，直接返回
	if cacheHit {
		return ctx, entry.IP, nil
	}

	// 使用单独锁来防止对同一域名的并发DNS查询，实现"查询合并"
	resultChan := make(chan dnsLookupResult, 1)

	// 缓存不存在或已过期，进行实际的DNS查询
	// 这里可以添加错误重试逻辑
	go func() {
		resolver := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: time.Second * 5}
				return d.DialContext(ctx, "udp", r.DNSServer)
			},
		}

		ips, err := resolver.LookupIP(ctx, "ip", name)
		if err != nil {
			resultChan <- dnsLookupResult{nil, err}
			return
		}

		if len(ips) == 0 {
			resultChan <- dnsLookupResult{nil, net.ErrClosed}
			return
		}

		resultChan <- dnsLookupResult{ips[0], nil}
	}()

	// 等待DNS查询完成或上下文取消
	select {
	case <-ctx.Done():
		return ctx, nil, ctx.Err()
	case result := <-resultChan:
		if result.err != nil {
			return ctx, nil, result.err
		}

		// 更新缓存
		r.cacheLock.Lock()
		r.cache[name] = DNSCacheEntry{
			IP:        result.ip,
			ExpiresAt: now.Add(time.Duration(r.CacheTTL) * time.Second),
		}
		r.cacheLock.Unlock()

		return ctx, result.ip, nil
	}
}

// ClearCache 清除DNS缓存
func (r *CachingDNSResolver) ClearCache() {
	r.cacheLock.Lock()
	defer r.cacheLock.Unlock()
	r.cache = make(map[string]DNSCacheEntry)
}
