//go:build unix

package vocab

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// maxRSS is an exited process's peak resident set in bytes: getrusage's
// ru_maxrss, which Darwin counts in bytes and the others in KiB.
func maxRSS(p *os.ProcessState) (float64, error) {
	ru, ok := p.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0, fmt.Errorf("peak RSS: no rusage from %s", p)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "ios" {
		return float64(ru.Maxrss), nil
	}
	return float64(ru.Maxrss) * 1024, nil
}
