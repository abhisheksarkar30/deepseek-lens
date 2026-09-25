package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/config"
)

// configFlagNames are the flags config.Load accepts. Anything else on
// `lens reload` is this command's own flag and is stripped first.
var configFlagNames = map[string]bool{
	"proxy-addr": true, "dashboard-addr": true, "upstream-url": true, "db-path": true,
	"body-policy": true, "body-cap-bytes": true, "allow-remote": true, "capture": true,
	"session-gap-minutes": true, "replay": true, "replay-cost-threshold-usd": true,
	"model-map": true, "model-max-tokens": true, "off-peak-dates": true, "work-dates": true,
	"retention-days": true, "hot-days": true,
}

// Reload implements `lens reload`.
func Reload(args []string) error {
	cfg, err := config.Load(stripOwnFlags(args))
	if err != nil {
		return err
	}
	path := serveStatePath(cfg.DBPath)
	st, err := readServeState(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("serve is not running (no state file at %s); start with lens serve", path)
		}
		return err
	}
	dash := st.DashboardAddr
	if dash == "" {
		dash = cfg.DashboardAddr
	}
	dash = dialAddr(dash)
	c := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodPost, "http://"+dash+"/api/reload", nil)
	if err != nil {
		return err
	}
	res, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("reload: post: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	fmt.Fprintln(os.Stdout, strings.TrimSpace(string(body)))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("reload: status %d", res.StatusCode)
	}
	return nil
}

func stripOwnFlags(args []string) []string {
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name = name[:eq]
		}
		if configFlagNames[name] {
			rest = append(rest, a)
			continue
		}
		if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
		}
	}
	return rest
}
