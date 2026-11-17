package fleet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const repository = "Cytech-Team/CyComAgent-MCP"

type Peer struct {
	Name           string    `json:"name"`
	DNSName        string    `json:"dns_name,omitempty"`
	OS             string    `json:"os,omitempty"`
	IPs            []string  `json:"ips,omitempty"`
	Online         bool      `json:"online"`
	Self           bool      `json:"self,omitempty"`
	LastSeen       time.Time `json:"last_seen,omitempty"`
	SSHReachable   bool      `json:"ssh_reachable"`
	CyComInstalled bool      `json:"cycom_installed"`
	Version        string    `json:"version,omitempty"`
	Commit         string    `json:"commit,omitempty"`
	Modified       bool      `json:"modified,omitempty"`
	VersionStatus  string    `json:"version_status"`
}
type Snapshot struct {
	GeneratedAt      time.Time `json:"generated_at"`
	TailnetAvailable bool      `json:"tailnet_available"`
	TailnetError     string    `json:"tailnet_error,omitempty"`
	Self             string    `json:"self"`
	LocalVersion     string    `json:"local_version"`
	LatestVersion    string    `json:"latest_version,omitempty"`
	LatestCommit     string    `json:"latest_commit,omitempty"`
	LatestSource     string    `json:"latest_source,omitempty"`
	LatestCheckedAt  time.Time `json:"latest_checked_at,omitempty"`
	Peers            []Peer    `json:"peers"`
}
type status struct {
	Self struct {
		HostName     string
		DNSName      string
		OS           string
		TailscaleIPs []string
		Online       bool
	} `json:"Self"`
	Peer map[string]struct {
		HostName     string
		DNSName      string
		OS           string
		TailscaleIPs []string
		Online       bool
		LastSeen     time.Time
	} `json:"Peer"`
}
type Manager struct {
	mu           sync.RWMutex
	current      Snapshot
	refreshing   bool
	localVersion string
	checked      time.Time
}

func New(localVersion string) *Manager {
	return &Manager{localVersion: localVersion, current: Snapshot{LocalVersion: localVersion, Peers: []Peer{}}}
}
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	stale := time.Since(m.checked) > 45*time.Second && !m.refreshing
	if stale {
		m.refreshing = true
		go m.refresh()
	}
	out := m.current
	out.Peers = append([]Peer(nil), m.current.Peers...)
	m.mu.Unlock()
	return out
}
func (m *Manager) Refresh() {
	m.mu.Lock()
	if m.refreshing {
		m.mu.Unlock()
		return
	}
	m.refreshing = true
	m.mu.Unlock()
	m.refresh()
}
func (m *Manager) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	next := Snapshot{GeneratedAt: time.Now().UTC(), LocalVersion: m.localVersion, Peers: []Peer{}}
	raw, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		next.TailnetError = "Tailscale status unavailable"
	} else {
		var st status
		if json.Unmarshal(raw, &st) != nil {
			next.TailnetError = "Unable to parse Tailscale status"
		} else {
			next.TailnetAvailable = true
			next.Self = st.Self.HostName
			next.Peers = append(next.Peers, Peer{Name: st.Self.HostName, DNSName: strings.TrimSuffix(st.Self.DNSName, "."), OS: st.Self.OS, IPs: st.Self.TailscaleIPs, Online: true, Self: true, SSHReachable: true, CyComInstalled: true, Version: m.localVersion, VersionStatus: "local"})
			for _, p := range st.Peer {
				next.Peers = append(next.Peers, Peer{Name: p.HostName, DNSName: strings.TrimSuffix(p.DNSName, "."), OS: p.OS, IPs: append([]string(nil), p.TailscaleIPs...), Online: p.Online, LastSeen: p.LastSeen, VersionStatus: "not_checked"})
			}
		}
	}
	next.LatestVersion, next.LatestCommit, next.LatestSource, next.LatestCheckedAt = latestGitHubVersion(ctx)
	for i := range next.Peers {
		p := &next.Peers[i]
		if p.Self {
			p.Commit, p.Modified = buildIdentity(p.Version)
			p.VersionStatus = compareBuild(p.Version, next.LatestVersion, next.LatestCommit)
			continue
		}
		if !p.Online {
			p.VersionStatus = "offline"
			continue
		}
		probePeer(ctx, p)
		if p.CyComInstalled {
			p.Commit, p.Modified = buildIdentity(p.Version)
			p.VersionStatus = compareBuild(p.Version, next.LatestVersion, next.LatestCommit)
		} else if p.SSHReachable {
			p.VersionStatus = "not_installed"
		} else {
			p.VersionStatus = "unreachable"
		}
	}
	sort.Slice(next.Peers, func(i, j int) bool {
		if next.Peers[i].Self {
			return true
		}
		if next.Peers[j].Self {
			return false
		}
		return next.Peers[i].Name < next.Peers[j].Name
	})
	m.mu.Lock()
	m.current = next
	m.checked = time.Now()
	m.refreshing = false
	m.mu.Unlock()
}
func latestGitHubVersion(ctx context.Context) (string, string, string, time.Time) {
	checked := time.Now().UTC()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// gh reads the user's existing GitHub credential store; no token is copied into CyCom state or logs.
	b, err := exec.CommandContext(cctx, "gh", "api", "repos/"+repository+"/contents/internal/buildinfo/VERSION", "--jq", ".content").Output()
	if err != nil {
		return "", "", "", checked
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return "", "", "", checked
	}
	v := strings.TrimSpace(string(decoded))
	if v == "" {
		return "", "", "", checked
	}
	revCtx, revCancel := context.WithTimeout(ctx, 4*time.Second)
	defer revCancel()
	rev, revErr := exec.CommandContext(revCtx, "gh", "api", "repos/"+repository+"/commits/main", "--jq", ".sha").Output()
	if revErr != nil {
		return v, "", "github-main", checked
	}
	return v, strings.TrimSpace(string(rev)), "github-main", checked
}
func probePeer(ctx context.Context, p *Peer) {
	if !p.Online || p.Name == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3", "-o", "StrictHostKeyChecking=yes", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-o", "LogLevel=ERROR", p.Name, "if test -x /usr/local/bin/cycomagent; then /usr/local/bin/cycomagent --version; elif command -v cycomagent >/dev/null 2>&1; then cycomagent --version; else printf 'NOT_INSTALLED'; fi")
	b, err := cmd.Output()
	if err != nil {
		return
	}
	p.SSHReachable = true
	v := strings.TrimSpace(string(b))
	if v != "" && v != "NOT_INSTALLED" {
		p.CyComInstalled = true
		p.Version = v
	}
}
func buildIdentity(installed string) (string, bool) {
	modified := strings.HasSuffix(installed, ".dirty")
	installed = strings.TrimSuffix(installed, ".dirty")
	parts := strings.SplitN(installed, "+", 2)
	if len(parts) != 2 {
		return "", modified
	}
	return parts[1], modified
}
func compareBuild(installed, latestVersion, latestCommit string) string {
	if latestVersion == "" {
		return "unknown"
	}
	base := strings.TrimSuffix(strings.SplitN(installed, "+", 2)[0], ".dirty")
	if base != latestVersion {
		return "version_mismatch"
	}
	commit, _ := buildIdentity(installed)
	if latestCommit == "" || commit == "" {
		return "unknown"
	}
	if len(commit) >= 7 && strings.HasPrefix(latestCommit, commit) {
		return "current"
	}
	return "behind"
}
