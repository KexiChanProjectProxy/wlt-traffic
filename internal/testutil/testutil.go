package testutil

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/kexi/traffic-count/internal/config"
	httplib "github.com/kexi/traffic-count/internal/http"
	"github.com/kexi/traffic-count/internal/runtime"
	"github.com/kexi/traffic-count/internal/storage"
)

type Harness struct {
	mu        sync.Mutex
	vethPairs []VethPair
	daemon    *DaemonHandle
	config    *config.Config
	repo      *storage.Repository
	flush     *runtime.FlushLoop
	server    *httplib.Server
	tmpDir    string
}

type VethPair struct {
	HostName string
	HostMAC  net.HardwareAddr
	PeerName string
	PeerMAC  net.HardwareAddr
	PeerIP   string
}

type DaemonHandle struct {
	Service *runtime.Service
	Server  *httplib.Server
	Repo    *storage.Repository
	Flush   *runtime.FlushLoop
	tmpDir  string
}

func NewHarness(ifaceName string) (*Harness, error) {
	h := &Harness{}

	tmpDir, err := os.MkdirTemp("", "traffic-count-test-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	h.tmpDir = tmpDir

	cfg := config.New()
	cfg.Interfaces = []string{ifaceName}
	cfg.DatabasePath = tmpDir + "/traffic-count.db"
	cfg.FlushInterval = 1
	cfg.AllowPartial = true

	h.config = cfg
	return h, nil
}

func (h *Harness) CreateVethPair(prefix string) (*VethPair, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	hostName := prefix + "-host"
	peerName := prefix + "-peer"

	exec.Command("ip", "link", "add", hostName, "type", "veth", "peer", "name", peerName).Run()

	hostMac, err := net.ParseMAC("aa:bb:cc:dd:ee:01")
	if err != nil {
		return nil, err
	}
	peerMac, err := net.ParseMAC("aa:bb:cc:dd:ee:02")
	if err != nil {
		return nil, err
	}

	pair := &VethPair{
		HostName: hostName,
		HostMAC:  hostMac,
		PeerName: peerName,
		PeerMAC:  peerMac,
		PeerIP:   "10.0.0.2/24",
	}

	h.vethPairs = append(h.vethPairs, *pair)
	return pair, nil
}

func (h *Harness) SetupInterface(ifaceName string) error {
	exec.Command("ip", "link", "set", ifaceName, "up").Run()
	exec.Command("ip", "addr", "add", "10.0.0.1/24", "dev", ifaceName).Run()
	return nil
}

func (h *Harness) StartDaemon(ctx context.Context) (*DaemonHandle, error) {
	cfg := h.config

	svc := runtime.NewService(cfg, "")
	if err := svc.Start(); err != nil {
		return nil, fmt.Errorf("starting service: %w", err)
	}

	repo, err := storage.New(cfg.DatabasePath)
	if err != nil {
		svc.Stop()
		return nil, fmt.Errorf("creating repository: %w", err)
	}

	flush := runtime.NewFlushLoop(repo, svc.GetTrafficMap(), cfg.FlushInterval)
	if err := flush.Start(ctx); err != nil {
		repo.Close()
		svc.Stop()
		return nil, fmt.Errorf("starting flush loop: %w", err)
	}

	srv := httplib.NewServer(cfg, svc, repo, flush)

	go srv.Start()

	time.Sleep(100 * time.Millisecond)

	h.daemon = &DaemonHandle{
		Service: svc,
		Server:  srv,
		Repo:    repo,
		Flush:   flush,
		tmpDir:  h.tmpDir,
	}

	return h.daemon, nil
}

func (h *Harness) Cleanup() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, pair := range h.vethPairs {
		exec.Command("ip", "link", "del", pair.HostName).Run()
	}

	if h.daemon != nil {
		h.daemon.Flush.Stop(context.Background())
		h.daemon.Repo.Close()
		h.daemon.Service.Stop()
	}

	os.RemoveAll(h.tmpDir)
}

func SendTraffic(ifaceName string, dstMAC net.HardwareAddr, count int) error {
	for i := 0; i < count; i++ {
		cmd := exec.Command("bash", "-c", fmt.Sprintf(
			"echo 'data' | ip neigh replace %s lladdr %s dev %s",
			"10.0.0.2", dstMAC.String(), ifaceName,
		))
		cmd.Run()
	}
	return nil
}

func GenerateTraffic(ifaceName string, srcMAC, dstMAC net.HardwareAddr, bytes int) error {
	frame := buildEthernetFrame(srcMAC, dstMAC, bytes)

	template := []byte{
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x08, 0x00,
	}
	copy(template, srcMAC)
	copy(template[6:], dstMAC)

	template = append(template, frame...)

	return nil
}

func buildEthernetFrame(srcMAC, dstMAC net.HardwareAddr, payloadSize int) []byte {
	frame := make([]byte, 14+payloadSize)
	copy(frame[0:6], dstMAC)
	copy(frame[6:12], srcMAC)
	frame[12] = 0x08
	frame[13] = 0x00
	return frame
}

func (h *Harness) QueryAPI(path string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("http://%s%s", h.config.BindAddress, path)
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return buf.Bytes(), nil
}

type MACAddress [6]byte

func ParseMAC(s string) (net.HardwareAddr, error) {
	return net.ParseMAC(s)
}

func NewMACFromBytes(b [6]byte) net.HardwareAddr {
	return net.HardwareAddr(b[:])
}

func (h *Harness) GetConfig() *config.Config {
	return h.config
}

func (h *Harness) GetRepo() *storage.Repository {
	return h.repo
}

func (h *Harness) GetService() *runtime.Service {
	if h.daemon == nil {
		return nil
	}
	return h.daemon.Service
}

type TempDB struct {
	Path string
	Repo *storage.Repository
}

func NewTempDB() (*TempDB, error) {
	tmpFile, err := os.CreateTemp("", "traffic-count-*.db")
	if err != nil {
		return nil, err
	}
	tmpFile.Close()

	repo, err := storage.New(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		return nil, err
	}

	return &TempDB{
		Path: tmpFile.Name(),
		Repo: repo,
	}, nil
}

func (td *TempDB) Close() {
	if td.Repo != nil {
		td.Repo.Close()
	}
	os.Remove(td.Path)
}

func CreateTestInterface(name string) error {
	cmd := exec.Command("ip", "link", "add", name, "type", "dummy")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("creating dummy interface: %w", err)
	}

	cmd = exec.Command("ip", "link", "set", name, "up")
	if err := cmd.Run(); err != nil {
		exec.Command("ip", "link", "del", name).Run()
		return fmt.Errorf("setting interface up: %w", err)
	}

	return nil
}

func DeleteTestInterface(name string) error {
	cmd := exec.Command("ip", "link", "del", name)
	return cmd.Run()
}

func ListTestInterfaces(prefix string) ([]string, error) {
	cmd := exec.Command("ip", "-o", "link", "show")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var ifaces []string
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, prefix+":") || strings.Contains(line, prefix+"@") {
			parts := strings.Split(line, ":")
			if len(parts) >= 2 {
				name := strings.TrimSpace(parts[1])
				name = strings.Split(name, "@")[0]
				ifaces = append(ifaces, name)
			}
		}
	}

	return ifaces, nil
}

func EnsureLoopbackUp() error {
	cmd := exec.Command("ip", "link", "set", "lo", "up")
	return cmd.Run()
}
