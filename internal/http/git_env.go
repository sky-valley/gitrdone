package httpapi

import (
	"os"
	"strings"
)

func gitProcessEnv(extra ...string) []string {
	env := make([]string, 0, 4+len(extra))
	if path := strings.TrimSpace(os.Getenv("PATH")); path != "" {
		env = append(env, "PATH="+path)
	}
	env = append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
	env = append(env, extra...)
	return env
}
