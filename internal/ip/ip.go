package ip

import "net"

func IPv4NumFromBytes(b [4]byte) uint32 {
	var n uint32 = 0

	n = (uint32(b[0]) << 24) | (uint32(b[1]) << 16) | (uint32(b[2]) << 8) | uint32(b[3])

	return n
}

func IPv4BytesFromNum(num uint32) [4]byte {
	return [4]byte{
		byte(num >> 24),
		byte(num >> 16),
		byte(num >> 8),
		byte(num),
	}
}

func IPv4FromNum(num uint32) net.IP {
	return net.IPv4(byte(num>>24), byte(num>>16), byte(num>>8), byte(num))
}
