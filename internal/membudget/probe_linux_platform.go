//go:build linux

package membudget

func probePlatform(options ProbeOptions) (Headroom, error) {
	return probeLinux(options)
}
