package core

import (
	"fmt"
	"net"
	"testing"
	"time"
)

// TestOutboundIpBindSemantics 验证 Xray sendThrough 的绑定语义。
//
// 关键结论：sendThrough 最终落到 socket 的 bind(localAddr)，
// 因此必须是**本机实际拥有**的地址 —— 否则 bind 失败、连接建立不起来。
//
// 本测试用本机真实网卡地址做对照：
//  1. 绑本机真实 IP   → 应该成功
//  2. 绑非本机公网 IP → 应该失败（EADDRNOTAVAIL）
func TestOutboundIpBindSemantics(t *testing.T) {
	// 找出本机第一个可用的 IPv4
	var localIP net.IP
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("InterfaceAddrs failed: %v", err)
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if v4 := ipnet.IP.To4(); v4 != nil {
				localIP = v4
				break
			}
		}
	}
	if localIP == nil {
		t.Skip("no non-loopback IPv4 found on this host")
	}

	dialFrom := func(src net.IP) error {
		d := &net.Dialer{
			Timeout:   5 * time.Second,
			LocalAddr: &net.TCPAddr{IP: src},
		}
		conn, err := d.Dial("tcp", "1.1.1.1:80")
		if err == nil {
			conn.Close()
		}
		return err
	}

	// 1) 绑本机真实 IP
	errLocal := dialFrom(localIP)
	if errLocal == nil {
		t.Logf("OK bind 本机真实 IP %s -> 成功", localIP)
	} else {
		t.Logf("OK bind 本机真实 IP %s -> 失败(外网不可达, 非 bind 问题): %v", localIP, errLocal)
	}

	// 2) 绑一个确定不属于本机的公网 IP —— 必须失败
	//    203.0.113.0/24 是 RFC 5737 保留的文档用地址段
	fakePublic := net.ParseIP("203.0.113.77")
	errFake := dialFrom(fakePublic)
	if errFake == nil {
		t.Fatalf("bind 非本机 IP %s 居然成功了 —— 结论有误", fakePublic)
	}
	t.Logf("OK bind 非本机公网 IP %s -> 失败: %v", fakePublic, errFake)

	// 3) 绑回环
	t.Logf("   bind 回环 127.0.0.1 -> %v", dialFrom(net.ParseIP("127.0.0.1")))

	t.Logf("---")
	t.Logf("sendThrough = socket.bind(localAddr)")
	t.Logf("所以：必须是本机网卡上真实配置的 IP（或回环），否则 bind 失败")
	t.Logf("本机接口列表: %s", fmt.Sprint(addrs))
}
