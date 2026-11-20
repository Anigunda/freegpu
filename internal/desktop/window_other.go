//go:build !darwin

package desktop

import (
	"os/exec"
	"runtime"
)

func OpenWindow(url string) {
	if runtime.GOOS == "linux" {
		_ = exec.Command("xdg-open", url).Start()
	}
	select {}
}
