package helpers

import (
	"net"
	"regexp"
)

var ipAtStart = regexp.MustCompile(`^([0-9]{1,3}\.){3}[0-9]{1,3}`)

func startsWithIP(s string) (bool, net.IP) {
	m := ipAtStart.FindString(s)
	if m == "" {
		return false, nil
	}
	ip := net.ParseIP(m)
	if ip == nil {
		return false, nil
	}
	return true, ip
}

func IPToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	if ip == nil {
		return 0
	}
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

func GetIPRange(cidr string) (uint32, uint32, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0, 0, err
	}
	start := IPToUint32(ipNet.IP)
	mask := IPToUint32(net.IP(ipNet.Mask))
	end := start | (^mask)
	return start, end, nil
}
