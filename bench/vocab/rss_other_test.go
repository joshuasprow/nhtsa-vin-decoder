//go:build !unix

package vocab

import (
	"errors"
	"os"
)

func maxRSS(*os.ProcessState) (float64, error) {
	return 0, errors.New("peak RSS: needs getrusage (unix)")
}
