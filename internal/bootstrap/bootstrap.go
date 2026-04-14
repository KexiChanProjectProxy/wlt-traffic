package bootstrap

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/kexi/traffic-count/internal/config"
)

type Mode string

const (
	ModeHealthy  Mode = "healthy"
	ModeDegraded Mode = "degraded"
	ModeFailed   Mode = "failed"
)

type StartupResult struct {
	Mode               Mode
	TotalInterfaces    int
	AttachedInterfaces []string
	FailedInterfaces   []string
	AttachErrors       []string
}

type EnvironmentCheck func(iface string) error
type QdiscCheck func(iface string) error
type CapabilityCheck func() error

type Validator struct {
	cfg             *config.Config
	checkCapability CapabilityCheck
	checkInterface  EnvironmentCheck
	checkQdisc      QdiscCheck
}

func NewValidator(cfg *config.Config) *Validator {
	return &Validator{
		cfg:             cfg,
		checkCapability: realCheckCapabilities,
		checkInterface:  realValidateInterface,
		checkQdisc:      realCheckQdisc,
	}
}

func (v *Validator) Validate() (*StartupResult, error) {
	result := &StartupResult{
		TotalInterfaces:    len(v.cfg.Interfaces),
		AttachedInterfaces: []string{},
		FailedInterfaces:   []string{},
		AttachErrors:       []string{},
	}

	if len(v.cfg.Interfaces) == 0 {
		return result, fmt.Errorf("startup validation failed: no interfaces configured")
	}

	if v.checkCapability != nil {
		if err := v.checkCapability(); err != nil {
			result.Mode = ModeFailed
			return result, fmt.Errorf("startup validation failed: %w", err)
		}
	}

	attached := 0
	for _, iface := range v.cfg.Interfaces {
		if v.checkInterface != nil {
			if err := v.checkInterface(iface); err != nil {
				result.FailedInterfaces = append(result.FailedInterfaces, iface)
				result.AttachErrors = append(result.AttachErrors, err.Error())
				continue
			}
		}
		if v.checkQdisc != nil {
			if err := v.checkQdisc(iface); err != nil {
				result.FailedInterfaces = append(result.FailedInterfaces, iface)
				result.AttachErrors = append(result.AttachErrors, err.Error())
				continue
			}
		}
		result.AttachedInterfaces = append(result.AttachedInterfaces, iface)
		attached++
	}

	if attached == 0 {
		result.Mode = ModeFailed
		return result, fmt.Errorf("startup validation failed: zero interfaces attached")
	}

	if attached < len(v.cfg.Interfaces) {
		if v.cfg.AllowPartial {
			result.Mode = ModeDegraded
			return result, nil
		}
		result.Mode = ModeFailed
		return result, fmt.Errorf("startup validation failed: %d of %d interfaces failed (allow_partial=false)",
			len(v.cfg.Interfaces)-attached, len(v.cfg.Interfaces))
	}

	result.Mode = ModeHealthy
	return result, nil
}

func realCheckCapabilities() error {
	if os.Getuid() != 0 {
		return fmt.Errorf("root privileges required (CAP_SYS_ADMIN, CAP_NET_ADMIN)")
	}
	return nil
}

func realValidateInterface(iface string) error {
	ifacePath := "/sys/class/net/" + iface
	if _, err := os.Stat(ifacePath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("interface %q does not exist", iface)
		}
		return fmt.Errorf("failed to check interface %q: %w", iface, err)
	}
	return nil
}

func realCheckQdisc(iface string) error {
	cmd := exec.Command("tc", "qdisc", "show", "dev", iface)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("qdisc check failed for %q: %w (output: %s)", iface, err, string(output))
	}
	return nil
}

func (v *Validator) AttachCheck() error {
	_, err := v.Validate()
	return err
}
