package extract

import (
	"os"
	"runtime"
	"strings"
)

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }

// workerEnv es el entorno mínimo del worker: sin proxies ni variables heredadas.
// En Windows (solo desarrollo) se agregan las variables que Python necesita.
func workerEnv(tempDir string) []string {
	env := []string{"PYTHONIOENCODING=utf-8", "PYTHONDONTWRITEBYTECODE=1"}
	if tempDir != "" {
		env = append(env, "TMPDIR="+tempDir, "TEMP="+tempDir, "TMP="+tempDir)
	}
	if runtime.GOOS != "windows" {
		return append(env, "HOME=/nonexistent", "PATH=/usr/local/bin:/usr/bin:/bin")
	}
	for _, k := range []string{"SYSTEMROOT", "PATH", "TEMP", "USERPROFILE", "LOCALAPPDATA", "APPDATA"} {
		if v := os.Getenv(k); v != "" && !(k == "TEMP" && tempDir != "") {
			env = append(env, k+"="+v)
		}
	}
	return env
}
